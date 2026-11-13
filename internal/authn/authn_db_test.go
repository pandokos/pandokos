package authn_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

func newAuth(t *testing.T, db *store.DB, ttl time.Duration, kinds ...authn.Kind) (*authn.Authenticator, *testutil.LogBuffer) {
	t.Helper()
	logs := &testutil.LogBuffer{}
	a, err := authn.New(authn.Options{DB: db, Logger: telemetry.NewLogger(logs, "debug"), Kinds: kinds, CacheTTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return a, logs
}

// fixture creates a tenant, a service account and keys with the superuser connection.
type fixture struct {
	d      *dbtest.Database
	tenant tenant.ID
	sa     string
}

func newFixture(t *testing.T, d *dbtest.Database, tenantName string) fixture {
	t.Helper()
	f := fixture{d: d, tenant: d.SeedTenant(t, tenantName)}
	if err := d.Super(t).QueryRow(ctx(t), "INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'bot') RETURNING id::text", f.tenant.String()).Scan(&f.sa); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"tenant-admin", "inference"} {
		if _, err := d.Super(t).Exec(ctx(t), "INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, $3)", f.tenant.String(), f.sa, role); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// key inserts a virtual key. extra is a SQL fragment for the other columns: "expires_at = ...".
func (f fixture) key(t *testing.T, hashOf *[32]byte, scopes []string, expires string) (string, string) {
	t.Helper()
	key, keyID, hash, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	if hashOf != nil {
		hash = *hashOf
	}
	var sc any
	if scopes != nil {
		sc = scopes
	}
	if _, err := f.d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash, scopes, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::text[], `+orNull(expires)+`)`, f.tenant.String(), f.sa, keyID, hash[:], sc); err != nil {
		t.Fatal(err)
	}
	return key.Reveal(), keyID
}

func orNull(s string) string {
	if s == "" {
		return "NULL"
	}
	return s
}

func (f fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.d.Super(t).Exec(ctx(t), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func platformKey(t *testing.T, d *dbtest.Database, bootstrap bool, expires string) (string, string) {
	t.Helper()
	key, keyID, hash, err := authn.Generate(authn.KindPlatform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.platform_key (key_id, description, secret_hash, bootstrap, expires_at) VALUES ($1, 'ops key', $2, $3, `+orNull(expires)+`)`, keyID, hash[:], bootstrap); err != nil {
		t.Fatal(err)
	}
	return key.Reveal(), keyID
}

func failure(t *testing.T, err error) *authn.Failure {
	t.Helper()
	var f *authn.Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want a *authn.Failure", err)
	}
	if !errors.Is(err, authz.ErrUnauthenticated) {
		t.Errorf("%v does not wrap authz.ErrUnauthenticated", err)
	}
	return f
}

