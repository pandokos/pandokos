package gateway_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
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
