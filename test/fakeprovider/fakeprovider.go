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
	// ModelNoUsage answers 2xx without a usage object, for every operation, streaming or not.
	ModelNoUsage = "fake-no-usage"
	// ModelStreamError streams two content events and then an error event in the middle of the
	// stream, and ends without a final marker.
	ModelStreamError = "fake-stream-error"
	// ModelKeepalive streams with a comment event (": ping") before every event.
	ModelKeepalive = "fake-keepalive"
	// ModelTools streams tool-call deltas.
	ModelTools = "fake-tools"
	// ModelTinyWrites writes one byte at a time and flushes after each byte.
	ModelTinyWrites = "fake-tiny-writes"
	// ModelCut hijacks the connection, sends half of the answer and closes it.
	ModelCut = "fake-cut"
	// ModelCRLF streams with CRLF line endings.
	ModelCRLF = "fake-crlf"
	// ModelBigEvent sends an event (or, for a plain answer, a body) over the bounds of the readers.
	ModelBigEvent = "fake-big-event"
)

// Sizes of the oversized event or body of ModelBigEvent: over the 1 MiB bound of the chat reader,
// the 4 MiB bound of the responses reader and the 4 MiB bound of a buffered JSON answer.
const (
	BigChatEventBytes      = 1<<20 + 1024
	BigResponsesEventBytes = 4<<20 + 1024
	BigBodyBytes           = 4<<20 + 1024
)

// The operations the fake serves, by native path.
const (
	pathChat       = "/v1/chat/completions"
	pathResponses  = "/v1/responses"
	pathEmbeddings = "/v1/embeddings"
)

// Request is a request the fake received.
type Request struct {
	Method, Path, EscapedPath, RawQuery, Proto, Host string
	ContentLength                                    int64
	Header                                           http.Header
	Body                                             []byte
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
	stepping  atomic.Bool
	steps     chan struct{}
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
	return &Server{gate: make(chan struct{}), cancelled: make(chan struct{}, 1024), steps: make(chan struct{}, 4096),
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

// GateEachEvent makes every stream of every operation wait, after each event but the last, until
// Advance is called (or Release, which opens every gate for good). The provider then never sends
// event n+1 before the test has asked for it, so a test proves "no buffering" without timing.
func (s *Server) GateEachEvent() { s.stepping.Store(true) }

// Advance lets one gated stream send its next event.
func (s *Server) Advance() { s.steps <- struct{}{} }

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

// splitEvents splits a stream into events, each with its terminating blank line (LF or CRLF).
func splitEvents(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		lf, crlf := bytes.Index(b, []byte("\n\n")), bytes.Index(b, []byte("\r\n\r\n"))
		end := -1
		switch {
		case lf >= 0 && (crlf < 0 || lf < crlf):
			end = lf + 2
		case crlf >= 0:
			end = crlf + 4
		}
		if end < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:end])
		b = b[end:]
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
		Method: r.Method, Path: r.URL.Path, EscapedPath: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery, Proto: r.Proto, Host: r.Host,
		ContentLength: r.ContentLength, Header: r.Header.Clone(), Body: body,
	})
	s.mu.Unlock()

	if r.Method == http.MethodGet && (r.URL.Path == "/v1/models" || r.URL.Path == modelsMovedPath) {
		s.handleModels(w, r)
		return
	}
	op := r.URL.Path
	if r.Method != http.MethodPost || (op != pathChat && op != pathResponses && op != pathEmbeddings) {
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
		s.answer(w, r, op, req.Stream, includeUsage, false)
	case ModelDetails:
		writeJSON(w, http.StatusOK, oneOf(op, "chat_completion_details.json"))
	case ModelGatedStream:
		s.answer(w, r, op, true, includeUsage, true)
	case ModelNoUsage, ModelStreamError, ModelKeepalive, ModelTools, ModelTinyWrites, ModelCut, ModelCRLF, ModelBigEvent:
		s.behave(w, r, op, req.Model, req.Stream)
	case ModelHang:
		select {
		case <-s.gate:
			s.answer(w, r, op, req.Stream, includeUsage, false)
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
		writeJSON(w, status, oneOf(op, "chat_completion.json"))
	case ModelClose:
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	case ModelUsageOnLastChunk:
		if op != pathChat {
			s.answer(w, r, op, req.Stream, includeUsage, false)
			return
		}
		s.stream(w, r, fixtures.Read("chat_stream_usage_on_last_chunk.sse"), false)
	default:
		writeJSON(w, http.StatusNotFound, []byte(`{"error":{"message":"The model does not exist.","type":"invalid_request_error","param":null,"code":"model_not_found"}}`))
	}
}

