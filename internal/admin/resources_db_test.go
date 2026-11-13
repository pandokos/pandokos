package admin_test

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/testutil"
)

func me(t *testing.T, a *apitest.API, key string) *adminapi.Me {
	t.Helper()
	out, err := a.Client(key).GetMe(ctx(t), adminapi.GetMeInput{})
	if err != nil {
		t.Fatalf("getMe: %v", err)
	}
	return out
}

// TestTenants is T-236.
func TestTenants(t *testing.T) {
	api := apitest.New(t)
	plat := api.Client(api.PlatformKey())
	soon := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)

	t.Run("create returns an active tenant, its administrator and a key that works", func(t *testing.T) {
		out, err := plat.CreateTenant(ctx(t), adminapi.CreateTenantInput{Body: adminapi.TenantCreate{
			Name: "acme", DisplayName: "Acme Inc", AdminServiceAccountName: "root", AdminKeyExpiresAt: &soon}})
		if err != nil {
			t.Fatal(err)
		}
		if out.Tenant.State != adminapi.TenantStateActive || out.Tenant.Name != "acme" || out.Tenant.DisplayName != "Acme Inc" {
			t.Errorf("tenant: %+v", out.Tenant)
		}
		sa := out.AdminServiceAccount
		if sa.Name != "root" || sa.TenantID != out.Tenant.ID || len(sa.Roles) != 1 || sa.Roles[0] != "tenant-admin" || sa.State != adminapi.ServiceAccountStateActive {
			t.Errorf("service account: %+v", sa)
		}
		k := out.AdminKey
		if k.Key.ServiceAccountID != sa.ID || k.Key.TenantID != out.Tenant.ID || k.Key.State != adminapi.KeyStateActive || k.Key.ExpiresAt == nil || !k.Key.ExpiresAt.Equal(soon) || k.Key.Scopes != nil {
			t.Errorf("key: %+v", k.Key)
		}
		if !strings.HasPrefix(k.Secret, "pdk_t"+k.Key.ID[1:]+"_") || len(k.Secret) != 81 {
			t.Errorf("secret %q does not match the key ID %q", "…", k.Key.ID)
		}
		m := me(t, api, k.Secret)
		if m.PrincipalType != adminapi.PrincipalTypeServiceAccount || m.ID != sa.ID || m.Tenant == nil || m.Tenant.ID != out.Tenant.ID || m.Tenant.State != adminapi.TenantStateActive || m.Bootstrap {
			t.Errorf("me: %+v", m)
		}
		if strings.Join(m.Roles, ",") != "tenant-admin" || len(m.Permissions) != 7 {
			t.Errorf("roles %v, permissions %v", m.Roles, m.Permissions)
		}
		// The default name of the first service account is admin; no expiry means none.
		def, err := plat.CreateTenant(ctx(t), adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: "defaults"}})
		if err != nil || def.AdminServiceAccount.Name != "admin" || def.AdminKey.Key.ExpiresAt != nil {
			t.Errorf("defaults: %+v %v", def, err)
		}
		// An expiry that is not in the future is refused, and nothing is created.
		past := time.Now().Add(-time.Hour)
		_, err = plat.CreateTenant(ctx(t), adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: "never", AdminKeyExpiresAt: &past}})
		var pe *adminapi.ProblemError
		if !errors.As(err, &pe) || pe.Problem.Code != adminapi.ProblemCodeInvalidRequest {
			t.Errorf("past expiry: %v", err)
		}
		if l, err := plat.ListTenants(ctx(t), adminapi.ListTenantsInput{Name: "never"}); err != nil || len(l.Items) != 0 {
			t.Errorf("a refused creation left a tenant: %+v %v", l, err)
		}
	})

	t.Run("the name rule", func(t *testing.T) {
		for _, bad := range []string{"", "Acme", "-acme", "acme-", "a_b", "has space", strings.Repeat("a", 64), "ünï"} {
			r := api.Do(t, "POST", "/v1/tenants", api.PlatformKey(), map[string]any{"name": bad}, nil)
			expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		}
		for _, good := range []string{"a", "a1", "a-b", strings.Repeat("a", 63)} {
			if _, err := plat.CreateTenant(ctx(t), adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: good}}); err != nil {
				t.Errorf("%q: %v", good, err)
			}
		}
		// Unique among tenants that are not deleted.
		r := api.Do(t, "POST", "/v1/tenants", api.PlatformKey(), map[string]any{"name": "acme"}, nil)
		expect(t, r, 409, adminapi.ProblemCodeConflict)
		// Changed by update; the old name is free then.
		acme, err := plat.ListTenants(ctx(t), adminapi.ListTenantsInput{Name: "acme"})
		if err != nil || len(acme.Items) != 1 {
			t.Fatalf("%+v %v", acme, err)
		}
		id := acme.Items[0].ID
		upd, err := plat.UpdateTenant(ctx(t), adminapi.UpdateTenantInput{Tenant: id, Body: adminapi.TenantUpdate{Name: "acme-renamed"}})
		if err != nil || upd.Name != "acme-renamed" || upd.DisplayName != "Acme Inc" || !upd.UpdatedAt.After(acme.Items[0].UpdatedAt.Add(-time.Second)) {
			t.Errorf("rename: %+v %v", upd, err)
		}
		if _, err := plat.CreateTenant(ctx(t), adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: "acme"}}); err != nil {
			t.Errorf("a name that was changed away is free: %v", err)
		}
		upd, err = plat.UpdateTenant(ctx(t), adminapi.UpdateTenantInput{Tenant: id, Body: adminapi.TenantUpdate{DisplayName: "Renamed Inc"}})
		if err != nil || upd.Name != "acme-renamed" || upd.DisplayName != "Renamed Inc" {
			t.Errorf("display name: %+v %v", upd, err)
		}
		// Renaming to a name in use is a conflict; neither member is invalid_request.
		expect(t, api.Do(t, "PATCH", "/v1/tenants/"+id, api.PlatformKey(), map[string]any{"name": "defaults"}, nil), 409, adminapi.ProblemCodeConflict)
		expect(t, api.Do(t, "PATCH", "/v1/tenants/"+id, api.PlatformKey(), map[string]any{}, nil), 400, adminapi.ProblemCodeInvalidRequest)
		// Reuse after the tenant is deleted (the superuser plays the deletion job of a later sprint).
		super := api.DB.Super(t)
		for _, st := range []string{"deleting", "deleted"} {
			if _, err := super.Exec(ctx(t), "UPDATE pandokos.tenant SET state = $1 WHERE id = $2::uuid", st, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := plat.CreateTenant(ctx(t), adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: "acme-renamed"}}); err != nil {
			t.Errorf("a name is reusable once its tenant is deleted: %v", err)
		}
		// A deleted tenant cannot be updated.
		expect(t, api.Do(t, "PATCH", "/v1/tenants/"+id, api.PlatformKey(), map[string]any{"display_name": "x"}, nil), 409, adminapi.ProblemCodeInvalidState)
	})

	t.Run("suspend and reactivate", func(t *testing.T) {
		out, err := plat.CreateTenant(ctx(t), adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: "lifecycle"}})
		if err != nil {
			t.Fatal(err)
		}
		id := out.Tenant.ID
		for i := 0; i < 2; i++ { // the second call is idempotent
			s, err := plat.SuspendTenant(ctx(t), adminapi.SuspendTenantInput{Tenant: id, Body: adminapi.Reason{Reason: "unpaid"}})
			if err != nil || s.State != adminapi.TenantStateSuspended {
				t.Fatalf("suspend %d: %+v %v", i, s, err)
			}
		}
		g, err := plat.GetTenant(ctx(t), adminapi.GetTenantInput{Tenant: id})
		if err != nil || g.State != adminapi.TenantStateSuspended || !g.StateChangedAt.After(g.CreatedAt) {
			t.Errorf("get: %+v %v", g, err)
		}
		for i := 0; i < 2; i++ {
			s, err := plat.ReactivateTenant(ctx(t), adminapi.ReactivateTenantInput{Tenant: id})
			if err != nil || s.State != adminapi.TenantStateActive {
				t.Fatalf("reactivate %d: %+v %v", i, s, err)
			}
		}
		// Reactivating an active tenant, and suspending a suspended one, change nothing.
		// A tenant that is provisioning, deleting or deleted cannot be suspended or reactivated.
		super := api.DB.Super(t)
		var provisioning string
		if err := super.QueryRow(ctx(t), "INSERT INTO pandokos.tenant (name) VALUES ('stuck') RETURNING id::text").Scan(&provisioning); err != nil {
			t.Fatal(err)
		}
		expect(t, api.Do(t, "POST", "/v1/tenants/"+provisioning+"/suspend", api.PlatformKey(), nil, nil), 409, adminapi.ProblemCodeInvalidState)
		expect(t, api.Do(t, "POST", "/v1/tenants/"+provisioning+"/reactivate", api.PlatformKey(), nil, nil), 409, adminapi.ProblemCodeInvalidState)
		for _, st := range []string{"deleting", "deleted"} {
			if _, err := super.Exec(ctx(t), "UPDATE pandokos.tenant SET state = $1 WHERE id = $2::uuid", st, id); err != nil {
				t.Fatal(err)
			}
			expect(t, api.Do(t, "POST", "/v1/tenants/"+id+"/suspend", api.PlatformKey(), nil, nil), 409, adminapi.ProblemCodeInvalidState)
			expect(t, api.Do(t, "POST", "/v1/tenants/"+id+"/reactivate", api.PlatformKey(), nil, nil), 409, adminapi.ProblemCodeInvalidState)
		}
		expect(t, api.Do(t, "POST", "/v1/tenants/"+provisioning+"/suspend", api.PlatformKey(), map[string]any{"reason": strings.Repeat("r", 501)}, nil), 400, adminapi.ProblemCodeInvalidRequest)
	})

	t.Run("read and list", func(t *testing.T) {
		l, err := plat.ListTenants(ctx(t), adminapi.ListTenantsInput{})
		if err != nil || len(l.Items) < 5 {
			t.Fatalf("%+v %v", l, err)
		}
		for _, tn := range l.Items {
			g, err := plat.GetTenant(ctx(t), adminapi.GetTenantInput{Tenant: tn.ID})
			if err != nil || g.ID != tn.ID || g.Name != tn.Name {
				t.Errorf("get %s: %+v %v", tn.ID, g, err)
			}
		}
		expect(t, api.Do(t, "GET", "/v1/tenants/0192f0f0-0000-7000-8000-0000000000ff", api.PlatformKey(), nil, nil), 404, adminapi.ProblemCodeNotFound)
	})
}

