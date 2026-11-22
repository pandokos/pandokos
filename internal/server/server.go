// Package server wires the listener, the health endpoints and the gateway, and runs the
// graceful shutdown.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/health"
)

// Options configure a Server.
type Options struct {
	Config        *config.Config          // required
	Logger        *slog.Logger            // required
	Authenticator gateway.Authenticator   // required: authenticates inference requests
	Admin         http.Handler            // required: the administration API (admin.New)
	Catalogue     gateway.Catalogue       // required: provider and model resolution
	Credentials   gateway.Credentials     // required: opens the provider credential of a call
	Failures      gateway.FailureRecorder // optional: records authentication failures
	Egress        *egress.Guard           // required: the only way out of the process
	Recorder      gateway.Recorder        // required: receives the usage record of every admitted request
	Tracer        trace.TracerProvider    // required: the trace provider of the gateway
	Limits        gateway.Limits          // optional: reserves capacity before the credential is opened; nil enforces nothing
	Metrics       gateway.Metrics         // optional: receives one observation per inference request
	// MetricsHandler serves GET /metrics on the administration listener; nil: the path is the
	// administration API's (404). MetricsAuth authenticates the scrape and is required with it.
	MetricsHandler http.Handler
	MetricsAuth    gateway.Authenticator
}

// listener is one HTTP listener with its server.
type listener struct {
	name string
	srv  *http.Server
	ln   net.Listener
	tls  *config.ListenerTLS
}

// handlerWait bounds the wait for the handlers that the forced close cancelled, so that their usage
// records exist when Serve returns. It comes after the drain and before the flush of the ledger.
const handlerWait = 5 * time.Second

// Server serves two listeners with two handlers: the inference gateway and the administration
// API. They share the health state, the logger and the shutdown sequence.
type Server struct {
	cfg     *config.Config
	logger  *slog.Logger
	health  *health.State
	healthH http.Handler
	gw      *gateway.Handler
	admin   http.Handler
	egress  *egress.Guard

	metrics     http.Handler          // the scrape handler; nil: no route
	metricsAuth gateway.Authenticator // authenticates a scrape
	inf, adm    *listener

	baseCtx    context.Context
	cancelBase context.CancelCauseFunc
	handlers   atomic.Int64 // inference handlers still running
}

// New wires logger, health, gateway and the two HTTP servers. It performs no I/O.
func New(o Options) (*Server, error) {
	switch {
	case o.Config == nil:
		return nil, errors.New("server: Config is required")
	case o.Logger == nil:
		return nil, errors.New("server: Logger is required")
	case o.Authenticator == nil:
		return nil, errors.New("server: Authenticator is required")
	case o.Admin == nil:
		return nil, errors.New("server: Admin is required")
	case o.Catalogue == nil:
		return nil, errors.New("server: Catalogue is required")
	case o.Credentials == nil:
		return nil, errors.New("server: Credentials is required")
	case o.Egress == nil:
		return nil, errors.New("server: Egress is required")
	case o.Recorder == nil:
		return nil, errors.New("server: Recorder is required")
	case o.Tracer == nil:
		return nil, errors.New("server: Tracer is required")
	case o.MetricsHandler != nil && o.MetricsAuth == nil:
		return nil, errors.New("server: MetricsAuth is required with MetricsHandler")
	}
	cfg, logger := o.Config, o.Logger
	s := &Server{cfg: cfg, logger: logger, health: health.New(logger), admin: o.Admin, egress: o.Egress, metrics: o.MetricsHandler, metricsAuth: o.MetricsAuth}
	s.healthH = s.health.Handler()
	gw, err := gateway.New(gateway.Options{
		Logger:                 logger,
		Authenticator:          o.Authenticator,
		Catalogue:              o.Catalogue,
		Credentials:            o.Credentials,
		Failures:               o.Failures,
		Families:               gateway.Families(),
		DefaultFamily:          openai.Name,
		Public:                 o.Egress.Transport(false),
		Private:                o.Egress.Transport(true),
		MaxRequestBodyBytes:    cfg.Limits.MaxRequestBodyBytes,
		RequestBodyReadTimeout: time.Duration(cfg.Limits.RequestBodyReadTimeout),
		MaxRequestDuration:     time.Duration(cfg.Limits.MaxRequestDuration),
		Recorder:               o.Recorder,
		Tracer:                 o.Tracer,
		PropagateTrace:         cfg.Telemetry.Traces.Propagate,
		Limits:                 o.Limits,
		DefaultOutputTokens:    cfg.Limits.DefaultOutputTokens,
		Metrics:                o.Metrics,
	})
	if err != nil {
		return nil, fmt.Errorf("server: %w", err)
	}
	s.gw = gw
	s.baseCtx, s.cancelBase = context.WithCancelCause(context.Background())
	errLog := slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
	base := func(net.Listener) context.Context { return s.baseCtx }
	l, a := cfg.Listeners.Inference, cfg.Listeners.Admin
	s.inf = &listener{name: "inference", tls: &cfg.Listeners.Inference.TLS, srv: &http.Server{
		Handler:           http.HandlerFunc(s.route),
		ReadHeaderTimeout: time.Duration(l.ReadHeaderTimeout),
		IdleTimeout:       time.Duration(l.IdleTimeout),
		MaxHeaderBytes:    l.MaxHeaderBytes,
		ErrorLog:          errLog,
		BaseContext:       base,
	}}
	s.adm = &listener{name: "admin", tls: &cfg.Listeners.Admin.TLS, srv: &http.Server{
		Handler:           http.HandlerFunc(s.routeAdmin),
		ReadHeaderTimeout: time.Duration(a.ReadHeaderTimeout),
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       time.Duration(a.IdleTimeout),
		MaxHeaderBytes:    a.MaxHeaderBytes,
		ErrorLog:          errLog,
		BaseContext:       base,
	}}
	for _, x := range []*listener{s.inf, s.adm} {
		if c := x.tls.Certificate; c != nil {
			min := uint16(tls.VersionTLS12)
			if x.tls.MinVersion == "TLS1.3" {
				min = tls.VersionTLS13
			}
			x.srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{*c}, MinVersion: min} //nolint:gosec // min is TLS 1.2 or 1.3
		}
	}
	return s, nil
}