// oneOf returns the JSON fixture of the operation op: chat is the one of chat completions.
func oneOf(op, chat string) []byte {
	switch op {
	case pathResponses:
		return fixtures.Read("responses.json")
	case pathEmbeddings:
		return fixtures.Read("embeddings.json")
	}
	return fixtures.Read(chat)
}

// streams reports whether the operation can stream.
func streams(op string) bool { return op == pathChat || op == pathResponses }

func (s *Server) answer(w http.ResponseWriter, r *http.Request, op string, stream, includeUsage, gated bool) {
	if !stream || !streams(op) {
		writeJSON(w, http.StatusOK, oneOf(op, "chat_completion.json"))
		return
	}
	if op == pathResponses {
		s.stream(w, r, fixtures.Read("responses_stream.sse"), gated)
		return
	}
	name := "chat_stream.sse"
	if includeUsage {
		name = "chat_stream_usage.sse"
	}
	s.stream(w, r, fixtures.Read(name), gated)
}

// dropUsage removes the usage object from a JSON document, at the top level and under "response".
func dropUsage(b []byte) []byte {
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return b
	}
	delete(doc, "usage")
	if resp, ok := doc["response"].(map[string]any); ok {
		delete(resp, "usage")
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return b
	}
	return out
}

// dropUsageSSE removes the usage of every event of a stream that carries JSON data.
func dropUsageSSE(b []byte) []byte {
	var out []byte
	for _, ev := range splitEvents(b) {
		var lines []string
		for _, line := range strings.SplitAfter(string(ev), "\n") {
			if payload, ok := strings.CutPrefix(line, "data: "); ok && strings.HasPrefix(payload, "{") {
				eol := payload[len(strings.TrimRight(payload, "\r\n")):]
				line = "data: " + string(dropUsage([]byte(strings.TrimRight(payload, "\r\n")))) + eol
			}
			lines = append(lines, line)
		}
		out = append(out, strings.Join(lines, "")...)
	}
	return out
}

// base returns the usual answer of the operation: its stream when stream is true and the
// operation streams, its JSON document otherwise; withUsage chooses the variant of the chat
// stream that carries usage.
func base(op string, stream bool) (data []byte, isStream bool) {
	switch {
	case stream && op == pathResponses:
		return fixtures.Read("responses_stream.sse"), true
	case stream && op == pathChat:
		return fixtures.Read("chat_stream_usage.sse"), true
	}
	return oneOf(op, "chat_completion.json"), false
}

