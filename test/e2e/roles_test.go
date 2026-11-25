package e2e

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
)

// withKey returns a function that calls the administration listener of the process with the key.
func (p *proc) withKey(t *testing.T, key string) func(method, path string, body any) *apitest.Response {
	t.Helper()
	b := p.browser()
	hdr := http.Header{"Authorization": {"Bearer " + key}}
	return func(method, path string, body any) *apitest.Response { return b.Do(t, method, path, body, hdr) }
}

// signedIn returns a browser of the process with a session that has changed its generated password.
func (p *proc) signedIn(t *testing.T, username, password string) *apitest.Browser {
	t.Helper()
	b := p.browser()
	get[adminapi.Session](t, b.SignIn(t, username, password), 200)
	get[adminapi.Session](t, b.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": password, "new_password": dbtest.NewPassword(t)}, nil), 200)
	return b
}

// TestRolesExit is T-890, the exit of the sprint for roles: the binary and PostgreSQL only.
func TestRolesExit(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	env := baseEnv(t, d.Env(t))
	password := initAdmin(t, env, "roles.admin")
	p := startBinary(t, opts{level: "info", db: d, noMigrate: true})
	root := p.signedIn(t, "roles.admin", password)
	created := get[adminapi.TenantCreated](t, root.Do(t, "POST", "/v1/tenants", map[string]any{"name": "acme"}, nil), 201)
	if created.AdminKey == nil || !reflect.DeepEqual(created.AdminServiceAccount.Roles, []string{"owner"}) {
		t.Fatalf("the first service account of the tenant: %+v", created.AdminServiceAccount)
	}
	owner := p.withKey(t, created.AdminKey.Secret)
	tid := created.Tenant.ID
	base := "/v1/tenants/" + tid

	// The owner defines a role and makes three members.
	custom := get[adminapi.Role](t, (owner("POST", base+"/roles", map[string]any{"name": "ops-reader", "permissions": []string{"usage:read", "member:read"}})), 201)
	if custom.State != adminapi.RoleStateActive || !reflect.DeepEqual(custom.Permissions, []string{"member:read", "usage:read"}) {
		t.Fatalf("the custom role: %+v", custom)
	}
	member := func(role string) *apitest.Browser {
		t.Helper()
		name := "member." + strings.ReplaceAll(role, "-", ".")
		m := get[adminapi.MemberCreated](t, (owner("POST", base+"/members", map[string]any{"account": "new", "username": name, "roles": []string{role}})), 201)
		return p.signedIn(t, name, m.InitialPassword)
	}
	viewer, ops, admin := member("usage-viewer"), member("ops-reader"), member("tenant-admin")

	// getMe shows exactly the permissions of each.
	golden := apitest.Golden(t)
	withSession := func(perms []string) []string {
		out := append([]string(nil), perms...)
		out = append(out, "session:self")
		return sortedStrings(out)
	}
	for name, c := range map[string]struct {
		b    *apitest.Browser
		want []string
	}{
		"usage-viewer": {viewer, withSession(golden["usage-viewer"])},
		"ops-reader":   {ops, withSession([]string{"identity:read", "member:read", "usage:read"})},
		"tenant-admin": {admin, withSession(golden["tenant-admin"])},
	} {
		me := get[adminapi.Me](t, c.b.Do(t, "GET", "/v1/me", nil, nil), 200)
		if len(me.Memberships) != 1 || me.Memberships[0].Tenant.ID != tid || !reflect.DeepEqual(me.Memberships[0].Permissions, c.want) {
			t.Errorf("%s: getMe reports %+v, want the permissions %v", name, me.Memberships, c.want)
		}
	}
	// The usage viewer lists usage and is refused a credential and a member operation (UC-16).
	if r := viewer.Do(t, "GET", base+"/usage-records", nil, nil); r.Status != 200 {
		t.Errorf("the usage viewer lists usage: %d %s", r.Status, r.Body)
	}
	if r := viewer.Do(t, "POST", base+"/credentials", map[string]any{"provider": "openai", "label": "nope", "secret": "sk-not-a-real-secret"}, nil); r.Status != 403 || r.Problem() != "forbidden" {
		t.Errorf("the usage viewer registers a credential: %d %s", r.Status, r.Body)
	}
	if r := viewer.Do(t, "GET", base+"/members", nil, nil); r.Status != 403 || r.Problem() != "forbidden" {
		t.Errorf("the usage viewer lists members: %d %s", r.Status, r.Body)
	}
	if r := ops.Do(t, "GET", base+"/members", nil, nil); r.Status != 200 {
		t.Errorf("the custom role lists members: %d %s", r.Status, r.Body)
	}
	if r := ops.Do(t, "GET", base+"/credentials", nil, nil); r.Status != 403 {
		t.Errorf("the custom role lists credentials: %d", r.Status)
	}
	// A tenant administrator is refused when it assigns owner.
	if r := admin.Do(t, "POST", base+"/members", map[string]any{"account": "new", "username": "grab.owner", "roles": []string{"owner"}}, nil); r.Status != 403 || r.Problem() != "escalation_refused" {
		t.Errorf("a tenant administrator assigns owner: %d %s, want 403 escalation_refused", r.Status, r.Body)
	}
	if r := admin.Do(t, "POST", base+"/service-accounts", map[string]any{"name": "grab", "roles": []string{"owner"}}, nil); r.Status != 403 || r.Problem() != "escalation_refused" {
		t.Errorf("a tenant administrator makes an owner's account: %d %s", r.Status, r.Body)
	}
	if r := admin.Do(t, "POST", base+"/roles", map[string]any{"name": "ender", "permissions": []string{"support_action:end"}}, nil); r.Status != 403 || r.Problem() != "escalation_refused" {
		t.Errorf("a tenant administrator defines a role with support_action:end: %d %s", r.Status, r.Body)
	}
	// The built-in roles and the catalogue are public to a caller of the tenant.
	if roles := get[adminapi.BuiltinRoleList](t, viewer.Do(t, "GET", "/v1/roles", nil, nil), 200); len(roles.Items) != 10 {
		t.Errorf("%d built-in roles", len(roles.Items))
	}
	if code := p.stop(t); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestSupportExit is T-891, the exit of the sprint for support actions: a platform administrator, by
// session and by key, is refused the tenant's usage; opens an action and is still refused it; issues
// a key for the tenant under the action and closes; the owner sees the action; with support.enabled
// false the opening is refused and an open action stops working; all of it also through PgBouncer.
func TestSupportExit(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		env := baseEnv(t, d.Env(t))
		password := initAdmin(t, env, "support.admin")
		p := startBinary(t, opts{level: "info", db: d, noMigrate: true})
		admin := p.signedIn(t, "support.admin", password)
		created := get[adminapi.TenantCreated](t, admin.Do(t, "POST", "/v1/tenants", map[string]any{"name": "recover-me"}, nil), 201)
		tid := created.Tenant.ID
		base := "/v1/tenants/" + tid
		keyed := get[adminapi.PlatformKeyCreated](t, admin.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "ops"}, nil), 201)
		platform := p.withKey(t, keyed.Secret)

		// Refused the tenant's usage, by key and by session.
		if r := platform("GET", base+"/usage-records", nil); r.Status != 403 || r.Problem() != "forbidden" {
			t.Errorf("the platform key reads the tenant's usage: %d %s", r.Status, r.Body)
		}
		if r := admin.Do(t, "GET", base+"/usage-records", nil, nil); r.Status != 404 {
			t.Errorf("the platform user reads the tenant's usage: %d %s, want the 404 of 4.2", r.Status, r.Body)
		}
		// Opens an action: still refused it.
		reason := apitest.Reason("recover")
		action := get[adminapi.SupportAction](t, platform("POST", "/v1/tenant-support/"+tid+"/actions", map[string]any{"reason": reason, "permissions": []string{"service_account:read", "virtual_key:write"}}), 201)
		if action.State != "open" || action.Reason != reason {
			t.Fatalf("the action: %+v", action)
		}
		if r := platform("GET", base+"/usage-records", nil); r.Status != 403 {
			t.Errorf("the platform key reads the tenant's usage under an action: %d %s", r.Status, r.Body)
		}
		if r := platform("GET", base+"/credentials", nil); r.Status != 403 {
			t.Errorf("the platform key lists the tenant's credentials under an action: %d", r.Status)
		}
		// Issues a key for the tenant under the action.
		list := get[adminapi.ServiceAccountList](t, platform("GET", base+"/service-accounts", nil), 200)
		if len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0].Roles, []string{"owner"}) {
			t.Fatalf("the service accounts under the action: %+v", list.Items)
		}
		issued := get[adminapi.VirtualKeyCreated](t, platform("POST", base+"/virtual-keys", map[string]any{"service_account_id": list.Items[0].ID, "description": "issued by support"}), 201)
		// And closes.
		closed := get[adminapi.SupportAction](t, platform("POST", "/v1/tenant-support/"+tid+"/actions/"+action.ID+"/close", map[string]any{"reason": "keys handed over"}), 200)
		if closed.State != "closed" {
			t.Errorf("the closed action: %+v", closed)
		}
		if r := platform("GET", base+"/service-accounts", nil); r.Status != 403 {
			t.Errorf("the platform key after the close: %d", r.Status)
		}
		// The owner sees the action, with the platform as its actor, and its key works.
		owner := p.withKey(t, issued.Secret)
		seen := get[adminapi.SupportActionList](t, owner("GET", base+"/support-actions", nil), 200)
		if len(seen.Items) != 1 || seen.Items[0].ID != action.ID || seen.Items[0].Reason != reason || seen.Items[0].Actor.Type != adminapi.SupportActionActorTypePlatform || seen.Items[0].State != "closed" {
			t.Errorf("the owner's view of the action: %+v", seen.Items)
		}

		// support.enabled false: an action that is open stops working, and none can be opened.
		second := get[adminapi.SupportAction](t, platform("POST", "/v1/tenant-support/"+tid+"/actions", map[string]any{"reason": apitest.Reason("again"), "permissions": []string{"service_account:read"}}), 201)
		if r := platform("GET", base+"/service-accounts", nil); r.Status != 200 {
			t.Fatalf("the second action is not honoured: %d", r.Status)
		}
		if code := p.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
		off := startBinary(t, opts{level: "info", db: d, noMigrate: true, env: []string{"PANDOKOS_SUPPORT_ENABLED=false"}})
		if !strings.Contains(off.stdout.String()+off.stderr.String(), "support actions are off") {
			t.Errorf("no warning at start with support.enabled false:\n%s%s", off.stdout.String(), off.stderr.String())
		}
		offPlatform := off.withKey(t, keyed.Secret)
		if r := offPlatform("GET", base+"/service-accounts", nil); r.Status != 403 {
			t.Errorf("an open action with the switch off: %d %s, want 403", r.Status, r.Body)
		}
		if r := offPlatform("POST", "/v1/tenant-support/"+tid+"/actions", map[string]any{"reason": apitest.Reason("refused"), "permissions": []string{"service_account:read"}}); r.Status != 403 || r.Problem() != "support_disabled" {
			t.Errorf("an opening with the switch off: %d %s, want 403 support_disabled", r.Status, r.Body)
		}
		// What was done stays visible and can be tidied.
		if r := offPlatform("GET", "/v1/tenant-support/"+tid+"/actions/"+second.ID, nil); r.Status != 200 {
			t.Errorf("reading with the switch off: %d", r.Status)
		}
		offSession := off.browser()
		offSession.SetCookie(session.CookieName, admin.Token())
		if r := offSession.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
			t.Errorf("the session with the switch off: %d", r.Status)
		}
		if code := off.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
	})
}

