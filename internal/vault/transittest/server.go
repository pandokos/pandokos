package transittest

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// The operations Count and the requests of the provider are classed by (sprint 12.1 §5.1).
const (
	OpWrap     = "wrap"
	OpUnwrap   = "unwrap"
	OpDescribe = "describe"
	OpIdentify = "identify"
	OpLookup   = "lookup"
	OpRenew    = "renew"
	OpLogin    = "login"
)

// Request is a request the double received.
type Request struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// faults are the faults in force until Clear.
type faults struct {
	sealed     bool
	status     int
	delay      time.Duration
	hang       bool
	drop       bool
	redirect   string
	ignoreAAD  bool
	echo       bool
	echoExtra  []string
	oversize   bool
	releaseHng chan struct{}
}

// Server is the double. It is safe for concurrent use.
type Server struct {
	tb testing.TB

	mu       sync.Mutex
	scopes   map[string]*Scope
	tokens   map[string]*token
	roles    map[string]*role // by auth mount and role ID
	f        faults
	offset   time.Duration
	reqs     []Request
	plains   [][]byte
	payloads [][]byte

	addr    string
	tlsCfg  *tls.Config
	ln      net.Listener
	srv     *http.Server
	running bool
}

// New starts a double on plain HTTP on 127.0.0.1; it is closed with the test.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := newServer(tb)
	s.listen()
	tb.Cleanup(s.Stop)
	return s
}

// NewTLS starts a double on HTTPS with a throw-away certificate for 127.0.0.1 and localhost, and
// returns the file that holds it as the only authority to trust. (test/testcert is not used: the
// product tree may not import the test tree.)
func NewTLS(tb testing.TB) (*Server, string) {
	tb.Helper()
	cert, caFile := selfSigned(tb)
	s := newServer(tb)
	s.tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.listen()
	tb.Cleanup(s.Stop)
	return s, caFile
}

func newServer(tb testing.TB) *Server {
	s := &Server{tb: tb, scopes: map[string]*Scope{}, tokens: map[string]*token{}, roles: map[string]*role{}}
	s.f.releaseHng = make(chan struct{})
	s.scopes[""] = newScope(s, "")
	return s
}

// listen opens the listener (on the address kept by an earlier Stop, when there is one) and serves.
func (s *Server) listen() {
	s.tb.Helper()
	addr := s.addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	var ln net.Listener
	var err error
	var lc net.ListenConfig
	for i := 0; i < 50; i++ {
		if ln, err = lc.Listen(context.Background(), "tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		s.tb.Fatalf("transittest: %v", err)
	}
	s.addr = ln.Addr().String()
	if s.tlsCfg != nil {
		ln = tls.NewListener(ln, s.tlsCfg)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	s.mu.Lock()
	s.ln, s.srv, s.running = ln, srv, true
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
}

// URL returns the address of the double, with its scheme.
func (s *Server) URL() string {
	if s.tlsCfg != nil {
		return "https://" + s.addr
	}
	return "http://" + s.addr
}

// Stop closes the listener and the connections; the state is kept.
func (s *Server) Stop() {
	s.mu.Lock()
	srv, run := s.srv, s.running
	s.running = false
	s.mu.Unlock()
	if run {
		_ = srv.Close()
	}
}

// Start listens again on the same address.
func (s *Server) Start() {
	s.tb.Helper()
	s.mu.Lock()
	run := s.running
	s.mu.Unlock()
	if !run {
		s.listen()
	}
}

// now is the clock of the double: the real one and what Advance added.
func (s *Server) now() time.Time { return time.Now().Add(s.offset) }

// Advance moves the clock of the double, which counts token lifetimes.
func (s *Server) Advance(d time.Duration) {
	s.mu.Lock()
	s.offset += d
	s.mu.Unlock()
}

func (s *Server) setFault(f func(*faults)) {
	s.mu.Lock()
	f(&s.f)
	s.mu.Unlock()
}

// Sealed makes every request answer 503 {"errors":["Vault is sealed"]}.
func (s *Server) Sealed() { s.setFault(func(f *faults) { f.sealed = true }) }

// Status makes every request answer with that status and an error body.
func (s *Server) Status(code int) { s.setFault(func(f *faults) { f.status = code }) }

// Delay makes every answer wait for d.
func (s *Server) Delay(d time.Duration) { s.setFault(func(f *faults) { f.delay = d }) }

// Hang makes the double accept requests and never answer.
func (s *Server) Hang() { s.setFault(func(f *faults) { f.hang = true }) }

// Drop closes the connection after reading the request.
func (s *Server) Drop() { s.setFault(func(f *faults) { f.drop = true }) }

// Redirect answers 307 to the address.
func (s *Server) Redirect(to string) { s.setFault(func(f *faults) { f.redirect = to }) }

// IgnoreAssociatedData accepts associated_data and ignores it: what Vault before 1.13 does.
func (s *Server) IgnoreAssociatedData() { s.setFault(func(f *faults) { f.ignoreAAD = true }) }

// EchoInErrors makes every error body repeat the request's token, its body and extra.
func (s *Server) EchoInErrors(extra ...string) {
	s.setFault(func(f *faults) { f.echo, f.echoExtra = true, append([]string(nil), extra...) })
}

// OversizeAnswers makes every request answer 200 with more than 64 KiB.
func (s *Server) OversizeAnswers() { s.setFault(func(f *faults) { f.oversize = true }) }

// Clear ends every fault; the state is kept.
func (s *Server) Clear() {
	s.mu.Lock()
	close(s.f.releaseHng)
	s.f = faults{releaseHng: make(chan struct{})}
	s.mu.Unlock()
}

// Requests returns what the double received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Count returns the number of requests of an operation (the Op constants).
func (s *Server) Count(operation string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.reqs {
		if operationOf(r.Method, r.Path) == operation {
			n++
		}
	}
	return n
}

// Plaintexts returns every value the double encrypted or decrypted.
func (s *Server) Plaintexts() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copies(s.plains)
}

// Payloads returns every decoded ciphertext payload the double produced or received.
func (s *Server) Payloads() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copies(s.payloads)
}

// Reset forgets what the double saw and keeps its state.
func (s *Server) Reset() {
	s.mu.Lock()
	s.reqs, s.plains, s.payloads = nil, nil, nil
	s.mu.Unlock()
}

func copies(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i, b := range in {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

// operationOf classes a request by the operations of §5.1; "" is none of them.
func operationOf(method, path string) string {
	p := strings.TrimPrefix(path, "/v1/")
	switch {
	case p == "auth/token/lookup-self":
		return OpLookup
	case p == "auth/token/renew-self":
		return OpRenew
	case strings.HasPrefix(p, "auth/") && strings.HasSuffix(p, "/login"):
		return OpLogin
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		switch {
		case seg == "encrypt" && method == http.MethodPost:
			return OpWrap
		case seg == "decrypt" && method == http.MethodPost:
			return OpUnwrap
		case seg == "hmac" && method == http.MethodPost:
			return OpIdentify
		case seg == "keys" && method == http.MethodGet && i == len(segs)-2:
			return OpDescribe
		}
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