// behave serves the behaviours of the models of section 7 of the sprint 3.2 design, for every
// operation.
func (s *Server) behave(w http.ResponseWriter, r *http.Request, op, model string, stream bool) {
	data, isStream := base(op, stream)
	switch model {
	case ModelNoUsage:
		if isStream {
			if op == pathChat {
				data = fixtures.Read("chat_stream.sse")
			}
			s.stream(w, r, dropUsageSSE(data), false)
			return
		}
		writeJSON(w, http.StatusOK, dropUsage(data))
	case ModelStreamError:
		if !isStream {
			writeJSON(w, http.StatusInternalServerError, []byte(`{"error":{"message":"synthetic upstream failure","type":"server_error","param":null,"code":null}}`))
			return
		}
		if op == pathChat {
			s.stream(w, r, fixtures.Read("chat_stream_error.sse"), false)
			return
		}
		evs := splitEvents(data)
		out := bytes.Join(evs[:3], nil)
		out = append(out, "event: error\ndata: {\"type\":\"error\",\"code\":\"synthetic\",\"message\":\"synthetic upstream failure\"}\n\n"...)
		s.stream(w, r, out, false)
	case ModelKeepalive:
		if !isStream {
			writeJSON(w, http.StatusOK, data)
			return
		}
		var out []byte
		for _, ev := range splitEvents(data) {
			out = append(out, ": ping\n\n"...)
			out = append(out, ev...)
		}
		s.stream(w, r, out, false)
	case ModelTools:
		switch {
		case !isStream:
			writeJSON(w, http.StatusOK, data)
		case op == pathChat:
			s.stream(w, r, fixtures.Read("chat_stream_tools.sse"), false)
		default:
			s.stream(w, r, bytes.ReplaceAll(data, []byte("response.output_text.delta"), []byte("response.function_call_arguments.delta")), false)
		}
	case ModelCRLF:
		if !isStream {
			writeJSON(w, http.StatusOK, data)
			return
		}
		if op == pathChat {
			s.stream(w, r, fixtures.Read("chat_stream_keepalive_crlf.sse"), false)
			return
		}
		s.stream(w, r, bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n")), false)
	case ModelTinyWrites:
		ct := "application/json"
		if isStream {
			ct = "text/event-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		for i := range data {
			if _, err := w.Write(data[i : i+1]); err != nil {
				s.cancelled <- struct{}{}
				return
			}
			_ = rc.Flush()
		}
	case ModelCut:
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		half := data[:len(data)/2]
		if isStream {
			_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"+strconv.FormatInt(int64(len(half)), 16)+"\r\n")
			_, _ = conn.Write(half)
			_, _ = io.WriteString(conn, "\r\n")
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(len(data))+"\r\n\r\n")
		_, _ = conn.Write(half)
	case ModelBigEvent:
		s.big(w, r, op, data, isStream)
	}
}

// big answers with an event over the bound of the reader of the operation, followed by the usual
// events (a stream), or with a JSON body over the bound of a buffered answer.
func (s *Server) big(w http.ResponseWriter, r *http.Request, op string, data []byte, isStream bool) {
	if !isStream {
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil {
			writeJSON(w, http.StatusOK, data)
			return
		}
		doc["pad"] = strings.Repeat("a", BigBodyBytes)
		b, _ := json.Marshal(doc)
		writeJSON(w, http.StatusOK, b)
		return
	}
	var first []byte
	if op == pathResponses {
		pad := strings.Repeat("a", BigResponsesEventBytes)
		first = []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + pad + "\"}\n\n")
	} else {
		pad := strings.Repeat("a", BigChatEventBytes)
		first = []byte("data: {\"id\":\"chatcmpl-fixture0005\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + pad + "\"},\"finish_reason\":null}],\"usage\":null}\n\n")
	}
	s.stream(w, r, append(first, data...), false)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, data []byte, gated bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	events := splitEvents(data)
	for i, ev := range events {
		if _, err := w.Write(ev); err != nil {
			s.cancelled <- struct{}{}
			return
		}
		_ = rc.Flush()
		switch {
		case gated && i == 0:
			if !s.wait(r, s.gate) {
				return
			}
		case s.stepping.Load() && i < len(events)-1:
			if !s.wait(r, s.steps) {
				return
			}
		}
	}
}

// wait blocks until c delivers (or, for the step channel, Release opens the gate for good) or the
// request is cancelled; it reports false after a cancellation.
func (s *Server) wait(r *http.Request, c chan struct{}) bool {
	select {
	case <-c:
		return true
	case <-s.gate:
		return true
	case <-r.Context().Done():
		s.cancelled <- struct{}{}
		return false
	}
}
