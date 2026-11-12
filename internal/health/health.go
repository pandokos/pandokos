// Package health implements liveness and readiness.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const checkTimeout = 2 * time.Second

// Check reports whether a dependency is usable; nil means yes.
type Check func(ctx context.Context) error

type namedCheck struct {
	name  string
	check Check
}

// State is the health of the process.
type State struct {
	logger *slog.Logger

	mu       sync.Mutex
	started  bool
	draining bool
	checks   []namedCheck
}

// New returns a State that logs failing checks to logger.
func New(logger *slog.Logger) *State { return &State{logger: logger} }

// AddCheck registers a readiness check. Call it before serving.
func (s *State) AddCheck(name string, c Check) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks = append(s.checks, namedCheck{name, c})
}

// SetStarted records that the listeners are bound.
func (s *State) SetStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
}

// SetDraining records that shutdown began. It is irreversible.
func (s *State) SetDraining() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draining = true
}

// Ready reports whether the process is started, not draining and every check passes.
func (s *State) Ready(ctx context.Context) bool {
	s.mu.Lock()
	ok := s.started && !s.draining
	checks := append([]namedCheck(nil), s.checks...)
	s.mu.Unlock()
	if !ok {
		return false
	}
	ready := true
	for _, c := range checks {
		if err := s.run(ctx, c); err != nil {
			s.logger.Warn("readiness check failed", "check", c.name, "error", err.Error())
			ready = false
		}
	}
	return ready
}

// run executes a check with a timeout, also when the check ignores its context.
func (s *State) run(ctx context.Context, c namedCheck) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.check(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Handler serves /healthz and /readyz (GET and HEAD). Everything else is answered with 404.
func (s *State) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			respond(w, r, http.StatusOK, `{"status":"ok"}`)
		case "/readyz":
			if s.Ready(r.Context()) {
				respond(w, r, http.StatusOK, `{"status":"ready"}`)
			} else {
				respond(w, r, http.StatusServiceUnavailable, `{"status":"not_ready"}`)
			}
		default:
			http.NotFound(w, r)
		}
	})
}

func respond(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}