// TestServiceAccounts is T-237.
func TestServiceAccounts(t *testing.T) {
	w := newWorld(t, "accounts")
	c := w.api.Client(w.adminKey)
	t0 := time.Now().UTC().Truncate(time.Second)
	from, until := t0.Add(time.Hour), t0.Add(48*time.Hour)

	sa, err := c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{
		Name: "robot", Description: "a robot", ValidFrom: &from, ValidUntil: &until, Roles: []string{"inference", "tenant-admin"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sa.Name != "robot" || sa.Description != "a robot" || sa.State != adminapi.ServiceAccountStateActive || sa.ValidFrom == nil || !sa.ValidFrom.Equal(from) ||
		sa.ValidUntil == nil || !sa.ValidUntil.Equal(until) || strings.Join(sa.Roles, ",") != "inference,tenant-admin" || sa.TenantID != w.tenant.ID {
		t.Errorf("created: %+v", sa)
	}
	// Roles and items are never null; a service account without roles has an empty array.
	r := w.api.Do(t, "POST", "/v1/tenants/"+w.tenant.ID+"/service-accounts", w.adminKey, map[string]any{"name": "noroles"}, nil)
	ok(t, r, 201, nil)
	if !strings.Contains(string(r.Body), `"roles":[]`) {
		t.Errorf("roles of an account without roles: %s", r.Body)
	}
	if strings.Contains(string(r.Body), "valid_from") || strings.Contains(string(r.Body), "null") {
		t.Errorf("an absent optional member is left out, not sent as null: %s", r.Body)
	}

	path := "/v1/tenants/" + w.tenant.ID + "/service-accounts"
	for name, body := range map[string]map[string]any{
		"unknown role":     {"name": "x1", "roles": []string{"owner"}},
		"a platform role":  {"name": "x2", "roles": []string{"platform-admin"}},
		"bad role format":  {"name": "x3", "roles": []string{"Not A Role"}},
		"duplicate roles":  {"name": "x4", "roles": []string{"inference", "inference"}},
		"validity order":   {"name": "x5", "valid_from": until.Format(time.RFC3339), "valid_until": from.Format(time.RFC3339)},
		"no name":          {"description": "x"},
		"bad name":         {"name": "Robot"},
		"tenant in a body": {"name": "x6", "tenant_id": w.tenant.ID},
	} {
		p := expect(t, w.api.Do(t, "POST", path, w.adminKey, body, nil), 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) == 0 {
			t.Errorf("%s: no member named", name)
		}
	}
	p := expect(t, w.api.Do(t, "POST", path, w.adminKey, map[string]any{"name": "x", "roles": []string{"owner"}}, nil), 400, adminapi.ProblemCodeInvalidRequest)
	if len(p.Errors) != 1 || p.Errors[0].Field != "roles" {
		t.Errorf("an unknown role names the member roles: %+v", p.Errors)
	}
	p = expect(t, w.api.Do(t, "POST", path, w.adminKey, map[string]any{"name": "x", "valid_from": until.Format(time.RFC3339), "valid_until": from.Format(time.RFC3339)}, nil), 400, adminapi.ProblemCodeInvalidRequest)
	if len(p.Errors) != 1 || p.Errors[0].Field != "valid_until" {
		t.Errorf("a validity that is out of order names valid_until: %+v", p.Errors)
	}
	expect(t, w.api.Do(t, "POST", path, w.adminKey, map[string]any{"name": "robot"}, nil), 409, adminapi.ProblemCodeConflict)

	// Each member of an update alone.
	upd := func(body map[string]any) *adminapi.ServiceAccount {
		t.Helper()
		var out adminapi.ServiceAccount
		ok(t, w.api.Do(t, "PATCH", path+"/"+sa.ID, w.adminKey, body, nil), 200, &out)
		return &out
	}
	if got := upd(map[string]any{"name": "robot2"}); got.Name != "robot2" || got.Description != "a robot" || strings.Join(got.Roles, ",") != "inference,tenant-admin" || got.ValidUntil == nil {
		t.Errorf("name only: %+v", got)
	}
	if got := upd(map[string]any{"description": "changed"}); got.Name != "robot2" || got.Description != "changed" {
		t.Errorf("description only: %+v", got)
	}
	later := until.Add(time.Hour)
	if got := upd(map[string]any{"valid_until": later.Format(time.RFC3339)}); got.ValidUntil == nil || !got.ValidUntil.Equal(later) || got.ValidFrom == nil {
		t.Errorf("valid_until only: %+v", got)
	}
	// null removes a bound; the other stays.
	got := upd(map[string]any{"valid_until": nil})
	if got.ValidUntil != nil || got.ValidFrom == nil {
		t.Errorf("valid_until null: %+v", got)
	}
	if got := upd(map[string]any{"valid_from": nil}); got.ValidFrom != nil || got.ValidUntil != nil {
		t.Errorf("valid_from null: %+v", got)
	}
	// roles replaces the set; an empty array removes all of them.
	if got := upd(map[string]any{"roles": []string{"inference"}}); strings.Join(got.Roles, ",") != "inference" {
		t.Errorf("roles: %+v", got.Roles)
	}
	if got := upd(map[string]any{"roles": []string{}}); got.Roles == nil || len(got.Roles) != 0 {
		t.Errorf("roles []: %+v", got.Roles)
	}
	// Validity order is checked against what is stored, too.
	expect(t, w.api.Do(t, "PATCH", path+"/"+sa.ID, w.adminKey, map[string]any{"valid_from": until.Format(time.RFC3339), "valid_until": from.Format(time.RFC3339)}, nil), 400, adminapi.ProblemCodeInvalidRequest)
	expect(t, w.api.Do(t, "PATCH", path+"/"+sa.ID, w.adminKey, map[string]any{}, nil), 400, adminapi.ProblemCodeInvalidRequest)
	expect(t, w.api.Do(t, "PATCH", path+"/"+sa.ID, w.adminKey, map[string]any{"roles": []string{"owner"}}, nil), 400, adminapi.ProblemCodeInvalidRequest)
	expect(t, w.api.Do(t, "PATCH", path+"/"+sa.ID, w.adminKey, map[string]any{"name": "worker"}, nil), 409, adminapi.ProblemCodeConflict)
	// A bound that is set to its stored value is not an error.
	upd(map[string]any{"valid_until": later.Format(time.RFC3339)})
	upd(map[string]any{"valid_from": from.Format(time.RFC3339)})

	// Disable and enable.
	d, err := c.DisableServiceAccount(ctx(t), adminapi.DisableServiceAccountInput{Tenant: w.tenant.ID, ServiceAccount: sa.ID, Body: adminapi.Reason{Reason: "left the team"}})
	if err != nil || d.State != adminapi.ServiceAccountStateDisabled {
		t.Errorf("disable: %+v %v", d, err)
	}
	e, err := c.EnableServiceAccount(ctx(t), adminapi.EnableServiceAccountInput{Tenant: w.tenant.ID, ServiceAccount: sa.ID})
	if err != nil || e.State != adminapi.ServiceAccountStateActive {
		t.Errorf("enable: %+v %v", e, err)
	}

	// Nothing deletes a service account: there is no route, and the database role cannot.
	for _, m := range []string{"DELETE", "PUT"} {
		expect(t, w.api.Do(t, m, path+"/"+sa.ID, w.adminKey, nil, nil), 404, adminapi.ProblemCodeNotFound)
	}
	conn := w.api.DB.Connect(t, "admin")
	if _, err := conn.Exec(ctx(t), "DELETE FROM pandokos.service_account"); !isPrivilegeError(err) {
		t.Errorf("the admin database role deleted service accounts: %v", err)
	}

	// Get and list.
	g, err := c.GetServiceAccount(ctx(t), adminapi.GetServiceAccountInput{Tenant: w.tenant.ID, ServiceAccount: sa.ID})
	if err != nil || g.ID != sa.ID {
		t.Errorf("get: %+v %v", g, err)
	}
	l, err := c.ListServiceAccounts(ctx(t), adminapi.ListServiceAccountsInput{Tenant: w.tenant.ID})
	if err != nil || len(l.Items) != 4 { // admin, worker, noroles, robot
		t.Errorf("list: %d %v", len(l.Items), err)
	}
}

func isPrivilegeError(err error) bool { return err != nil && store.SQLState(err) == "42501" }

// TestVirtualKeys is T-238.
func TestVirtualKeys(t *testing.T) {
	w := newWorld(t, "keys")
	c := w.api.Client(w.adminKey)

	// Three keys of one service account all work.
	keys := []adminapi.VirtualKeyCreated{w.key}
	for i := 0; i < 2; i++ {
		k, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: w.sa.ID, Description: "extra"}})
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, *k)
	}
	for i, k := range keys {
		if !authenticated(w.api, t, k.Secret) {
			t.Errorf("key %d does not authenticate", i)
		}
	}
	// Revoking one leaves the others.
	rev, err := c.RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: w.tenant.ID, Key: keys[1].Key.ID})
	if err != nil || rev.State != adminapi.KeyStateRevoked || rev.RevokedAt == nil {
		t.Fatalf("revoke: %+v %v", rev, err)
	}
	expect(t, w.api.Do(t, "GET", "/v1/me", keys[1].Secret, nil, nil), 401, adminapi.ProblemCodeUnauthenticated)
	for _, i := range []int{0, 2} {
		if !authenticated(w.api, t, keys[i].Secret) {
			t.Errorf("key %d after the revocation of another does not authenticate", i)
		}
	}
	// Revoking again succeeds and changes nothing.
	again, err := c.RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: w.tenant.ID, Key: keys[1].Key.ID})
	if err != nil || again.RevokedAt == nil || !again.RevokedAt.Equal(*rev.RevokedAt) {
		t.Errorf("a second revoke: %+v %v", again, err)
	}

	// The secret is in the create response and in no later response.
	for _, k := range keys {
		forms := testutil.KeyForms(k.Secret)
		for _, r := range []*apitest.Response{
			w.api.Do(t, "GET", "/v1/tenants/"+w.tenant.ID+"/virtual-keys/"+k.Key.ID, w.adminKey, nil, nil),
			w.api.Do(t, "GET", "/v1/tenants/"+w.tenant.ID+"/virtual-keys", w.adminKey, nil, nil),
			w.api.Do(t, "POST", "/v1/tenants/"+w.tenant.ID+"/virtual-keys/"+k.Key.ID+"/revoke", w.adminKey, nil, nil),
		} {
			testutil.AssertAbsent(t, "a response", string(r.Body), forms...)
			if strings.Contains(string(r.Body), `"secret"`) || strings.Contains(string(r.Body), "hash") {
				t.Errorf("a response carries a secret or a hash member: %s", r.Body)
			}
		}
	}
	// The database holds the SHA-256 of the secret and nothing that contains the secret.
	super := w.api.DB.Super(t)
	for _, k := range keys {
		forms := testutil.KeyForms(k.Secret)
		var stored []byte
		if err := super.QueryRow(ctx(t), "SELECT secret_hash FROM pandokos.virtual_key WHERE key_id = $1", k.Key.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		_, _, want, err := authn.Parse(k.Secret)
		if err != nil || string(stored) != string(want[:]) {
			t.Errorf("the stored hash is not the hash of the secret: %v", err)
		}
		sum := sha256.Sum256(decodeSecret(t, k.Secret))
		if string(stored) != string(sum[:]) {
			t.Error("the stored value is not the SHA-256 of the 32 secret bytes")
		}
		for _, table := range w.api.Tables(t) {
			var dump string
			if err := super.QueryRow(ctx(t), `SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '') FROM pandokos.`+quoteIdent(table)+` t`).Scan(&dump); err != nil {
				t.Fatal(err)
			}
			testutil.AssertAbsent(t, "table "+table, dump, forms[0], forms[1], forms[2], forms[4], forms[5])
		}
	}

	// Scopes.
	vpath := "/v1/tenants/" + w.tenant.ID + "/virtual-keys"
	for name, scopes := range map[string][]string{
		"unknown permission":      {"widget:frobnicate"},
		"platform permission":     {"tenant:read"},
		"platform key permission": {"platform_key:write"},
		"bad format":              {"ModelInvoke"},
		"empty list":              {},
		"repeated":                {"model:invoke", "model:invoke"},
	} {
		p := expect(t, w.api.Do(t, "POST", vpath, w.adminKey, map[string]any{"service_account_id": w.sa.ID, "scopes": scopes}, nil), 400, adminapi.ProblemCodeInvalidRequest)
		if name != "empty list" && name != "repeated" && name != "bad format" && (len(p.Errors) != 1 || p.Errors[0].Field != "scopes") {
			t.Errorf("%s: %+v", name, p.Errors)
		}
	}
	// A service account that is not there, or of another tenant, is the same error.
	other, _ := w.api.Tenant(t, "keys-other")
	var otherSAID string
	if err := super.QueryRow(ctx(t), "SELECT id::text FROM pandokos.service_account WHERE tenant_id = $1::uuid", other.ID).Scan(&otherSAID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{otherSAID, "0192f0f0-0000-7000-8000-0000000000ff"} {
		p := expect(t, w.api.Do(t, "POST", vpath, w.adminKey, map[string]any{"service_account_id": id}, nil), 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) != 1 || p.Errors[0].Field != "service_account_id" {
			t.Errorf("%s: %+v", id, p.Errors)
		}
	}
	// Expiry in the past is refused.
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	expect(t, w.api.Do(t, "POST", vpath, w.adminKey, map[string]any{"service_account_id": w.sa.ID, "expires_at": past}, nil), 400, adminapi.ProblemCodeInvalidRequest)

	// A key with expires_at stops at that time.
	soon := time.Now().Add(1500 * time.Millisecond)
	exp, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: w.sa.ID, ExpiresAt: &soon}})
	if err != nil {
		t.Fatal(err)
	}
	if !authenticated(w.api, t, exp.Secret) {
		t.Error("the key does not work before its expiry")
	}
	testutil.Eventually(t, 5*time.Second, "the key to expire", func() bool {
		return w.api.Do(t, "GET", "/v1/me", exp.Secret, nil, nil).Status == 401
	})

	// A scoped key is refused what its scope excludes.
	scoped, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: w.sa.ID, Scopes: []string{authz.VirtualKeyRead, authz.IdentityRead}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Key.Scopes) != 2 {
		t.Errorf("scopes: %v", scoped.Key.Scopes)
	}
	// The service account of this key holds inference only, so give the scoped key an
	// administrator account to be scoped down from.
	adminSA := w.api.Do(t, "GET", "/v1/tenants/"+w.tenant.ID+"/service-accounts?name=admin", w.adminKey, nil, nil)
	var list adminapi.ServiceAccountList
	ok(t, adminSA, 200, &list)
	scopedAdmin, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: list.Items[0].ID, Scopes: []string{authz.VirtualKeyRead, authz.IdentityRead}}})
	if err != nil {
		t.Fatal(err)
	}
	if m := me(t, w.api, scopedAdmin.Secret); strings.Join(m.Permissions, ",") != "identity:read,virtual_key:read" {
		t.Errorf("permissions of a scoped key: %v", m.Permissions)
	}
	if r := w.api.Do(t, "GET", vpath, scopedAdmin.Secret, nil, nil); r.Status != 200 {
		t.Errorf("list with a scope that allows it: %d", r.Status)
	}
	expect(t, w.api.Do(t, "POST", vpath, scopedAdmin.Secret, map[string]any{"service_account_id": w.sa.ID}, nil), 403, adminapi.ProblemCodeForbidden)
	expect(t, w.api.Do(t, "GET", "/v1/tenants/"+w.tenant.ID+"/service-accounts", scopedAdmin.Secret, nil, nil), 403, adminapi.ProblemCodeForbidden)

	// A key can be issued for a disabled service account; it works once the account is enabled.
	if _, err := c.DisableServiceAccount(ctx(t), adminapi.DisableServiceAccountInput{Tenant: w.tenant.ID, ServiceAccount: w.sa.ID}); err != nil {
		t.Fatal(err)
	}
	dis, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: w.sa.ID}})
	if err != nil {
		t.Fatalf("a key for a disabled service account: %v", err)
	}
	expect(t, w.api.Do(t, "GET", "/v1/me", dis.Secret, nil, nil), 401, adminapi.ProblemCodeUnauthenticated)
	if _, err := c.EnableServiceAccount(ctx(t), adminapi.EnableServiceAccountInput{Tenant: w.tenant.ID, ServiceAccount: w.sa.ID}); err != nil {
		t.Fatal(err)
	}
	if !authenticated(w.api, t, dis.Secret) {
		t.Error("the key does not authenticate after enabling")
	}
	// Reading a key that does not exist.
	expect(t, w.api.Do(t, "GET", vpath+"/tzzzzzzzzzzzzzzzz", w.adminKey, nil, nil), 404, adminapi.ProblemCodeNotFound)
	expect(t, w.api.Do(t, "POST", vpath+"/tzzzzzzzzzzzzzzzz/revoke", w.adminKey, nil, nil), 404, adminapi.ProblemCodeNotFound)
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// decodeSecret returns the 32 secret bytes of a key.
func decodeSecret(t *testing.T, key string) []byte {
	t.Helper()
	raw, err := base32Decode(key[22:74])
	if err != nil || len(raw) != 32 {
		t.Fatalf("cannot decode the secret: %v", err)
	}
	return raw
}

