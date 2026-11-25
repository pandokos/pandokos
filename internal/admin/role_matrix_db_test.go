package admin_test

// The matrix of the exit condition (sprint 4.3): what each of the ten roles may do, what
// /v1/me says about it, and that the two agree: T-867 to T-869.

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
)

// short is the start of a response body, for the messages of tests that probe every operation.
func short(r *apitest.Response) string {
	if len(r.Body) > 160 {
		return string(r.Body[:160]) + "..."
	}
	return string(r.Body)
}

func setOf(in []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range in {
		m[s] = true
	}
	return m
}

// probeOps returns the operations that a matrix probes, in an order that ends with the one that signs a session out.
func probeOps() []adminapi.Operation {
	var ops []adminapi.Operation
	var last []adminapi.Operation
	var open []adminapi.Operation
	for _, op := range adminapi.Operations {
		switch {
		case op.Public:
		case op.ID == "signOut":
			last = append(last, op)
		case op.ID == "openSupportAction":
			// It really opens an action for the probing identity, which then holds member:read in the
			// tenant it probes: it is probed after every other operation, before the sign-out.
			open = append(open, op)
		default:
			ops = append(ops, op)
		}
	}
	return append(append(ops, open...), last...)
}

// TestRoleMatrix is T-867, the exit condition: for each of the ten roles and every operation of the
// document, with a key (a service account of a tenant role, a platform key of a platform role) and
// with a session (a member, a platform user) for the roles a person can hold, the answer is 403
// forbidden exactly when the role's line of the golden file lacks the permission of the operation.
func TestRoleMatrix(t *testing.T) {
	t.Parallel()
	api := apitest.NewPeople(t, apitest.PeopleConfig{})
	acme, _ := api.Tenant(t, "matrix-tenant")
	other, _ := api.Tenant(t, "matrix-platform-probes") // the platform roles probe another tenant: a platform administrator suspends the one it probes
	tenantParams, platformParams := probeParams(t, api, acme.ID), probeParams(t, api, other.ID)
	roles := apitest.GoldenRoles(t)
	if len(roles) != 10 {
		t.Fatalf("%d roles in the golden file", len(roles))
	}
	self := []string{authz.IdentityRead, authz.SessionSelf}

	// check probes every operation as the identity and compares each answer with the role's line.
	check := func(t *testing.T, role apitest.GoldenRole, who apitest.Doer, kind string, p apitest.Params) {
		t.Helper()
		held := setOf(role.Permissions)
		if kind == "session" {
			for _, s := range self {
				held[s] = true // a person holds the two self permissions whatever its roles
			}
		}
		allowed, refused := 0, 0
		for _, op := range probeOps() {
			if kind == "key" && op.Permission == authz.SessionSelf {
				continue // a key has no session (4.2)
			}
			r := apitest.Probe(t, who, op, p)
			perm, _ := authz.Lookup(op.Permission)
			switch {
			case held[op.Permission]:
				allowed++
				if apitest.Refused(r) {
					t.Errorf("%s %s: %s needs %s, which the golden file gives the role, and was refused: %d %s", role.Name, kind, op.ID, op.Permission, r.Status, short(r))
				}
			case role.Scope == "platform" && kind == "session" && perm.Scope == authz.ScopeTenant:
				// A platform user is not a member of any tenant: the answer of 4.2 is 404, not 403.
				refused++
				if r.Status != 404 || r.Problem() != "not_found" {
					t.Errorf("%s %s: %s (%s) answered %d %s, want 404 not_found", role.Name, kind, op.ID, op.Permission, r.Status, short(r))
				}
			default:
				refused++
				if !apitest.Refused(r) {
					t.Errorf("%s %s: %s needs %s, which the golden file does not give the role, and was answered %d %s, want 403 forbidden", role.Name, kind, op.ID, op.Permission, r.Status, short(r))
				}
			}
		}
		// inference holds no permission that an operation of the administration API needs.
		if (allowed < 2 && role.Name != authz.RoleInference) || refused < 20 {
			t.Errorf("%s %s: %d allowed and %d refused operations: the test proves nothing", role.Name, kind, allowed, refused)
		}
	}
	for _, role := range roles {
		role := role
		t.Run(role.Name, func(t *testing.T) {
			if role.Scope == "tenant" {
				check(t, role, api.AsRole(t, acme.ID, role.Name), "key", tenantParams)
				if role.Members {
					check(t, role, api.MemberAs(t, acme.ID, role.Name), "session", tenantParams)
				} else if role.Name != authz.RoleInference {
					t.Errorf("the golden file says that %s cannot be held by a person: only inference is such a role", role.Name)
				}
				return
			}
			key, session := api.PlatformAs(t, role.Name)
			check(t, role, key, "key", platformParams)
			if role.Members {
				check(t, role, session, "session", platformParams)
			}
		})
	}
}