// TestAuthenticate is T-260.
func TestAuthenticate(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		f := newFixture(t, d, "acme")
		gw, admin := d.Open(t, store.RoleGateway), d.Open(t, store.RoleAdmin)
		gwAuth, _ := newAuth(t, gw, 0, authn.KindTenant)
		adAuth, _ := newAuth(t, admin, 0, authn.KindTenant, authn.KindPlatform)

		good, goodID := f.key(t, nil, nil, "")
		scoped, _ := f.key(t, nil, []string{authz.ModelInvoke, "virtual_key:read"}, "now() + interval '1 hour'")

		t.Run("a good key gives the principal", func(t *testing.T) {
			p, err := gwAuth.Authenticate(ctx(t), good)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != authz.KindServiceAccount || p.ID != f.sa || p.Name != "bot" || p.KeyID != goodID || p.Bootstrap ||
				p.TenantID != f.tenant || p.TenantName != "acme" || p.TenantState != tenant.StateActive ||
				strings.Join(p.Roles, ",") != "inference,tenant-admin" || p.Scopes != nil {
				t.Errorf("principal: %+v", p)
			}
			sp, err := gwAuth.Authenticate(ctx(t), scoped)
			if err != nil || strings.Join(sp.Scopes, ",") != "model:invoke,virtual_key:read" {
				t.Errorf("scoped principal: %+v %v", sp, err)
			}
			ap, err := adAuth.Authenticate(ctx(t), good)
			if err != nil || ap.ID != p.ID {
				t.Errorf("the admin authenticator accepts virtual keys: %+v %v", ap, err)
			}
		})

		t.Run("a suspended tenant still authenticates; the decision refuses it", func(t *testing.T) {
			f.exec(t, "UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid", f.tenant.String())
			defer f.exec(t, "UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid", f.tenant.String())
			p, err := gwAuth.Authenticate(ctx(t), good)
			if err != nil || p.TenantState != tenant.StateSuspended {
				t.Fatalf("%+v %v", p, err)
			}
			if authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) != authz.DenyTenantState {
				t.Error("a suspended tenant is not refused by the decision")
			}
		})

		reasons := []struct {
			name   string
			setup  func() string // returns the key to present
			reason string
			keyID  bool
			tenant bool
			auth   *authn.Authenticator
		}{
			{name: "malformed", setup: func() string { return "pdk_nonsense" }, reason: "malformed", auth: gwAuth},
			{name: "empty", setup: func() string { return "" }, reason: "malformed", auth: gwAuth},
			{name: "kind not accepted", setup: func() string {
				k, _ := platformKey(t, d, false, "")
				return k
			}, reason: "kind_not_accepted", keyID: true, auth: gwAuth},
			{name: "unknown key", setup: func() string {
				k, _, _, _ := authn.Generate(authn.KindTenant)
				return k.Reveal()
			}, reason: "unknown_key", keyID: true, auth: gwAuth},
			{name: "wrong secret", setup: func() string {
				_, _, other, _ := authn.Generate(authn.KindTenant)
				k, _ := f.key(t, &other, nil, "")
				return k
			}, reason: "wrong_secret", keyID: true, tenant: true, auth: gwAuth},
			{name: "revoked", setup: func() string {
				k, id := f.key(t, nil, nil, "")
				f.exec(t, "UPDATE pandokos.virtual_key SET revoked_at = now() WHERE key_id = $1", id)
				return k
			}, reason: "revoked", keyID: true, tenant: true, auth: gwAuth},
			{name: "expired", setup: func() string {
				k, _ := f.key(t, nil, nil, "now() - interval '1 second'")
				return k
			}, reason: "expired", keyID: true, tenant: true, auth: gwAuth},
			{name: "service account disabled", setup: func() string {
				g := newFixture(t, d, "other-disabled")
				k, _ := g.key(t, nil, nil, "")
				g.exec(t, "UPDATE pandokos.service_account SET state = 'disabled' WHERE id = $1::uuid", g.sa)
				return k
			}, reason: "service_account_disabled", keyID: true, tenant: true, auth: gwAuth},
			{name: "service account not yet valid", setup: func() string {
				g := newFixture(t, d, "other-early")
				k, _ := g.key(t, nil, nil, "")
				g.exec(t, "UPDATE pandokos.service_account SET valid_from = now() + interval '1 hour' WHERE id = $1::uuid", g.sa)
				return k
			}, reason: "service_account_not_valid", keyID: true, tenant: true, auth: gwAuth},
			{name: "service account no longer valid", setup: func() string {
				g := newFixture(t, d, "other-late")
				k, _ := g.key(t, nil, nil, "")
				g.exec(t, "UPDATE pandokos.service_account SET valid_until = now() - interval '1 second' WHERE id = $1::uuid", g.sa)
				return k
			}, reason: "service_account_not_valid", keyID: true, tenant: true, auth: gwAuth},
			{name: "platform key, unknown", setup: func() string {
				k, _, _, _ := authn.Generate(authn.KindPlatform)
				return k.Reveal()
			}, reason: "unknown_key", keyID: true, auth: adAuth},
			{name: "platform key, revoked", setup: func() string {
				k, id := platformKey(t, d, false, "")
				f.exec(t, "UPDATE pandokos.platform_key SET revoked_at = now() WHERE key_id = $1", id)
				return k
			}, reason: "revoked", keyID: true, auth: adAuth},
			{name: "platform key, expired", setup: func() string {
				k, _ := platformKey(t, d, false, "now() - interval '1 second'")
				return k
			}, reason: "expired", keyID: true, auth: adAuth},
			{name: "platform key, wrong secret", setup: func() string {
				_, _, other, _ := authn.Generate(authn.KindPlatform)
				k, id, _, _ := authn.Generate(authn.KindPlatform)
				f.exec(t, `INSERT INTO pandokos.platform_key (key_id, secret_hash) VALUES ($1, $2)`, id, other[:])
				return k.Reveal()
			}, reason: "wrong_secret", keyID: true, auth: adAuth},
		}
		for _, r := range reasons {
			t.Run(r.name, func(t *testing.T) {
				presented := r.setup()
				_, err := r.auth.Authenticate(ctx(t), presented)
				fl := failure(t, err)
				if fl.Reason != r.reason {
					t.Errorf("reason %q, want %q", fl.Reason, r.reason)
				}
				if r.keyID != (fl.KeyID != "") {
					t.Errorf("KeyID %q", fl.KeyID)
				}
				if r.keyID && !strings.Contains(presented, fl.KeyID) {
					t.Errorf("KeyID %q is not the key ID of the presented key", fl.KeyID)
				}
				if r.tenant != !fl.TenantID.IsZero() {
					t.Errorf("TenantID %v", fl.TenantID)
				}
				if msg := err.Error(); msg != "authn: "+r.reason {
					t.Errorf("Error() = %q", msg)
				}
				if presented != "" {
					testutil.AssertAbsent(t, "the error", err.Error(), testutil.KeyForms(presented)...)
				}
			})
		}

		t.Run("platform keys", func(t *testing.T) {
			boot, bootID := platformKey(t, d, true, "now() + interval '1 hour'")
			p, err := adAuth.Authenticate(ctx(t), boot)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != authz.KindPlatform || p.ID != bootID || p.KeyID != bootID || p.Name != "ops key" || !p.Bootstrap ||
				!p.TenantID.IsZero() || strings.Join(p.Roles, ",") != "platform-admin" || p.Scopes != nil {
				t.Errorf("platform principal: %+v", p)
			}
			plain, _ := platformKey(t, d, false, "")
			if p, err := adAuth.Authenticate(ctx(t), plain); err != nil || p.Bootstrap {
				t.Errorf("%+v %v", p, err)
			}
			// The gateway cannot even look: the data plane does not hold platform keys.
			if _, err := gwAuth.Authenticate(ctx(t), plain); failure(t, err).Reason != "kind_not_accepted" {
				t.Errorf("gateway with a platform key: %v", err)
			}
		})

		t.Run("an authenticator needs a pool that can read what it serves", func(t *testing.T) {
			if _, err := authn.New(authn.Options{DB: gw, Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "info"), Kinds: []authn.Kind{authn.KindPlatform}}); err == nil {
				t.Error("the gateway pool was accepted for platform keys")
			}
			if _, err := authn.New(authn.Options{DB: gw, Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "info")}); err == nil {
				t.Error("an authenticator without a kind was accepted")
			}
			if _, err := authn.New(authn.Options{Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "info"), Kinds: []authn.Kind{authn.KindTenant}}); err == nil {
				t.Error("an authenticator without a database was accepted")
			}
			if _, err := authn.New(authn.Options{DB: gw, Kinds: []authn.Kind{authn.KindTenant}}); err == nil {
				t.Error("an authenticator without a logger was accepted")
			}
		})

		t.Run("a stopped database is unavailable, not unauthenticated", func(t *testing.T) {
			d.SetAcceptingConnections(t, false)
			defer d.SetAcceptingConnections(t, true)
			_, err := gwAuth.Authenticate(ctx(t), good)
			if !errors.Is(err, authz.ErrUnavailable) || errors.Is(err, authz.ErrUnauthenticated) {
				t.Errorf("err = %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "pandokos") || strings.Contains(err.Error(), d.Host)) {
				t.Errorf("the error carries the driver's text: %v", err)
			}
		})
	})
}

