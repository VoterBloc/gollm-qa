// Package server hosts the HTTP control-plane API for gollm-qa.
// `gollm serve` boots a Server; the Cohort panel (VoterBloc/cohort) is the
// primary consumer. Phase 1 is read-only: endpoints list the YAML files
// under apps/, campaigns/, and personas/ so the panel can render browse
// views. Run lifecycle, persistence, and SSE arrive in later phases.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Config controls server lifecycle and the file-system roots the read-only
// endpoints read from. All paths are relative to the working directory or
// absolute.
type Config struct {
	Addr         string // listen address, e.g. ":8080"
	ConfigsDir   string // apps/
	CampaignsDir string // campaigns/
	PersonasDir  string // personas/
}

// Server wraps an http.Server with the gollm-qa routes mounted.
type Server struct {
	cfg    Config
	logger *slog.Logger
	http   *http.Server
}

// New builds a Server with all routes mounted. It does not start listening;
// call Run for that. A nil logger discards logs (useful in tests).
func New(cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{cfg: cfg, logger: logger}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           withRequestLogging(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Handler returns the fully-wrapped HTTP handler. Exposed for tests; in
// production callers should use Run.
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// Run starts listening and blocks until ctx is cancelled, then performs a
// graceful shutdown with a fixed deadline.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", s.cfg.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		s.logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// withRequestLogging emits one structured log line per request. Lives at
// the outermost layer so it captures handler errors and durations through
// any inner middleware added later (auth, recovery, etc.).
func withRequestLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.status,
			"dur", time.Since(start),
		)
	})
}

// statusRecorder captures the response status so the request logger can
// include it. http.ResponseWriter doesn't expose the status itself.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
