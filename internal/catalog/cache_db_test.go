package catalog_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// world is a database with two tenants and a catalogue made with SQL, and the principals of the
// two tenants. The changes are made as the platform administrator would make them (rows of the
// same tables, so that the same notifications are sent); the handlers are tested in internal/admin.
type world struct {
	*env
	gateway *store.DB // the gateway role, through the view the test runs on
	a, b    tenant.ID
	pa, pb  authz.Principal
}

func newWorld(t *testing.T, d, view *dbtest.Database) *world {
	t.Helper()
	e := newEnvOn(t, d, view, "synthetic-resolve", "synthetic-cached", "synthetic-pooled")
	w := &world{env: e, gateway: view.Open(t, store.RoleGateway), a: d.SeedTenant(t, "tenant-a"), b: d.SeedTenant(t, "tenant-b")}
	e.sql(`INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url, is_default) SELECT id, 'main', 'https://synthetic.invalid', true FROM pandokos.provider WHERE name = 'synthetic'`)
	principal := func(id tenant.ID, name string) authz.Principal {
		return authz.Principal{Kind: authz.KindServiceAccount, ID: "sa-" + name, Name: "sa", TenantID: id, TenantName: name, TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}}
	}
	w.pa, w.pb = principal(w.a, "tenant-a"), principal(w.b, "tenant-b")
	return w
}

func (w *world) setAvailability(model, availability string) {
	w.t.Helper()
	w.sql(`UPDATE pandokos.model SET availability = $2 WHERE name = $1`, model, availability)
}

func (w *world) grant(model string, id tenant.ID) {
	w.t.Helper()
	w.sql(`INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id) SELECT id, $2::uuid FROM pandokos.model WHERE name = $1`, model, id.String())
}

func (w *world) revoke(model string, id tenant.ID) {
	w.t.Helper()
	w.sql(`DELETE FROM pandokos.model_tenant_grant WHERE tenant_id = $2::uuid AND model_id = (SELECT id FROM pandokos.model WHERE name = $1)`, model, id.String())
}

func (w *world) restrict(id tenant.ID, refs ...string) {
	w.t.Helper()
	if refs == nil {
		refs = []string{}
	}
	w.sql(`INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, $2::text[]) ON CONFLICT (tenant_id) DO UPDATE SET models = EXCLUDED.models`, id.String(), refs)
}

func (w *world) unrestrict(id tenant.ID) {
	w.t.Helper()
	w.sql(`DELETE FROM pandokos.tenant_model_restriction WHERE tenant_id = $1::uuid`, id.String())
}