// TestTenantComesFromTheKey is T-261.
func TestTenantComesFromTheKey(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := newFixture(t, d, "tenant-a"), newFixture(t, d, "tenant-b")
	keyA, idA := a.key(t, nil, nil, "")
	keyB, _ := b.key(t, nil, nil, "")
	auth, _ := newAuth(t, d.Open(t, store.RoleGateway), 0, authn.KindTenant)

	pa, err := auth.Authenticate(ctx(t), keyA)
	if err != nil || pa.TenantID != a.tenant || pa.ID != a.sa || pa.Name != "bot" {
		t.Fatalf("A: %+v %v", pa, err)
	}
	pb, err := auth.Authenticate(ctx(t), keyB)
	if err != nil || pb.TenantID != b.tenant || pb.ID != b.sa || pb.Name != "bot" {
		t.Fatalf("B: %+v %v", pb, err)
	}
	if pa.ID == pb.ID {
		t.Error("two service accounts of the same name share an identity")
	}

	// A key row copied into the other tenant with the same key ID is impossible.
	_, err = d.Super(t).Exec(ctx(t), "INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, sha256('x'::bytea))", b.tenant.String(), b.sa, idA)
	if kind, _, ok := store.ConstraintViolation(err); !ok || kind != "unique" {
		t.Errorf("a duplicate key ID: %v", err)
	}

	// A directory entry pointed at the other tenant makes the key unknown, not valid for B.
	if _, err := d.Super(t).Exec(ctx(t), "UPDATE pandokos.virtual_key_directory SET tenant_id = $1::uuid WHERE key_id = $2", b.tenant.String(), idA); err != nil {
		t.Fatal(err)
	}
	_, err = auth.Authenticate(ctx(t), keyA)
	if f := failure(t, err); f.Reason != "unknown_key" {
		t.Errorf("reason %q, want unknown_key", f.Reason)
	}
	if p, err := auth.Authenticate(ctx(t), keyB); err != nil || p.TenantID != b.tenant {
		t.Errorf("B after the tampering: %+v %v", p, err)
	}
}

