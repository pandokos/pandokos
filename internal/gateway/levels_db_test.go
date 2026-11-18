package gateway_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/ledger"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// levelsEnv is a gateway over a real rule cache on the test database, with its listener, and the
// callers of one tenant: two keys of one service account, a key of another one and a token.
type levelsEnv struct {
	h      *harness
	d      *dbtest.Database
	tenant tenant.ID
	hdrs   map[string]http.Header // by caller name
}

func newLevelsEnv(t *testing.T, d *dbtest.Database) *levelsEnv {
	t.Helper()
	tn := d.SeedTenant(t, "levels-"+testutil.RandomUUID()[:8])
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	logs := &testutil.LogBuffer{}
	cache, err := limits.NewRuleCache(limits.RuleCacheOptions{DB: d.Open(t, store.RoleGateway), Logger: quiet, TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	l := limitstest.WithCache(t, cache)
	lis, err := store.NewListener(store.ListenerOptions{Conn: d.ConnOptions("gateway"), Role: store.RoleGateway,
		Logger: telemetry.NewLogger(logs, "debug"), OnNotify: cache.Invalidate, OnReset: cache.Flush})
	if err != nil {
		t.Fatal(err)
	}
	lctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { lis.Run(lctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	testutil.Eventually(t, 10*time.Second, "the cache listener to connect", func() bool {
		return strings.Contains(logs.String(), `"msg":"cache listener connected"`)
	})

	principals := map[string]authz.Principal{}
	hdrs := map[string]http.Header{}
	add := func(name, sa, key, issuer string) {
		p := testutil.InferencePrincipal()
		p.TenantID, p.ID, p.KeyID, p.IssuerID = tn, sa, key, issuer
		if issuer != "" {
			p.Subject = "user-" + name
		}
		k := testutil.NewKey(t)
		principals[k] = p
		hdrs[name] = http.Header{"Authorization": {"Bearer " + k}, "Content-Type": {"application/json"}}
	}
	add("key-1", "sa-1", "key-1", "")
	add("key-2", "sa-1", "key-2", "")
	add("other-sa", "sa-2", "key-3", "")
	add("token", "sa-1", "mapping-1", "issuer-1")

	h := newHarness(t, func(o *gateway.Options) {
		o.Authenticator = testutil.StaticAuthenticator(principals)
		o.Limits = l
	})
	return &levelsEnv{h: h, d: d, tenant: tn, hdrs: hdrs}
}

func (e *levelsEnv) call(t *testing.T, who string) captured {
	t.Helper()
	return e.h.send(t, http.MethodPost, route, chat("fake-basic", false), e.hdrs[who])
}

// admits sends n requests as who and returns how many were admitted before the first refusal, and
// the first refusal.
func (e *levelsEnv) admits(t *testing.T, who string, n int) (admitted int, refusal *captured) {
	t.Helper()
	for i := 0; i < n; i++ {
		c := e.call(t, who)
		if c.status == 200 {
			admitted++
			continue
		}
		if c.status != 429 {
			t.Fatalf("%s: status %d %s", who, c.status, c.body)
		}
		return admitted, &c
	}
	return admitted, nil
}

// TestLevels is T-639: rules written to the database through the admin role, the real rule cache
// and the real store, behind the gateway.
func TestLevels(t *testing.T) {
	d := dbtest.NewMigrated(t)

	t.Run("a platform ceiling below the tenant rule wins", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedPlatformLimit(t, &e.tenant, "requests_per_minute", 2)
		d.SeedLimitRule(t, e.tenant, "tenant", "", "requests_per_minute", 5)
		n, ref := e.admits(t, "key-1", 10)
		if n != 2 || ref == nil || ref.header.Get("X-Pandokos-Limit") != "2" || ref.header.Get("X-Pandokos-Limit-Subject") != "tenant" {
			t.Fatalf("%d admitted, refusal %v", n, ref)
		}
	})

	t.Run("a tenant rule below the platform ceiling wins", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedPlatformLimit(t, &e.tenant, "requests_per_minute", 5)
		d.SeedLimitRule(t, e.tenant, "tenant", "", "requests_per_minute", 2)
		if n, ref := e.admits(t, "key-1", 10); n != 2 || ref == nil || ref.header.Get("X-Pandokos-Limit") != "2" {
			t.Fatalf("%d admitted, refusal %v", n, ref)
		}
	})

	t.Run("a platform default applies to a tenant without a rule", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedPlatformLimit(t, nil, "concurrent_requests", 100)
		d.SeedPlatformLimit(t, &e.tenant, "requests_per_minute", 3)
		if n, _ := e.admits(t, "key-1", 10); n != 3 {
			t.Fatalf("%d admitted, want 3", n)
		}
	})

	t.Run("a key rule above the tenant limit has no effect", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedLimitRule(t, e.tenant, "tenant", "", "requests_per_minute", 2)
		d.SeedLimitRule(t, e.tenant, "key", "key-1", "requests_per_minute", 100)
		if n, ref := e.admits(t, "key-1", 10); n != 2 || ref == nil || ref.header.Get("X-Pandokos-Limit-Subject") != "tenant" {
			t.Fatalf("%d admitted, refusal %v", n, ref)
		}
	})

	t.Run("a key rule restricts that key only", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedLimitRule(t, e.tenant, "key", "key-1", "requests_per_minute", 1)
		n, ref := e.admits(t, "key-1", 5)
		if n != 1 || ref == nil || ref.header.Get("X-Pandokos-Limit-Subject") != "key" {
			t.Fatalf("key-1: %d admitted, refusal %v", n, ref)
		}
		for _, who := range []string{"key-2", "other-sa"} {
			if n, ref := e.admits(t, who, 6); n != 6 || ref != nil {
				t.Errorf("%s: %d admitted, refusal %v: the rule of another key applied", who, n, ref)
			}
		}
	})

	t.Run("a service account rule spans two keys and a token of that account", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedLimitRule(t, e.tenant, "service_account", "sa-1", "requests_per_minute", 3)
		for _, who := range []string{"key-1", "key-2", "token"} {
			if c := e.call(t, who); c.status != 200 {
				t.Fatalf("%s: %d %s", who, c.status, c.body)
			}
		}
		for _, who := range []string{"key-1", "key-2", "token"} {
			c := e.call(t, who)
			if c.status != 429 || c.header.Get("X-Pandokos-Limit-Subject") != "service_account" {
				t.Errorf("%s: %d %v, want the service account's refusal", who, c.status, c.header)
			}
		}
		if n, ref := e.admits(t, "other-sa", 5); n != 5 || ref != nil {
			t.Errorf("another service account: %d admitted, refusal %v", n, ref)
		}
	})

	t.Run("a token is limited by a key rule on its mapping ID", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedLimitRule(t, e.tenant, "key", "mapping-1", "requests_per_minute", 1)
		n, ref := e.admits(t, "token", 5)
		if n != 1 || ref == nil || ref.header.Get("X-Pandokos-Limit-Subject") != "key" {
			t.Fatalf("token: %d admitted, refusal %v", n, ref)
		}
		if n, ref := e.admits(t, "key-1", 5); n != 5 || ref != nil {
			t.Errorf("a key of the same service account: %d admitted, refusal %v", n, ref)
		}
	})

	t.Run("a changed rule applies after its notification, without a restart", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedLimitRule(t, e.tenant, "tenant", "", "requests_per_minute", 2)
		if n, ref := e.admits(t, "key-1", 10); n != 2 || ref == nil {
			t.Fatalf("%d admitted, refusal %v", n, ref)
		}
		d.SetLimitRule(t, e.tenant, "tenant", "", "requests_per_minute", 10)
		testutil.Eventually(t, 2*time.Second, "the raised limit to apply", func() bool { return e.call(t, "key-1").status == 200 })
		// The counters were not reset: the two admitted before count against the new limit.
		n, ref := e.admits(t, "key-1", 20)
		if ref == nil || n > 7 {
			t.Errorf("%d more admitted under a limit of 10 with 3 counted already, refusal %v", n, ref)
		}
		d.DeleteLimitRule(t, e.tenant, "tenant", "", "requests_per_minute")
		testutil.Eventually(t, 2*time.Second, "the removal of the rule to apply", func() bool { return e.call(t, "key-1").status == 200 })
	})

	t.Run("a platform change applies after its notification", func(t *testing.T) {
		e := newLevelsEnv(t, d)
		d.SeedPlatformLimit(t, &e.tenant, "requests_per_minute", 1)
		if n, ref := e.admits(t, "key-1", 5); n != 1 || ref == nil {
			t.Fatalf("%d admitted, refusal %v", n, ref)
		}
		d.SeedPlatformLimit(t, &e.tenant, "requests_per_minute", 50)
		testutil.Eventually(t, 2*time.Second, "the raised ceiling to apply", func() bool { return e.call(t, "key-1").status == 200 })
	})
}

