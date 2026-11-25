package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// zoneGateway is one gateway process over the test database: a rule cache with the tenants'
// zones, an unlimited-traffic auditor, and one key per tenant.
type zoneGateway struct {
	h     *harness
	real  *limitstest.Real
	cache *limits.RuleCache
	hdrs  map[string]http.Header
}

func newZoneGateway(t *testing.T, d *dbtest.Database, tenants map[string]tenant.ID, o limitstest.Options) *zoneGateway {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	zones, err := limits.NewZones(time.UTC)
	if err != nil {
		t.Fatalf("NewZones: %v", err)
	}
	gw := d.Open(t, store.RoleGateway)
	cache, err := limits.NewRuleCache(limits.RuleCacheOptions{DB: gw, Logger: quiet, TTL: time.Minute, Zones: zones, DefaultCurrency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	aud := resolver.NewUnlimitedAuditor(gw, quiet, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { aud.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		cc, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		aud.Close(cc)
	})
	o.Cache, o.Unlimited = cache, aud
	if o.Ledger == nil {
		o.Ledger = limits.NewLedger(d.Open(t, store.RoleWorker))
	}
	real := limitstest.NewWith(t, o)
	principals := map[string]authz.Principal{}
	hdrs := map[string]http.Header{}
	for name, id := range tenants {
		p := testutil.InferencePrincipal()
		p.TenantID = id
		k := testutil.NewKey(t)
		principals[k] = p
		hdrs[name] = http.Header{"Authorization": {"Bearer " + k}, "Content-Type": {"application/json"}}
	}
	h := newHarness(t, func(g *gateway.Options) {
		g.Authenticator = testutil.StaticAuthenticator(principals)
		g.Limits = real
	})
	return &zoneGateway{h: h, real: real, cache: cache, hdrs: hdrs}
}

func (g *zoneGateway) call(t *testing.T, tenantName string) captured {
	t.Helper()
	return g.h.send(t, http.MethodPost, route, chat("fake-basic", false), g.hdrs[tenantName])
}

type unlimitedRow struct {
	tenantID, actorType, actorName, resource, resourceID, requestID, outcome string
	detail                                                                   map[string]any
}

// unlimitedRows reads the rows limit.unlimited_traffic of a log, as the superuser.
func unlimitedRows(t *testing.T, d *dbtest.Database, platform bool, tenantID string) []unlimitedRow {
	t.Helper()
	tbl, sel, where := "audit_event", "tenant_id::text", "tenant_id = $1::uuid"
	if platform {
		tbl, sel, where = "platform_audit_event", "''", "resource_id = $1"
	}
	rows, err := d.Super(t).Query(context.Background(), `SELECT `+sel+`, actor_type, actor_name, resource_type, resource_id, request_id, outcome, detail
		FROM pandokos.`+tbl+` WHERE action = 'limit.unlimited_traffic' AND `+where+` ORDER BY detail ->> 'day'`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []unlimitedRow
	for rows.Next() {
		var r unlimitedRow
		var detail []byte
		if err := rows.Scan(&r.tenantID, &r.actorType, &r.actorName, &r.resource, &r.resourceID, &r.requestID, &r.outcome, &detail); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(detail, &r.detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func dayIn(t *testing.T, zone string, at time.Time) string {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return at.In(loc).Format("2006-01-02")
}

// TestUnlimitedTrafficAudit is T-709, the exit condition of the sprint: the first unlimited request
// of a tenant in a day of the tenant writes one row in each log; a hundred more write none; the
// next day of the tenant's zone writes one; a tenant with a rule that matches writes none; two
// gateways on one database write one.
func TestUnlimitedTrafficAudit(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	a, b, c := d.SeedTenant(t, "unl-a"), d.SeedTenant(t, "unl-b"), d.SeedTenant(t, "unl-c")
	d.SeedLimitRule(t, b, "tenant", "", "requests_per_day", 1_000_000) // B is limited
	d.SetTenantZone(t, c, "Pacific/Auckland", "", time.Time{})         // C's day is not the UTC day
	tenants := map[string]tenant.ID{"a": a, "b": b, "c": c}
	g1 := newZoneGateway(t, d, tenants, limitstest.Options{})

	t.Run("the first request writes one row in each log with the fields of section 5.6", func(t *testing.T) {
		start := g1.real.Now()
		r := g1.call(t, "a")
		if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		reqID := r.header.Get("X-Pandokos-Request-Id")
		testutil.Eventually(t, 5*time.Second, "the audit rows", func() bool {
			return len(unlimitedRows(t, d, false, a.String())) == 1 && len(unlimitedRows(t, d, true, a.String())) == 1
		})
		for _, platform := range []bool{false, true} {
			rows := unlimitedRows(t, d, platform, a.String())
			if len(rows) != 1 {
				t.Fatalf("platform log %v: %d rows", platform, len(rows))
			}
			row := rows[0]
			if row.actorType != "platform" || row.actorName != "gateway" || row.resource != "tenant" || row.resourceID != a.String() || row.outcome != "success" || row.requestID != reqID {
				t.Errorf("platform log %v: %+v", platform, row)
			}
			want := map[string]any{"day": dayIn(t, "UTC", start), "time_zone": "UTC", "provider": "openai", "model": "fake-basic"}
			for k, v := range want {
				if row.detail[k] != v {
					t.Errorf("platform log %v: detail %s = %v, want %v (detail %v)", platform, k, row.detail[k], v, row.detail)
				}
			}
			if len(row.detail) != len(want) {
				t.Errorf("platform log %v: detail has other fields: %v", platform, row.detail)
			}
		}
	})

	t.Run("a hundred more write none", func(t *testing.T) {
		for range 100 {
			if r := g1.call(t, "a"); r.status != 200 {
				t.Fatalf("%d %s", r.status, r.body)
			}
		}
		time.Sleep(300 * time.Millisecond) // nothing to wait for: give a wrong implementation the time to write
		if n := len(unlimitedRows(t, d, false, a.String())); n != 1 {
			t.Errorf("%d rows in the tenant's log", n)
		}
		if n := len(unlimitedRows(t, d, true, a.String())); n != 1 {
			t.Errorf("%d rows in the platform log", n)
		}
	})

	t.Run("a tenant with a rule that matches writes none", func(t *testing.T) {
		for range 5 {
			if r := g1.call(t, "b"); r.status != 200 {
				t.Fatalf("%d %s", r.status, r.body)
			}
		}
		time.Sleep(300 * time.Millisecond)
		if n := len(unlimitedRows(t, d, false, b.String())); n != 0 {
			t.Errorf("%d rows for a limited tenant", n)
		}
	})

	t.Run("the next day of the tenant's zone writes one", func(t *testing.T) {
		g1.real.Advance(24 * time.Hour)
		if r := g1.call(t, "a"); r.status != 200 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		testutil.Eventually(t, 5*time.Second, "the second day's rows", func() bool {
			return len(unlimitedRows(t, d, false, a.String())) == 2 && len(unlimitedRows(t, d, true, a.String())) == 2
		})
		days := map[any]bool{}
		for _, r := range unlimitedRows(t, d, false, a.String()) {
			days[r.detail["day"]] = true
		}
		if len(days) != 2 {
			t.Errorf("the two rows are for %v days", days)
		}
	})

	t.Run("the day is the tenant's civil date", func(t *testing.T) {
		now := g1.real.Now()
		if r := g1.call(t, "c"); r.status != 200 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		testutil.Eventually(t, 5*time.Second, "the row of the tenant with a zone", func() bool { return len(unlimitedRows(t, d, false, c.String())) == 1 })
		row := unlimitedRows(t, d, false, c.String())[0]
		if row.detail["day"] != dayIn(t, "Pacific/Auckland", now) || row.detail["time_zone"] != "Pacific/Auckland" {
			t.Errorf("detail %v, want the day of %s in Pacific/Auckland", row.detail, now)
		}
	})

	t.Run("the rows and the log hold no content, header or credential", func(t *testing.T) {
		// Every request above carried the canary prompt; the rows, the request lines and the new
		// fields (limit_state, limit_currency, price_currency) hold identifiers and names only.
		for _, platform := range []bool{false, true} {
			id := a.String()
			for _, r := range unlimitedRows(t, d, platform, id) {
				testutil.AssertNoCanary(t, "the audit row", fmt.Sprintf("%+v", r))
			}
		}
		testutil.AssertNoCanary(t, "the request log", g1.h.logs.String())
		for _, l := range g1.h.logs.Find(t, "request") {
			if l["limit_state"] == nil {
				t.Errorf("a request line without limit_state: %v", l)
			}
		}
	})

	t.Run("two gateways on one database write one", func(t *testing.T) {
		d2 := dbtest.NewMigrated(t)
		a2 := d2.SeedTenant(t, "unl-two")
		first := newZoneGateway(t, d2, map[string]tenant.ID{"a": a2}, limitstest.Options{})
		second := newZoneGateway(t, d2, map[string]tenant.ID{"a": a2}, limitstest.Options{})
		for i := 0; i < 4; i++ {
			for _, g := range []*zoneGateway{first, second} {
				if r := g.call(t, "a"); r.status != 200 {
					t.Fatalf("%d %s", r.status, r.body)
				}
			}
		}
		testutil.Eventually(t, 5*time.Second, "the row", func() bool { return len(unlimitedRows(t, d2, false, a2.String())) >= 1 })
		time.Sleep(300 * time.Millisecond)
		if n := len(unlimitedRows(t, d2, false, a2.String())); n != 1 {
			t.Errorf("%d rows in the tenant's log from two gateways", n)
		}
		if n := len(unlimitedRows(t, d2, true, a2.String())); n != 1 {
			t.Errorf("%d rows in the platform log from two gateways", n)
		}
	})
}
