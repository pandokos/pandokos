package gateway_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// The fixtures of the tests of sprint 5.4 at the gateway, whose amounts are chosen by hand. A request
// for fake-basic carries 9 input and 12 output tokens (the fixture's usage) and the estimate of
// `chat("fake-basic", false)` is ceil(bytes / 4) input tokens and 4096 output tokens (the
// harness's default). In millionths of a currency:
//
//	catalogue USD, 1 input / 2 output per million:  cost 9 + 24 = 33     estimate in + 8192
//	tenant EUR,    4 input / 8 output per million:  cost 36 + 96 = 132    estimate 4 in + 32768

const (
	catalogueCost = 33  // millionths USD, one request
	tenantCost    = 132 // millionths EUR, one request
)

// estimates returns the estimate of a request of the fixture in millionths, for the catalogue's USD
// price and for the tenant's EUR price.
func estimates() (usd, eur int64) {
	in := int64((len(chat("fake-basic", false)) + 3) / 4)
	return in + 8192, 4*in + 32768
}

// priceWorld is the catalogue of the test database as the gateway reads it: the provider openai
// with its default endpoint moved to the fake provider, a model for everybody, and a USD price of the
// catalogue.
type priceWorld struct {
	d  *dbtest.Database
	up *fakeprovider.Server
}

