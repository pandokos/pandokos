package lifecycle_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/lifecycle"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	return c
}

// recordingProxy forwards the connections of one client to the test database and records the text of
// every statement the client sends (simple queries and the Parse messages of the extended protocol).
type recordingProxy struct {
	addr string
	mu   sync.Mutex
	sqls []string
}

func newRecordingProxy(t *testing.T, target string) *recordingProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &recordingProxy{addr: l.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); p.serve(c, target) }()
		}
	}()
	return p
}

func (p *recordingProxy) serve(client net.Conn, target string) {
	defer client.Close()
	server, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer server.Close()
	go func() { _, _ = io.Copy(client, server) }()
	r := bufio.NewReader(client)
	// The startup message has no type byte: a length, then the rest.
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return
	}
	rest := make([]byte, binary.BigEndian.Uint32(head[:])-4)
	if _, err := io.ReadFull(r, rest); err != nil {
		return
	}
	if _, err := server.Write(append(head[:], rest...)); err != nil {
		return
	}
	for {
		typ, err := r.ReadByte()
		if err != nil {
			return
		}
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(head[:])-4)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		switch typ {
		case 'Q':
			p.record(string(body[:max(0, len(body)-1)]))
		case 'P': // name, query, parameter types
			if _, q, ok := strings.Cut(string(body), "\x00"); ok {
				query, _, _ := strings.Cut(q, "\x00")
				p.record(query)
			}
		}
		if _, err := server.Write(append(append([]byte{typ}, head[:]...), body...)); err != nil {
			return
		}
	}
}

func (p *recordingProxy) record(sql string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sqls = append(p.sqls, sql)
}

func (p *recordingProxy) statements() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sqls...)
}

func (p *recordingProxy) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sqls = nil
}

// workerVia opens the worker pool of the database through a proxy.
func workerVia(t *testing.T, d *dbtest.Database, addr string) *store.DB {
	t.Helper()
	host, portText, _ := net.SplitHostPort(addr)
	opts := d.ConnOptions("worker")
	opts.Host = host
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	opts.Port = port
	ms, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(store.Options{Conn: opts, Role: store.RoleWorker, MaxConns: 2, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// clock is a clock the test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Now()} }

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// deleter returns a Deleter on the pool with a debug log in logs and the clock.
func deleter(db *store.DB, logs *testutil.LogBuffer, c *clock) *lifecycle.Deleter {
	return &lifecycle.Deleter{DB: db, Interval: time.Minute, Logger: telemetry.NewLogger(logs, "debug"), Now: c.Now}
}

// seedLedger writes n records of the tenant (the default partition and the current month), with
// identifiers that name a caller, and metadata that holds a canary.
func seedLedger(t *testing.T, d *dbtest.Database, id tenant.ID, n int) {
	t.Helper()
	_, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, key_id, provider, api_family,
			operation, model, outcome, duration_ms, input_source, output_source, cost_status, metadata)
		SELECT $1::uuid, pandokos.uuid_v7(), now() - make_interval(secs => g) - CASE WHEN g % 2 = 0 THEN interval '14 months' ELSE interval '0' END,
		       md5(g::text || $1::text), 'service_account', '0192f0f0-3333-7000-8000-00000000000' || (g % 5), 't' || translate(substr(md5((g % 3)::text), 1, 16), '0189', 'ghij'),
		       'openai', 'openai', 'chat.completions', 'gpt-x', 'ok', g, 'none', 'none', 'none', jsonb_build_object('team', 'canary-team')
		  FROM generate_series(1, $2::int) g`, id.String(), n)
	if err != nil {
		t.Fatalf("seed the ledger: %v", err)
	}
}

func text(t *testing.T, d *dbtest.Database, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := d.Super(t).QueryRow(ctx(t), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	return s
}

func number(t *testing.T, d *dbtest.Database, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.Super(t).QueryRow(ctx(t), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	return n
}

func stepOf(t *testing.T, d *dbtest.Database, id tenant.ID) string {
	t.Helper()
	return text(t, d, `SELECT state || '/' || step FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String())
}