func newCache(t *testing.T, db *store.DB, ttl time.Duration, now func() time.Time) *catalog.Cache {
	t.Helper()
	c, err := catalog.NewCache(catalog.CacheOptions{DB: db, Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"), TTL: ttl, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func modelID(t *testing.T, e *env, name string) string {
	t.Helper()
	var id string
	if err := e.scan(`SELECT id::text FROM pandokos.model WHERE name = $1`, name)(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// resolveFresh asks a cache that has just been made, so that the answer is the database's.
func resolveFresh(t *testing.T, db *store.DB, p authz.Principal, provider, name string) (catalog.Model, catalog.Endpoint, error) {
	t.Helper()
	return newCache(t, db, time.Minute, nil).Resolve(ctx(t), p, provider, name)
}

// TestResolveDB is T-398 (exit condition 1 at the database): a model that is selected and granted
// to tenant A resolves for A's principal and is refused to B's with the same error; `all` makes it
// visible to both; A's restriction and a key's list narrow it.
func TestResolveDB(t *testing.T) {
	d := dbtest.NewMigrated(t)
	w := newWorld(t, d, d)
	e := w.env
	e.sql(`UPDATE pandokos.model SET upstream_id = 'up-synthetic-resolve' WHERE name = 'synthetic-resolve'`)
	resolve := func(p authz.Principal, provider, name string) (catalog.Model, catalog.Endpoint, error) {
		t.Helper()
		return resolveFresh(t, w.gateway, p, provider, name)
	}
	w.grant("synthetic-resolve", w.a)

	m, ep, err := resolve(w.pa, "synthetic", "synthetic-resolve")
	if err != nil || m.ID != modelID(t, e, "synthetic-resolve") || m.Provider != "synthetic" || m.Name != "synthetic-resolve" || m.UpstreamID != "up-synthetic-resolve" ||
		m.Availability != "selected" || ep.Name != "main" || ep.BaseURL != "https://synthetic.invalid" || !ep.Default || !ep.Enabled {
		t.Fatalf("A: %+v %+v %v", m, ep, err)
	}
	_, _, errB := resolve(w.pb, "synthetic", "synthetic-resolve")
	_, _, errMissing := resolve(w.pb, "synthetic", "synthetic-does-not-exist")
	_, _, errUpstream := resolve(w.pa, "synthetic", "up-synthetic-resolve")
	if !errors.Is(errB, catalog.ErrModelNotAllowed) || errB != errMissing || errB != errUpstream { //nolint:errorlint // the point is that it is the same value
		t.Errorf("B: %v; a model that does not exist: %v; an upstream identifier: %v: they must be the same error", errB, errMissing, errUpstream)
	}
	if _, _, err := resolve(w.pa, "no-such-provider", "x"); !errors.Is(err, catalog.ErrProviderUnavailable) {
		t.Errorf("an unknown provider: %v", err)
	}
	// The seed is available to both.
	for name, p := range map[string]authz.Principal{"A": w.pa, "B": w.pb} {
		if m, _, err := resolve(p, "openai", "gpt-4o"); err != nil || m.Name != "gpt-4o" {
			t.Errorf("%s: gpt-4o %+v %v", name, m, err)
		}
		if _, ep, err := resolve(p, "anthropic", "claude-sonnet-4-6"); err != nil || ep.BaseURL != "https://api.anthropic.com" {
			t.Errorf("%s: claude-sonnet-4-6 %+v %v", name, ep, err)
		}
	}

	// all makes it visible to both, selected hides it again; the grant is kept.
	w.setAvailability("synthetic-resolve", "all")
	if _, _, err := resolve(w.pb, "synthetic", "synthetic-resolve"); err != nil {
		t.Errorf("B, after availability all: %v", err)
	}
	w.setAvailability("synthetic-resolve", "selected")
	if _, _, err := resolve(w.pb, "synthetic", "synthetic-resolve"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("B, back to selected: %v", err)
	}
	if _, _, err := resolve(w.pa, "synthetic", "synthetic-resolve"); err != nil {
		t.Errorf("A, back to selected: %v", err)
	}

	// A's restriction narrows it and does not widen; B is not restricted by it.
	w.restrict(w.a, "openai/gpt-4o")
	if _, _, err := resolve(w.pa, "synthetic", "synthetic-resolve"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("A, restricted to gpt-4o only: %v", err)
	}
	if _, _, err := resolve(w.pa, "openai", "gpt-4o"); err != nil {
		t.Errorf("A, restricted to gpt-4o: %v", err)
	}
	if _, _, err := resolve(w.pa, "openai", "gpt-4o-mini"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("A, restricted to gpt-4o, asks for gpt-4o-mini: %v", err)
	}
	if _, _, err := resolve(w.pb, "openai", "gpt-4o-mini"); err != nil {
		t.Errorf("B: %v", err)
	}
	w.restrict(w.a, "synthetic/synthetic-resolve", "openai/gpt-4o")
	if _, _, err := resolve(w.pa, "synthetic", "synthetic-resolve"); err != nil {
		t.Errorf("A, restricted to it: %v", err)
	}
	// An empty restriction allows nothing.
	w.restrict(w.a)
	if _, _, err := resolve(w.pa, "openai", "gpt-4o"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("A, restricted to nothing: %v", err)
	}
	w.restrict(w.a, "synthetic/synthetic-resolve", "openai/gpt-4o")

	// A key's list narrows it further.
	listed := w.pa
	listed.Models = []string{"openai/gpt-4o"}
	if _, _, err := resolve(listed, "openai", "gpt-4o"); err != nil {
		t.Errorf("a key listing gpt-4o: %v", err)
	}
	if _, _, err := resolve(listed, "synthetic", "synthetic-resolve"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("a key listing gpt-4o asks for the model of the restriction: %v", err)
	}
	w.unrestrict(w.a)
	if _, _, err := resolve(listed, "synthetic", "synthetic-resolve"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("a key listing gpt-4o, no restriction: %v", err)
	}
	if _, _, err := resolve(w.pa, "synthetic", "synthetic-resolve"); err != nil {
		t.Errorf("A, restriction cleared: %v", err)
	}

	// LoadTenantAccess is what the cache loads: the grants and the restriction of one tenant.
	ta, err := catalog.LoadTenantAccess(ctx(t), w.gateway, w.a)
	if err != nil || !ta.Granted[modelID(t, e, "synthetic-resolve")] || ta.Restricted || len(ta.Granted) != 1 {
		t.Errorf("tenant access of A: %+v %v", ta, err)
	}
	w.restrict(w.a, "openai/gpt-4o")
	if ta, err = catalog.LoadTenantAccess(ctx(t), w.gateway, w.a); err != nil || !ta.Restricted || !ta.Allowed["openai/gpt-4o"] || len(ta.Allowed) != 1 {
		t.Errorf("tenant access of A with a restriction: %+v %v", ta, err)
	}
	if tb, err := catalog.LoadTenantAccess(ctx(t), w.gateway, w.b); err != nil || len(tb.Granted) != 0 || tb.Restricted {
		t.Errorf("tenant access of B: %+v %v", tb, err)
	}

	// Retired, a disabled provider, and a principal that is the platform's.
	w.unrestrict(w.a)
	e.sql(`UPDATE pandokos.model SET status = 'retired' WHERE name = 'synthetic-resolve'`)
	if _, _, err := resolve(w.pa, "synthetic", "synthetic-resolve"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("a retired model: %v", err)
	}
	e.sql(`UPDATE pandokos.provider SET enabled = false WHERE name = 'synthetic'`)
	if _, _, err := resolve(w.pa, "synthetic", "synthetic-cached"); !errors.Is(err, catalog.ErrProviderUnavailable) {
		t.Errorf("a disabled provider: %v", err)
	}
	if _, _, err := resolve(authz.Principal{Kind: authz.KindPlatform, ID: "pk", Roles: []string{authz.RolePlatformAdmin}}, "openai", "gpt-4o"); !errors.Is(err, catalog.ErrModelNotAllowed) {
		t.Errorf("a platform principal: %v", err)
	}
}

// TestCacheDB is T-397: changes of the catalogue reach the cache through the listener within two
// seconds, and, where there is no listener (a transaction pooler), within the lifetime.
func TestCacheDB(t *testing.T) {
	t.Run("direct: by notification", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		w := newWorld(t, d, d)
		logs := &testutil.LogBuffer{}
		cache := newCache(t, w.gateway, 5*time.Minute, nil) // a lifetime so long that only a notification can explain what follows
		l, err := store.NewListener(store.ListenerOptions{Conn: d.ConnOptions("gateway"), Role: store.RoleGateway, Logger: telemetry.NewLogger(logs, "debug"), OnNotify: cache.Invalidate, OnReset: cache.Flush})
		if err != nil {
			t.Fatal(err)
		}
		lctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { l.Run(lctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		testutil.Eventually(t, 10*time.Second, "the cache listener to connect", func() bool {
			return strings.Contains(logs.String(), `"msg":"cache listener connected"`)
		})

		allowed := func(p authz.Principal) bool {
			_, _, err := cache.Resolve(ctx(t), p, "synthetic", "synthetic-cached")
			if err != nil && !errors.Is(err, catalog.ErrModelNotAllowed) {
				t.Fatalf("a load failed: %v", err)
			}
			return err == nil
		}
		eventually := func(what string, cond func() bool) {
			t.Helper()
			testutil.Eventually(t, 2*time.Second, what, cond)
		}
		if allowed(w.pa) { // the cache now holds the snapshot and A's access
			t.Fatal("a model that is granted to nobody resolves")
		}

		w.grant("synthetic-cached", w.a)
		eventually("the grant to reach Resolve", func() bool { return allowed(w.pa) })
		if allowed(w.pb) {
			t.Error("the grant of A reached B")
		}
		w.revoke("synthetic-cached", w.a)
		eventually("the revocation to reach Resolve", func() bool { return !allowed(w.pa) })

		w.grant("synthetic-cached", w.a)
		eventually("the second grant", func() bool { return allowed(w.pa) })
		w.restrict(w.a, "openai/gpt-4o")
		eventually("the restriction to reach Resolve", func() bool { return !allowed(w.pa) })
		w.unrestrict(w.a)
		eventually("the clearing of the restriction", func() bool { return allowed(w.pa) })

		// A change of the model itself (the catalogue payload): availability, status.
		w.setAvailability("synthetic-cached", "all")
		eventually("availability all to reach B", func() bool { return allowed(w.pb) })
		w.setAvailability("synthetic-cached", "selected")
		eventually("availability selected to reach B", func() bool { return !allowed(w.pb) })
		e := w.env
		e.sql(`UPDATE pandokos.model SET status = 'retired' WHERE name = 'synthetic-cached'`)
		eventually("a retirement to reach Resolve", func() bool { return !allowed(w.pa) })

		// A price version reaches Versions.
		id := modelID(t, e, "synthetic-cached")
		versions := func() []pricing.Version {
			s, err := cache.Snapshot(ctx(t))
			if err != nil {
				t.Fatal(err)
			}
			return s.Versions(id)
		}
		if len(versions()) != 0 {
			t.Fatal("a model with no price has versions")
		}
		e.sql(`INSERT INTO pandokos.price_version (model_id, currency, source) VALUES ($1::uuid, 'EUR', 'override')`, id)
		e.sql(`INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate) SELECT id, 'input', 0, 4.5 FROM pandokos.price_version WHERE model_id = $1::uuid`, id)
		eventually("a price version to reach Versions", func() bool { return len(versions()) == 1 })
		v := versions()[0]
		if v.Currency != "EUR" || v.Source != "override" || len(v.Rates) != 1 || v.Rates[0].PerMillion.String() != "4.5" {
			t.Errorf("the version in the cache: %+v", v)
		}
		if got, ok := pricing.VersionAt(versions(), time.Now().Add(time.Second)); !ok || got.ID != v.ID {
			t.Errorf("VersionAt over the cached versions: %+v %v", got, ok)
		}
	})

	t.Run("through the pooler: by lifetime", func(t *testing.T) {
		d := dbtest.New(t)
		pooled := d.Pooled(t)
		d.Migrate(t)
		w := newWorld(t, d, pooled)
		var mu sync.Mutex
		clock := time.Now()
		now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
		advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
		const ttl = 30 * time.Second
		cache := newCache(t, w.gateway, ttl, now)

		allowed := func(p authz.Principal) bool {
			_, _, err := cache.Resolve(ctx(t), p, "synthetic", "synthetic-pooled")
			if err != nil && !errors.Is(err, catalog.ErrModelNotAllowed) {
				t.Fatalf("a load failed: %v", err)
			}
			return err == nil
		}
		if allowed(w.pa) {
			t.Fatal("a model that is granted to nobody resolves")
		}
		w.grant("synthetic-pooled", w.a)
		// No notification arrives through a pooler: the cache keeps its answer until its lifetime ends.
		advance(ttl - time.Second)
		if allowed(w.pa) {
			t.Error("the grant reached the cache before its lifetime ended, without a notification")
		}
		advance(time.Second)
		if !allowed(w.pa) {
			t.Error("the grant did not reach the cache within its lifetime")
		}
		// A revocation and a restriction the same way.
		w.revoke("synthetic-pooled", w.a)
		advance(ttl)
		if allowed(w.pa) {
			t.Error("the revocation did not reach the cache within its lifetime")
		}
		w.grant("synthetic-pooled", w.a)
		advance(ttl)
		if !allowed(w.pa) {
			t.Fatal("the second grant")
		}
		w.restrict(w.a, "openai/gpt-4o")
		advance(ttl)
		if allowed(w.pa) {
			t.Error("the restriction did not reach the cache within its lifetime")
		}
		// A price version.
		id := modelID(t, w.env, "synthetic-pooled")
		w.sql(`INSERT INTO pandokos.price_version (model_id, currency, source) VALUES ($1::uuid, 'USD', 'override')`, id)
		w.sql(`INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate) SELECT id, 'input', 0, 1 FROM pandokos.price_version WHERE model_id = $1::uuid`, id)
		s, err := cache.Snapshot(ctx(t))
		if err != nil || len(s.Versions(id)) != 0 {
			t.Errorf("the version reached the snapshot before its lifetime ended: %v %v", s.Versions(id), err)
		}
		advance(ttl)
		if s, err = cache.Snapshot(ctx(t)); err != nil || len(s.Versions(id)) != 1 {
			t.Errorf("the version did not reach the snapshot within its lifetime: %v %v", s.Versions(id), err)
		}
	})
}