// Health returns the health state, so that callers can add checks before Serve.
func (s *Server) Health() *health.State { return s.health }

// Listen binds the inference listener and then the admin listener. If the second fails the first
// is closed. After it returns, InferenceAddr and AdminAddr are valid.
func (s *Server) Listen() error {
	for _, x := range []struct {
		l    *listener
		addr string
	}{{s.inf, s.cfg.Listeners.Inference.Address}, {s.adm, s.cfg.Listeners.Admin.Address}} {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", x.addr)
		if err != nil {
			if s.inf.ln != nil && x.l == s.adm {
				_ = s.inf.ln.Close()
				s.inf.ln = nil
			}
			return fmt.Errorf("listen on the %s address: %w", x.l.name, err)
		}
		x.l.ln = ln
		s.logger.Info("listening", "listener", x.l.name, "address", ln.Addr().String(), "tls", x.l.tls.Certificate != nil)
	}
	return nil
}

// InferenceAddr returns the bound address of the inference listener.
func (s *Server) InferenceAddr() net.Addr {
	if s.inf.ln == nil {
		return nil
	}
	return s.inf.ln.Addr()
}

// AdminAddr returns the bound address of the admin listener.
func (s *Server) AdminAddr() net.Addr {
	if s.adm.ln == nil {
		return nil
	}
	return s.adm.ln.Addr()
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
			"route", "GET "+r.URL.Path, // fixed labels: HEAD is served as GET
			"status", rec.code,
			"outcome", "ok",
			"duration_ms", time.Since(start).Milliseconds())
		return
	}
	s.handlers.Add(1)
	defer s.handlers.Add(-1)
	s.gw.ServeHTTP(w, r)
}

// routeAdmin sends the health paths to health and everything else to the administration API.
func (s *Server) routeAdmin(w http.ResponseWriter, r *http.Request) {
	if p := r.URL.Path; (p == "/healthz" || p == "/readyz") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		s.healthH.ServeHTTP(rec, r)
		s.logger.Debug("admin request",
			"request_id", gateway.NewRequestID(),
			"method", r.Method,
			"operation", strings.TrimPrefix(p, "/"),
			"status", rec.code,
			"duration_ms", time.Since(start).Milliseconds())
		return
	}
	if r.URL.Path == "/metrics" && s.metrics != nil {
		s.scrape(w, r)
		return
	}
	s.admin.ServeHTTP(w, r)
}