type created struct {
	tenant adminapi.Tenant
	admin  string
	sa     adminapi.ServiceAccount
	key    string
	keyID  string
}

// newKey makes a tenant, an inference service account and a key through the API.
func newKey(t *testing.T, api *apitest.API, name string, roles ...string) created {
	t.Helper()
	c := created{}
	c.tenant, c.admin = api.Tenant(t, name)
	cl := api.Client(c.admin)
	sa, err := cl.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: c.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "caller", Roles: roles}})
	if err != nil {
		t.Fatal(err)
	}
	c.sa = *sa
	k, err := cl.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: c.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID}})
	if err != nil {
		t.Fatal(err)
	}
	c.key, c.keyID = k.Secret, k.Key.ID
	return c
}

// TestRevocationBoundWithoutNotifications is T-262: with a lifetime of 2 s and no listener, a
// change reaches the cache within the lifetime and no later.
func TestRevocationBoundWithoutNotifications(t *testing.T) {
	api := apitest.New(t)
	auth, _ := newAuth(t, api.DB.Open(t, store.RoleGateway), 2*time.Second, authn.KindTenant)

	t.Run("revoked key", func(t *testing.T) {
		c := newKey(t, api, "revoke-me", authz.RoleInference)
		if _, err := auth.Authenticate(ctx(t), c.key); err != nil { // fills the cache
			t.Fatal(err)
		}
		if _, err := api.Client(c.admin).RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: c.tenant.ID, Key: c.keyID}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.Authenticate(ctx(t), c.key); err != nil {
			t.Fatalf("the cached key must still be accepted at once: %v", err)
		}
		testutil.Eventually(t, 2500*time.Millisecond, "the revoked key to be refused", func() bool {
			_, err := auth.Authenticate(ctx(t), c.key)
			var f *authn.Failure
			return errors.As(err, &f) && f.Reason == "revoked"
		})
	})

	t.Run("disabled service account", func(t *testing.T) {
		c := newKey(t, api, "disable-me", authz.RoleInference)
		if _, err := auth.Authenticate(ctx(t), c.key); err != nil {
			t.Fatal(err)
		}
		if _, err := api.Client(c.admin).DisableServiceAccount(ctx(t), adminapi.DisableServiceAccountInput{Tenant: c.tenant.ID, ServiceAccount: c.sa.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.Authenticate(ctx(t), c.key); err != nil {
			t.Fatalf("the cached key must still be accepted at once: %v", err)
		}
		testutil.Eventually(t, 2500*time.Millisecond, "the key of the disabled account to be refused", func() bool {
			_, err := auth.Authenticate(ctx(t), c.key)
			var f *authn.Failure
			return errors.As(err, &f) && f.Reason == "service_account_disabled"
		})
	})

	t.Run("removed role", func(t *testing.T) {
		c := newKey(t, api, "unrole-me", authz.RoleInference)
		p, err := auth.Authenticate(ctx(t), c.key)
		if err != nil || authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) != authz.Allow {
			t.Fatalf("%+v %v", p, err)
		}
		if _, err := api.Client(c.admin).UpdateServiceAccount(ctx(t), adminapi.UpdateServiceAccountInput{Tenant: c.tenant.ID, ServiceAccount: c.sa.ID, Body: adminapi.ServiceAccountUpdate{Roles: []string{}}}); err != nil {
			t.Fatal(err)
		}
		if p, err := auth.Authenticate(ctx(t), c.key); err != nil || authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) != authz.Allow {
			t.Fatalf("the cached principal must still be allowed at once: %+v %v", p, err)
		}
		testutil.Eventually(t, 2500*time.Millisecond, "the role to be gone", func() bool {
			p, err := auth.Authenticate(ctx(t), c.key)
			return err == nil && authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) == authz.Deny
		})
	})
}

