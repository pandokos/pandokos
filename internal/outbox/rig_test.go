package outbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrate"
	"github.com/pandokos/pandokos/internal/store/migrations"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// pgProxy is a TCP proxy to the test database that reads what the client sends: the text of every
// statement (the simple query and the Parse of the extended protocol) and every parameter of every
// Bind. It is how a test sees the statements of the relay without a hook in the store.
type pgProxy struct {
	ln     net.Listener
	mu     sync.Mutex
	stmts  []string
	params []string
}

func newPGProxy(t *testing.T, host string, port int) *pgProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &pgProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c, net.JoinHostPort(host, itoa(int64(port))))
		}
	}()
	return p
}

func (p *pgProxy) addr() (string, int) {
	a := p.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func (p *pgProxy) serve(client net.Conn, target string) {
	defer client.Close()
	server, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer server.Close()
	go func() { _, _ = io.Copy(client, server); _ = client.Close() }()
	r := bufio.NewReader(client)
	// The startup message has no type byte; a request that is not the protocol 3.0 is forwarded as it is.
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint32(head[:])
	rest := make([]byte, n-4)
	if _, err := io.ReadFull(r, rest); err != nil {
		return
	}
	_, _ = server.Write(append(head[:], rest...))
	for {
		var h [5]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(h[1:])
		body := make([]byte, n-4)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		if _, err := server.Write(append(h[:], body...)); err != nil {
			return
		}
		switch h[0] {
		case 'Q':
			p.record(string(bytes.TrimRight(body, "\x00")), nil)
		case 'P': // statement name, query, parameter types
			parts := bytes.SplitN(body, []byte{0}, 3)
			if len(parts) >= 2 {
				p.record(string(parts[1]), nil)
			}
		case 'B': // portal, statement, formats, parameters, result formats
			p.record("", bindParams(body))
		}
	}
}

func bindParams(b []byte) []string {
	skip := func(n int) bool {
		if len(b) < n {
			return false
		}
		b = b[n:]
		return true
	}
	cstr := func() bool {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			return false
		}
		b = b[i+1:]
		return true
	}
	if !cstr() {
		return nil
	}
	if !cstr() || len(b) < 2 {
		return nil
	}
	nf := int(binary.BigEndian.Uint16(b))
	if !skip(2+2*nf) || len(b) < 2 {
		return nil
	}
	np := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	var out []string
	for i := 0; i < np; i++ {
		if len(b) < 4 {
			return out
		}
		l := int(int32(binary.BigEndian.Uint32(b)))
		b = b[4:]
		if l < 0 {
			continue
		}
		if len(b) < l {
			return out
		}
		out = append(out, string(b[:l]))
		b = b[l:]
	}
	return out
}

func (p *pgProxy) record(stmt string, params []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stmt != "" {
		p.stmts = append(p.stmts, strings.Join(strings.Fields(stmt), " "))
	}
	p.params = append(p.params, params...)
}

func (p *pgProxy) statements() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.stmts...)
}

func (p *pgProxy) parameters() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.params...)
}

// sink is a receiver of webhooks that scripts its answers and records every request.
type sink struct {
	*httptest.Server
	mu    sync.Mutex
	got   []delivery
	reply func(d delivery, n int) int // the status for the n-th request for this event ID; nil: 204
	seen  map[string]int
}

type delivery struct {
	Path, ID, Timestamp, Signature string
	Body                           []byte
	At                             time.Time
}

func newSink(t *testing.T, reply func(d delivery, n int) int) *sink {
	t.Helper()
	s := &sink{reply: reply, seen: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		d := delivery{Path: r.URL.Path, ID: r.Header.Get("webhook-id"), Timestamp: r.Header.Get("webhook-timestamp"), Signature: r.Header.Get("webhook-signature"), Body: body, At: time.Now()}
		s.mu.Lock()
		s.got = append(s.got, d)
		s.seen[d.Path+" "+d.ID]++
		n := s.seen[d.Path+" "+d.ID]
		s.mu.Unlock()
		status := 204
		if s.reply != nil {
			status = s.reply(d, n)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sink) url(path string) string {
	return strings.Replace(s.URL, "127.0.0.1", "localhost", 1) + path
}

func (s *sink) deliveries() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]delivery(nil), s.got...)
}