// TestUpgradeWithRoles is T-892: a database of the previous release, migrated: every key works as
// before, the first service account is an owner, and a key scoped to limit:write no longer sets the
// time zone.
func TestUpgradeWithRoles(t *testing.T) {
	t.Parallel()
	d := dbtest.New(t)
	d.MigrateTo(t, 13)
	tn := d.SeedTenant(t, "upgrade-roles")
	first := d.SeedServiceAccount(t, tn, "first-admin")
	worker := d.SeedServiceAccount(t, tn, "workload")
	scopedSA := d.SeedServiceAccount(t, tn, "limits-only")
	super := d.Super(t)
	ctx := context.Background()
	key := func(sa string, scopes []string) (secret string) {
		k, keyID, hash, err := authn.Generate(authn.KindTenant)
		if err != nil {
			t.Fatal(err)
		}
		var sc any
		if scopes != nil {
			sc = scopes
		}
		if _, err := super.Exec(ctx, `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash, scopes) VALUES ($1::uuid, $2::uuid, $3, $4, $5::text[])`,
			tn.String(), sa, keyID, hash[:], sc); err != nil {
			t.Fatal(err)
		}
		return k.Reveal()
	}
	for _, r := range []struct{ sa, role string }{{first, "tenant-admin"}, {worker, "inference"}, {scopedSA, "tenant-admin"}} {
		if _, err := super.Exec(ctx, `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, $3)`, tn.String(), r.sa, r.role); err != nil {
			t.Fatal(err)
		}
	}
	adminKey, workerKey, scopedKey := key(first, nil), key(worker, nil), key(scopedSA, []string{"identity:read", "limit:write", "limit:read"})
	pk, pkeyID, phash, _ := authn.Generate(authn.KindPlatform)
	if _, err := super.Exec(ctx, `INSERT INTO pandokos.platform_key (key_id, secret_hash, description) VALUES ($1, $2, 'ops')`, pkeyID, phash[:]); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := runBinary(t, d.Env(t), "migrate"); code != 0 || !strings.Contains(out, "applied 0014_roles_support") {
		t.Fatalf("migrate: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	p := startBinary(t, opts{level: "info", db: d, noMigrate: true, up: fakeprovider.New(t)})

	// Every key works as before.
	for name, c := range map[string]struct {
		key  string
		want int
	}{"a platform key": {pk.Reveal(), 200}, "the first administrator's key": {adminKey, 200}, "a key scoped to limit:write": {scopedKey, 200}, "a wrong key": {"pdk_not_a_key", 401}} {
		if status, text := exchange(t, "GET", p.adminURL("/v1/me"), authHeader(c.key), nil); status != c.want {
			t.Errorf("%s: %d, want %d\n%s", name, status, c.want, text)
		}
	}
	if status, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(workerKey), chatBody("fake-basic", false, "x")); status == 401 {
		t.Errorf("an inference key on the inference listener: %d\n%s", status, text)
	}
	// The first service account is an owner, and it still holds tenant-admin: nobody holds less than before.
	me := get[adminapi.Me](t, p.withKey(t, adminKey)("GET", "/v1/me", nil), 200)
	if !reflect.DeepEqual(me.Roles, []string{"owner", "tenant-admin"}) {
		t.Errorf("the roles of the first administrator after the upgrade: %v, want [owner tenant-admin]", me.Roles)
	}
	if !reflect.DeepEqual(me.Permissions, apitest.Golden(t)["owner"]) {
		t.Errorf("the permissions of the first administrator: %v, want those of owner", me.Permissions)
	}
	// A key scoped to limit:write no longer sets the time zone; the unscoped key does.
	path := "/v1/tenants/" + tn.String() + "/time-zone"
	zone := map[string]any{"time_zone": ""}
	if r := p.withKey(t, scopedKey)("PUT", path, zone); r.Status != 403 || r.Problem() != "forbidden" {
		t.Errorf("a key scoped to limit:write sets the time zone: %d %s, want 403 forbidden", r.Status, r.Body)
	}
	if r := p.withKey(t, adminKey)("PUT", path, zone); r.Status != 200 {
		t.Errorf("the first administrator sets the time zone: %d %s", r.Status, r.Body)
	}
	if r := p.withKey(t, scopedKey)("GET", path, nil); r.Status != 200 {
		t.Errorf("a key scoped to limit:read reads the time zone: %d", r.Status)
	}
}