// ---- sprint 5.2: quotas and budgets on the test database ------------------------------------

// lossyRecorder hands every n-th record to nobody: a record lost before the ledger writer had it.
type lossyRecorder struct {
	inner gateway.Recorder
	n     int
	seen  atomic.Int64
	lost  atomic.Int64
}

func (l *lossyRecorder) Admit() bool { return true }

func (l *lossyRecorder) Record(r usage.Record) {
	if l.n > 0 && l.seen.Add(1)%int64(l.n) == 0 {
		l.lost.Add(1)
		return
	}
	if l.inner.Admit() {
		l.inner.Record(r)
	}
}

type stackOptions struct {
	ledger        bool   // the real ledger writer and the database reader; otherwise records stay in memory
	zone          string // limits.time_zone
	defaultOutput int64
	dropEvery     int // with a ledger: every n-th record is lost before the writer has it
	callers       []string
	newH          func(t *testing.T, mutate func(*gateway.Options)) *harness
	unknownPrice  string
}

// quotaStack is a gateway with the period limits of sprint 5.2 over the test database: the rules
// from the database, the counters from the ledger when it has one, the events written to limit_event.
type quotaStack struct {
	d          *dbtest.Database
	tn         tenant.ID
	h          *harness
	real       *limitstest.Real
	writer     *ledger.Writer
	events     *limits.EventWriter
	lossy      *lossyRecorder
	keys       map[string]string
	principals map[string]authz.Principal
	gw, worker *store.DB
}