// ids returns the distinct event IDs the path received, sorted, and the number of requests.
func (s *sink) ids(path string) ([]string, int) {
	set := map[string]bool{}
	n := 0
	for _, d := range s.deliveries() {
		if d.Path == path {
			set[d.ID] = true
			n++
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, n
}

func (s *sink) count(path, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[path+" "+id]
}

// hmacSignature is the signature of Standard Webhooks for a key in its text form, computed here and
// not by sign.go.
func hmacSignature(keyText, id, ts string, body []byte) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(keyText, "whsec_"))
	if err != nil {
		panic(err)
	}
	m := hmac.New(sha256.New, raw)
	m.Write([]byte(id + "." + ts + "."))
	m.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// verifies reports whether the header carries a signature that one of the keys makes.
func (d delivery) verifies(keys ...string) bool {
	for _, k := range keys {
		want := hmacSignature(k, d.ID, d.Timestamp, d.Body)
		for _, got := range strings.Fields(d.Signature) {
			if hmac.Equal([]byte(got), []byte(want)) {
				return true
			}
		}
	}
	return false
}

// rig is a relay on a migrated database, its logs, the statements it sends and a sink.
type rig struct {
	t     *testing.T
	d     *dbtest.Database
	relay *Relay
	logs  *testutil.LogBuffer
	proxy *pgProxy
	deliv *Deliverer
}

func (r *rig) pass() (int, error) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return r.relay.Pass(ctx)
}

// drain passes until a pass runs no round, at most 200 times, and returns the number of rounds.
func (r *rig) drain() int {
	r.t.Helper()
	total := 0
	for i := 0; i < 200; i++ {
		n, err := r.pass()
		if err != nil {
			r.t.Fatalf("a pass of the relay: %v", err)
		}
		if n == 0 {
			return total
		}
		total += n
	}
	r.t.Fatal("the relay still had work after 200 passes")
	return total
}

