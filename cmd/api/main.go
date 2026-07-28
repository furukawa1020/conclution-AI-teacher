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
	"github.com/furukawa1020/conclution-ai-teacher/internal/evaluation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/store"
)

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
	var evaluationStore store.EvaluationStore
	var closeFirestore func() error

	if cfg.AllowInsecureDev {
		logger.Warn("local authentication bypass is enabled")
		evaluator = evaluation.DevelopmentEvaluator{}
		verifier = identity.DevelopmentVerifier{}
		evaluationStore = store.MemoryEvaluationStore{}
		closeFirestore = func() error { return nil }
	} else {
		evaluator, err = evaluation.NewGenkitEvaluator(ctx, cfg.ProjectID, cfg.VertexLocation, cfg.FastModel)
		if err != nil {
			logger.Error("initialize Genkit", "error", err)
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
		evaluationStore = store.NewFirestoreEvaluationStore(firestoreClient)
		closeFirestore = firestoreClient.Close
	}
	defer func() {
		if err := closeFirestore(); err != nil {
			logger.Error("close Firestore client", "error", err)
		}
	}()

	handler := httpapi.New(
		logger,
		verifier,
		evaluator,
		evaluationStore,
		cfg.RequestTimeout,
		cfg.MaxRequestBytes,
	)
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      55 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}

	go func() {
		logger.Info("API listening",
			"port", cfg.Port,
			"environment", cfg.AppEnv,
			"vertex_location", cfg.VertexLocation,
			"model_logical_id", "fast-judge",
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