func newPriceWorld(t *testing.T, d *dbtest.Database) *priceWorld {
	t.Helper()
	w := &priceWorld{d: d, up: fakeprovider.New(t)}
	super := d.Super(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(context.Background(), q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	exec(`UPDATE pandokos.provider_endpoint SET base_url = $1, private = true, enabled = true
		WHERE is_default AND provider_id = (SELECT id FROM pandokos.provider WHERE name = 'openai')`, w.up.URL())
	d.SeedModel(t, "openai", "fake-basic")
	d.SeedPrice(t, "openai", "fake-basic", "USD", time.Time{})
	return w
}

// priceStack is a quotaStack whose gateway resolves its targets from the catalogue cache over the
// database: the currency and the prices of the tenant reach the estimate and the record.
type priceStack struct {
	*quotaStack
	cat *catalog.Cache
}

type priceOptions struct {
	stackOptions
	catalogTTL time.Duration // cache.catalog_ttl; a minute when zero
	listener   bool          // the notifications of the triggers reach both caches
}

func (w *priceWorld) stack(t *testing.T, tn tenant.ID, o priceOptions) *priceStack {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw := w.d.Open(t, store.RoleGateway)
	ttl := o.catalogTTL
	if ttl == 0 {
		ttl = time.Minute
	}
	cat, err := catalog.NewCache(catalog.CacheOptions{DB: gw, Logger: quiet, TTL: ttl, DefaultCurrency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	o.newH = func(t *testing.T, mutate func(*gateway.Options)) *harness {
		return newHarnessWith(t, w.up, testutil.TestGuard(t), func(g *gateway.Options) {
			mutate(g)
			g.Catalogue = resolver.NewCatalogue(cat, quiet) // the targets come from the database, with the tenant's currency and prices
		})
	}
	s := &priceStack{quotaStack: newQuotaStack(t, w.d, tn, o.stackOptions), cat: cat}
	if o.listener {
		logs := &testutil.LogBuffer{}
		l, err := store.NewListener(store.ListenerOptions{Conn: w.d.ConnOptions("gateway"), Role: store.RoleGateway, Logger: telemetry.NewLogger(logs, "debug"),
			OnNotify: func(p string) { cat.Invalidate(p); s.cache.Invalidate(p) }, OnReset: func() { cat.Flush(); s.cache.Flush() }})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { l.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		testutil.Eventually(t, 10*time.Second, "the cache listener to connect", func() bool {
			return strings.Contains(logs.String(), `"msg":"cache listener connected"`)
		})
	}
	return s
}

// ask is one request of the caller a for fake-basic.
func (s *priceStack) ask(t *testing.T) captured {
	t.Helper()
	return s.call(t, "a", "openai", "fake-basic")
}

// ledgerRow is a usage record as the database holds it.
type ledgerRow struct {
	Source, Currency, Status, Reason string
	Cost                             string
}

// rows reads the records of the tenant, oldest first, once there are n of them: from the ledger when
// the stack has one, else from the recorder of the harness.
func (s *priceStack) rows(t *testing.T, n int) []ledgerRow {
	t.Helper()
	var out []ledgerRow
	testutil.Eventually(t, 5*time.Second, "usage records in the ledger", func() bool {
		out = s.readRows(t)
		return len(out) >= n
	})
	return out
}

// served are the rows of the requests that were answered (a refusal writes a record with no cost).
func served(rows []ledgerRow) []ledgerRow {
	var out []ledgerRow
	for _, r := range rows {
		if r.Status != "none" {
			out = append(out, r)
		}
	}
	return out
}

func (s *priceStack) readRows(t *testing.T) []ledgerRow {
	t.Helper()
	if s.writer == nil { // a stack without a ledger keeps its records in memory
		var out []ledgerRow
		for _, r := range s.h.rec.Records() {
			row := ledgerRow{Source: r.Cost.Source, Currency: r.Cost.Currency, Status: r.CostStatus, Reason: r.Cost.Reason}
			if r.Cost.Known {
				row.Cost = r.Cost.Amount.String()
			}
			out = append(out, row)
		}
		return out
	}
	rs, err := s.d.Super(t).Query(context.Background(), `SELECT COALESCE(price_source, ''), COALESCE(currency, ''), cost_status, cost_reason, COALESCE(cost::text, '')
		FROM pandokos.usage_record WHERE tenant_id = $1::uuid ORDER BY started_at, id`, s.tn.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []ledgerRow
	for rs.Next() {
		var r ledgerRow
		if err := rs.Scan(&r.Source, &r.Currency, &r.Status, &r.Reason, &r.Cost); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// spent is the sum of the recorded costs of the tenant in a currency, in millionths.
func (s *priceStack) spent(t *testing.T, currency string) int64 {
	t.Helper()
	var n int64
	if err := s.d.Super(t).QueryRow(context.Background(), `SELECT COALESCE(round(sum(cost) * 1000000), 0)::bigint FROM pandokos.usage_record
		WHERE tenant_id = $1::uuid AND currency = $2`, s.tn.String(), currency).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (s *priceStack) setLimit(t *testing.T, currency string, millionths int64) {
	t.Helper()
	if _, err := s.d.Super(t).Exec(context.Background(), `UPDATE pandokos.limit_rule SET limit_value = $3 WHERE tenant_id = $1::uuid AND currency = $2`,
		s.tn.String(), currency, millionths); err != nil {
		t.Fatal(err)
	}
	s.cache.Flush()
}

func header(c captured, k string) string { return c.header.Get(k) }

var _ = http.StatusOK

// TestActiveCurrencyBudget is T-736, the exit condition: tenant A has a EUR list and a EUR monthly
// budget next to its USD one. Before the switch it is priced in USD and only the USD budget counts.
// After its active currency is EUR it is refused with 429 quota_exceeded (currency EUR) once the
// recorded costs and the estimate reach the EUR budget, and the USD budget plays no part; tenant B on
// the same model keeps the catalogue's USD price and its USD budget; the records carry tenant/EUR and
// catalogue/USD; the ledger holds exactly the amounts counted. A default USD ceiling does not apply
// to A; A's own EUR ceiling does and counts the tenant's price.
func TestActiveCurrencyBudget(t *testing.T) {
	d := dbtest.NewMigrated(t)
	w := newPriceWorld(t, d)
	estUSD, estEUR := estimates()
	a, b := d.SeedTenant(t, "cur-a"), d.SeedTenant(t, "cur-b")

	// A: a list in EUR (4 / 8), a USD budget that admits three requests and a EUR budget of one
	// millionth, which would refuse everything if it applied. The platform's default ceiling in USD is
	// of one millionth too: it does not apply to A once A is in EUR, and applies to B.
	d.SeedTenantPrice(t, a, "EUR", "openai", "fake-basic", time.Time{}, dbtest.Rate{Category: "input", Rate: "4"}, dbtest.Rate{Category: "output", Rate: "8"})
	d.SeedLimitRule(t, a, "tenant", "", "cost_per_month", estUSD+3*catalogueCost-1, dbtest.LimitOptions{Currency: "USD"})
	d.SeedLimitRule(t, a, "tenant", "", "cost_per_month", 1, dbtest.LimitOptions{Currency: "EUR"})
	d.SeedLimitRule(t, b, "tenant", "", "cost_per_month", estUSD+3*catalogueCost-1, dbtest.LimitOptions{Currency: "USD"})
	d.SeedPlatformLimit(t, nil, "cost_per_day", 1, dbtest.LimitOptions{Currency: "USD"}) // a default ceiling of one millionth

	sa := w.stack(t, a, priceOptions{stackOptions: stackOptions{ledger: true}, listener: true})
	sb := w.stack(t, b, priceOptions{stackOptions: stackOptions{ledger: true}, listener: true})

	t.Run("before the switch A is priced in USD and only the USD budget counts", func(t *testing.T) {
		// The default ceiling of one millionth in USD applies to both while they are in USD: lift it for the phase.
		if _, err := d.Super(t).Exec(context.Background(), `UPDATE pandokos.platform_limit_rule SET limit_value = 1000000000000 WHERE tenant_id IS NULL`); err != nil {
			t.Fatal(err)
		}
		sa.cache.Flush()
		sb.cache.Flush()
		for i := 0; i < 3; i++ {
			if c := sa.ask(t); c.status != 200 {
				t.Fatalf("request %d of A in USD: %d %s (the EUR budget of one millionth must be inactive)", i+1, c.status, c.body)
			}
		}
		c := sa.ask(t)
		if c.status != 429 || header(c, "X-Pandokos-Limit-Currency") != "USD" || header(c, "X-Pandokos-Error") != "quota_exceeded" {
			t.Errorf("the fourth request of A in USD: %d currency %q, want 429 quota_exceeded in USD", c.status, header(c, "X-Pandokos-Limit-Currency"))
		}
		if got := sa.spent(t, "USD"); got != 3*catalogueCost {
			t.Errorf("A has spent %d in USD, want %d", got, 3*catalogueCost)
		}
	})

	t.Run("after the switch the EUR budget counts, in EUR, at the tenant's price", func(t *testing.T) {
		sa.setLimit(t, "EUR", estEUR+3*tenantCost-1) // three requests; the USD budget stays exhausted and plays no part
		d.SetTenantCurrency(t, a, "EUR", "USD", time.Time{})
		testutil.Eventually(t, 2*time.Second, "the two caches to see EUR", func() bool {
			ta, err := sa.cat.Access(context.Background(), a)
			rs, err2 := sa.cache.Rules(context.Background(), a)
			return err == nil && err2 == nil && ta.Currency.At(time.Now()) == "EUR" && rs.Currency.At(time.Now()) == "EUR"
		})
		for i := 0; i < 3; i++ {
			if c := sa.ask(t); c.status != 200 {
				t.Fatalf("request %d of A in EUR: %d %s (the exhausted USD budget must play no part)", i+1, c.status, c.body)
			}
		}
		c := sa.ask(t)
		if c.status != 429 || header(c, "X-Pandokos-Limit-Currency") != "EUR" || header(c, "X-Pandokos-Error") != "quota_exceeded" {
			t.Errorf("the fourth request of A in EUR: %d currency %q, want 429 quota_exceeded in EUR", c.status, header(c, "X-Pandokos-Limit-Currency"))
		}
		if got := sa.spent(t, "EUR"); got != 3*tenantCost {
			t.Errorf("A has spent %d in EUR, want %d", got, 3*tenantCost)
		}
		rows := served(sa.rows(t, 8))
		var tenantEUR, catalogueUSD int
		for _, r := range rows {
			switch {
			case r.Source == "tenant" && r.Currency == "EUR" && r.Status == "known":
				tenantEUR++
			case r.Source == "catalogue" && r.Currency == "USD" && r.Status == "known":
				catalogueUSD++
			default:
				t.Errorf("a record of A: %+v", r)
			}
		}
		if tenantEUR != 3 || catalogueUSD != 3 {
			t.Errorf("A's records: %d tenant/EUR and %d catalogue/USD, want 3 and 3", tenantEUR, catalogueUSD)
		}
	})

	t.Run("tenant B keeps the catalogue's USD price and its USD budget", func(t *testing.T) {
		// The default ceiling in USD applies to B (B is in USD) and is of one millionth.
		if _, err := d.Super(t).Exec(context.Background(), `UPDATE pandokos.platform_limit_rule SET limit_value = 1000000000000 WHERE tenant_id IS NULL`); err != nil {
			t.Fatal(err)
		}
		sb.cache.Flush()
		for i := 0; i < 3; i++ {
			if c := sb.ask(t); c.status != 200 {
				t.Fatalf("request %d of B: %d %s", i+1, c.status, c.body)
			}
		}
		if c := sb.ask(t); c.status != 429 || header(c, "X-Pandokos-Limit-Currency") != "USD" {
			t.Errorf("the fourth request of B: %d currency %q", c.status, header(c, "X-Pandokos-Limit-Currency"))
		}
		for _, r := range served(sb.rows(t, 4)) {
			if r.Source != "catalogue" || r.Currency != "USD" {
				t.Errorf("a record of B: %+v", r)
			}
		}
	})

	t.Run("a default USD ceiling does not apply to A; A's own EUR ceiling does and counts the tenant's price", func(t *testing.T) {
		tc := d.SeedTenant(t, "cur-c")
		d.SeedTenantPrice(t, tc, "EUR", "openai", "fake-basic", time.Time{}, dbtest.Rate{Category: "input", Rate: "4"}, dbtest.Rate{Category: "output", Rate: "8"})
		d.SetTenantCurrency(t, tc, "EUR", "", time.Time{})
		d.SeedPlatformLimit(t, &tc, "cost_per_day", estEUR+tenantCost-1, dbtest.LimitOptions{Currency: "EUR"}) // admits one request
		if _, err := d.Super(t).Exec(context.Background(), `UPDATE pandokos.platform_limit_rule SET limit_value = 1 WHERE tenant_id IS NULL`); err != nil {
			t.Fatal(err)
		}
		sc := w.stack(t, tc, priceOptions{stackOptions: stackOptions{ledger: true}, listener: true})
		if c := sc.ask(t); c.status != 200 {
			t.Fatalf("the first request of C: %d %s: the default ceiling in USD applied to a tenant in EUR", c.status, c.body)
		}
		if c := sc.ask(t); c.status != 429 || header(c, "X-Pandokos-Limit-Currency") != "EUR" || header(c, "X-Pandokos-Limit-Metric") != "cost_per_day" {
			t.Errorf("the second request of C: %d currency %q metric %q, want 429 at the platform's EUR ceiling", c.status, header(c, "X-Pandokos-Limit-Currency"), header(c, "X-Pandokos-Limit-Metric"))
		}
		if got := sc.spent(t, "EUR"); got != tenantCost {
			t.Errorf("C has spent %d in EUR, want %d: the ceiling counts the tenant's price", got, tenantCost)
		}
	})
}
