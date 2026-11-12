// Package fakeprovider is a fake OpenAI-compatible upstream for hermetic tests.
package fakeprovider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fixtures"
)

// Models the fake understands; the behaviour is picked from the model field of the request.
const (
	ModelBasic            = "fake-basic"
	ModelDetails          = "fake-details"
	ModelGatedStream      = "fake-gated-stream"
	ModelHang             = "fake-hang"
	ModelError429         = "fake-error-429"
	ModelError500Echo     = "fake-error-500-echo"
	ModelUsageOnLastChunk = "fake-usage-on-last-chunk"
)

// Request is a request the fake received.
type Request struct {
	Method, Path, RawQuery, Proto string
	Header                        http.Header
	Body                          []byte
}

// Server is the fake upstream.
type Server struct {
	ts        *httptest.Server
	mu        sync.Mutex
	requests  []Request
	gate      chan struct{}
	once      sync.Once
	cancelled chan struct{}
	inflight  atomic.Int64
}

// New starts a plain HTTP/1.1 fake on 127.0.0.1; it is closed through tb.Cleanup.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := newServer()
	s.ts = httptest.NewServer(http.HandlerFunc(s.handle))
	tb.Cleanup(s.close)
	return s
}

// NewTLS starts a fake with TLS and HTTP/2 enabled.
func NewTLS(tb testing.TB) *Server {
	tb.Helper()
	s := newServer()
	s.ts = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.ts.EnableHTTP2 = true
	s.ts.StartTLS()
	tb.Cleanup(s.close)
	return s
}

func newServer() *Server {
	return &Server{gate: make(chan struct{}), cancelled: make(chan struct{}, 1024)}
}

func (s *Server) close() {
	s.Release()
	s.ts.Close()
}

// URL returns scheme://127.0.0.1:port.
func (s *Server) URL() string { return s.ts.URL }

// BaseURL returns URL() + "/v1".
func (s *Server) BaseURL() string { return s.ts.URL + "/v1" }

// Transport returns a transport that trusts the server.
func (s *Server) Transport() http.RoundTripper { return s.ts.Client().Transport }

// Requests returns a copy of the requests received, in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Release lets gated and hanging handlers continue. It is idempotent.
func (s *Server) Release() { s.once.Do(func() { close(s.gate) }) }

// WaitCancelled reports whether a handler saw its context cancelled within d.
func (s *Server) WaitCancelled(d time.Duration) bool {
	select {
	case <-s.cancelled:
		return true
	case <-time.After(d):
		return false
	}
}

// InFlight returns the number of handlers currently running.
func (s *Server) InFlight() int { return int(s.inflight.Load()) }

func splitEvents(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.Index(b, []byte("\n\n"))
		if i < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:i+2])
		b = b[i+2:]
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.inflight.Add(1)
	defer s.inflight.Add(-1)

	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Proto: r.Proto,
		Header: r.Header.Clone(), Body: body,
	})
	s.mu.Unlock()

	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		writeJSON(w, http.StatusNotFound, []byte(`{"error":{"message":"not found","type":"invalid_request_error","param":null,"code":null}}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fixtures.CanaryProviderKey {
		writeJSON(w, http.StatusUnauthorized, fixtures.Read("error_401.json"))
		return
	}
	h := w.Header()
	h.Set("Set-Cookie", "fake=1")
	h.Set("Server", "fake")
	h.Set("X-Request-Id", "req_fake_0001")
	h.Set("Openai-Processing-Ms", "1")
	h.Set("X-Ratelimit-Remaining-Requests", "99")
	h.Set("X-Pandokos-Error", "spoofed")

	var req struct {
		Model         string
		Stream        bool
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, []byte(`{"error":{"message":"invalid JSON","type":"invalid_request_error","param":null,"code":null}}`))
		return
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage

	switch req.Model {
	case ModelBasic:
		s.answer(w, r, req.Stream, includeUsage, false)
	case ModelDetails:
		writeJSON(w, http.StatusOK, fixtures.Read("chat_completion_details.json"))
	case ModelGatedStream:
		s.answer(w, r, true, includeUsage, true)
	case ModelHang:
		select {
		case <-s.gate:
			s.answer(w, r, req.Stream, includeUsage, false)
		case <-r.Context().Done():
			s.cancelled <- struct{}{}
		}
	case ModelError429:
		h.Set("Retry-After", "7")
		writeJSON(w, http.StatusTooManyRequests, fixtures.Read("error_429.json"))
	case ModelError500Echo:
		msg := "upstream failure echo: " + fixtures.CanaryPrompt + " " + fixtures.CanaryCompletion
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "server_error", "param": nil, "code": nil}})
		writeJSON(w, http.StatusInternalServerError, b)
	case ModelUsageOnLastChunk:
		s.stream(w, r, fixtures.Read("chat_stream_usage_on_last_chunk.sse"), false)
	default:
		writeJSON(w, http.StatusNotFound, []byte(`{"error":{"message":"The model does not exist.","type":"invalid_request_error","param":null,"code":"model_not_found"}}`))
	}
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request, stream, includeUsage, gated bool) {
	if !stream {
		writeJSON(w, http.StatusOK, fixtures.Read("chat_completion.json"))
		return
	}
	name := "chat_stream.sse"
	if includeUsage {
		name = "chat_stream_usage.sse"
	}
	s.stream(w, r, fixtures.Read(name), gated)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, data []byte, gated bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	for i, ev := range splitEvents(data) {
		if _, err := w.Write(ev); err != nil {
			s.cancelled <- struct{}{}
			return
		}
		_ = rc.Flush()
		if gated && i == 0 {
			select {
			case <-s.gate:
			case <-r.Context().Done():
				s.cancelled <- struct{}{}
				return
			}
		}
	}
}
