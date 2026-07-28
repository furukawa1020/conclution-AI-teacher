package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
	"github.com/furukawa1020/conclution-ai-teacher/internal/evaluation"
	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/store"
)

type Server struct {
	logger          *slog.Logger
	verifier        identity.Verifier
	evaluator       evaluation.Evaluator
	store           store.EvaluationStore
	requestTimeout  time.Duration
	maxRequestBytes int64
}

func New(
	logger *slog.Logger,
	verifier identity.Verifier,
	evaluator evaluation.Evaluator,
	evaluationStore store.EvaluationStore,
	requestTimeout time.Duration,
	maxRequestBytes int64,
) http.Handler {
	server := &Server{
		logger:          logger,
		verifier:        verifier,
		evaluator:       evaluator,
		store:           evaluationStore,
		requestTimeout:  requestTimeout,
		maxRequestBytes: maxRequestBytes,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.Handle("GET /api/v1/me", server.requireIdentity(http.HandlerFunc(server.me)))
	mux.Handle("POST /api/v1/evaluations", server.requireIdentity(http.HandlerFunc(server.evaluate)))

	return server.recoverPanic(
		server.securityHeaders(
			server.requestContext(
				server.rejectCrossSiteWrites(mux),
			),
		),
	)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "kotae-api",
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uid":   principal.UID,
		"appId": principal.AppID,
		"roles": principal.Roles,
	})
}

func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input contracts.EvaluationInput
	if err := decoder.Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Only one JSON value is allowed.")
		return
	}
	if err := input.Validate(); err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_input", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()

	started := time.Now()
	result, err := s.evaluator.Evaluate(ctx, input)
	if err != nil {
		s.logger.ErrorContext(ctx, "evaluation failed",
			"request_id", requestIDFromContext(ctx),
			"uid_hash", shortHash(principal.UID),
			"duration_ms", time.Since(started).Milliseconds(),
			"error_class", "model_or_schema_failure",
		)
		writeProblem(w, http.StatusBadGateway, "evaluation_unavailable", "The evaluation service is temporarily unavailable.")
		return
	}

	attemptID, err := s.store.Save(ctx, principal.UID, requestIDFromContext(ctx), input, result)
	if err != nil {
		s.logger.ErrorContext(ctx, "evaluation persistence failed",
			"request_id", requestIDFromContext(ctx),
			"uid_hash", shortHash(principal.UID),
			"duration_ms", time.Since(started).Milliseconds(),
			"error_class", "firestore_write_failure",
		)
		writeProblem(w, http.StatusServiceUnavailable, "persistence_unavailable", "The result could not be saved safely.")
		return
	}

	s.logger.InfoContext(ctx, "evaluation completed",
		"request_id", requestIDFromContext(ctx),
		"uid_hash", shortHash(principal.UID),
		"attempt_id", attemptID,
		"model_logical_id", result.ModelLogicalID,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	writeJSON(w, http.StatusCreated, map[string]any{
		"attemptId":  attemptID,
		"evaluation": result,
	})
}

func (s *Server) requireIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders := r.Header.Values("Authorization")
		appCheckHeaders := r.Header.Values("X-Firebase-AppCheck")
		if len(authHeaders) != 1 || len(appCheckHeaders) != 1 {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
			return
		}

		authHeader := strings.TrimSpace(authHeaders[0])
		appCheckHeader := strings.TrimSpace(appCheckHeaders[0])
		const bearer = "Bearer "
		if !strings.HasPrefix(authHeader, bearer) || len(authHeader) <= len(bearer) || appCheckHeader == "" {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
			return
		}

		principal, err := s.verifier.Verify(
			r.Context(),
			strings.TrimSpace(strings.TrimPrefix(authHeader, bearer)),
			appCheckHeader,
		)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authentication or app attestation failed.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func (s *Server) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID)))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rejectCrossSiteWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
				writeProblem(w, http.StatusForbidden, "cross_site_request", "Cross-site writes are not allowed.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.ErrorContext(r.Context(), "request panic recovered",
					"request_id", requestIDFromContext(r.Context()),
					"error_class", "panic",
				)
				writeProblem(w, http.StatusInternalServerError, "internal_error", "An internal error occurred.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
		"detail": detail,
	})
}

type principalContextKey struct{}
type requestIDContextKey struct{}

func principalFromContext(ctx context.Context) (identity.Principal, bool) {
	value, ok := ctx.Value(principalContextKey{}).(identity.Principal)
	return value, ok
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return value
}

func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(value[:])
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