// TestPlatformKeys is T-239.
func TestPlatformKeys(t *testing.T) {
	api := apitest.New(t)
	boot := api.PlatformKey()
	c := api.Client(boot)

	m := me(t, api, boot)
	if m.PrincipalType != adminapi.PrincipalTypePlatform || !m.Bootstrap || m.Tenant != nil || strings.Join(m.Roles, ",") != "platform-admin" || len(m.Permissions) != 7 {
		t.Errorf("me: %+v", m)
	}
	r := api.Do(t, "GET", "/v1/me", boot, nil, nil)
	if r.Header.Get("X-Pandokos-Warning") != "bootstrap-key-active" {
		t.Errorf("a response to the bootstrap key lacks the warning header: %v", r.Header)
	}

	exp := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	created, err := c.CreatePlatformKey(ctx(t), adminapi.CreatePlatformKeyInput{Body: adminapi.PlatformKeyCreate{Description: "ops", ExpiresAt: &exp}})
	if err != nil {
		t.Fatal(err)
	}
	k := created.Key
	if !strings.HasPrefix(created.Secret, "pdk_p") || k.Role != "platform-admin" || k.Bootstrap || k.State != adminapi.KeyStateActive || k.Description != "ops" || k.ExpiresAt == nil || !k.ExpiresAt.Equal(exp) {
		t.Errorf("created: %+v", k)
	}
	if r := api.Do(t, "GET", "/v1/me", created.Secret, nil, nil); r.Header.Get("X-Pandokos-Warning") != "" {
		t.Errorf("a platform key that is not the bootstrap key carries the warning: %v", r.Header)
	}
	list, err := c.ListPlatformKeys(ctx(t), adminapi.ListPlatformKeysInput{})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	got, err := c.GetPlatformKey(ctx(t), adminapi.GetPlatformKeyInput{Key: k.ID})
	if err != nil || got.ID != k.ID {
		t.Errorf("get: %+v %v", got, err)
	}
	testutil.AssertAbsent(t, "the list", mustJSON(t, list), testutil.KeyForms(created.Secret)...)

	// A second platform key works after the first is revoked.
	second := api.Client(created.Secret)
	if _, err := second.RevokePlatformKey(ctx(t), adminapi.RevokePlatformKeyInput{Key: findBootstrap(t, list)}); err != nil {
		t.Fatal(err)
	}
	expect(t, api.Do(t, "GET", "/v1/me", boot, nil, nil), 401, adminapi.ProblemCodeUnauthenticated)
	if m := me(t, api, created.Secret); m.KeyID != k.ID {
		t.Errorf("the second key after the first is revoked: %+v", m)
	}
	rv, err := second.GetPlatformKey(ctx(t), adminapi.GetPlatformKeyInput{Key: findBootstrap(t, list)})
	if err != nil || rv.State != adminapi.KeyStateRevoked || rv.RevokedAt == nil {
		t.Errorf("revoked: %+v %v", rv, err)
	}
	expect(t, api.Do(t, "GET", "/v1/platform-keys/pzzzzzzzzzzzzzzzz", created.Secret, nil, nil), 404, adminapi.ProblemCodeNotFound)
	expect(t, api.Do(t, "POST", "/v1/platform-keys", created.Secret, map[string]any{"role": "platform-admin"}, nil), 400, adminapi.ProblemCodeInvalidRequest)
	expect(t, api.Do(t, "POST", "/v1/platform-keys", created.Secret, map[string]any{"bootstrap": true}, nil), 400, adminapi.ProblemCodeInvalidRequest)

	// A platform key is refused on the inference side without a database lookup: the gateway
	// authenticator knows the kind from the first characters and stops there, so it answers
	// the same with the database down.
	gw := api.DB.Open(t, store.RoleGateway)
	auth, err := authn.New(authn.Options{DB: gw, Logger: testLogger(), Kinds: []authn.Kind{authn.KindTenant}})
	if err != nil {
		t.Fatal(err)
	}
	api.DB.SetAcceptingConnections(t, false)
	defer api.DB.SetAcceptingConnections(t, true)
	_, err = auth.Authenticate(ctx(t), created.Secret)
	var f *authn.Failure
	if !errors.As(err, &f) || f.Reason != "kind_not_accepted" {
		t.Errorf("a platform key on the data plane: %v", err)
	}
}

