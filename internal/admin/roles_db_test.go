package admin_test

// The tests of sprint 4.3 for custom roles: the operations, deactivation, deletion, what can be
// assigned, the first owner, replays and the bound of a change of role (T-859 to T-861, T-866, T-881,
// T-894 and T-896).

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// rw is a tenant of an API with sign-in, its owner (the key of the first service account), and the
// helpers of the tests of roles.
type rw struct {
	*people
	owner *apitest.Caller
}

func newRW(t *testing.T) *rw {
	t.Helper()
	p := newPeople(t, apitest.PeopleConfig{})
	return &rw{people: p, owner: p.api.Owner(t, p.tenant.ID)}
}

// tp is a path under the tenant.
func (w *rw) tp(rest string) string { return "/v1/tenants/" + w.tenant.ID + rest }

// role makes a custom role through the API as the owner.
func (w *rw) role(t *testing.T, name string, perms ...string) adminapi.Role {
	t.Helper()
	return decode[adminapi.Role](t, w.owner.Do(t, http.MethodPost, w.tp("/roles"), map[string]any{"name": name, "permissions": perms}, nil), 201)
}

// sa makes a service account with the roles, as the owner, and returns its ID and the secret of a key.
func (w *rw) sa(t *testing.T, roles ...string) (string, string) {
	t.Helper()
	sa := decode[adminapi.ServiceAccount](t, w.owner.Do(t, http.MethodPost, w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": roles}, nil), 201)
	key := decode[adminapi.VirtualKeyCreated](t, w.owner.Do(t, http.MethodPost, w.tp("/virtual-keys"), map[string]any{"service_account_id": sa.ID}, nil), 201)
	return sa.ID, key.Secret
}

func (w *rw) saRoles(t *testing.T, id string) []string {
	t.Helper()
	return decode[adminapi.ServiceAccount](t, w.owner.Do(t, http.MethodGet, w.tp("/service-accounts/"+id), nil, nil), 200).Roles
}

func (w *rw) memberRoles(t *testing.T, userID string) []string {
	t.Helper()
	return decode[adminapi.Member](t, w.owner.Do(t, http.MethodGet, w.tp("/members/"+userID), nil, nil), 200).Roles
}

func (w *rw) audit(t *testing.T, action string) []dbtest.AuditRow {
	t.Helper()
	var out []dbtest.AuditRow
	for _, r := range w.d.AuditRows(t, w.tid) {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// member makes a member with the roles (a new local account, signed in) and returns its user ID and its session.
func (w *rw) member(t *testing.T, roles ...string) (string, *apitest.Browser) {
	t.Helper()
	b := w.api.MemberWith(t, w.tenant.ID, roles...)
	return w.api.MemberID(t, b), b
}

func problemOf(t *testing.T, r *apitest.Response) adminapi.Problem {
	t.Helper()
	return problem(t, r)
}

func hasField(p adminapi.Problem, field string) bool {
	for _, f := range fieldsOf(p) {
		if f == field {
			return true
		}
	}
	return false
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestRoles is T-859.
func TestRoles(t *testing.T) {
	t.Parallel()
	w := newRW(t)

	t.Run("the two catalogue operations", func(t *testing.T) {
		viewer := w.api.AsRole(t, w.tenant.ID, authz.RoleUsageViewer)
		for name, who := range map[string]apitest.Doer{"a key": viewer, "a member": w.api.MemberAs(t, w.tenant.ID, authz.RoleUsageViewer)} {
			perms := decode[adminapi.PermissionList](t, who.Do(t, "GET", "/v1/permissions", nil, nil), 200)
			cat := authz.Catalogue()
			if len(perms.Items) != len(cat) {
				t.Fatalf("%s: %d permissions, want %d", name, len(perms.Items), len(cat))
			}
			for i, p := range cat {
				got := perms.Items[i]
				if got.Name != p.Name || string(got.Scope) != string(p.Scope) || got.Description != p.Description || got.InTenant != p.InTenant || got.Support != p.Support {
					t.Errorf("%s: permission %d is %+v, want %+v", name, i, got, p)
				}
			}
			roles := decode[adminapi.BuiltinRoleList](t, who.Do(t, "GET", "/v1/roles", nil, nil), 200)
			want := authz.Roles()
			if len(roles.Items) != len(want) {
				t.Fatalf("%s: %d built-in roles, want %d", name, len(roles.Items), len(want))
			}
			for i, r := range want {
				got := roles.Items[i]
				if got.Name != r.Name || string(got.Scope) != string(r.Scope) || got.Members != r.Members || !reflect.DeepEqual(got.Permissions, r.Permissions) || got.Description != r.Description {
					t.Errorf("%s: built-in role %d is %+v, want %+v", name, i, got, r)
				}
			}
		}
		if r := w.api.Do(t, "GET", "/v1/permissions", "", nil, nil); r.Status != 401 {
			t.Errorf("the catalogue without a credential: %d", r.Status)
		}
	})

	t.Run("createRole and getRole", func(t *testing.T) {
		r := w.owner.Do(t, "POST", w.tp("/roles"), map[string]any{"name": "reader", "description": "reads", "permissions": []string{"role:read", "member:read", "member:read"}}, nil)
		created := decode[adminapi.Role](t, r, 201)
		if created.Name != "reader" || created.Description != "reads" || created.State != adminapi.RoleStateActive || created.TenantID != w.tenant.ID ||
			!reflect.DeepEqual(created.Permissions, []string{"member:read", "role:read"}) || created.ID == "" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
			t.Errorf("the created role: %+v (the set is stored sorted and without duplicates)", created)
		}
		got := decode[adminapi.Role](t, w.owner.Do(t, "GET", w.tp("/roles/reader"), nil, nil), 200)
		if got.ID != created.ID || got.Assignments == nil || got.Assignments.Members != 0 || got.Assignments.ServiceAccounts != 0 {
			t.Errorf("getRole: %+v, want the role and assignments 0/0", got)
		}
		// assignments: the number of members and of service accounts that hold it.
		w.sa(t, "reader")
		w.sa(t, "reader")
		w.member(t, "reader")
		got = decode[adminapi.Role](t, w.owner.Do(t, "GET", w.tp("/roles/reader"), nil, nil), 200)
		if got.Assignments == nil || got.Assignments.Members != 1 || got.Assignments.ServiceAccounts != 2 {
			t.Errorf("getRole assignments %+v, want 1 member and 2 service accounts", got.Assignments)
		}
		if r := w.owner.Do(t, "GET", w.tp("/roles/no-such-role"), nil, nil); r.Status != 404 {
			t.Errorf("a role that does not exist: %d", r.Status)
		}
		rows := w.audit(t, "role.create")
		if len(rows) != 1 || rows[0].ActorType != "service_account" || rows[0].Outcome != "success" {
			t.Fatalf("the audit rows of role.create: %+v", rows)
		}
		if rows[0].Detail["name"] != "reader" || !reflect.DeepEqual(rows[0].Detail["permissions"], []any{"member:read", "role:read"}) {
			t.Errorf("the detail of role.create: %v", rows[0].Detail)
		}
	})

	t.Run("the refusals of creation", func(t *testing.T) {
		before := len(w.audit(t, "role.create"))
		create := func(body map[string]any) *apitest.Response { return w.owner.Do(t, "POST", w.tp("/roles"), body, nil) }
		for what, perm := range map[string]string{"a platform permission": "tenant:read", "another platform permission": "tenant_support:open", "a self permission": "identity:read",
			"the other self permission": "session:self", "an unknown permission": "ghost:thing"} {
			r := create(map[string]any{"name": apitest.UniqueName("refused"), "permissions": []string{"member:read", perm}})
			p := invalidOn(t, what, r, "permissions")
			if len(p.Errors) > 0 && !strings.Contains(p.Errors[0].Message, perm) {
				t.Errorf("%s: the message %q does not name the offender %s", what, p.Errors[0].Message, perm)
			}
		}
		for what, name := range map[string]string{"a built-in name": "owner", "another built-in name": "inference", "a platform role's name": "platform-admin", "the platform auditor": "platform-auditor",
			"a reserved prefix": "platform-ops", "the other prefix": "pandokos-x", "an upper-case name": "Reader", "a name that starts with a digit": "1st"} {
			invalidOn(t, what, create(map[string]any{"name": name, "permissions": []string{"member:read"}}), "name")
		}
		for what, perms := range map[string][]string{"no permission": {}, "65 permissions": repeat("member:read", 65), "a malformed name": {"Member:Read"}} {
			invalidOn(t, what, create(map[string]any{"name": apitest.UniqueName("refused"), "permissions": perms}), "permissions")
		}
		if r := create(map[string]any{"name": "reader", "permissions": []string{"member:read"}}); r.Status != 409 || problemOf(t, r).Code != adminapi.ProblemCodeConflict {
			t.Errorf("a second role of the same name: %d %s, want 409 conflict", r.Status, r.Body)
		}
		if n := len(w.audit(t, "role.create")); n != before {
			t.Errorf("the refusals wrote %d audit rows", n-before)
		}
		// Exactly 64 permissions are accepted: the whole tenant catalogue is one of them.
		if r := create(map[string]any{"name": "everything", "permissions": authz.TenantPermissions()}); r.Status != 201 {
			t.Errorf("a role with every tenant permission: %d %s", r.Status, r.Body)
		}
	})

	t.Run("the 101st role", func(t *testing.T) {
		v := newRW(t)
		for i := 1; i <= 100; i++ {
			v.d.CustomRole(t, v.tid, fmt.Sprintf("bulk-%d", i), "member:read")
		}
		r := v.owner.Do(t, "POST", v.tp("/roles"), map[string]any{"name": "one-too-many", "permissions": []string{"member:read"}}, nil)
		if r.Status != 409 || problemOf(t, r).Code != adminapi.ProblemCodeConflict {
			t.Errorf("the 101st role: %d %s, want 409 conflict", r.Status, r.Body)
		}
		if r := v.owner.Do(t, "DELETE", v.tp("/roles/bulk-100"), nil, nil); r.Status != 204 {
			t.Fatalf("deleting a role: %d %s", r.Status, r.Body)
		}
		if r := v.owner.Do(t, "POST", v.tp("/roles"), map[string]any{"name": "fits-again", "permissions": []string{"member:read"}}, nil); r.Status != 201 {
			t.Errorf("a role once there is room: %d %s", r.Status, r.Body)
		}
	})

	t.Run("listing: pages, sort, state", func(t *testing.T) {
		v := newRW(t)
		for _, n := range []string{"delta-role", "alpha-role", "charlie-role", "bravo-role"} {
			v.role(t, n, "member:read")
		}
		if r := v.owner.Do(t, "POST", v.tp("/roles/bravo-role/deactivate"), map[string]any{"reason": "not needed"}, nil); r.Status != 200 {
			t.Fatalf("deactivate: %d %s", r.Status, r.Body)
		}
		names := func(query string) []string {
			var out []string
			cursor := ""
			for i := 0; i < 10; i++ {
				q := query
				if cursor != "" {
					q += "&cursor=" + url.QueryEscape(cursor)
				}
				page := decode[adminapi.RoleList](t, v.owner.Do(t, "GET", v.tp("/roles?"+q), nil, nil), 200)
				for _, r := range page.Items {
					out = append(out, r.Name)
					if r.Assignments != nil {
						t.Errorf("a role in a list carries assignments: %+v", r)
					}
				}
				if page.NextCursor == "" {
					return out
				}
				cursor = page.NextCursor
			}
			t.Fatal("too many pages")
			return nil
		}
		if got := names("sort=name&limit=2"); !reflect.DeepEqual(got, []string{"alpha-role", "bravo-role", "charlie-role", "delta-role"}) {
			t.Errorf("by name in pages of two: %v", got)
		}
		if got := names("sort=-name&limit=3"); !reflect.DeepEqual(got, []string{"delta-role", "charlie-role", "bravo-role", "alpha-role"}) {
			t.Errorf("by name, descending: %v", got)
		}
		if got := names("sort=created_at&limit=2"); !reflect.DeepEqual(got, []string{"delta-role", "alpha-role", "charlie-role", "bravo-role"}) {
			t.Errorf("by creation: %v", got)
		}
		if got := names("sort=-created_at&limit=50"); !reflect.DeepEqual(got, []string{"bravo-role", "charlie-role", "alpha-role", "delta-role"}) {
			t.Errorf("by creation, descending: %v", got)
		}
		if got := names("state=inactive&sort=name"); !reflect.DeepEqual(got, []string{"bravo-role"}) {
			t.Errorf("state inactive: %v", got)
		}
		if got := names("state=active&sort=name"); !reflect.DeepEqual(got, []string{"alpha-role", "charlie-role", "delta-role"}) {
			t.Errorf("state active: %v", got)
		}
		for _, q := range []string{"state=frozen", "sort=size", "limit=0", "limit=201", "cursor=not-a-cursor"} {
			if r := v.owner.Do(t, "GET", v.tp("/roles?"+q), nil, nil); r.Status != 400 {
				t.Errorf("?%s: %d, want 400", q, r.Status)
			}
		}
	})

	t.Run("updateRole", func(t *testing.T) {
		v := newRW(t)
		v.role(t, "editable", "member:read", "role:read")
		reason := apitest.Reason("update")
		r := decode[adminapi.Role](t, v.owner.Do(t, "PATCH", v.tp("/roles/editable"), map[string]any{"description": "now with text", "permissions": []string{"role:read", "usage:read", "usage:read"}, "reason": reason}, nil), 200)
		if r.Description != "now with text" || !reflect.DeepEqual(r.Permissions, []string{"role:read", "usage:read"}) {
			t.Errorf("the updated role: %+v", r)
		}
		rows := v.audit(t, "role.update")
		if len(rows) != 1 || rows[0].Reason != reason {
			t.Fatalf("role.update rows: %+v", rows)
		}
		change, _ := rows[0].Detail["permissions"].(map[string]any)
		if !reflect.DeepEqual(change["added"], []any{"usage:read"}) || !reflect.DeepEqual(change["removed"], []any{"member:read"}) {
			t.Errorf("the detail of role.update: %v, want permissions added and removed", rows[0].Detail)
		}
		// Only the description: the permissions stay.
		r = decode[adminapi.Role](t, v.owner.Do(t, "PATCH", v.tp("/roles/editable"), map[string]any{"description": "only text"}, nil), 200)
		if r.Description != "only text" || !reflect.DeepEqual(r.Permissions, []string{"role:read", "usage:read"}) {
			t.Errorf("a description alone: %+v", r)
		}
		// A change that changes nothing writes no row.
		n := len(v.audit(t, "role.update"))
		decode[adminapi.Role](t, v.owner.Do(t, "PATCH", v.tp("/roles/editable"), map[string]any{"description": "only text", "permissions": []string{"usage:read", "role:read"}}, nil), 200)
		if got := len(v.audit(t, "role.update")); got != n {
			t.Errorf("an update that changes nothing wrote %d rows", got-n)
		}
		for what, body := range map[string]map[string]any{"an empty body": {}, "a reason alone": {"reason": "why"}} {
			if r := v.owner.Do(t, "PATCH", v.tp("/roles/editable"), body, nil); r.Status != 400 || problemOf(t, r).Code != adminapi.ProblemCodeInvalidRequest {
				t.Errorf("%s: %d %s, want 400 invalid_request", what, r.Status, r.Body)
			}
		}
		invalidOn(t, "a platform permission", v.owner.Do(t, "PATCH", v.tp("/roles/editable"), map[string]any{"permissions": []string{"tenant:write"}}, nil), "permissions")
		invalidOn(t, "no permission", v.owner.Do(t, "PATCH", v.tp("/roles/editable"), map[string]any{"permissions": []string{}}, nil), "permissions")
		if r := v.owner.Do(t, "PATCH", v.tp("/roles/no-such-role"), map[string]any{"description": "x"}, nil); r.Status != 404 {
			t.Errorf("updating a role that does not exist: %d", r.Status)
		}
		if r := v.owner.Do(t, "PATCH", v.tp("/roles/owner"), map[string]any{"description": "x"}, nil); r.Status != 404 {
			t.Errorf("a built-in role is not a custom role: %d", r.Status)
		}
	})

	t.Run("deleteRole answers 204 and the role is gone", func(t *testing.T) {
		v := newRW(t)
		v.role(t, "short-lived", "member:read")
		r := v.owner.Do(t, "DELETE", v.tp("/roles/short-lived"), nil, nil)
		if r.Status != 204 || len(r.Body) != 0 {
			t.Errorf("deleteRole: %d %q, want 204 and no body", r.Status, r.Body)
		}
		if r := v.owner.Do(t, "GET", v.tp("/roles/short-lived"), nil, nil); r.Status != 404 {
			t.Errorf("the deleted role: %d", r.Status)
		}
		if r := v.owner.Do(t, "DELETE", v.tp("/roles/short-lived"), nil, nil); r.Status != 404 {
			t.Errorf("deleting twice: %d", r.Status)
		}
		rows := v.audit(t, "role.delete")
		if len(rows) != 1 || !reflect.DeepEqual(rows[0].Detail["permissions"], []any{"member:read"}) {
			t.Errorf("role.delete rows: %+v, want one with the last permissions", rows)
		}
	})
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// TestRoleDeactivation is T-860.
func TestRoleDeactivation(t *testing.T) {
	t.Parallel()
	w := newRW(t)
	w.role(t, "viewer-x", "member:read", "role:read")
	memberID, member := w.member(t, "viewer-x")
	holder, holderKey := w.sa(t, "viewer-x")
	asKey := &apitest.Caller{API: w.api, Key: holderKey}
	if r := member.Do(t, "GET", w.tp("/roles"), nil, nil); r.Status != 200 {
		t.Fatalf("a member with the role: %d %s", r.Status, r.Body)
	}

	reason := apitest.Reason("deactivate")
	for what, body := range map[string]map[string]any{"no reason": {}, "an empty reason": {"reason": ""}, "a blank reason": {"reason": "   "}} {
		if r := w.owner.Do(t, "POST", w.tp("/roles/viewer-x/deactivate"), body, nil); r.Status != 400 || !hasField(problemOf(t, r), "reason") {
			t.Errorf("deactivate with %s: %d %s, want 400 on reason", what, r.Status, r.Body)
		}
	}
	if got := decode[adminapi.Role](t, w.owner.Do(t, "GET", w.tp("/roles/viewer-x"), nil, nil), 200); got.State != adminapi.RoleStateActive {
		t.Fatalf("a refused deactivation changed the state to %s", got.State)
	}
	got := decode[adminapi.Role](t, w.owner.Do(t, "POST", w.tp("/roles/viewer-x/deactivate"), map[string]any{"reason": reason}, nil), 200)
	if got.State != adminapi.RoleStateInactive {
		t.Fatalf("deactivateRole: %+v", got)
	}
	if rows := w.audit(t, "role.deactivate"); len(rows) != 1 || rows[0].Reason != reason {
		t.Errorf("role.deactivate rows: %+v, want one with the reason", rows)
	}
	// An inactive role gives its holders nothing at the next request.
	if r := member.Do(t, "GET", w.tp("/roles"), nil, nil); !apitest.Refused(r) {
		t.Errorf("a member of an inactive role: %d %s, want 403 forbidden", r.Status, r.Body)
	}
	if r := asKey.Do(t, "GET", w.tp("/roles"), nil, nil); !apitest.Refused(r) {
		t.Errorf("a key of an inactive role: %d %s, want 403 forbidden", r.Status, r.Body)
	}
	// The assignments stay.
	if roles := w.memberRoles(t, memberID); !reflect.DeepEqual(roles, []string{"viewer-x"}) {
		t.Errorf("the member's roles: %v", roles)
	}
	if roles := w.saRoles(t, holder); !reflect.DeepEqual(roles, []string{"viewer-x"}) {
		t.Errorf("the service account's roles: %v", roles)
	}
	if a := decode[adminapi.Role](t, w.owner.Do(t, "GET", w.tp("/roles/viewer-x"), nil, nil), 200).Assignments; a == nil || a.Members != 1 || a.ServiceAccounts != 1 {
		t.Errorf("the assignments of an inactive role: %+v", a)
	}
	// It cannot be assigned.
	invalidOn(t, "a service account with an inactive role", w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": "x-sa", "roles": []string{"viewer-x"}}, nil), "roles")
	invalidOn(t, "a member with an inactive role", w.owner.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": "inactive.holder", "roles": []string{"viewer-x"}}, nil), "roles")
	invalidOn(t, "an update that adds an inactive role", w.owner.Do(t, "PATCH", w.tp("/service-accounts/"+holder), map[string]any{"roles": []string{"viewer-x", "inference"}}, nil), "roles")
	// Deactivating an inactive role succeeds and changes nothing.
	if r := w.owner.Do(t, "POST", w.tp("/roles/viewer-x/deactivate"), map[string]any{"reason": reason}, nil); r.Status != 200 {
		t.Errorf("deactivating an inactive role: %d %s", r.Status, r.Body)
	}

	// activateRole restores it, and also needs a reason.
	for what, body := range map[string]map[string]any{"no reason": {}, "a blank reason": {"reason": " \t"}} {
		if r := w.owner.Do(t, "POST", w.tp("/roles/viewer-x/activate"), body, nil); r.Status != 400 || !hasField(problemOf(t, r), "reason") {
			t.Errorf("activate with %s: %d %s, want 400 on reason", what, r.Status, r.Body)
		}
	}
	if r := member.Do(t, "GET", w.tp("/roles"), nil, nil); !apitest.Refused(r) {
		t.Errorf("a refused activation restored the role: %d", r.Status)
	}
	reason2 := apitest.Reason("activate")
	if got := decode[adminapi.Role](t, w.owner.Do(t, "POST", w.tp("/roles/viewer-x/activate"), map[string]any{"reason": reason2}, nil), 200); got.State != adminapi.RoleStateActive {
		t.Fatalf("activateRole: %+v", got)
	}
	if rows := w.audit(t, "role.activate"); len(rows) != 1 || rows[0].Reason != reason2 {
		t.Errorf("role.activate rows: %+v", rows)
	}
	if r := member.Do(t, "GET", w.tp("/roles"), nil, nil); r.Status != 200 {
		t.Errorf("a member after activateRole: %d %s", r.Status, r.Body)
	}
	if r := asKey.Do(t, "GET", w.tp("/roles"), nil, nil); r.Status != 200 {
		t.Errorf("a key after activateRole: %d %s", r.Status, r.Body)
	}
	// A role that does not exist.
	for _, verb := range []string{"deactivate", "activate"} {
		if r := w.owner.Do(t, "POST", w.tp("/roles/no-such-role/"+verb), map[string]any{"reason": "x"}, nil); r.Status != 404 {
			t.Errorf("%s of a role that does not exist: %d", verb, r.Status)
		}
	}
}

// TestRoleDeletionGuard is T-861.
func TestRoleDeletionGuard(t *testing.T) {
	t.Parallel()
	w := newRW(t)
	t.Run("a role held by a member or by a service account is not deleted", func(t *testing.T) {
		w.role(t, "held-twice", "member:read")
		memberID, _ := w.member(t, "held-twice")
		saID, _ := w.sa(t, "held-twice")
		del := func() *apitest.Response { return w.owner.Do(t, "DELETE", w.tp("/roles/held-twice"), nil, nil) }
		if r := del(); r.Status != 409 || problemOf(t, r).Code != adminapi.ProblemCodeRoleInUse {
			t.Fatalf("a role held by both: %d %s, want 409 role_in_use", r.Status, r.Body)
		}
		// The member loses it; the service account still holds it.
		decode[adminapi.Member](t, w.owner.Do(t, "PATCH", w.tp("/members/"+memberID), map[string]any{"roles": []string{"read-only"}}, nil), 200)
		if r := del(); r.Status != 409 || problemOf(t, r).Code != adminapi.ProblemCodeRoleInUse {
			t.Errorf("a role held by a service account: %d %s, want 409 role_in_use", r.Status, r.Body)
		}
		decode[adminapi.ServiceAccount](t, w.owner.Do(t, "PATCH", w.tp("/service-accounts/"+saID), map[string]any{"roles": []string{"inference"}}, nil), 200)
		if r := del(); r.Status != 204 {
			t.Errorf("a role that nobody holds: %d %s, want 204", r.Status, r.Body)
		}
	})
	t.Run("a role held by a member only", func(t *testing.T) {
		w.role(t, "member-only", "member:read")
		memberID, _ := w.member(t, "member-only")
		if r := w.owner.Do(t, "DELETE", w.tp("/roles/member-only"), nil, nil); r.Status != 409 || problemOf(t, r).Code != adminapi.ProblemCodeRoleInUse {
			t.Errorf("%d %s, want 409 role_in_use", r.Status, r.Body)
		}
		if r := w.owner.Do(t, "DELETE", w.tp("/members/"+memberID), nil, nil); r.Status != 200 {
			t.Fatalf("removing the member: %d %s", r.Status, r.Body)
		}
		if r := w.owner.Do(t, "DELETE", w.tp("/roles/member-only"), nil, nil); r.Status != 204 {
			t.Errorf("after the member was removed: %d %s", r.Status, r.Body)
		}
	})
	t.Run("a concurrent assignment and deletion never leave an assignment of a missing role", func(t *testing.T) {
		const rounds = 50
		for i := 0; i < rounds; i++ {
			name := fmt.Sprintf("race-%d", i)
			w.role(t, name, "member:read")
			saID, _ := w.sa(t, "inference")
			var assign, del *apitest.Response
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				assign = w.owner.Do(t, "PATCH", w.tp("/service-accounts/"+saID), map[string]any{"roles": []string{"inference", name}}, nil)
			}()
			go func() {
				defer wg.Done()
				del = w.owner.Do(t, "DELETE", w.tp("/roles/"+name), nil, nil)
			}()
			wg.Wait()
			holds := false
			for _, r := range w.saRoles(t, saID) {
				holds = holds || r == name
			}
			exists := w.owner.Do(t, "GET", w.tp("/roles/"+name), nil, nil).Status == 200
			switch {
			case exists && holds && assign.Status == 200 && del.Status == 409:
			case !exists && !holds && assign.Status == 400 && del.Status == 204:
			default:
				t.Fatalf("round %d: the role exists %v, is held %v, the assignment answered %d and the deletion %d; either it exists and is assigned (200, 409) or it is gone and the assignment was refused (400, 204)",
					i, exists, holds, assign.Status, del.Status)
			}
		}
	})
	t.Run("the trigger is the backstop", func(t *testing.T) {
		w.role(t, "backstop", "member:read")
		saID, _ := w.sa(t, "backstop")
		if _, err := w.d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_role WHERE tenant_id = $1::uuid AND name = 'backstop'`, w.tid.String()); err == nil {
			t.Error("the database deleted a role that is assigned")
		}
		if roles := w.saRoles(t, saID); !reflect.DeepEqual(roles, []string{"backstop"}) {
			t.Errorf("the roles of the holder: %v", roles)
		}
	})
}

// TestAssignableRoles is T-866.
func TestAssignableRoles(t *testing.T) {
	t.Parallel()
	w := newRW(t)
	t.Run("a member cannot hold inference", func(t *testing.T) {
		invalidOn(t, "createMember", w.owner.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": "inference.person", "roles": []string{"inference"}}, nil), "roles")
		_, b := w.member(t, "read-only")
		id := w.api.MemberID(t, b)
		invalidOn(t, "updateMember", w.owner.Do(t, "PATCH", w.tp("/members/"+id), map[string]any{"roles": []string{"read-only", "inference"}}, nil), "roles")
	})
	t.Run("a service account can hold each tenant role", func(t *testing.T) {
		for _, r := range authz.Roles() {
			if r.Scope != authz.ScopeTenant {
				continue
			}
			if resp := w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("holds"), "roles": []string{r.Name}}, nil); resp.Status != 201 {
				t.Errorf("a service account with %s: %d %s", r.Name, resp.Status, resp.Body)
			}
		}
	})
	t.Run("a platform role in a tenant, and the reverse", func(t *testing.T) {
		for _, role := range []string{"platform-admin", "platform-auditor"} {
			invalidOn(t, "a service account with "+role, w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{role}}, nil), "roles")
			invalidOn(t, "a member with "+role, w.owner.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{role}}, nil), "roles")
		}
		for _, role := range []string{"tenant-admin", "owner", "read-only"} {
			invalidOn(t, "a platform user with "+role, w.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": apitest.UniqueName("p"), "platform_roles": []string{role}}), "platform_roles")
			invalidOn(t, "a platform key with "+role, w.platform(t, "POST", "/v1/platform-keys", map[string]any{"description": "x", "role": role}), "role")
		}
		invalidOn(t, "a platform key with an unknown role", w.platform(t, "POST", "/v1/platform-keys", map[string]any{"description": "x", "role": "ghost"}), "role")
	})
	t.Run("a custom role of another tenant, an inactive one, an unknown one", func(t *testing.T) {
		other, _ := w.api.Tenant(t, "other-tenant")
		w.api.CustomRole(t, other.ID, "member:read") // makes custom-N in the other tenant only
		foreign := w.api.CustomRole(t, other.ID, "member:read")
		invalidOn(t, "a role of another tenant", w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{foreign}}, nil), "roles")
		invalidOn(t, "an unknown role", w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"ghost"}}, nil), "roles")
		invalidOn(t, "an unknown role on a member", w.owner.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{"ghost"}}, nil), "roles")
		mine := w.api.CustomRole(t, w.tenant.ID, "member:read")
		if r := w.owner.Do(t, "POST", w.tp("/roles/"+mine+"/deactivate"), map[string]any{"reason": "x"}, nil); r.Status != 200 {
			t.Fatalf("deactivate: %d", r.Status)
		}
		invalidOn(t, "an inactive custom role", w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{mine}}, nil), "roles")
		// An active custom role of the tenant is assignable to a member and to a service account.
		active := w.api.CustomRole(t, w.tenant.ID, "member:read")
		if r := w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{active}}, nil); r.Status != 201 {
			t.Errorf("a service account with an active custom role: %d %s", r.Status, r.Body)
		}
		w.member(t, active)
	})
}

// TestProvisioningOwner is T-881.
func TestProvisioningOwner(t *testing.T) {
	t.Parallel()
	api := apitest.NewPeople(t, apitest.PeopleConfig{})
	out := decode[adminapi.TenantCreated](t, api.Do(t, "POST", "/v1/tenants", api.PlatformKey(), map[string]any{"name": "first-owner"}, nil), 201)
	if !reflect.DeepEqual(out.AdminServiceAccount.Roles, []string{"owner"}) {
		t.Errorf("the first service account of a tenant has the roles %v, want [owner]", out.AdminServiceAccount.Roles)
	}
	if out.AdminKey == nil {
		t.Fatal("no first key")
	}
	key := &apitest.Caller{API: api, Key: out.AdminKey.Secret}
	me := decode[adminapi.Me](t, key.Do(t, "GET", "/v1/me", nil, nil), 200)
	if !reflect.DeepEqual(me.Roles, []string{"owner"}) || !reflect.DeepEqual(me.Permissions, apitest.Golden(t)["owner"]) {
		t.Errorf("the key of the first service account: roles %v permissions %v", me.Roles, me.Permissions)
	}
	saPath := "/v1/tenants/" + out.Tenant.ID + "/service-accounts/" + out.AdminServiceAccount.ID
	if got := decode[adminapi.ServiceAccount](t, key.Do(t, "GET", saPath, nil, nil), 200); !reflect.DeepEqual(got.Roles, []string{"owner"}) {
		t.Errorf("getServiceAccount: roles %v", got.Roles)
	}
	tid := parseTenant(t, out.Tenant.ID)
	found := false
	for _, r := range api.DB.AuditRows(t, tid) {
		if roles, ok := r.Detail["roles"].([]any); ok && reflect.DeepEqual(roles, []any{"owner"}) {
			found = true
		}
		if roles, ok := r.Detail["roles"].([]any); ok && reflect.DeepEqual(roles, []any{"tenant-admin"}) {
			t.Errorf("an audit row of the creation still says tenant-admin: %+v", r)
		}
	}
	if !found {
		t.Error("no audit row of the creation says roles: [owner]")
	}
	// Its key is refused by no tenant operation.
	p := probeParams(t, api, out.Tenant.ID)
	for _, op := range adminapi.Operations {
		perm, _ := authz.Lookup(op.Permission)
		if op.Public || perm.Scope != authz.ScopeTenant {
			continue
		}
		if r := apitest.Probe(t, key, op, p); apitest.Refused(r) || problemCode(r) == "escalation_refused" {
			t.Errorf("%s: the key of the first owner is refused: %d %s", op.ID, r.Status, r.Body)
		}
	}
}

func problemCode(r *apitest.Response) string { return r.Problem() }

// TestRolesIdempotency is T-894.
func TestRolesIdempotency(t *testing.T) {
	t.Parallel()
	w := newRW(t)
	plat := &apitest.Caller{API: w.api, Key: w.api.PlatformKey(), Name: "platform"}
	t.Run("createRole", func(t *testing.T) {
		hdr := http.Header{"Idempotency-Key": {"role-once"}}
		body := map[string]any{"name": "once", "permissions": []string{"member:read"}}
		first := w.owner.Do(t, "POST", w.tp("/roles"), body, hdr)
		second := w.owner.Do(t, "POST", w.tp("/roles"), body, hdr)
		if first.Status != 201 || second.Status != 201 || second.Header.Get("Idempotency-Replayed") != "true" {
			t.Fatalf("first %d, second %d replayed %q\n%s\n%s", first.Status, second.Status, second.Header.Get("Idempotency-Replayed"), first.Body, second.Body)
		}
		var a, b adminapi.Role
		first.JSON(t, &a)
		second.JSON(t, &b)
		if a.ID != b.ID || a.Name != "once" {
			t.Errorf("the replay is another role: %+v / %+v", a, b)
		}
		roles := decode[adminapi.RoleList](t, w.owner.Do(t, "GET", w.tp("/roles"), nil, nil), 200)
		if len(roles.Items) != 1 {
			t.Errorf("%d roles after two requests with one key", len(roles.Items))
		}
		if n := len(w.audit(t, "role.create")); n != 1 {
			t.Errorf("%d role.create rows, want one", n)
		}
	})
	t.Run("openSupportAction", func(t *testing.T) {
		hdr := http.Header{"Idempotency-Key": {"open-once"}}
		body := map[string]any{"reason": apitest.Reason("open"), "permissions": []string{"member:read"}, "duration_seconds": 600}
		path := "/v1/tenant-support/" + w.tenant.ID + "/actions"
		first := plat.Do(t, "POST", path, body, hdr)
		var a adminapi.SupportAction
		if first.Status != 201 {
			t.Fatalf("the first opening: %d %s", first.Status, first.Body)
		}
		first.JSON(t, &a)
		second := plat.Do(t, "POST", path, body, hdr)
		if second.Status != 201 || second.Header.Get("Idempotency-Replayed") != "true" {
			t.Fatalf("the replay: %d %q %s", second.Status, second.Header.Get("Idempotency-Replayed"), second.Body)
		}
		var b adminapi.SupportAction
		second.JSON(t, &b)
		if a.ID != b.ID || !a.ExpiresAt.Equal(b.ExpiresAt) || !a.OpenedAt.Equal(b.OpenedAt) {
			t.Errorf("the replay of the opening extended or replaced the action:\n%+v\n%+v", a, b)
		}
		list := decode[adminapi.SupportActionList](t, plat.Do(t, "GET", path, nil, nil), 200)
		if len(list.Items) != 1 {
			t.Errorf("%d actions after two requests with one key", len(list.Items))
		}
		if n := len(w.audit(t, "support_action.open")); n != 1 {
			t.Errorf("%d support_action.open rows in the tenant's log, want one", n)
		}
		platformRows := 0
		for _, r := range w.d.PlatformAuditRows(t) {
			if r.Action == "support_action.open" {
				platformRows++
			}
		}
		if platformRows != 1 {
			t.Errorf("%d support_action.open rows in the platform's log, want one", platformRows)
		}
	})
}

// TestRoleChangeBound is T-896: a change of what a role gives holds at the next request, for a
// member and for a service account's key on the administration listener.
func TestRoleChangeBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(t *testing.T, w *rw, memberID, saID string)
	}{
		{"the role is removed from the holders", func(t *testing.T, w *rw, memberID, saID string) {
			decode[adminapi.Member](t, w.owner.Do(t, "PATCH", w.tp("/members/"+memberID), map[string]any{"roles": []string{"usage-viewer"}}, nil), 200)
			decode[adminapi.ServiceAccount](t, w.owner.Do(t, "PATCH", w.tp("/service-accounts/"+saID), map[string]any{"roles": []string{"inference"}}, nil), 200)
		}},
		{"a permission is removed from the role", func(t *testing.T, w *rw, _, _ string) {
			decode[adminapi.Role](t, w.owner.Do(t, "PATCH", w.tp("/roles/bounded"), map[string]any{"permissions": []string{"member:read"}}, nil), 200)
		}},
		{"the role is deactivated", func(t *testing.T, w *rw, _, _ string) {
			decode[adminapi.Role](t, w.owner.Do(t, "POST", w.tp("/roles/bounded/deactivate"), map[string]any{"reason": "bound"}, nil), 200)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newRW(t)
			w.role(t, "bounded", "role:read", "member:read")
			memberID, member := w.member(t, "bounded")
			saID, key := w.sa(t, "bounded")
			asKey := &apitest.Caller{API: w.api, Key: key}
			for name, who := range map[string]apitest.Doer{"the member": member, "the key": asKey} {
				if r := who.Do(t, "GET", w.tp("/roles"), nil, nil); r.Status != 200 {
					t.Fatalf("%s before the change: %d %s", name, r.Status, r.Body)
				}
			}
			c.change(t, w, memberID, saID)
			for name, who := range map[string]apitest.Doer{"the member": member, "the key": asKey} {
				if r := who.Do(t, "GET", w.tp("/roles"), nil, nil); !apitest.Refused(r) {
					t.Errorf("%s at the next request: %d %s, want 403 forbidden", name, r.Status, r.Body)
				}
			}
		})
	}
}