func newRig(t *testing.T, d *dbtest.Database, mutate ...func(*Relay)) *rig {
	t.Helper()
	proxy := newPGProxy(t, d.Host, d.Port)
	host, port := proxy.addr()
	opts := d.ConnOptions("worker")
	opts.Host, opts.Port = host, port
	db, err := store.Open(store.Options{Conn: opts, Role: store.RoleWorker, MaxConns: 8, Migrations: mustMigrations(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	g, err := egress.New(egress.Options{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.CloseIdleConnections)
	deliv := &Deliverer{Transport: g.Transport, PrivateHosts: []string{"localhost"}, Timeout: 3 * time.Second, UserAgent: "pandokos/test", Now: time.Now}
	logs := &testutil.LogBuffer{}
	relay := &Relay{DB: db, Provider: vaulttest.ProviderFor(t, d), Deliverer: deliv, Source: "urn:pandokos", Interval: 20 * time.Millisecond, Workers: 4, BatchSize: 100,
		MaxAttempts: 10, BackoffMax: 15 * time.Minute, Logger: telemetry.NewLogger(logs, "debug")}
	for _, m := range mutate {
		m(relay)
	}
	return &rig{t: t, d: d, relay: relay, logs: logs, proxy: proxy, deliv: deliv}
}

func mustMigrations(t *testing.T) []migrate.Migration {
	t.Helper()
	ms, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// setSQL runs a statement as the superuser.
func setSQL(t *testing.T, d *dbtest.Database, sql string, args ...any) {
	t.Helper()
	if _, err := d.Super(t).Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

// destRow is the state of a destination, read as the superuser.
type destRow struct {
	Cursor, Failures, Expired int64
	Enabled, Leased           bool
	FailingSince              *time.Time
	NextAttempt               time.Time
	LastError                 string
	LastStatus                *int32
	LastSuccess, LastFailure  *time.Time
	TestOutcome               string
	TestStatus                *int32
	PreviousExpires           *time.Time
}

func readDest(t *testing.T, d *dbtest.Database, id string) destRow {
	t.Helper()
	var r destRow
	err := d.Super(t).QueryRow(context.Background(), `SELECT cursor_seq, failures, expired_events, enabled, lease_token IS NOT NULL, failing_since, next_attempt_at, last_error, last_status,
		last_success_at, last_failure_at, test_outcome, test_status, previous_secret_expires_at FROM pandokos.event_destination WHERE id = $1::uuid`, id).
		Scan(&r.Cursor, &r.Failures, &r.Expired, &r.Enabled, &r.Leased, &r.FailingSince, &r.NextAttempt, &r.LastError, &r.LastStatus, &r.LastSuccess, &r.LastFailure, &r.TestOutcome, &r.TestStatus, &r.PreviousExpires)
	if err != nil {
		t.Fatalf("read the destination: %v", err)
	}
	return r
}

type letter struct {
	Seq      int64
	State    string
	Attempts int
	Reason   string
	Status   *int32
}

func readLetters(t *testing.T, d *dbtest.Database, dest string) []letter {
	t.Helper()
	rows, err := d.Super(t).Query(context.Background(), `SELECT seq, state, attempts, reason, status FROM pandokos.outbox_dead_letter WHERE destination_id = $1::uuid ORDER BY seq`, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []letter
	for rows.Next() {
		var l letter
		if err := rows.Scan(&l.Seq, &l.State, &l.Attempts, &l.Reason, &l.Status); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

// due makes the destination due now.
func due(t *testing.T, d *dbtest.Database, dest string) {
	t.Helper()
	setSQL(t, d, `UPDATE pandokos.event_destination SET next_attempt_at = now() - interval '1 second' WHERE id = $1::uuid`, dest)
}

// expireLease makes the lease of the destination end.
func expireLease(t *testing.T, d *dbtest.Database, dest string) {
	t.Helper()
	setSQL(t, d, `UPDATE pandokos.event_destination SET lease_until = now() - interval '1 second' WHERE id = $1::uuid AND lease_token IS NOT NULL`, dest)
}

// eventIDs returns the IDs of the events of the tenant in the order of their sequence numbers.
func eventIDs(t *testing.T, d *dbtest.Database, id tenant.ID) []string {
	t.Helper()
	var out []string
	for _, e := range d.OutboxEvents(t, id) {
		out = append(out, e.ID)
	}
	return out
}

// allowedStatement reports whether a statement sent by the relay is one of plan A2 or a statement of
// the session of the store, and never writes the log of events.
var (
	writesLog   = regexp.MustCompile(`(?i)\b(insert\s+into|update|delete\s+from|truncate(\s+table)?)\s+(only\s+)?(pandokos\.)?(outbox_event|outbox_stream|platform_outbox_event|platform_outbox_stream)\b`)
	sessionStmt = regexp.MustCompile(`(?i)^(begin|commit|rollback|reset|set |select pg_catalog\.set_config|select set_config|discard|deallocate|select 1\b|;?$)`)
	relayStmts  = []string{"outbox_relay_claim", "outbox_relay_state", "tenant_outbox_retention", "pandokos.event_destination", "pandokos.outbox_dead_letter", "pandokos.outbox_event", "pandokos.outbox_stream", "pandokos.tenant"}
)

func checkStatements(t *testing.T, stmts []string) {
	t.Helper()
	for _, s := range stmts {
		if writesLog.MatchString(s) {
			t.Errorf("the relay sent a statement that writes the log of events: %s", s)
		}
		if sessionStmt.MatchString(s) {
			continue
		}
		known := false
		for _, k := range relayStmts {
			known = known || strings.Contains(s, k)
		}
		if !known {
			t.Errorf("the relay sent a statement that is not one of plan A2: %s", s)
		}
	}
}
