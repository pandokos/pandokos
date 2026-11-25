package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/examples/webhook-receiver/receiver"
	"github.com/pandokos/pandokos/test/fixtures"
)

// The helpers of the end-to-end tests of sprint 6.4: a receiver that scripts faults in front of the
// example receiver, and the commands that make a destination.

// dupStore is the store of the receiver: it keeps each ID once and counts what it saw twice.
type dupStore struct {
	mu   sync.Mutex
	got  map[string]receiver.Event
	dups int
}

func (s *dupStore) Put(id string, e receiver.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.got[id]; seen {
		s.dups++
		return false, nil
	}
	s.got[id] = e
	return true, nil
}

func (s *dupStore) snapshot() (map[string]receiver.Event, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]receiver.Event, len(s.got))
	for k, v := range s.got {
		out[k] = v
	}
	return out, s.dups
}

func (s *dupStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

// faultReceiver is the example receiver behind a listener of 127.0.0.1, reached as localhost, whose
// answers are scripted: ok, refuse (the connection is closed at once), 500, stall (the request is read
// and never answered) and late (the answer comes after the client gave up, and the event is stored).
type faultReceiver struct {
	t        *testing.T
	ln       net.Listener
	conns    chan net.Conn
	srv      *http.Server
	mode     atomic.Value
	store    *dupStore
	handler  *receiver.Handler
	stop     chan struct{}
	requests atomic.Int64
	lateWait time.Duration
}

type chanListener struct {
	conns chan net.Conn
	addr  net.Addr
	done  chan struct{}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

func newFaultReceiver(t *testing.T, path string) *faultReceiver {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &faultReceiver{t: t, ln: ln, conns: make(chan net.Conn, 64), stop: make(chan struct{}), store: &dupStore{got: map[string]receiver.Event{}}, lateWait: 2500 * time.Millisecond}
	f.mode.Store("ok")
	f.handler = &receiver.Handler{Store: f.store}
	cl := &chanListener{conns: f.conns, addr: ln.Addr(), done: f.stop}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = f.srv.Serve(cl) }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if f.mode.Load() == "refuse" {
				_ = c.Close()
				continue
			}
			select {
			case f.conns <- c:
			case <-f.stop:
				_ = c.Close()
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(f.stop)
		_ = ln.Close()
		_ = f.srv.Close()
	})
	_ = path
	return f
}

func (f *faultReceiver) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	switch f.mode.Load() {
	case "500":
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
	case "stall":
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-f.stop:
		}
	case "late":
		body, _ := io.ReadAll(r.Body)
		select {
		case <-time.After(f.lateWait):
		case <-f.stop:
			return
		}
		req := r.Clone(context.Background())
		req.Body = io.NopCloser(bytes.NewReader(body))
		f.handler.ServeHTTP(httptest.NewRecorder(), req)
	default:
		f.handler.ServeHTTP(w, r)
	}
}

func (f *faultReceiver) setMode(m string) { f.mode.Store(m) }

func (f *faultReceiver) setSecret(secret string) { f.handler.Secrets = []string{secret} }

// url is the address the destination is given: the host localhost, which the binary lists as private.
func (f *faultReceiver) url(path string) string {
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	return "http://localhost:" + port + path
}

var secretRe = regexp.MustCompile(`whsec_[A-Za-z0-9+/]{43}=`)

// newDestination makes a destination through the command line as the tenant's administrator and
// returns its ID and its signing secret.
func newDestination(t *testing.T, p *proc, adminFile, tenantID, name, url string, flags ...string) (id, secret string) {
	t.Helper()
	args := append([]string{"event-destination", "create", "--tenant", tenantID, "--url", url}, flags...)
	out := p.mustCLI(t, adminFile, append(args, name)...)
	secret = secretRe.FindString(out)
	if secret == "" {
		t.Fatalf("event-destination create printed no signing secret:\n%s", out)
	}
	list := cliJSON(t, p, adminFile, "event-destination", "list", "--tenant", tenantID)
	items, _ := list["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["name"] == name {
			id, _ = m["id"].(string)
		}
	}
	if id == "" {
		t.Fatalf("no destination %s in %v", name, list)
	}
	return id, secret
}

// destinationState reads the delivery of a destination through the command line.
type delivery struct {
	State       string `json:"state"`
	Cursor      int64  `json:"cursor"`
	Head        int64  `json:"head"`
	Failures    int    `json:"failures"`
	DeadLetters int    `json:"dead_letters"`
}

func destinationDelivery(t *testing.T, p *proc, adminFile, tenantID, id string) delivery {
	t.Helper()
	out := p.mustCLI(t, adminFile, "event-destination", "get", "--tenant", tenantID, id, "-o", "json")
	var d struct {
		Delivery delivery `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("event-destination get: %v\n%s", err, out)
	}
	return d.Delivery
}

// quietCall is one call through the gateway that never fails the test: the status, or -1 when the
// process did not answer (it is being killed or restarted).
func quietCall(client *http.Client, url, key, model string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(chatBody(model, false, fixtures.CanaryPrompt)))
	if err != nil {
		return -1
	}
	req.Header = authHeader(key)
	resp, err := client.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// ledgerOf returns the usage records of the tenant: record ID to request ID.
func ledgerOf(t *testing.T, p *proc, tenantID string) map[string]string {
	t.Helper()
	out := map[string]string{}
	p.db.WithSuper(t, func(ctx context.Context, c *pgx.Conn) { // tests poll this: no connection is kept
		rows, err := c.Query(ctx, `SELECT id::text, request_id FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, req string
			if err := rows.Scan(&id, &req); err != nil {
				t.Fatal(err)
			}
			out[id] = req
		}
	})
	return out
}

// assertNoEventSecrets checks the text of an output (a process log, a scrape) for what must never be
// in one: the tenant, the destination, the URL and its host, the signing secret.
func assertNoEventSecrets(t *testing.T, what, text string, forbidden map[string]string) {
	t.Helper()
	for name, v := range forbidden {
		if v != "" && strings.Contains(text, v) {
			i := strings.Index(text, v)
			lo, hi := max(0, i-80), min(len(text), i+len(v)+80)
			t.Errorf("%s holds %s: ...%s...", what, name, text[lo:hi])
		}
	}
}