// scrape serves GET /metrics: the method, then a bearer credential, then the permission
// metrics:read, then the handler. It answers in the problem format of the administration API.
func (s *Server) scrape(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := gateway.NewRequestID()
	rec := &statusRecorder{ResponseWriter: w}
	defer func() {
		s.logger.Debug("admin request",
			"request_id", id,
			"method", logMethod(r.Method),
			"operation", "metrics",
			"status", rec.code,
			"duration_ms", time.Since(start).Milliseconds())
	}()
	rec.Header().Set("X-Request-Id", id)
	rec.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		rec.Header().Set("Allow", "GET, HEAD")
		problem(rec, id, http.StatusMethodNotAllowed, "invalid_request", "Method not allowed")
		return
	}
	token, ok := bearerOf(r.Header.Get("Authorization"))
	if !ok {
		s.logger.Warn("authentication failed", "listener", "admin", "request_id", id, "reason", "malformed")
		rec.Header().Set("WWW-Authenticate", "Bearer")
		problem(rec, id, http.StatusUnauthorized, "unauthenticated", "Unauthenticated")
		return
	}
	p, err := s.metricsAuth.Authenticate(r.Context(), token)
	switch {
	case errors.Is(err, authz.ErrUnavailable):
		rec.Header().Set("Retry-After", "5")
		problem(rec, id, http.StatusServiceUnavailable, "unavailable", "Unavailable")
		return
	case err != nil:
		reason := "unauthenticated"
		var d interface{ Detail() authn.FailureDetail }
		if errors.As(err, &d) {
			reason = d.Detail().Reason
		}
		s.logger.Warn("authentication failed", "listener", "admin", "request_id", id, "reason", reason)
		rec.Header().Set("WWW-Authenticate", "Bearer")
		problem(rec, id, http.StatusUnauthorized, "unauthenticated", "Unauthenticated")
		return
	}
	if authz.Decide(p, authz.MetricsRead, authz.Target{}) != authz.Allow {
		problem(rec, id, http.StatusForbidden, "forbidden", "Forbidden")
		return
	}
	s.metrics.ServeHTTP(rec, r)
}

// bearerOf returns the value of an "Authorization: Bearer <value>" header.
func bearerOf(h string) (string, bool) {
	scheme, v, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// problem writes an application/problem+json answer with the members of the administration API's
// problem that do not depend on the operation (ADR 0020).
func problem(w http.ResponseWriter, requestID string, status int, code, title string) {
	b, err := json.Marshal(map[string]any{
		"type": "urn:pandokos:problem:" + code, "title": title, "status": status, "code": code, "request_id": requestID,
	})
	if err != nil {
		b = []byte(`{"code":"internal"}`)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func logMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// Serve serves both listeners until ctx is done, then shuts down: readiness goes down, in-flight
// requests drain up to the configured deadline, and what remains is cancelled. It returns nil
// after a shutdown caused by ctx, and an error if either server fails for another reason.
func (s *Server) Serve(ctx context.Context) error {
	if s.inf.ln == nil || s.adm.ln == nil {
		return errors.New("server: Serve called before Listen")
	}
	s.health.SetStarted()
	errc := make(chan error, 2)
	serveOne := func(l *listener) {
		if l.srv.TLSConfig != nil {
			errc <- l.srv.ServeTLS(l.ln, "", "")
			return
		}
		errc <- l.srv.Serve(l.ln)
	}
	go serveOne(s.inf)
	go serveOne(s.adm)

	var failure error
	remaining := 2
	select {
	case err := <-errc:
		remaining--
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			failure = fmt.Errorf("serve: %w", err)
		}
	case <-ctx.Done():
	}

	drain := time.Duration(s.cfg.Shutdown.DrainTimeout)
	start := time.Now()
	s.logger.Info("shutdown started", "drain_timeout_ms", drain.Milliseconds())
	s.health.SetDraining()

	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	var forced atomic.Bool
	var wg sync.WaitGroup
	for _, l := range []*listener{s.inf, s.adm} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.srv.Shutdown(drainCtx) != nil {
				forced.Store(true)
			}
		}()
	}
	wg.Wait()
	if forced.Load() {
		s.cancelBase(gateway.ErrShutdown)
		_ = s.inf.srv.Close()
		_ = s.adm.srv.Close()
		// Close does not wait for handlers: a cancelled handler still records its request, and
		// the ledger is closed after Serve returns, so wait for them (bounded).
		for deadline := time.Now().Add(handlerWait); s.handlers.Load() > 0 && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
	} else {
		s.cancelBase(nil)
	}
	for ; remaining > 0; remaining-- {
		if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) && failure == nil {
			failure = fmt.Errorf("serve: %w", err)
		}
	}
	s.egress.CloseIdleConnections()

	level := slog.LevelInfo
	if forced.Load() {
		level = slog.LevelWarn
	}
	s.logger.Log(context.Background(), level, "shutdown complete", "forced", forced.Load(), "duration_ms", time.Since(start).Milliseconds())
	return failure
}
