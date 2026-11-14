// Package fakeprovider is a fake OpenAI-compatible upstream for hermetic tests.
package fakeprovider

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	// ModelRedirect answers the status of the request header X-Fake-Status (default 307) with
	// Location set by RedirectTo.
	ModelRedirect = "fake-redirect"
	// ModelEchoAuth answers 200 (or X-Fake-Status) and copies the Authorization it received into
	// the response headers X-Echo-Authorization and Www-Authenticate.
	ModelEchoAuth = "fake-echo-auth"
	// ModelClose hijacks the connection and closes it without a response.
	ModelClose = "fake-close"
)

// Request is a request the fake received.
type Request struct {
	Method, Path, RawQuery, Proto, Host string
	ContentLength                       int64
	Header                              http.Header
	Body                                []byte
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
	accepted  map[string]bool
	models    string
	redirect  string
	conns     atomic.Int64
}

// New starts a plain HTTP/1.1 fake on 127.0.0.1; it is closed through tb.Cleanup.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := newServer()
	s.ts = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.countConnections()
	s.ts.Start()
	tb.Cleanup(s.close)
	return s
}

// countConnections makes the server count the connections it accepts.
func (s *Server) countConnections() {
	s.ts.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			s.conns.Add(1)
		}
	}
}

// NewTLS starts a fake with TLS and HTTP/2 enabled.
func NewTLS(tb testing.TB) *Server {
	tb.Helper()
	s := newServer()
	s.ts = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.ts.EnableHTTP2 = true
	s.countConnections()
	s.ts.StartTLS()
	tb.Cleanup(s.close)
	return s
}

func newServer() *Server {
	return &Server{gate: make(chan struct{}), cancelled: make(chan struct{}, 1024),
		accepted: map[string]bool{fixtures.CanaryProviderKey: true}}
}

// Accept replaces the set of credentials the fake accepts, for GET /v1/models (a bearer token,
// or the x-api-key of a request that carries anthropic-version) and for chat completions (a
// bearer token). The default is the canary provider key.
func (s *Server) Accept(keys ...string) {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	s.mu.Lock()
	s.accepted = set
	s.mu.Unlock()
}

// RedirectTo sets the Location of the model fake-redirect.
func (s *Server) RedirectTo(location string) {
	s.mu.Lock()
	s.redirect = location
	s.mu.Unlock()
}

// Connections returns the number of connections the server accepted.
func (s *Server) Connections() int { return int(s.conns.Load()) }

// ModelsBehaviour makes GET /v1/models misbehave: "403", "429", "500" answer that status,
// "redirect" answers 302 to another path of the fake that answers 200, and "stall" gives no
// answer until the request is cancelled. The empty string restores the normal behaviour.
func (s *Server) ModelsBehaviour(b string) {
	s.mu.Lock()
	s.models = b
	s.mu.Unlock()
}

// modelsMovedPath is where the "redirect" behaviour sends the client.
const modelsMovedPath = "/v1/models-moved"

// handleModels answers the credential check of the vault validator.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	presented, ok := credential(r)
	s.mu.Lock()
	behaviour := s.models
	accepted := ok && s.accepted[presented]
	s.mu.Unlock()

	if r.URL.Path == modelsMovedPath {
		writeJSON(w, http.StatusOK, []byte(`{"object":"list","data":[]}`))
		return
	}
	switch behaviour {
	case "403":
		writeJSON(w, http.StatusForbidden, []byte(`{"error":{"message":"forbidden","type":"invalid_request_error","param":null,"code":null}}`))
		return
	case "429":
		writeJSON(w, http.StatusTooManyRequests, fixtures.Read("error_429.json"))
		return
	case "500":
		writeJSON(w, http.StatusInternalServerError, []byte(`{"error":{"message":"upstream failure","type":"server_error","param":null,"code":null}}`))
		return
	case "redirect":
		http.Redirect(w, r, modelsMovedPath, http.StatusFound)
		return
	case "stall":
		<-r.Context().Done()
		s.cancelled <- struct{}{}
		return
	}
	if accepted {
		writeJSON(w, http.StatusOK, []byte(`{"object":"list","data":[]}`))
		return
	}
	msg := "Incorrect API key provided: " + presented + "."
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "param": nil, "code": "invalid_api_key"}})
	writeJSON(w, http.StatusUnauthorized, b)
}

// credential returns the credential a request presents: x-api-key for a request that carries
// anthropic-version, otherwise the bearer token.
func credential(r *http.Request) (string, bool) {
	if r.Header.Get("Anthropic-Version") != "" {
		v := r.Header.Get("X-Api-Key")
		return v, v != ""
	}
	v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return v, ok
}

func (s *Server) close() {
	s.Release()
	s.ts.Close()
}

// URL returns scheme://127.0.0.1:port, the base URL of the endpoint of a provider in the
// catalogue (the native path, which includes /v1, is appended to it).
func (s *Server) URL() string { return s.ts.URL }

// CertPool returns a pool that holds the certificate of a TLS server (nil for a plain one), for
// the RootCAs of a guard or a client that must trust it.
func (s *Server) CertPool() *x509.CertPool {
	cert := s.ts.Certificate()
	if cert == nil {
		return nil
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

// CertPEM returns the certificate of a TLS server as PEM (nil for a plain one), for a file that a
// process under test reads as its CA file.
func (s *Server) CertPEM() []byte {
	cert := s.ts.Certificate()
	if cert == nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

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
		Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Proto: r.Proto, Host: r.Host,
		ContentLength: r.ContentLength, Header: r.Header.Clone(), Body: body,
	})
	s.mu.Unlock()

	if r.Method == http.MethodGet && (r.URL.Path == "/v1/models" || r.URL.Path == modelsMovedPath) {
		s.handleModels(w, r)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		writeJSON(w, http.StatusNotFound, []byte(`{"error":{"message":"not found","type":"invalid_request_error","param":null,"code":null}}`))
		return
	}
	presented, hasCred := r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
	s.mu.Lock()
	ok := s.accepted[strings.TrimPrefix(presented, "Bearer ")] && strings.HasPrefix(presented, "Bearer ")
	s.mu.Unlock()
	if !ok {
		if hasCred { // a provider that quotes what it was given in its headers
			w.Header().Set("X-Echo-Authorization", presented)
			w.Header().Set("Www-Authenticate", presented)
		}
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
	case ModelRedirect:
		s.mu.Lock()
		location := s.redirect
		s.mu.Unlock()
		status := http.StatusTemporaryRedirect
		if v, err := strconv.Atoi(r.Header.Get("X-Fake-Status")); err == nil && v >= 300 && v < 400 {
			status = v
		}
		h.Set("Location", location)
		w.WriteHeader(status)
	case ModelEchoAuth:
		status := http.StatusOK
		if v, err := strconv.Atoi(r.Header.Get("X-Fake-Status")); err == nil && v >= 200 && v < 600 {
			status = v
		}
		h.Set("X-Echo-Authorization", r.Header.Get("Authorization"))
		h.Set("Www-Authenticate", r.Header.Get("Authorization"))
		writeJSON(w, status, fixtures.Read("chat_completion.json"))
	case ModelClose:
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
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
