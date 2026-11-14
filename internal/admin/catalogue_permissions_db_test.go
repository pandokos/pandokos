package admin_test

import (
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestCatalogPermissions is T-406: the five permissions of section 5.9, each tried with a key that
// holds it and keys that hold the others.
func TestCatalogPermissions(t *testing.T) {
	w := newWorld(t, "catperm")
	api := w.api

	// A platform key with a role that grants nothing (the database allows any role name; the
	// product knows one): it holds no catalogue permission.
	key, keyID, hash, err := authn.Generate(authn.KindPlatform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.platform_key (key_id, description, role, secret_hash) VALUES ($1, 'no permission', 'nobody', $2)`, keyID, hash[:]); err != nil {
		t.Fatal(err)
	}
	nobody := key.Reveal()
	if r := api.Do(t, "GET", "/v1/me", nobody, nil, nil); r.Status != 403 {
		t.Fatalf("a platform key with a role that grants nothing: getMe gives %d, want 403 (it holds no permission)", r.Status)
	}

	// Service accounts: a tenant administrator, scoped keys, and an inference account.
	c := api.Client(w.adminKey)
	sa, err := c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "scoped-admin", Roles: []string{"tenant-admin"}}})
	if err != nil {
		t.Fatal(err)
	}
	scoped := func(scopes ...string) string {
		t.Helper()
		k, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, Scopes: scopes}})
		if err != nil {
			t.Fatal(err)
		}
		return k.Secret
	}
	inference := w.key.Secret // the key of the inference account of the world

	platformOps, tenantOps := 0, 0
	for _, op := range adminapi.Operations {
		perm, ok := authz.Lookup(op.Permission)
		catalogue := strings.HasPrefix(op.Permission, "catalog:") || (strings.HasPrefix(op.Permission, "model:") && op.Permission != "model:invoke")
		if !ok || !catalogue {
			continue
		}
		call := apitest.CallFor(t, op, w.params())
		do := func(key string) *apitest.Response { return api.Do(t, call.Method, call.Path, key, call.Body, nil) }
		switch perm.Scope {
		case authz.ScopePlatform:
			platformOps++
			// A platform key without the permission, a tenant administrator and an inference key: refused, the same way.
			for name, k := range map[string]string{"a platform key that holds nothing": nobody, "the tenant administrator": w.adminKey, "an inference key": inference} {
				if r := do(k); r.Status != 403 {
					t.Errorf("%s with %s: %d\n%s", op.ID, name, r.Status, r.Body)
				}
			}
		case authz.ScopeTenant:
			tenantOps++
			// A platform key is refused tenant permissions, with a nobody key too.
			for name, k := range map[string]string{"the platform key": w.platform, "a platform key that holds nothing": nobody, "an inference key": inference} {
				if r := do(k); r.Status != 403 {
					t.Errorf("%s with %s: %d\n%s", op.ID, name, r.Status, r.Body)
				}
			}
		}
	}
	if platformOps != 25 || tenantOps != 4 {
		t.Errorf("%d platform and %d tenant operations were exercised, want 25 and 4", platformOps, tenantOps)
	}

	t.Run("model:read reads and model:restrict writes, and neither implies the other", func(t *testing.T) {
		readOnly, restrictOnly := scoped(authz.ModelRead, authz.IdentityRead), scoped(authz.ModelRestrict, authz.IdentityRead)
		base := "/v1/tenants/" + w.tenant.ID
		models := []string{"openai/gpt-4o"}
		for _, c := range []struct {
			name, method, path string
			body               any
			read, restrict     int
		}{
			{"listTenantModels", "GET", base + "/models", nil, 200, 403},
			{"getModelRestriction", "GET", base + "/model-restriction", nil, 200, 403},
			{"setModelRestriction", "PUT", base + "/model-restriction", map[string]any{"models": models}, 403, 200},
			{"clearModelRestriction", "DELETE", base + "/model-restriction", nil, 403, 200},
		} {
			if r := api.Do(t, c.method, c.path, readOnly, c.body, nil); r.Status != c.read {
				t.Errorf("%s with model:read: %d, want %d\n%s", c.name, r.Status, c.read, r.Body)
			}
			if r := api.Do(t, c.method, c.path, restrictOnly, c.body, nil); r.Status != c.restrict {
				t.Errorf("%s with model:restrict: %d, want %d\n%s", c.name, r.Status, c.restrict, r.Body)
			}
		}
		// The tenant administrator, with no scope, may do all four.
		for _, c := range []struct {
			method, path string
			body         any
		}{{"GET", base + "/models", nil}, {"GET", base + "/model-restriction", nil}, {"PUT", base + "/model-restriction", map[string]any{"models": models}}, {"DELETE", base + "/model-restriction", nil}} {
			if r := api.Do(t, c.method, c.path, w.adminKey, c.body, nil); r.Status != 200 {
				t.Errorf("the tenant administrator: %s %s: %d\n%s", c.method, c.path, r.Status, r.Body)
			}
		}
		// A tenant key reaches only its own tenant's models.
		other, _ := api.Tenant(t, "catperm-other")
		for _, p := range []string{"/models", "/model-restriction"} {
			if r := api.Do(t, "GET", "/v1/tenants/"+other.ID+p, w.adminKey, nil, nil); r.Status != 404 {
				t.Errorf("another tenant's %s: %d", p, r.Status)
			}
		}
	})

	t.Run("the permissions are in the catalogue, so a key can be scoped to them, and to nothing else", func(t *testing.T) {
		for _, scope := range []string{authz.ModelRead, authz.ModelRestrict} {
			r := api.Do(t, "POST", "/v1/tenants/"+w.tenant.ID+"/virtual-keys", w.adminKey, map[string]any{"service_account_id": sa.ID, "scopes": []string{scope}}, nil)
			if r.Status != 201 {
				t.Errorf("a key scoped to %s: %d %s", scope, r.Status, r.Body)
			}
		}
		// Platform permissions are not scopes of a tenant key.
		r := api.Do(t, "POST", "/v1/tenants/"+w.tenant.ID+"/virtual-keys", w.adminKey, map[string]any{"service_account_id": sa.ID, "scopes": []string{authz.CatalogWrite}}, nil)
		if r.Status != 201 {
			// A scope names a permission of the catalogue; whether a platform one is accepted is the sprint-1.3 rule.
			t.Logf("a key scoped to a platform permission: %d", r.Status)
		} else if got := me(t, api, decodeSecretOf(t, r)); len(got.Permissions) != 0 {
			t.Errorf("a tenant key scoped to catalog:write holds %v", got.Permissions)
		}
		// Issuing a key with models needs only virtual_key:write.
		onlyKeys := scoped(authz.VirtualKeyWrite, authz.IdentityRead)
		r = api.Do(t, "POST", "/v1/tenants/"+w.tenant.ID+"/virtual-keys", onlyKeys, map[string]any{"service_account_id": sa.ID, "models": []string{"openai/gpt-4o"}}, nil)
		if r.Status != 201 {
			t.Errorf("a key with models, issued with virtual_key:write alone: %d\n%s", r.Status, r.Body)
		}
		// The platform administrator holds the three catalogue permissions and no tenant one.
		got := me(t, api, w.platform)
		held := map[string]bool{}
		for _, p := range got.Permissions {
			held[p] = true
		}
		for _, p := range []string{authz.CatalogRead, authz.CatalogWrite, authz.CatalogPrice} {
			if !held[p] {
				t.Errorf("the platform administrator does not hold %s", p)
			}
		}
		if held[authz.ModelRead] || held[authz.ModelRestrict] {
			t.Errorf("the platform administrator holds a tenant permission: %v", got.Permissions)
		}
		// The inference role holds none of the five.
		role, _ := authz.RoleByName(authz.RoleInference)
		for _, p := range role.Permissions {
			if strings.HasPrefix(p, "catalog:") || p == authz.ModelRead || p == authz.ModelRestrict {
				t.Errorf("the role inference holds %s", p)
			}
		}
	})
}

// decodeSecretOf returns the secret of a created key response.
func decodeSecretOf(t *testing.T, r *apitest.Response) string {
	t.Helper()
	return decode[adminapi.VirtualKeyCreated](t, r, 201).Secret
}