// TestRevocationByNotification is T-263: with a lifetime of 60 s and a listener on the direct
// connection, a change reaches the cache within 2 s.
func TestRevocationByNotification(t *testing.T) {
	api := apitest.New(t)
	logs := &testutil.LogBuffer{}
	auth, err := authn.New(authn.Options{DB: api.DB.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(logs, "debug"), Kinds: []authn.Kind{authn.KindTenant}, CacheTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	l, err := store.NewListener(store.ListenerOptions{
		Conn: api.DB.ConnOptions("gateway"), Role: store.RoleGateway, Logger: telemetry.NewLogger(logs, "debug"),
		OnNotify: auth.Invalidate, OnReset: auth.Flush,
	})
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

	c := newKey(t, api, "notify-me", authz.RoleInference)
	if _, err := auth.Authenticate(ctx(t), c.key); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Client(c.admin).RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: c.tenant.ID, Key: c.keyID}); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 2*time.Second, "the revoked key to be refused", func() bool {
		_, err := auth.Authenticate(ctx(t), c.key)
		var f *authn.Failure
		return errors.As(err, &f) && f.Reason == "revoked"
	})

	// Suspension: the principal carries the state, and the decision refuses it.
	s := newKey(t, api, "suspend-me", authz.RoleInference)
	if p, err := auth.Authenticate(ctx(t), s.key); err != nil || authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) != authz.Allow {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := api.Client(api.PlatformKey()).SuspendTenant(ctx(t), adminapi.SuspendTenantInput{Tenant: s.tenant.ID}); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 2*time.Second, "the suspended tenant to be refused", func() bool {
		p, err := auth.Authenticate(ctx(t), s.key)
		return err == nil && authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) == authz.DenyTenantState
	})
	if _, err := api.Client(api.PlatformKey()).ReactivateTenant(ctx(t), adminapi.ReactivateTenantInput{Tenant: s.tenant.ID}); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 2*time.Second, "the reactivated tenant to work again", func() bool {
		p, err := auth.Authenticate(ctx(t), s.key)
		return err == nil && authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) == authz.Allow
	})
}