func newQuotaStack(t *testing.T, d *dbtest.Database, tn tenant.ID, o stackOptions) *quotaStack {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &quotaStack{d: d, tn: tn, keys: map[string]string{}, principals: map[string]authz.Principal{}, gw: d.Open(t, store.RoleGateway), worker: d.Open(t, store.RoleWorker)}
	callers := o.callers
	if len(callers) == 0 {
		callers = []string{"a", "b"}
	}
	auth := map[string]authz.Principal{}
	for _, name := range callers {
		p := testutil.InferencePrincipal()
		p.TenantID, p.KeyID = tn, "tk-"+name
		k := testutil.NewKey(t)
		s.keys[name], s.principals[name], auth[k] = k, p, p
	}
	cache, err := limits.NewRuleCache(limits.RuleCacheOptions{DB: s.gw, Logger: quiet, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	s.events, err = limits.NewEventWriter(limits.EventWriterOptions{DB: s.gw, Logger: quiet, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	evDone := make(chan struct{})
	go func() { s.events.Run(ctx); close(evDone) }()

	lo := limitstest.Options{Cache: cache, Events: s.events, Zone: o.zone, OnUnknownPrice: o.unknownPrice}
	var rec gateway.Recorder
	if o.ledger {
		s.writer, err = ledger.New(ledger.Options{DB: s.gw, Logger: quiet, BufferSize: 4096, BatchSize: 64, FlushInterval: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		wDone := make(chan struct{})
		go func() { s.writer.Run(ctx); close(wDone) }()
		t.Cleanup(func() {
			cancel()
			<-wDone
			cctx, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			s.writer.Close(cctx)
		})
		lo.Ledger, lo.Pending = limits.NewLedger(s.worker), s.writer.OldestPendingStart
		rec = s.writer
		if o.dropEvery > 0 {
			s.lossy = &lossyRecorder{inner: s.writer, n: o.dropEvery}
			rec = s.lossy
		}
	}
	s.real = limitstest.NewWith(t, lo)
	newH := o.newH
	if newH == nil {
		newH = newHarness
	}
	s.h = newH(t, func(g *gateway.Options) {
		g.Authenticator = testutil.StaticAuthenticator(auth)
		g.Limits = s.real
		if rec != nil {
			g.Recorder = rec
		}
		if o.defaultOutput > 0 {
			g.DefaultOutputTokens = o.defaultOutput
		}
	})
	t.Cleanup(func() {
		cancel()
		<-evDone
		cctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		s.events.Close(cctx)
	})
	return s
}

func (s *quotaStack) hdr(who string) http.Header {
	return http.Header{"Authorization": {"Bearer " + s.keys[who]}, "Content-Type": {"application/json"}}
}

// call is an openai chat call of a caller to the model of a provider.
func (s *quotaStack) call(t *testing.T, who, provider, model string) captured {
	t.Helper()
	return s.h.send(t, http.MethodPost, "/"+provider+"/v1/chat/completions", chat(model, false), s.hdr(who))
}

func (s *quotaStack) ok(t *testing.T, who, model string) {
	t.Helper()
	if c := s.call(t, who, "openai", model); c.status != 200 {
		t.Fatalf("%s %s: %d %s", who, model, c.status, c.body)
	}
}

// post is a call that holds a goroutine: it returns the status when the answer is in.
func (s *quotaStack) post(who, provider, model string) int {
	req, err := http.NewRequest(http.MethodPost, s.h.srv.URL+"/"+provider+"/v1/chat/completions", bytesReader(chat(model, false)))
	if err != nil {
		return -1
	}
	req.Header = s.hdr(who)
	resp, err := s.h.srv.Client().Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func (s *quotaStack) count(t *testing.T, table string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var n int
	err := s.d.Open(t, store.RoleAdmin).WithTenant(ctx, s.tn, func(ctx context.Context, tx store.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM pandokos.`+table).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type eventRow struct {
	Kind        string
	Percent     int
	Metric      string
	LimitValue  int64
	PeriodStart time.Time
	Observed    string
}

func (s *quotaStack) eventRows(t *testing.T) []eventRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out []eventRow
	err := s.d.Open(t, store.RoleAdmin).WithTenant(ctx, s.tn, func(ctx context.Context, tx store.Tx) error {
		rows, err := tx.Query(ctx, `SELECT kind, percent, metric, limit_value, period_start, observed::text FROM pandokos.limit_event ORDER BY occurred_at, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r eventRow
			var p int16
			if err := rows.Scan(&r.Kind, &p, &r.Metric, &r.LimitValue, &r.PeriodStart, &r.Observed); err != nil {
				return err
			}
			r.Percent = int(p)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// limitsFor resolves the limits of a caller for a model, from the rules in the database, at a time.
func (s *quotaStack) limitsFor(t *testing.T, who, provider, model string, at time.Time) []limits.Limit {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	set, err := limits.LoadRules(ctx, s.gw, s.tn)
	if err != nil {
		t.Fatal(err)
	}
	p := s.principals[who]
	return set.For(limits.Query{ServiceAccountID: p.ID, KeyID: p.KeyID, Provider: provider, Model: model, Start: at}, s.real.Calendar)
}

func findLimit(ls []limits.Limit, m limits.Metric) (limits.Limit, bool) {
	for _, l := range ls {
		if l.Metric == m {
			return l, true
		}
	}
	return limits.Limit{}, false
}

// fmtMicro writes millionths of a unit as a decimal without trailing zeros.
func fmtMicro(n int64) string {
	s := strconv.FormatInt(n/1_000_000, 10)
	if f := n % 1_000_000; f != 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%06d", f), "0")
	}
	return s
}

// estimateMicro is the cost, in millionths of a dollar, that the gateway reserves for a chat body
// at the prices of the fake targets (3 and 6 per million) and an output of out tokens.
func estimateMicro(body []byte, out int64) int64 { return ceil4(len(body))*3 + out*6 }

// TestMonthlyBudget is T-669: the exit condition of the sprint, with the real store, reconciler,
// ledger writer and database.
func TestMonthlyBudget(t *testing.T) {
	d := dbtest.NewMigrated(t)
	const out = 1000

	t.Run("reservations reach the budget, the unused part returns at settlement", func(t *testing.T) {
		tn := d.SeedTenant(t, "month-a")
		hang := chat(fakeprovider.ModelHang, false)
		est := estimateMicro(hang, out)
		const held = 9
		budget := held*est + est/2
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", budget, dbtest.LimitOptions{Currency: "USD"})
		s := newQuotaStack(t, d, tn, stackOptions{ledger: true, defaultOutput: out})

		statuses := make(chan int, held)
		for range held {
			go func() { statuses <- s.post("a", "openai", fakeprovider.ModelHang) }()
		}
		testutil.Eventually(t, 5*time.Second, "nine requests in flight", func() bool { return s.h.up.InFlight() == held })
		c := s.call(t, "b", "openai", "fake-basic")
		assertContract(t, "the tenth", c, family.CodeQuotaExceeded)
		want := map[string]string{
			family.HeaderLimitMetric: "cost_per_month", family.HeaderLimitCurrency: "USD", family.HeaderLimitScope: "all",
			family.HeaderLimit: fmtMicro(budget), family.HeaderLimitRemaining: fmtMicro(budget - held*est),
		}
		for k, v := range want {
			if got := c.header.Get(k); got != v {
				t.Errorf("%s: %q, want %q", k, got, v)
			}
		}
		if n := s.h.up.InFlight(); n != held {
			t.Errorf("%d requests in flight after a refusal, want %d", n, held)
		}
		s.h.up.Release()
		for range held {
			if st := <-statuses; st != 200 {
				t.Fatalf("a held request answered %d", st)
			}
		}
		// The estimate of each request is returned at settlement: what remains is what was spent.
		testutil.Eventually(t, 5*time.Second, "the reservations to end", func() bool { return s.real.Outstanding() == 0 })
		s.ok(t, "b", "fake-basic")
		s.ok(t, "a", "fake-basic")
		l, ok := findLimit(s.limitsFor(t, "a", "openai", "fake-basic", s.real.Now()), limits.CostPerMonth)
		if !ok {
			t.Fatal("no budget resolved")
		}
		a, ok := s.real.Mem.Counted(l)
		if !ok || a.Cost.String() != "0.001089" { // eleven requests of 0.000099
			t.Errorf("the budget counts %v %v, want 0.001089", a.Cost, ok)
		}
	})

	t.Run("the first request of the next month is admitted and the old month still takes a settlement", func(t *testing.T) {
		tn := d.SeedTenant(t, "month-b")
		hang := chat(fakeprovider.ModelHang, false)
		est := estimateMicro(hang, out)
		budget := 2*est + est/2
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", budget, dbtest.LimitOptions{Currency: "USD"})
		s := newQuotaStack(t, d, tn, stackOptions{ledger: true, defaultOutput: out})
		oldNow := s.real.Now()
		oldMonth := s.real.Calendar.Of(limits.PerMonth, oldNow)

		held := make(chan int, 1)
		go func() { held <- s.post("a", "openai", fakeprovider.ModelHang) }()
		testutil.Eventually(t, 5*time.Second, "a request in flight", func() bool { return s.h.up.InFlight() == 1 })
		s.ok(t, "b", "fake-basic") // 2 × est > budget with the first in flight: this one fits and settles low
		s.real.AdvanceTo(oldMonth.End.Add(time.Hour))
		s.ok(t, "b", "fake-basic") // the new month: nothing counted yet
		s.h.up.Release()
		if st := <-held; st != 200 {
			t.Fatalf("the request in flight answered %d", st)
		}
		testutil.Eventually(t, 5*time.Second, "the reservations to end", func() bool { return s.real.Outstanding() == 0 })

		oldL, _ := findLimit(s.limitsFor(t, "a", "openai", "fake-hang", oldNow), limits.CostPerMonth)
		newL, _ := findLimit(s.limitsFor(t, "a", "openai", "fake-basic", s.real.Now()), limits.CostPerMonth)
		if oldL.Period.Start.Equal(newL.Period.Start) || !oldL.Period.Start.Equal(oldMonth.Start) {
			t.Fatalf("periods %v and %v", oldL.Period, newL.Period)
		}
		if a, ok := s.real.Mem.Counted(oldL); !ok || a.Cost.String() != "0.000198" {
			t.Errorf("the old month counts %v %v, want 0.000198: its two requests, the one in flight settled into it", a.Cost, ok)
		}
		if a, ok := s.real.Mem.Counted(newL); !ok || a.Cost.String() != "0.000099" {
			t.Errorf("the new month counts %v %v, want 0.000099", a.Cost, ok)
		}
	})
}

// TestRestartMatchesLedger is T-670: after a restart the counters hold what the ledger holds.
func TestRestartMatchesLedger(t *testing.T) {
	d := dbtest.NewMigrated(t)
	const out = 100
	body := chat("fake-basic", false)
	est := estimateMicro(body, out)

	// traffic sends calls of two keys to two models until the budget refuses; it returns the stack, the
	// number of calls that were admitted and the number of records the recorder dropped.
	traffic := func(t *testing.T, name string, dropEvery int) (*quotaStack, tenant.ID, int) {
		t.Helper()
		tn := d.SeedTenant(t, name)
		budget := 14*99 + est + est/2
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", budget, dbtest.LimitOptions{Currency: "USD"})
		d.SeedLimitRule(t, tn, "tenant", "", "requests_per_month", 1000)
		d.SeedLimitRule(t, tn, "key", "tk-a", "tokens_per_month", 1_000_000)
		d.SeedLimitRule(t, tn, "tenant", "", "requests_per_day", 500, dbtest.LimitOptions{Provider: "openai", Model: "fake-details"})
		s := newQuotaStack(t, d, tn, stackOptions{ledger: true, defaultOutput: out, dropEvery: dropEvery})
		admitted, refused := 0, false
		for i := 0; i < 60 && !refused; i++ {
			who, model := []string{"a", "b"}[i%2], []string{"fake-basic", "fake-details"}[(i/2)%2]
			switch c := s.call(t, who, "openai", model); c.status {
			case 200:
				admitted++
			case 429:
				refused = true
			default:
				t.Fatalf("call %d: %d %s", i, c.status, c.body)
			}
		}
		if !refused {
			t.Fatal("the budget never refused")
		}
		flush, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if left := s.writer.Close(flush); left != 0 {
			t.Fatalf("%d records unwritten", left)
		}
		return s, tn, admitted
	}

	// compare checks every period counter of the second store against the ledger.
	compare := func(t *testing.T, s *quotaStack) (compared int) {
		t.Helper()
		now := s.real.Now()
		seen := map[string]bool{}
		for _, who := range []string{"a", "b"} {
			for _, model := range []string{"fake-basic", "fake-details"} {
				var period []limits.Limit
				for _, l := range s.limitsFor(t, who, "openai", model, now) {
					if _, isPeriod := l.Metric.Period(); isPeriod {
						period = append(period, l)
					}
				}
				cctx, c := context.WithTimeout(context.Background(), 20*time.Second)
				err := s.real.Reconciler.Ensure(cctx, s.tn, period)
				c()
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range period {
					key := fmt.Sprintf("%s/%s/%s/%s/%s", l.Subject, l.ID, l.Provider, l.Model, l.Metric)
					if seen[key] {
						continue
					}
					seen[key] = true
					cctx, c := context.WithTimeout(context.Background(), 20*time.Second)
					groups, err := limits.NewLedger(s.worker).Usage(cctx, s.tn, l.Period.Start, l.Period.End, l.Period.End)
					c()
					if err != nil {
						t.Fatal(err)
					}
					want := limits.Sum(l, groups)
					got, ok := s.real.Mem.Counted(l)
					if !ok {
						t.Errorf("%s: no counter after the restart", key)
						continue
					}
					if got.N != want.N || got.Cost.String() != want.Cost.String() {
						t.Errorf("%s: counted %d / %s, the ledger holds %d / %s", key, got.N, got.Cost, want.N, want.Cost)
					}
					compared++
				}
			}
		}
		return compared
	}

	t.Run("the counters hold the ledger and an exhausted budget refuses the first request", func(t *testing.T) {
		s1, tn, _ := traffic(t, "restart-a", 0)
		s2 := newQuotaStack(t, d, tn, stackOptions{ledger: true, defaultOutput: out}) // the restart
		c := s2.call(t, "b", "openai", "fake-basic")
		assertContract(t, "the first request after the restart", c, family.CodeQuotaExceeded)
		if c.header.Get(family.HeaderLimitMetric) != "cost_per_month" {
			t.Errorf("refused by %q", c.header.Get(family.HeaderLimitMetric))
		}
		if n := compare(t, s2); n < 4 {
			t.Errorf("%d counters compared, want at least the four rules", n)
		}
		_ = s1
	})

	t.Run("a record lost before the flush is not counted after the restart", func(t *testing.T) {
		s1, tn, admitted := traffic(t, "restart-b", 4)
		if s1.lossy.lost.Load() < 2 {
			t.Fatalf("%d records lost: the test proves nothing", s1.lossy.lost.Load())
		}
		l1, _ := findLimit(s1.limitsFor(t, "a", "openai", "fake-basic", s1.real.Now()), limits.RequestsPerMonth)
		c1, _ := s1.real.Mem.Counted(l1)
		if c1.N != int64(admitted) {
			t.Fatalf("the first store counts %d requests, %d were admitted", c1.N, admitted)
		}
		s2 := newQuotaStack(t, d, tn, stackOptions{ledger: true, defaultOutput: out})
		compare(t, s2)
		l2, _ := findLimit(s2.limitsFor(t, "a", "openai", "fake-basic", s2.real.Now()), limits.RequestsPerMonth)
		c2, _ := s2.real.Mem.Counted(l2)
		if c2.N >= c1.N {
			t.Errorf("after the restart %d requests are counted, before it %d: a record that never reached the ledger was counted", c2.N, c1.N)
		}
	})
}

// TestScopedRules is T-672: rules with a scope, behind the gateway.
func TestScopedRules(t *testing.T) {
	d := dbtest.NewMigrated(t)
	envFor := func(t *testing.T, name string) *quotaStack {
		tn := d.SeedTenant(t, "scoped-"+name)
		s := newQuotaStack(t, d, tn, stackOptions{newH: newFamilies})
		for _, m := range []string{"fake-basic", "fake-details"} {
			s.h.targets["compat/"+m] = testutil.TargetFor(s.h.up, "compat", m)
		}
		return s
	}
	refused := func(t *testing.T, c captured, code family.ErrorCode, scope, metric string) {
		t.Helper()
		assertContract(t, "refusal", c, code)
		if c.header.Get(family.HeaderLimitScope) != scope || c.header.Get(family.HeaderLimitMetric) != metric {
			t.Errorf("scope %q metric %q, want %q and %q", c.header.Get(family.HeaderLimitScope), c.header.Get(family.HeaderLimitMetric), scope, metric)
		}
	}

	t.Run("a provider rule limits that provider only", func(t *testing.T) {
		s := envFor(t, "provider")
		d.SeedLimitRule(t, s.tn, "tenant", "", "requests_per_day", 2, dbtest.LimitOptions{Provider: "openai"})
		s.ok(t, "a", "fake-basic")
		s.ok(t, "a", "fake-details") // another model of the provider counts too
		refused(t, s.call(t, "a", "openai", "fake-basic"), family.CodeQuotaExceeded, "provider", "requests_per_day")
		for range 4 {
			if c := s.call(t, "a", "compat", "fake-basic"); c.status != 200 {
				t.Fatalf("another provider: %d %s", c.status, c.body)
			}
		}
	})

	t.Run("a model rule limits that model only", func(t *testing.T) {
		s := envFor(t, "model")
		d.SeedLimitRule(t, s.tn, "tenant", "", "requests_per_day", 1, dbtest.LimitOptions{Provider: "openai", Model: "fake-details"})
		s.ok(t, "a", "fake-details")
		refused(t, s.call(t, "a", "openai", "fake-details"), family.CodeQuotaExceeded, "model", "requests_per_day")
		for range 3 {
			s.ok(t, "a", "fake-basic")
		}
		if c := s.call(t, "a", "compat", "fake-details"); c.status != 200 { // the model of another provider
			t.Errorf("the model of another provider: %d", c.status)
		}
	})

	t.Run("a rate rule with a scope", func(t *testing.T) {
		s := envFor(t, "rate")
		d.SeedLimitRule(t, s.tn, "tenant", "", "requests_per_minute", 1, dbtest.LimitOptions{Provider: "openai", Model: "fake-basic"})
		s.ok(t, "a", "fake-basic")
		refused(t, s.call(t, "a", "openai", "fake-basic"), family.CodeRateLimited, "model", "requests_per_minute")
		s.ok(t, "a", "fake-details")
		s.ok(t, "a", "fake-details")
	})

	t.Run("a key rule with a model scope", func(t *testing.T) {
		s := envFor(t, "key")
		d.SeedLimitRule(t, s.tn, "key", "tk-a", "requests_per_day", 1, dbtest.LimitOptions{Provider: "openai", Model: "fake-basic"})
		s.ok(t, "a", "fake-basic")
		c := s.call(t, "a", "openai", "fake-basic")
		refused(t, c, family.CodeQuotaExceeded, "model", "requests_per_day")
		if c.header.Get(family.HeaderLimitSubject) != "key" {
			t.Errorf("subject %q", c.header.Get(family.HeaderLimitSubject))
		}
		s.ok(t, "a", "fake-details")
		s.ok(t, "b", "fake-basic")
		s.ok(t, "b", "fake-basic")
	})

	t.Run("the tightest refuses", func(t *testing.T) {
		s := envFor(t, "tight")
		d.SeedLimitRule(t, s.tn, "tenant", "", "requests_per_day", 5)
		d.SeedLimitRule(t, s.tn, "tenant", "", "requests_per_day", 3, dbtest.LimitOptions{Provider: "openai"})
		d.SeedLimitRule(t, s.tn, "tenant", "", "requests_per_day", 2, dbtest.LimitOptions{Provider: "openai", Model: "fake-basic"})
		s.ok(t, "a", "fake-basic")
		s.ok(t, "a", "fake-basic")
		refused(t, s.call(t, "a", "openai", "fake-basic"), family.CodeQuotaExceeded, "model", "requests_per_day")
		s.ok(t, "a", "fake-details") // the third of the provider
		refused(t, s.call(t, "a", "openai", "fake-details"), family.CodeQuotaExceeded, "provider", "requests_per_day")
		for range 2 { // compat is outside the provider rule: the tenant's 5 allow two more
			if c := s.call(t, "a", "compat", "fake-basic"); c.status != 200 {
				t.Errorf("another provider: %d %s", c.status, c.body)
			}
		}
		refused(t, s.call(t, "a", "compat", "fake-basic"), family.CodeQuotaExceeded, "all", "requests_per_day")
	})
}

// TestThresholdEvents is T-673: thresholds and exhaustion are recorded once per rule and period.
func TestThresholdEvents(t *testing.T) {
	d := dbtest.NewMigrated(t)
	const out = 10
	est := estimateMicro(chat("fake-basic", false), out)
	// Nine requests of 99 micro-dollars cross 80 % of 1,000 (at 891); the tenth is refused for want
	// of room for its estimate.
	if 891+est <= 1000 {
		t.Fatalf("the estimate %d leaves room for a tenth request: adjust the test", est)
	}

	t.Run("crossing 80 percent writes one row; the first refusal writes one exhausted row", func(t *testing.T) {
		tn := d.SeedTenant(t, "events-a")
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", 1000, dbtest.LimitOptions{Currency: "USD", Thresholds: []int{80}})
		s := newQuotaStack(t, d, tn, stackOptions{defaultOutput: out})
		for i := range 9 {
			if i == 8 {
				if rows := s.eventRows(t); len(rows) != 0 {
					t.Fatalf("%d events before the threshold: %+v", len(rows), rows)
				}
			}
			s.ok(t, "a", "fake-basic")
		}
		testutil.Eventually(t, 5*time.Second, "the threshold row", func() bool { return s.count(t, "limit_event") == 1 })
		rows := s.eventRows(t)
		if rows[0].Kind != "threshold_reached" || rows[0].Percent != 80 || rows[0].Metric != "cost_per_month" || rows[0].LimitValue != 1000 {
			t.Errorf("row %+v", rows[0])
		}
		if obs, _ := pricing.ParseDecimal(rows[0].Observed); obs.String() != "0.000891" {
			t.Errorf("observed %s, want 0.000891", rows[0].Observed)
		}
		for range 3 { // more usage under the same mark: no second row
			if c := s.call(t, "a", "openai", "fake-basic"); c.status != 429 {
				t.Fatalf("%d, want the refusal", c.status)
			}
		}
		testutil.Eventually(t, 5*time.Second, "the exhausted row", func() bool { return s.count(t, "limit_event") == 2 })
		time.Sleep(200 * time.Millisecond)
		rows = s.eventRows(t)
		if len(rows) != 2 || rows[1].Kind != "exhausted" || rows[1].Percent != 0 {
			t.Errorf("rows %+v, want the threshold and one exhausted row", rows)
		}
		// A new period records again.
		s.real.AdvanceTo(s.real.Calendar.Of(limits.PerMonth, s.real.Now()).End.Add(time.Hour))
		for range 9 {
			s.ok(t, "a", "fake-basic")
		}
		testutil.Eventually(t, 5*time.Second, "the threshold row of the next month", func() bool { return s.count(t, "limit_event") == 3 })
		rows = s.eventRows(t)
		if rows[2].Kind != "threshold_reached" || !rows[2].PeriodStart.After(rows[0].PeriodStart) {
			t.Errorf("rows %+v: the third is not the threshold of the next period", rows)
		}
	})

	t.Run("an unenforced rule never refuses and records its threshold of 100 percent", func(t *testing.T) {
		tn := d.SeedTenant(t, "events-b")
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_day", 500, dbtest.LimitOptions{Currency: "USD", NoEnforce: true, Thresholds: []int{100}})
		s := newQuotaStack(t, d, tn, stackOptions{defaultOutput: out})
		for range 12 { // 12 × 99 = 1,188: well over the 500 of the soft rule
			s.ok(t, "a", "fake-basic")
		}
		testutil.Eventually(t, 5*time.Second, "the threshold row", func() bool { return s.count(t, "limit_event") >= 1 })
		time.Sleep(200 * time.Millisecond)
		rows := s.eventRows(t)
		if len(rows) != 1 || rows[0].Kind != "threshold_reached" || rows[0].Percent != 100 || rows[0].LimitValue != 500 {
			t.Errorf("rows %+v, want one threshold_reached at 100", rows)
		}
	})
}
