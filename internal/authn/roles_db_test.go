package authn_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// keyOf inserts a virtual key for the service account and returns the secret.
func keyOf(t *testing.T, d *dbtest.Database, tid tenant.ID, sa string) string {
	t.Helper()
	key, keyID, hash, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, d, `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, tid.String(), sa, keyID, hash[:])
	return key.Reveal()
}

// TestCustomRolePrincipal is T-857: the permissions of the custom roles of a service account reach
// the principal of a key and of a token, an inactive role and a name without a role give none, and a
// change of the role drops the cached principal through the notification.
func TestCustomRolePrincipal(t *testing.T) {
	d := dbtest.NewMigrated(t)
	logs := &testutil.LogBuffer{}
	r := newTokenRig(t, d, time.Minute)
	l, err := store.NewListener(store.ListenerOptions{
		Conn: d.ConnOptions("gateway"), Role: store.RoleGateway, Logger: newLogger(logs),
		OnNotify: r.auth.Invalidate, OnReset: r.auth.Flush,
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

	tid, other := d.SeedTenant(t, "custom-roles"), d.SeedTenant(t, "custom-roles-other")
	d.CustomRole(t, tid, "reader", "role:read", "member:read")
	d.CustomRole(t, other, "reader", "usage:read") // the same name in another tenant is another role

	// One account for a key, one for a token, one whose role does not exist, one in the other tenant.
	keyAccount := newServiceAccount(t, d, tid, "key-account", authz.RoleInference, "reader")
	tokenAccount := newServiceAccount(t, d, tid, "token-account", authz.RoleInference, "reader")
	ghostAccount := newServiceAccount(t, d, tid, "ghost-account", authz.RoleInference, "ghost")
	otherAccount := newServiceAccount(t, d, other, "other-account", "reader")
	key, ghostKey, otherKey := keyOf(t, d, tid, keyAccount), keyOf(t, d, tid, ghostAccount), keyOf(t, d, other, otherAccount)
	const aud = "custom-roles-aud"
	issuer := newTrustedIssuer(t, d, tid, r.idp, aud)
	mapSubject(t, d, tid, issuer, false, "workload", tokenAccount)

	authKey := func(t *testing.T, k string) authz.Principal {
		t.Helper()
		p, err := r.auth.Authenticate(ctx(t), k)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	authToken := func(t *testing.T) authz.Principal {
		t.Helper()
		p, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "workload", aud))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	granted := func(p authz.Principal) []string { return p.Granted }
	eventually := func(t *testing.T, what string, ok func() bool) {
		t.Helper()
		testutil.Eventually(t, 3*time.Second, what, ok)
	}

	t.Run("a key and a token carry the permissions of the custom role", func(t *testing.T) {
		want := []string{"member:read", "role:read"}
		kp, tp := authKey(t, key), authToken(t)
		for name, p := range map[string]authz.Principal{"key": kp, "token": tp} {
			if !reflect.DeepEqual(granted(p), want) {
				t.Errorf("%s: Granted %v, want %v", name, p.Granted, want)
			}
			if strings.Join(p.Roles, ",") != "inference,reader" {
				t.Errorf("%s: Roles %v", name, p.Roles)
			}
			eff := p.Effective()
			for _, perm := range []string{"member:read", "role:read", "model:invoke"} {
				if authz.Decide(p, perm, authz.Target{TenantID: tid}) != authz.Allow {
					t.Errorf("%s: %s is not allowed (effective %v)", name, perm, eff)
				}
			}
			if authz.Decide(p, "member:write", authz.Target{TenantID: tid}) != authz.Deny {
				t.Errorf("%s: member:write is allowed", name)
			}
		}
	})
	t.Run("the same name in another tenant is not this role", func(t *testing.T) {
		if got := granted(authKey(t, otherKey)); !reflect.DeepEqual(got, []string{"usage:read"}) {
			t.Errorf("the other tenant's account: Granted %v, want its own role's [usage:read]", got)
		}
	})
	t.Run("a name without a role gives nothing", func(t *testing.T) {
		p := authKey(t, ghostKey)
		if len(p.Granted) != 0 || strings.Join(p.Roles, ",") != "ghost,inference" {
			t.Errorf("Granted %v Roles %v", p.Granted, p.Roles)
		}
		if authz.Decide(p, "role:read", authz.Target{TenantID: tid}) != authz.Deny {
			t.Error("a name without a role allows role:read")
		}
	})
	t.Run("after a change of the role the cached principal is dropped", func(t *testing.T) {
		authKey(t, key) // warm: the next call would be served from the cache
		exec(t, d, `UPDATE pandokos.tenant_role SET permissions = ARRAY['role:write', 'member:write'] WHERE tenant_id = $1::uuid AND name = 'reader'`, tid.String())
		eventually(t, "the key to carry the new permissions", func() bool {
			return reflect.DeepEqual(granted(authKey(t, key)), []string{"member:write", "role:write"})
		})
		eventually(t, "the token to carry the new permissions", func() bool {
			return reflect.DeepEqual(granted(authToken(t)), []string{"member:write", "role:write"})
		})
	})
	t.Run("an inactive role gives none, an active one again", func(t *testing.T) {
		authKey(t, key)
		exec(t, d, `UPDATE pandokos.tenant_role SET state = 'inactive' WHERE tenant_id = $1::uuid AND name = 'reader'`, tid.String())
		eventually(t, "the key to lose the permissions of the inactive role", func() bool { return len(authKey(t, key).Granted) == 0 })
		eventually(t, "the token to lose the permissions of the inactive role", func() bool { return len(authToken(t).Granted) == 0 })
		if p := authKey(t, key); strings.Join(p.Roles, ",") != "inference,reader" {
			t.Errorf("the roles of the principal (names) changed with the state of a role: %v", p.Roles)
		}
		exec(t, d, `UPDATE pandokos.tenant_role SET state = 'active' WHERE tenant_id = $1::uuid AND name = 'reader'`, tid.String())
		eventually(t, "the key to carry the permissions again", func() bool { return len(authKey(t, key).Granted) == 2 })
	})
	t.Run("a deleted role gives none", func(t *testing.T) {
		exec(t, d, `DELETE FROM pandokos.role_assignment WHERE role = 'reader' AND tenant_id = $1::uuid`, tid.String())
		exec(t, d, `DELETE FROM pandokos.tenant_role WHERE name = 'reader' AND tenant_id = $1::uuid`, tid.String())
		eventually(t, "the key to carry nothing", func() bool { return len(authKey(t, key).Granted) == 0 })
	})
}
