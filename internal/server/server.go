// Package server wires the listener, the health endpoints and the gateway, and runs the
// graceful shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/health"
)

// Server is the Pandokos server of the walking skeleton: one listener, the health endpoints
// and the inference gateway.
type Server struct {
	cfg       *config.Config
	logger    *slog.Logger
	health    *health.State
	healthH   http.Handler
	gw        *gateway.Handler
	transport *http.Transport
	httpSrv   *http.Server
	ln        net.Listener

	baseCtx    context.Context
	cancelBase context.CancelCauseFunc
}

// New wires logger, health, gateway and the HTTP server. It performs no I/O.
func New(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, logger: logger, health: health.New(logger)}
	s.healthH = s.health.Handler()
	s.transport = gateway.NewTransport(time.Duration(cfg.Upstream.ConnectTimeout))
	gw, err := gateway.New(gateway.Options{
		Logger:                 logger,
		BaseURL:                cfg.Skeleton.OpenAI.ParsedBaseURL,
		ProviderKey:            cfg.Skeleton.OpenAI.APIKey,
		AccessToken:            cfg.Skeleton.AccessToken,
		MaxRequestBodyBytes:    cfg.Limits.MaxRequestBodyBytes,
		RequestBodyReadTimeout: time.Duration(cfg.Limits.RequestBodyReadTimeout),
		MaxRequestDuration:     time.Duration(cfg.Limits.MaxRequestDuration),
		Transport:              s.transport,
	})
	if err != nil {
		return nil, fmt.Errorf("server: %w", err)
	}
	s.gw = gw
	s.baseCtx, s.cancelBase = context.WithCancelCause(context.Background())
	l := cfg.Listeners.Inference
	s.httpSrv = &http.Server{
		Handler:           http.HandlerFunc(s.route),
		ReadHeaderTimeout: time.Duration(l.ReadHeaderTimeout),
		IdleTimeout:       time.Duration(l.IdleTimeout),
		MaxHeaderBytes:    l.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
	}
	return s, nil
}

// Health returns the health state, so that callers can add checks before Serve.
func (s *Server) Health() *health.State { return s.health }

// Listen binds the inference listener. After it returns, InferenceAddr is valid.
func (s *Server) Listen() error {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", s.cfg.Listeners.Inference.Address)
	if err != nil {
		return fmt.Errorf("listen on the inference address: %w", err)
	}
	s.ln = ln
	s.logger.Info("listening", "listener", "inference", "address", ln.Addr().String())
	return nil
}

// InferenceAddr returns the bound address of the inference listener.
func (s *Server) InferenceAddr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// route sends the health paths to health and everything else to the gateway. The match is
// exact: http.ServeMux would answer non-canonical paths with its own redirect.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	if (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		s.healthH.ServeHTTP(rec, r)
		s.logger.Debug("request",
			"request_id", gateway.NewRequestID(),
			"method", r.Method,
			"route", r.Method+" "+r.URL.Path,
			"status", rec.code,
			"outcome", "ok",
			"duration_ms", time.Since(start).Milliseconds())
		return
	}
	s.gw.ServeHTTP(w, r)
}

// Serve serves until ctx is done, then shuts down: readiness goes down, in-flight requests
// drain up to the configured deadline, and what remains is cancelled. It returns nil after a
// shutdown caused by ctx, and an error if the HTTP server fails for any other reason.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		return errors.New("server: Serve called before Listen")
	}
	s.health.SetStarted()
	errc := make(chan error, 1)
	go func() { errc <- s.httpSrv.Serve(s.ln) }()

	select {
	case err := <-errc:
		s.cancelBase(nil)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	drain := time.Duration(s.cfg.Shutdown.DrainTimeout)
	start := time.Now()
	s.logger.Info("shutdown started", "drain_timeout_ms", drain.Milliseconds())
	s.health.SetDraining()

	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	forced := s.httpSrv.Shutdown(drainCtx) != nil
	if forced {
		s.cancelBase(gateway.ErrShutdown)
		_ = s.httpSrv.Close()
	} else {
		s.cancelBase(nil)
	}
	<-errc
	s.transport.CloseIdleConnections()

	level := slog.LevelInfo
	if forced {
		level = slog.LevelWarn
	}
	s.logger.Log(context.Background(), level, "shutdown complete", "forced", forced, "duration_ms", time.Since(start).Milliseconds())
	return nil
}