// TestMeMatrix is T-868, the exit condition: for each role getMe reports exactly the line of the
// golden file, at the top level for keys and platform users, in memberships for members.
func TestMeMatrix(t *testing.T) {
	t.Parallel()
	api := apitest.NewPeople(t, apitest.PeopleConfig{})
	acme, _ := api.Tenant(t, "me-matrix")
	golden := apitest.GoldenRoles(t)
	withSelf := func(perms []string) []string {
		return sorted(append(append([]string(nil), perms...), authz.SessionSelf))
	}

	for _, role := range golden {
		role := role
		t.Run(role.Name, func(t *testing.T) {
			if role.Name == authz.RoleInference {
				// The one role without identity:read answers no /v1/me: it is refused (the golden file says so).
				key := api.AsRole(t, acme.ID, role.Name)
				if r := key.Do(t, "GET", "/v1/me", nil, nil); !apitest.Refused(r) {
					t.Errorf("an inference key calling getMe: %d %s, want 403 forbidden", r.Status, r.Body)
				}
				return
			}
			if role.Scope == "tenant" {
				me := decode[adminapi.Me](t, api.AsRole(t, acme.ID, role.Name).Do(t, "GET", "/v1/me", nil, nil), 200)
				if !reflect.DeepEqual(me.Permissions, role.Permissions) || !reflect.DeepEqual(me.Roles, []string{role.Name}) {
					t.Errorf("a key: roles %v permissions %v, the golden file says %v", me.Roles, me.Permissions, role.Permissions)
				}
				if role.Members {
					b := api.MemberAs(t, acme.ID, role.Name)
					me := decode[adminapi.Me](t, b.Do(t, "GET", "/v1/me", nil, nil), 200)
					if len(me.Memberships) != 1 || me.Memberships[0].Tenant.ID != acme.ID || !reflect.DeepEqual(me.Memberships[0].Roles, []string{role.Name}) {
						t.Fatalf("a member: memberships %+v", me.Memberships)
					}
					if got, want := me.Memberships[0].Permissions, withSelf(role.Permissions); !reflect.DeepEqual(got, want) {
						t.Errorf("a member: permissions %v, want the golden line and session:self, %v", got, want)
					}
					if !reflect.DeepEqual(me.Permissions, []string{authz.IdentityRead, authz.SessionSelf}) {
						t.Errorf("a member's own permissions outside a tenant: %v", me.Permissions)
					}
				}
				return
			}
			key, session := api.PlatformAs(t, role.Name)
			me := decode[adminapi.Me](t, key.Do(t, "GET", "/v1/me", nil, nil), 200)
			if !reflect.DeepEqual(me.Permissions, role.Permissions) || !reflect.DeepEqual(me.Roles, []string{role.Name}) {
				t.Errorf("a platform key: roles %v permissions %v, the golden file says %v", me.Roles, me.Permissions, role.Permissions)
			}
			me = decode[adminapi.Me](t, session.Do(t, "GET", "/v1/me", nil, nil), 200)
			if !reflect.DeepEqual(me.Permissions, withSelf(role.Permissions)) || len(me.Memberships) != 0 {
				t.Errorf("a platform user: permissions %v memberships %v, want %v and none", me.Permissions, me.Memberships, withSelf(role.Permissions))
			}
		})
	}

	t.Run("a user in two tenants, one role of them custom", func(t *testing.T) {
		second, _ := api.Tenant(t, "me-matrix-second")
		custom := api.CustomRole(t, second.ID, authz.RoleRead, authz.MemberRead, authz.TenantSettingWrite)
		person := api.DB.PlatformUser(t, apitest.UniqueName("two-tenants"), nil)
		api.DB.Member(t, parseTenant(t, acme.ID), person.ID, []string{authz.RoleUsageViewer})
		api.DB.Member(t, parseTenant(t, second.ID), person.ID, []string{custom})
		b := api.NewBrowser()
		b.MustSignIn(t, person.Username, person.Password)
		me := decode[adminapi.Me](t, b.Do(t, "GET", "/v1/me", nil, nil), 200)
		if len(me.Memberships) != 2 {
			t.Fatalf("memberships %+v", me.Memberships)
		}
		want := map[string][]string{
			acme.ID:   withSelf(apitest.Golden(t)[authz.RoleUsageViewer]),
			second.ID: withSelf([]string{authz.IdentityRead, authz.RoleRead, authz.MemberRead, authz.TenantSettingWrite}),
		}
		for _, m := range me.Memberships {
			if !reflect.DeepEqual(m.Permissions, want[m.Tenant.ID]) {
				t.Errorf("tenant %s: permissions %v, want %v", m.Tenant.Name, m.Permissions, want[m.Tenant.ID])
			}
		}
	})

	t.Run("a platform principal sees its open support actions", func(t *testing.T) {
		keyAdmin, userAdmin := api.PlatformAs(t, authz.RolePlatformAdmin)
		auditorKey, _ := api.PlatformAs(t, authz.RolePlatformAuditor)
		id := apitest.OpenSupport(t, keyAdmin, acme.ID, authz.MemberRead, authz.RoleRead)
		me := decode[adminapi.Me](t, keyAdmin.Do(t, "GET", "/v1/me", nil, nil), 200)
		if len(me.SupportActions) != 1 || me.SupportActions[0].ID != id || me.SupportActions[0].Tenant.ID != acme.ID || me.SupportActions[0].Tenant.Name != "me-matrix" ||
			!reflect.DeepEqual(me.SupportActions[0].Permissions, []string{authz.MemberRead, authz.RoleRead}) || me.SupportActions[0].ExpiresAt.IsZero() {
			t.Errorf("the platform key's support actions: %+v", me.SupportActions)
		}
		if !reflect.DeepEqual(me.Permissions, apitest.Golden(t)[authz.RolePlatformAdmin]) {
			t.Errorf("permissions at the top level stay those held outside any tenant: %v", me.Permissions)
		}
		idUser := apitest.OpenSupport(t, userAdmin, acme.ID, authz.MemberRead)
		meUser := decode[adminapi.Me](t, userAdmin.Do(t, "GET", "/v1/me", nil, nil), 200)
		if len(meUser.SupportActions) != 1 || meUser.SupportActions[0].ID != idUser {
			t.Errorf("the platform user's support actions: %+v", meUser.SupportActions)
		}
		// Another principal's actions are not mine, and a principal without tenant_support:open sees none.
		meAuditor := decode[adminapi.Me](t, auditorKey.Do(t, "GET", "/v1/me", nil, nil), 200)
		if len(meAuditor.SupportActions) != 0 {
			t.Errorf("a platform auditor sees support actions that are not its own: %+v", meAuditor.SupportActions)
		}
		if r := keyAdmin.Do(t, "POST", "/v1/tenant-support/"+acme.ID+"/actions/"+id+"/close", map[string]any{}, nil); r.Status != 200 {
			t.Fatalf("closing: %d %s", r.Status, r.Body)
		}
		if me := decode[adminapi.Me](t, keyAdmin.Do(t, "GET", "/v1/me", nil, nil), 200); len(me.SupportActions) != 0 {
			t.Errorf("a closed action is listed: %+v", me.SupportActions)
		}
	})
}