func findBootstrap(t *testing.T, l *adminapi.PlatformKeyList) string {
	t.Helper()
	for _, k := range l.Items {
		if k.Bootstrap {
			return k.ID
		}
	}
	t.Fatal("no bootstrap key in the list")
	return ""
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSuspensionBlocksAdministration is T-264.
func TestSuspensionBlocksAdministration(t *testing.T) {
	w := newWorld(t, "suspended-admin")
	plat := w.api.Client(w.platform)
	if _, err := plat.SuspendTenant(ctx(t), adminapi.SuspendTenantInput{Tenant: w.tenant.ID}); err != nil {
		t.Fatal(err)
	}
	before := w.api.SnapshotTenant(t, w.tenant.ID)
	n := 0
	for _, op := range adminapi.Operations {
		if perm, _ := authz.Lookup(op.Permission); perm.Scope == authz.ScopePlatform {
			continue
		}
		n++
		for _, key := range []string{w.adminKey, w.key.Secret} {
			p := expect(t, w.call(t, op, key), 403, adminapi.ProblemCodeTenantSuspended)
			if p.Title != "Tenant not active" {
				t.Errorf("%s: %+v", op.ID, p)
			}
		}
	}
	if n != 11 {
		t.Errorf("only %d operations were exercised", n)
	}
	if after := w.api.SnapshotTenant(t, w.tenant.ID); after != before {
		t.Error("a row of the suspended tenant changed")
	}
	if _, err := plat.ReactivateTenant(ctx(t), adminapi.ReactivateTenantInput{Tenant: w.tenant.ID}); err != nil {
		t.Fatal(err)
	}
	if m := me(t, w.api, w.adminKey); m.Tenant == nil || m.Tenant.State != adminapi.TenantStateActive {
		t.Errorf("after reactivation: %+v", m)
	}
	if _, err := w.api.Client(w.adminKey).ListServiceAccounts(ctx(t), adminapi.ListServiceAccountsInput{Tenant: w.tenant.ID}); err != nil {
		t.Errorf("after reactivation: %v", err)
	}
}

var b32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func base32Decode(s string) ([]byte, error) { return b32.DecodeString(s) }

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// authenticated reports whether the API accepts the key as a credential: it is not 401. A key
// of the role inference is authenticated and still has no permission on getMe (403).
func authenticated(api *apitest.API, t *testing.T, key string) bool {
	t.Helper()
	return api.Do(t, "GET", "/v1/me", key, nil, nil).Status != 401
}
