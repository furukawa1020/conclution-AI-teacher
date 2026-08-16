package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	firebase "firebase.google.com/go/v4"

	"github.com/furukawa1020/conclution-ai-teacher/internal/config"
	"github.com/furukawa1020/conclution-ai-teacher/internal/conversation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/evaluation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativeflow"
	"github.com/furukawa1020/conclution-ai-teacher/internal/nativevoice"
	"github.com/furukawa1020/conclution-ai-teacher/internal/passkey"
	"github.com/furukawa1020/conclution-ai-teacher/internal/privacyguard"
	"github.com/furukawa1020/conclution-ai-teacher/internal/semanticshadow"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
	"github.com/furukawa1020/conclution-ai-teacher/internal/store"
	"github.com/furukawa1020/conclution-ai-teacher/internal/voiceflow"
	"google.golang.org/api/idtoken"
)

func newAPIServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var evaluator evaluation.Evaluator
	var verifier identity.Verifier
	var rateLimiter guard.Limiter
	var voiceRateLimiter guard.Limiter
	var voiceAppRateLimiter guard.Limiter
	var guestVoiceRateLimiter guard.Limiter
	var guestVoiceAppRateLimiter guard.Limiter
	var voiceLiveLeaseManager guard.VoiceLiveLeaseManager
	voiceLiveHandshakeGate := httpapi.NewVoiceLiveHandshakeGate(
		httpapi.DefaultVoiceLiveHandshakeLimit,
	)
	var evaluationStore store.EvaluationStore
	var voiceService httpapi.VoiceTurnService
	var nativeLiveService httpapi.VoiceTurnLiveService
	var nativeOpener nativevoice.Opener
	var nativeStatePreparer conversation.NativeStatePreparer
	var coachStateValidator httpapi.VoiceRespondentCheckpointValidator
	var passkeyService *passkey.Service
	var passkeyClientRateLimiter guard.Limiter
	var passkeyAppCircuitBreaker guard.Limiter
	var closeFirestore func() error
	var closeSpeech func() error
	closeNative := func() error { return nil }
	var semanticDispatcher *semanticshadow.Dispatcher

	if cfg.AllowInsecureDev {
		logger.Warn("local authentication bypass is enabled")
		evaluator = evaluation.DevelopmentEvaluator{}
		verifier = identity.DevelopmentVerifier{}
		rateLimiter, err = guard.NewMemoryLimiter(cfg.RateLimits)
		if err != nil {
			logger.Error("initialize development rate limiter", "error", err)
			os.Exit(1)
		}
		evaluationStore = store.MemoryEvaluationStore{}
		passkeyClientRateLimiter, err = guard.NewMemoryPasskeyClientLimiter(cfg.PasskeyClientRateLimits)
		if err != nil {
			logger.Error("initialize development passkey client rate limiter", "error", err)
			os.Exit(1)
		}
		passkeyAppCircuitBreaker, err = guard.NewMemoryPasskeyAppLimiter(cfg.PasskeyAppCircuitBreaker)
		if err != nil {
			logger.Error("initialize development passkey app circuit breaker", "error", err)
			os.Exit(1)
		}
		passkeyService, err = passkey.New(passkey.Config{
			RPID:           cfg.PasskeyRPID,
			RPDisplayName:  "コタエーAI",
			Origin:         cfg.PasskeyOrigin,
			Store:          passkey.NewMemoryStore(),
			TokenMinter:    passkey.DevelopmentTokenMinter{},
			AccountDeleter: passkey.DevelopmentTokenMinter{},
		})
		if err != nil {
			logger.Error("initialize development passkeys", "error", err)
			os.Exit(1)
		}
		voiceLiveLeaseManager = guard.NewMemoryVoiceLiveLeaseManager()
		closeFirestore = func() error { return nil }
		closeSpeech = func() error { return nil }
	} else {
		protector, protectorErr := privacyguard.New(ctx, privacyguard.Config{
			ProjectID: cfg.ProjectID,
			Location:  cfg.SpeechLocation,
			InfoTypes: privacyguard.DefaultInfoTypes(),
		})
		if protectorErr != nil {
			logger.Error("initialize fail-closed privacy boundary", "error", protectorErr)
			os.Exit(1)
		}
		// A fixed non-user probe proves regional API/IAM readiness. A broken
		// privacy boundary must prevent this revision from accepting traffic.
		if _, protectorErr = protector.Protect(ctx, "KOTAE privacy readiness"); protectorErr != nil {
			logger.Error("verify fail-closed privacy boundary", "error", protectorErr)
			os.Exit(1)
		}
		privacyInspector, protectorErr := privacyguard.NewGoogleDLPInspector(protector)
		if protectorErr != nil {
			logger.Error("initialize strict privacy inspector", "error", protectorErr)
			os.Exit(1)
		}

		genkitEvaluator, evaluatorErr := evaluation.NewGenkitEvaluator(
			ctx,
			cfg.ProjectID,
			cfg.VertexLocation,
			cfg.FastModel,
		)
		if evaluatorErr != nil {
			logger.Error("initialize Genkit", "error", evaluatorErr)
			os.Exit(1)
		}
		evaluator, err = evaluation.NewProtectedEvaluator(genkitEvaluator, protector)
		if err != nil {
			logger.Error("protect evaluation pipeline", "error", err)
			os.Exit(1)
		}

		app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: cfg.ProjectID})
		if err != nil {
			logger.Error("initialize Firebase Admin", "error", err)
			os.Exit(1)
		}
		authClient, err := app.Auth(ctx)
		if err != nil {
			logger.Error("initialize Firebase Auth client", "error", err)
			os.Exit(1)
		}
		appCheckClient, err := app.AppCheck(ctx)
		if err != nil {
			logger.Error("initialize Firebase App Check client", "error", err)
			os.Exit(1)
		}
		firestoreClient, err := app.Firestore(ctx)
		if err != nil {
			logger.Error("initialize Firestore client", "error", err)
			os.Exit(1)
		}

		verifier = identity.NewFirebaseVerifier(authClient, appCheckClient, cfg.AllowedAppIDs)
		rateLimiter, err = guard.NewFirestoreLimiter(firestoreClient, cfg.RateLimits)
		if err != nil {
			logger.Error("initialize evaluation rate limiter", "error", err)
			os.Exit(1)
		}
		evaluationStore = store.NewFirestoreEvaluationStore(firestoreClient)
		closeFirestore = firestoreClient.Close
		passkeyStore, err := passkey.NewFirestoreStore(firestoreClient)
		if err != nil {
			logger.Error("initialize passkey store", "error", err)
			os.Exit(1)
		}
		passkeyMinter, err := passkey.NewFirebaseTokenMinter(authClient)
		if err != nil {
			logger.Error("initialize passkey token minter", "error", err)
			os.Exit(1)
		}
		passkeyService, err = passkey.New(passkey.Config{
			RPID:           cfg.PasskeyRPID,
			RPDisplayName:  "コタエーAI",
			Origin:         cfg.PasskeyOrigin,
			Store:          passkeyStore,
			TokenMinter:    passkeyMinter,
			AccountDeleter: passkeyMinter,
		})
		if err != nil {
			logger.Error("initialize passkey service", "error", err)
			os.Exit(1)
		}
		passkeyClientRateLimiter, err = guard.NewFirestorePasskeyClientLimiter(
			firestoreClient,
			cfg.PasskeyClientRateLimits,
		)
		if err != nil {
			logger.Error("initialize passkey client rate limiter", "error", err)
			os.Exit(1)
		}
		passkeyAppCircuitBreaker, err = guard.NewFirestorePasskeyAppLimiter(
			firestoreClient,
			cfg.PasskeyAppCircuitBreaker,
		)
		if err != nil {
			logger.Error("initialize passkey app circuit breaker", "error", err)
			os.Exit(1)
		}

		voiceRateLimiter, err = guard.NewFirestoreLimiterForScope(
			firestoreClient,
			cfg.VoiceRateLimits,
			"voice",
		)
		if err != nil {
			logger.Error("initialize voice rate limiter", "error", err)
			os.Exit(1)
		}
		voiceAppRateLimiter, err = guard.NewFirestoreLimiterForScope(
			firestoreClient,
			cfg.VoiceAppRateLimits,
			"voice",
		)
		if err != nil {
			logger.Error("initialize voice app rate limiter", "error", err)
			os.Exit(1)
		}
		guestVoiceRateLimiter, err = guard.NewFirestoreLimiterForScope(firestoreClient, cfg.GuestVoiceRateLimits, "guest-voice")
		if err != nil {
			logger.Error("initialize guest voice rate limiter", "error", err)
			os.Exit(1)
		}
		guestVoiceAppRateLimiter, err = guard.NewFirestoreLimiterForScope(firestoreClient, cfg.GuestVoiceAppRateLimits, "guest-voice")
		if err != nil {
			logger.Error("initialize guest voice app rate limiter", "error", err)
			os.Exit(1)
		}
		voiceLiveLeaseManager, err = guard.NewFirestoreVoiceLiveLeaseManager(
			firestoreClient,
		)
		if err != nil {
			logger.Error("initialize voice live lease manager", "error", err)
			os.Exit(1)
		}
		conversationAgent, err := conversation.NewVertexAgent(
			ctx,
			cfg.ProjectID,
			cfg.VertexLocation,
			cfg.FastModel,
			cfg.PrecisionModel,
			cfg.StateKey,
			cfg.VertexPriority,
			cfg.CoachRestatementBinding,
			cfg.StateV2Writes,
			cfg.AnswerProofWrites,
			cfg.VerifierProgressWrites,
			cfg.RetrievalPolicyEnabled,
			cfg.AnswerTransitionWrites,
			cfg.AnswerTransitionEnabled,
		)
		if err != nil {
			logger.Error("initialize conversation agent", "error", err)
			os.Exit(1)
		}
		if cfg.NativeAudioEnabled {
			validator, ok := conversationAgent.(httpapi.VoiceRespondentCheckpointValidator)
			if !ok {
				logger.Error("initialize native coach state validator")
				os.Exit(1)
			}
			coachStateValidator = validator
			statePreparer, ok := conversationAgent.(conversation.NativeStatePreparer)
			if !ok {
				logger.Error("initialize native audio state boundary")
				os.Exit(1)
			}
			nativeStatePreparer = statePreparer
			var nativeErr error
			nativeOpener, nativeErr = nativevoice.New(ctx, nativevoice.Config{
				ProjectID:      cfg.ProjectID,
				Location:       cfg.NativeAudioLocation,
				Model:          cfg.NativeAudioModel,
				VoiceName:      cfg.NativeAudioVoice,
				SystemPrompt:   nativeflow.DefaultSystemPrompt,
				SessionTimeout: 10 * time.Minute,
			})
			if nativeErr != nil {
				logger.Error("initialize native audio client", "error", nativeErr)
				os.Exit(1)
			}
			// A fixed content-free setup probe verifies model access and IAM before
			// Cloud Run can route production traffic to this revision.
			probe, probeErr := nativeOpener.Open(ctx)
			if probeErr != nil {
				logger.Error("verify native audio readiness", "error", probeErr)
				os.Exit(1)
			}
			_ = probe.Close()
		}
		speechService, err := speechio.NewCloudService(
			ctx,
			cfg.ProjectID,
			cfg.SpeechLocation,
			cfg.SpeechModel,
			cfg.SpeechVoice,
		)
		if err != nil {
			logger.Error("initialize regional speech services", "error", err)
			os.Exit(1)
		}
		closeSpeech = speechService.Close
		voiceService, err = voiceflow.NewWithPrivacy(
			speechService,
			conversationAgent,
			privacyInspector,
		)
		if err != nil {
			logger.Error("initialize secure voice pipeline", "error", err)
			os.Exit(1)
		}
		if cfg.NativeAudioEnabled {
			var nativeService *nativeflow.Service
			var nativeErr error
			if cfg.NativeCaptionHandoffEnabled {
				captionHandoff, ok := voiceService.(httpapi.VoiceCaptionHandoffService)
				if !ok {
					logger.Error("initialize native caption handoff")
					os.Exit(1)
				}
				nativeService, nativeErr = nativeflow.NewWithCaptionHandoff(
					nativeOpener,
					nativeStatePreparer,
					captionHandoff,
				)
			} else {
				nativeService, nativeErr = nativeflow.New(
					nativeOpener,
					nativeStatePreparer,
				)
			}
			if nativeErr != nil {
				logger.Error("initialize native audio flow", "error", nativeErr)
				os.Exit(1)
			}
			nativeLiveService = nativeService
			closeNative = nativeService.Close
		}
	}
	defer func() {
		if err := closeFirestore(); err != nil {
			logger.Error("close Firestore client", "error", err)
		}
	}()
	if cfg.SemanticaShadowEnabled {
		authenticatedClient, clientErr := idtoken.NewClient(ctx, cfg.SemanticaShadowURL)
		if clientErr != nil {
			logger.Error("initialize Semantica shadow identity", "error_class", "semantic_shadow_identity_failure")
			os.Exit(1)
		}
		exporter, exporterErr := semanticshadow.NewHTTPExporter(authenticatedClient, cfg.SemanticaShadowURL)
		if exporterErr != nil {
			logger.Error("initialize Semantica shadow exporter", "error_class", "semantic_shadow_configuration_failure")
			os.Exit(1)
		}
		semanticDispatcher, err = semanticshadow.NewDispatcher(exporter)
		if err != nil {
			logger.Error("initialize Semantica shadow dispatcher", "error_class", "semantic_shadow_configuration_failure")
			os.Exit(1)
		}
		defer semanticDispatcher.Close()
	}
	defer func() {
		if err := closeSpeech(); err != nil {
			logger.Error("close regional speech clients", "error", err)
		}
	}()
	defer func() {
		if err := closeNative(); err != nil {
			logger.Error("close native audio sessions", "error", err)
		}
	}()

	handler := httpapi.NewWithVoiceAndPasskeys(
		logger,
		verifier,
		rateLimiter,
		evaluator,
		evaluationStore,
		cfg.RequestTimeout,
		cfg.MaxRequestBytes,
		httpapi.VoiceOptions{
			Service:              voiceService,
			NativeLiveService:    nativeLiveService,
			RateLimiter:          voiceRateLimiter,
			AppRateLimiter:       voiceAppRateLimiter,
			GuestRateLimiter:     guestVoiceRateLimiter,
			GuestAppRateLimiter:  guestVoiceAppRateLimiter,
			LiveLeaseManager:     voiceLiveLeaseManager,
			LiveHandshakeGate:    voiceLiveHandshakeGate,
			CoachStateValidator:  coachStateValidator,
			RequestTimeout:       cfg.VoiceTimeout,
			MaxRequestBytes:      cfg.MaxVoiceBytes,
			RequireRecentPasskey: cfg.RequireRecentPasskeyForVoice,
			GuestModeEnabled:     cfg.GuestModeEnabled,
			SemanticShadow:       semanticDispatcher,
			SemanticShadowKey:    cfg.StateKey,
		},
		passkeyService,
		passkeyClientRateLimiter,
		passkeyAppCircuitBreaker,
	)
	server := newAPIServer(":"+cfg.Port, handler)

	go func() {
		logger.Info("API listening",
			"port", cfg.Port,
			"environment", cfg.AppEnv,
			"vertex_location", cfg.VertexLocation,
			"model_logical_id", "fast-judge",
			"vertex_priority", cfg.VertexPriority,
			"speech_location", cfg.SpeechLocation,
			"speech_model", cfg.SpeechModel,
			"native_audio_enabled", cfg.NativeAudioEnabled,
			"native_caption_handoff_enabled", cfg.NativeCaptionHandoffEnabled,
			"native_audio_location", cfg.NativeAudioLocation,
			"semantica_shadow_enabled", cfg.SemanticaShadowEnabled,
			"privacy_boundary", "evaluation-deidentify-and-strict-voice-inspect-fail-closed",
		)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped unexpectedly", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