// TestMeMatchesTheHook is T-869: for 200 random sets of built-in and custom roles of a member, the
// permissions getMe reports for the tenant are exactly the permissions of the operations that the
// member is not refused: one function builds both, so they cannot differ.
func TestMeMatchesTheHook(t *testing.T) {
	t.Parallel()
	api := apitest.NewPeople(t, apitest.PeopleConfig{})
	acme, _ := api.Tenant(t, "me-hook")
	owner := api.Owner(t, acme.ID)
	seed := rand.Int63() //nolint:gosec // a test seed, logged
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // a test generator

	// Three custom roles with random permissions of the tenant catalogue.
	tenantPerms := authz.TenantPermissions()
	var custom []string
	for i := 0; i < 3; i++ {
		var perms []string
		for _, p := range tenantPerms {
			if rng.Intn(4) == 0 {
				perms = append(perms, p)
			}
		}
		if len(perms) == 0 {
			perms = []string{authz.MemberRead}
		}
		custom = append(custom, api.CustomRole(t, acme.ID, perms...))
	}
	var pool []string
	for _, r := range authz.Roles() {
		if r.Scope == authz.ScopeTenant && r.Members {
			pool = append(pool, r.Name)
		}
	}
	pool = append(pool, custom...)

	// One representative operation for every tenant permission that an operation needs.
	rep := map[string]adminapi.Operation{}
	for _, op := range adminapi.Operations {
		perm, ok := authz.Lookup(op.Permission)
		if !ok || perm.Scope != authz.ScopeTenant {
			continue
		}
		if cur, has := rep[op.Permission]; !has || (cur.Method != "GET" && op.Method == "GET") {
			rep[op.Permission] = op
		}
	}
	if len(rep) < 25 {
		t.Fatalf("%d tenant permissions have an operation", len(rep))
	}
	p := probeParams(t, api, acme.ID)

	userID, member := func() (string, *apitest.Browser) {
		b := api.MemberAs(t, acme.ID, authz.RoleReadOnly)
		return api.MemberID(t, b), b
	}()
	const rounds = 200
	for i := 0; i < rounds; i++ {
		n := 1 + rng.Intn(3)
		var set []string
		for _, j := range rng.Perm(len(pool))[:n] {
			set = append(set, pool[j])
		}
		if r := owner.Do(t, "PATCH", "/v1/tenants/"+acme.ID+"/members/"+userID, map[string]any{"roles": set}, nil); r.Status != 200 {
			t.Fatalf("round %d: setting the roles %v: %d %s", i, set, r.Status, r.Body)
		}
		me := decode[adminapi.Me](t, member.Do(t, "GET", "/v1/me", nil, nil), 200)
		if len(me.Memberships) != 1 {
			t.Fatalf("round %d: memberships %+v", i, me.Memberships)
		}
		reported := setOf(me.Memberships[0].Permissions)
		if !reported[authz.IdentityRead] || !reported[authz.SessionSelf] {
			t.Errorf("round %d: the two self permissions are missing from %v", i, me.Memberships[0].Permissions)
		}
		var notRefused, expected []string
		for perm, op := range rep {
			if !apitest.Refused(apitest.Probe(t, member, op, p)) {
				notRefused = append(notRefused, perm)
			}
			if reported[perm] {
				expected = append(expected, perm)
			}
		}
		sort.Strings(notRefused)
		sort.Strings(expected)
		if !reflect.DeepEqual(notRefused, expected) {
			t.Fatalf("round %d, roles %v: getMe reports %v for the tenant, the hook allows %v", i, set, expected, notRefused)
		}
	}
}
