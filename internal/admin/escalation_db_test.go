package admin_test

// The tests of the rule against escalation (sprint 4.3, section 5.4): T-862 to T-865 and T-895.

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
)

func escalation(t *testing.T, what string, r *apitest.Response) {
	t.Helper()
	if r.Status != 403 || r.Problem() != "escalation_refused" {
		t.Errorf("%s: %d %s, want 403 escalation_refused", what, r.Status, r.Body)
	}
}

// TestNoEscalationRoles is T-862.
func TestNoEscalationRoles(t *testing.T) {
	w := newRW(t)
	w.role(t, "role-manager", "role:read", "role:write", "role:delete")
	w.role(t, "has-usage", "member:read", "usage:read")
	w.role(t, "plain", "role:read")
	_, key := w.sa(t, "role-manager")
	k := &apitest.Caller{API: w.api, Key: key, Name: "role-manager"}
	// A scoped key of an owner: it holds role:read and role:write, and nothing else.
	ownerSA := decode[adminapi.ServiceAccount](t, w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": "scoped-owner", "roles": []string{"owner"}}, nil), 201)
	scopedKey := decode[adminapi.VirtualKeyCreated](t, w.owner.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": ownerSA.ID, "scopes": []string{"role:read", "role:write", "role:delete"}}, nil), 201)
	scoped := &apitest.Caller{API: w.api, Key: scopedKey.Secret, Name: "scoped owner"}
	// A deactivated role to activate.
	w.role(t, "was-active", "role:read", "usage:read")
	if r := w.owner.Do(t, "POST", w.tp("/roles/was-active/deactivate"), map[string]any{"reason": "test"}, nil); r.Status != 200 {
		t.Fatalf("deactivate: %d", r.Status)
	}

	for name, who := range map[string]*apitest.Caller{"a principal with role:write and without usage:read": k, "a key whose scope list lacks usage:read": scoped} {
		t.Run(name, func(t *testing.T) {
			before := w.api.SnapshotTenant(t, w.tenant.ID)
			escalation(t, "createRole with usage:read", who.Do(t, "POST", w.tp("/roles"), map[string]any{"name": "grab", "permissions": []string{"role:read", "usage:read"}}, nil))
			escalation(t, "updateRole adding usage:read", who.Do(t, "PATCH", w.tp("/roles/plain"), map[string]any{"permissions": []string{"role:read", "usage:read"}}, nil))
			escalation(t, "updateRole taking usage:read out", who.Do(t, "PATCH", w.tp("/roles/has-usage"), map[string]any{"permissions": []string{"member:read"}}, nil))
			escalation(t, "updateRole of a role it does not fully hold, the description only", who.Do(t, "PATCH", w.tp("/roles/has-usage"), map[string]any{"description": "mine now"}, nil))
			escalation(t, "deactivateRole of a role with usage:read", who.Do(t, "POST", w.tp("/roles/has-usage/deactivate"), map[string]any{"reason": "test"}, nil))
			escalation(t, "activateRole of a role with usage:read", who.Do(t, "POST", w.tp("/roles/was-active/activate"), map[string]any{"reason": "test"}, nil))
			escalation(t, "deleteRole of a role with usage:read", who.Do(t, "DELETE", w.tp("/roles/has-usage"), nil, nil))
			if after := w.api.SnapshotTenant(t, w.tenant.ID); after != before {
				t.Error("a refused grant changed a row of the tenant (and a refused grant writes no audit row)")
			}
			if r := who.Do(t, "GET", w.tp("/roles/grab"), nil, nil); r.Status != 404 {
				t.Errorf("the refused role exists: %d", r.Status)
			}
			// What it holds, it may.
			if r := who.Do(t, "POST", w.tp("/roles"), map[string]any{"name": apitest.UniqueName("fine"), "permissions": []string{"role:read"}}, nil); r.Status != 201 {
				t.Errorf("createRole inside its own permissions: %d %s", r.Status, r.Body)
			}
			if r := who.Do(t, "PATCH", w.tp("/roles/plain"), map[string]any{"description": "ok " + name}, nil); r.Status != 200 {
				t.Errorf("updateRole of a role inside its permissions: %d %s", r.Status, r.Body)
			}
		})
	}
	t.Run("the operations that a caller may use on a role it holds completely", func(t *testing.T) {
		if r := k.Do(t, "POST", w.tp("/roles/plain/deactivate"), map[string]any{"reason": "test"}, nil); r.Status != 200 {
			t.Errorf("deactivateRole: %d %s", r.Status, r.Body)
		}
		if r := k.Do(t, "POST", w.tp("/roles/plain/activate"), map[string]any{"reason": "test"}, nil); r.Status != 200 {
			t.Errorf("activateRole: %d %s", r.Status, r.Body)
		}
		if r := k.Do(t, "DELETE", w.tp("/roles/plain"), nil, nil); r.Status != 204 {
			t.Errorf("deleteRole: %d %s", r.Status, r.Body)
		}
	})
}

// grantWorld is a tenant with a weaker caller (a key with a custom role that lets it manage
// identities, and nothing that reads usage) and the stronger things it must not touch.
type grantWorld struct {
	*rw
	k            *apitest.Caller
	strongRole   string
	spareRole    string // as strong, and held by nobody: a role to deactivate, activate and delete
	weakRole     string
	strongSA     string
	strongMember string
	strongKey    string // the key ID of a key of strongSA
	issuerKey    *apitest.Caller
}

// issuerManager is a weaker caller that may change the tenant's token issuers and holds nothing
// that reads usage; it is made on first use.
func (g *grantWorld) issuerManager(t *testing.T) *apitest.Caller {
	t.Helper()
	if g.issuerKey == nil {
		g.role(t, "issuer-manager", authz.RoleRead, authz.TokenIssuerRead, authz.TokenIssuerWrite)
		_, key := g.sa(t, "issuer-manager")
		g.issuerKey = &apitest.Caller{API: g.api, Key: key, Name: "issuer-manager"}
	}
	return g.issuerKey
}

func newGrantWorld(t *testing.T) *grantWorld {
	t.Helper()
	w := newRW(t)
	g := &grantWorld{rw: w, strongRole: "strong", spareRole: "strong-spare", weakRole: "weak"}
	w.role(t, "identity-manager", authz.RoleRead, authz.RoleWrite, authz.RoleDelete, authz.ServiceAccountRead, authz.ServiceAccountWrite, authz.MemberRead, authz.MemberWrite,
		authz.VirtualKeyRead, authz.VirtualKeyWrite, authz.VirtualKeyRevoke, authz.SubjectMappingRead, authz.SubjectMappingWrite, authz.SubjectMappingDelete)
	w.role(t, g.strongRole, authz.RoleRead, authz.UsageRead)
	w.role(t, g.spareRole, authz.RoleRead, authz.UsageRead)
	w.role(t, g.weakRole, authz.RoleRead)
	w.role(t, "weak-too", authz.MemberRead)
	_, key := w.sa(t, "identity-manager")
	g.k = &apitest.Caller{API: w.api, Key: key, Name: "identity-manager"}
	g.strongSA, _ = w.sa(t, g.strongRole)
	kr := decode[adminapi.VirtualKeyCreated](t, w.owner.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": g.strongSA}, nil), 201)
	g.strongKey = kr.Key.ID
	g.strongMember, _ = w.member(t, g.strongRole)
	return g
}

// TestNoEscalationAssignments is T-863, the exit condition of the rule: nobody hands out, or takes
// over, a role with a permission it does not hold.
func TestNoEscalationAssignments(t *testing.T) {
	g := newGrantWorld(t)
	w := g.rw
	k := g.k

	t.Run("createServiceAccount", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.tenant.ID)
		for _, role := range []string{g.strongRole, "owner", "tenant-admin", "usage-viewer", "developer", "inference"} {
			escalation(t, "createServiceAccount with "+role, k.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{role}}, nil))
		}
		escalation(t, "createServiceAccount with a weak role and a strong one", k.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{g.weakRole, g.strongRole}}, nil))
		if after := w.api.SnapshotTenant(t, w.tenant.ID); after != before {
			t.Error("a refused creation changed the tenant")
		}
		if r := k.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{g.weakRole, "weak-too"}}, nil); r.Status != 201 {
			t.Errorf("createServiceAccount inside its permissions: %d %s", r.Status, r.Body)
		}
		invalidOn(t, "an unknown role", k.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"ghost"}}, nil), "roles")
	})
	t.Run("updateServiceAccount: adding, removing, and a role that stays", func(t *testing.T) {
		weakOnly, _ := w.sa(t, g.weakRole)
		both, _ := w.sa(t, g.weakRole, g.strongRole)
		path := func(id string) string { return w.tp("/service-accounts/" + id) }
		before := w.api.SnapshotTenant(t, w.tenant.ID)
		escalation(t, "adding a role it lacks", k.Do(t, "PATCH", path(weakOnly), map[string]any{"roles": []string{g.weakRole, g.strongRole}}, nil))
		escalation(t, "removing a role it lacks", k.Do(t, "PATCH", path(both), map[string]any{"roles": []string{g.weakRole}}, nil))
		escalation(t, "swapping a role it lacks for another", k.Do(t, "PATCH", path(both), map[string]any{"roles": []string{g.weakRole, "owner"}}, nil))
		if after := w.api.SnapshotTenant(t, w.tenant.ID); after != before {
			t.Error("a refused update changed the tenant")
		}
		// A role that stays is not checked, so the caller may change the rest of the account.
		if r := k.Do(t, "PATCH", path(both), map[string]any{"description": "renamed by the identity manager", "roles": []string{g.weakRole, g.strongRole}}, nil); r.Status != 200 {
			t.Errorf("updating around a role that stays: %d %s", r.Status, r.Body)
		}
		if r := k.Do(t, "PATCH", path(both), map[string]any{"roles": []string{g.weakRole, g.strongRole, "weak-too"}}, nil); r.Status != 200 {
			t.Errorf("adding a role it holds while another stays: %d %s", r.Status, r.Body)
		}
		if got := w.saRoles(t, both); !reflect.DeepEqual(got, sorted([]string{g.weakRole, g.strongRole, "weak-too"})) {
			t.Errorf("the roles after the update: %v", got)
		}
		invalidOn(t, "an unknown role", k.Do(t, "PATCH", path(weakOnly), map[string]any{"roles": []string{"ghost"}}, nil), "roles")
	})
	t.Run("createMember, updateMember, deleteMember", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.tenant.ID)
		escalation(t, "createMember with a role it lacks", k.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{g.strongRole}}, nil))
		escalation(t, "createMember with owner", k.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{"owner"}}, nil))
		escalation(t, "updateMember adding a role it lacks", k.Do(t, "PATCH", w.tp("/members/"+g.strongMember), map[string]any{"roles": []string{g.strongRole, g.weakRole, "owner"}}, nil))
		escalation(t, "updateMember removing a role it lacks", k.Do(t, "PATCH", w.tp("/members/"+g.strongMember), map[string]any{"roles": []string{g.weakRole}}, nil))
		escalation(t, "deleteMember of a stronger member", k.Do(t, "DELETE", w.tp("/members/"+g.strongMember), nil, nil))
		if after := w.api.SnapshotTenant(t, w.tenant.ID); after != before {
			t.Error("a refused change of a member changed the tenant")
		}
		invalidOn(t, "an unknown role on a member", k.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{"ghost"}}, nil), "roles")
		// Inside its permissions, and around a role that stays.
		if r := k.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{g.weakRole}}, nil); r.Status != 201 {
			t.Errorf("createMember inside its permissions: %d %s", r.Status, r.Body)
		}
		if r := k.Do(t, "PATCH", w.tp("/members/"+g.strongMember), map[string]any{"display_name": "Renamed", "roles": []string{g.strongRole}}, nil); r.Status != 200 {
			t.Errorf("updateMember with the same roles: %d %s", r.Status, r.Body)
		}
		if r := k.Do(t, "PATCH", w.tp("/members/"+g.strongMember), map[string]any{"roles": []string{g.strongRole, "weak-too"}}, nil); r.Status != 200 {
			t.Errorf("updateMember adding a role it holds while another stays: %d %s", r.Status, r.Body)
		}
		weakMember, _ := w.member(t, g.weakRole)
		if r := k.Do(t, "DELETE", w.tp("/members/"+weakMember), nil, nil); r.Status != 200 {
			t.Errorf("deleteMember of a weaker member: %d %s", r.Status, r.Body)
		}
	})
	t.Run("a tenant administrator cannot assign owner or a custom role with support_action:end", func(t *testing.T) {
		admin := w.api.AsRole(t, w.tenant.ID, authz.RoleTenantAdmin)
		w.role(t, "ender", authz.SupportActionEnd, authz.RoleRead)
		saID, _ := w.sa(t, "inference")
		for what, r := range map[string]*apitest.Response{
			"a service account with owner":     admin.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"owner"}}, nil),
			"a member with owner":              admin.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{"owner"}}, nil),
			"owner added to a service account": admin.Do(t, "PATCH", w.tp("/service-accounts/"+saID), map[string]any{"roles": []string{"inference", "owner"}}, nil),
			"a service account with the ender": admin.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"ender"}}, nil),
			"a member with the ender":          admin.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{"ender"}}, nil),
			"a role with support_action:end":   admin.Do(t, "POST", w.tp("/roles"), map[string]any{"name": "ender-too", "permissions": []string{authz.SupportActionEnd}}, nil),
			"the ender taken from its holders": admin.Do(t, "DELETE", w.tp("/roles/ender"), nil, nil),
		} {
			escalation(t, what, r)
		}
		if r := admin.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"tenant-admin"}}, nil); r.Status != 201 {
			t.Errorf("a tenant administrator makes another: %d %s", r.Status, r.Body)
		}
	})
	t.Run("the platform operations: nobody holds less than the one role that may use them", func(t *testing.T) {
		// createUser, updateUser and createPlatformKey need a permission that only platform-admin
		// holds, and platform-admin holds every permission of every platform role: no principal can
		// be weaker than the target, so the guard cannot refuse here today. The test says why, so
		// that a platform role added later turns it into a driver (see TestGrantSurfaceIsClosed).
		for _, op := range []string{"createUser", "updateUser", "createPlatformKey", "resetUserPassword"} {
			for _, o := range adminapi.Operations {
				if o.ID != op {
					continue
				}
				if holders := rolesHolding(o.Permission, authz.ScopePlatform); !reflect.DeepEqual(holders, []string{authz.RolePlatformAdmin}) {
					t.Errorf("%s needs %s, held by %v: if another platform role holds it, drive the guard of %s with it", op, o.Permission, holders, op)
				}
			}
		}
		admin := w.api.PlatformCaller()
		plat := func(body map[string]any) *apitest.Response { return admin.Do(t, "POST", "/v1/users", body, nil) }
		for _, role := range []string{authz.RolePlatformAdmin, authz.RolePlatformAuditor} {
			if r := plat(map[string]any{"sign_in": "local", "username": apitest.UniqueName("p"), "platform_roles": []string{role}}); r.Status != 201 {
				t.Errorf("createUser with %s as platform-admin: %d %s", role, r.Status, r.Body)
			}
			if r := admin.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "k", "role": role}, nil); r.Status != 201 {
				t.Errorf("createPlatformKey with %s as platform-admin: %d %s", role, r.Status, r.Body)
			}
		}
		invalidOn(t, "a tenant role for a platform user", plat(map[string]any{"sign_in": "local", "username": apitest.UniqueName("p"), "platform_roles": []string{"owner"}}), "platform_roles")
		invalidOn(t, "an unknown platform role", plat(map[string]any{"sign_in": "local", "username": apitest.UniqueName("p"), "platform_roles": []string{"ghost"}}), "platform_roles")
		invalidOn(t, "a tenant role for a platform key", admin.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "k", "role": "owner"}, nil), "role")
		auditorKey, _ := w.api.PlatformAs(t, authz.RolePlatformAuditor)
		for what, r := range map[string]*apitest.Response{
			"createUser as a platform auditor":        auditorKey.Do(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": apitest.UniqueName("p")}, nil),
			"createPlatformKey as a platform auditor": auditorKey.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "k"}, nil),
		} {
			if !apitest.Refused(r) {
				t.Errorf("%s: %d %s, want 403 forbidden (the permission, not the guard)", what, r.Status, r.Body)
			}
		}
	})
}

// rolesHolding returns the built-in roles of the scope that hold the permission.
func rolesHolding(permission string, scope authz.Scope) []string {
	var out []string
	for _, r := range authz.Roles() {
		if r.Scope != scope {
			continue
		}
		for _, p := range r.Permissions {
			if p == permission {
				out = append(out, r.Name)
			}
		}
	}
	return out
}

// TestNoEscalationCredentials is T-864.
func TestNoEscalationCredentials(t *testing.T) {
	g := newGrantWorld(t)
	w := g.rw
	k := g.k
	t.Run("createVirtualKey", func(t *testing.T) {
		escalation(t, "a key for a stronger service account", k.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": g.strongSA}, nil))
		// The first service account of the tenant is an owner: no key for it either.
		sas := decode[adminapi.ServiceAccountList](t, w.owner.Do(t, "GET", w.tp("/service-accounts?limit=200"), nil, nil), 200)
		for _, sa := range sas.Items {
			if len(sa.Roles) == 1 && sa.Roles[0] == "owner" {
				escalation(t, "a key for the owner's account", k.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": sa.ID}, nil))
			}
		}
		// A scope list inside the caller's permissions is allowed: the key can do what the caller can.
		if r := k.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": g.strongSA, "scopes": []string{authz.RoleRead}}, nil); r.Status != 201 {
			t.Errorf("a key for a stronger account, limited to a permission the caller holds: %d %s", r.Status, r.Body)
		}
		escalation(t, "a scope list that holds usage:read", k.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": g.strongSA, "scopes": []string{authz.RoleRead, authz.UsageRead}}, nil))
		weakSA, _ := w.sa(t, g.weakRole)
		if r := k.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": weakSA}, nil); r.Status != 201 {
			t.Errorf("a key for a weaker account: %d %s", r.Status, r.Body)
		}
	})
	t.Run("createSubjectMapping", func(t *testing.T) {
		issuer := w.api.SeedTokenIssuer(t, w.tenant.ID)
		body := func(sa string) map[string]any {
			return map[string]any{"issuer": map[string]any{"scope": "tenant", "id": issuer}, "subject": apitest.UniqueName("subject"), "service_account_id": sa}
		}
		escalation(t, "a subject mapped to a stronger account", k.Do(t, "POST", w.tp("/subject-mappings"), body(g.strongSA), nil))
		weakSA, _ := w.sa(t, g.weakRole)
		if r := k.Do(t, "POST", w.tp("/subject-mappings"), body(weakSA), nil); r.Status != 201 {
			t.Errorf("a subject mapped to a weaker account: %d %s", r.Status, r.Body)
		}
	})
	t.Run("resetMemberPassword and resetUserPassword", func(t *testing.T) {
		reason := map[string]any{"reason": "locked out"}
		escalation(t, "the password of a stronger member", k.Do(t, "POST", w.tp("/members/"+g.strongMember+"/reset-password"), reason, nil))
		weakMember, _ := w.member(t, g.weakRole)
		if r := k.Do(t, "POST", w.tp("/members/"+weakMember+"/reset-password"), reason, nil); r.Status != 200 {
			t.Errorf("the password of a weaker member: %d %s", r.Status, r.Body)
		}
		// A platform user: the platform administrator holds every platform permission, so it may reset the password of both roles.
		for _, role := range []string{authz.RolePlatformAdmin, authz.RolePlatformAuditor} {
			p := w.d.PlatformUser(t, apitest.UniqueName("target"), []string{role})
			if r := w.platform(t, "POST", "/v1/users/"+p.ID+"/reset-password", reason); r.Status != 200 {
				t.Errorf("the platform administrator resetting the password of a %s: %d %s", role, r.Status, r.Body)
			}
		}
		// A platform user that is a member of a tenant: the new password would give the caller the
		// member's permissions in the tenant, which the platform does not hold (section 5.6).
		for _, role := range []string{"owner", g.weakRole} {
			p := w.d.PlatformUser(t, apitest.UniqueName("member"), nil)
			w.d.Member(t, w.tid, p.ID, []string{role})
			escalation(t, "the password of a platform user that is a "+role+" of a tenant", w.platform(t, "POST", "/v1/users/"+p.ID+"/reset-password", reason))
			for _, r := range w.d.PlatformAuditRows(t) {
				if r.ResourceID == p.ID && r.Action == "user.password_reset" {
					t.Errorf("a refused reset wrote the row %s", r.Action)
				}
			}
		}
		// A membership whose only role is deactivated gives nothing but the self permissions.
		w.role(t, "dormant", authz.RoleRead)
		if r := w.owner.Do(t, "POST", w.tp("/roles/dormant/deactivate"), map[string]any{"reason": "test"}, nil); r.Status != 200 {
			t.Fatalf("deactivate: %d", r.Status)
		}
		p := w.d.PlatformUser(t, apitest.UniqueName("dormant"), nil)
		w.d.Member(t, w.tid, p.ID, []string{"dormant"})
		if r := w.platform(t, "POST", "/v1/users/"+p.ID+"/reset-password", reason); r.Status != 200 {
			t.Errorf("the password of a member that holds no permission of the tenant: %d %s", r.Status, r.Body)
		}
	})
	t.Run("updateTokenIssuer", func(t *testing.T) {
		im := g.issuerManager(t)
		issuer := w.api.SeedTokenIssuer(t, w.tenant.ID)
		w.api.SeedSubjectMapping(t, w.tenant.ID, issuer, g.strongSA)
		path := w.tp("/token-issuers/" + issuer)
		// Who controls the issuer's trust controls the tokens that act as the mapped accounts.
		for name, body := range map[string]map[string]any{
			"issuer":          {"issuer": "https://attacker.example.test"},
			"audience":        {"audience": "another-audience"},
			"jwks_uri":        {"jwks_uri": "https://attacker.example.test/jwks"},
			"subject_claim":   {"subject_claim": "azp"},
			"required_claims": {"required_claims": map[string]any{"env": "attacker"}},
			"algorithms":      {"algorithms": []string{"ES256"}},
		} {
			escalation(t, "changing "+name+" of an issuer mapped to a stronger account", im.Do(t, "PATCH", path, body, nil))
		}
		// Containment and labels are free.
		if r := im.Do(t, "PATCH", path, map[string]any{"description": "relabelled"}, nil); r.Status != 200 {
			t.Errorf("the description of an issuer mapped to a stronger account: %d %s", r.Status, r.Body)
		}
		if r := im.Do(t, "PATCH", path, map[string]any{"enabled": false}, nil); r.Status != 200 {
			t.Errorf("disabling an issuer mapped to a stronger account: %d %s", r.Status, r.Body)
		}
		escalation(t, "enabling an issuer mapped to a stronger account", im.Do(t, "PATCH", path, map[string]any{"enabled": true}, nil))
		// An issuer mapped only to accounts inside the caller's permissions is the caller's to change.
		weakSA, _ := w.sa(t, g.weakRole)
		mine := w.api.SeedTokenIssuer(t, w.tenant.ID)
		w.api.SeedSubjectMapping(t, w.tenant.ID, mine, weakSA)
		if r := im.Do(t, "PATCH", w.tp("/token-issuers/"+mine), map[string]any{"jwks_uri": "https://idp.example.test/jwks", "enabled": false}, nil); r.Status != 200 {
			t.Errorf("an issuer mapped to a weaker account: %d %s", r.Status, r.Body)
		}
		if r := im.Do(t, "PATCH", w.tp("/token-issuers/"+mine), map[string]any{"enabled": true}, nil); r.Status != 200 {
			t.Errorf("enabling an issuer mapped to a weaker account: %d %s", r.Status, r.Body)
		}
	})
	t.Run("the containment operations work on a stronger identity", func(t *testing.T) {
		issuer := w.api.SeedTokenIssuer(t, w.tenant.ID)
		mapping := w.api.SeedSubjectMapping(t, w.tenant.ID, issuer, g.strongSA)
		for what, r := range map[string]*apitest.Response{
			"revokeVirtualKey of a stronger account's key": k.Do(t, "POST", w.tp("/virtual-keys/"+g.strongKey+"/revoke"), map[string]any{}, nil),
			"disableServiceAccount":                        k.Do(t, "POST", w.tp("/service-accounts/"+g.strongSA+"/disable"), map[string]any{}, nil),
			"enableServiceAccount":                         k.Do(t, "POST", w.tp("/service-accounts/"+g.strongSA+"/enable"), map[string]any{}, nil),
			"disableMember":                                k.Do(t, "POST", w.tp("/members/"+g.strongMember+"/disable"), map[string]any{}, nil),
			"enableMember":                                 k.Do(t, "POST", w.tp("/members/"+g.strongMember+"/enable"), map[string]any{}, nil),
			"unlockMember":                                 k.Do(t, "POST", w.tp("/members/"+g.strongMember+"/unlock"), map[string]any{}, nil),
			"revokeMemberSessions":                         k.Do(t, "POST", w.tp("/members/"+g.strongMember+"/revoke-sessions"), map[string]any{}, nil),
			"deleteSubjectMapping to a stronger account":   k.Do(t, "DELETE", w.tp("/subject-mappings/"+mapping), nil, nil),
		} {
			if r.Status/100 != 2 {
				t.Errorf("%s: %d %s, want success: containment is not covered by the rule", what, r.Status, r.Body)
			}
		}
	})
}

type kind string

const (
	grantConfers  kind = "confers"
	grantRemoves  kind = "removes"
	grantContains kind = "contains"
	grantNone     kind = "none"
)

// TestGrantSurfaceIsClosed is T-865: every operation of the document is classified by what it does to
// somebody's power, and each one that confers or removes refuses a weaker caller.
func TestGrantSurfaceIsClosed(t *testing.T) {
	g := newGrantWorld(t)
	w := g.rw
	k := g.k
	// A driver sets up a weaker caller and a stronger target and makes the call. A nil driver
	// is for the operations that no weaker principal can reach (see the last check).
	drivers := map[string]func() *apitest.Response{
		"createRole": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/roles"), map[string]any{"name": apitest.UniqueName("grab"), "permissions": []string{authz.UsageRead}}, nil)
		},
		"updateRole": func() *apitest.Response {
			return k.Do(t, "PATCH", w.tp("/roles/"+g.weakRole), map[string]any{"permissions": []string{authz.RoleRead, authz.UsageRead}}, nil)
		},
		"deactivateRole": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/roles/"+g.spareRole+"/deactivate"), map[string]any{"reason": "t"}, nil)
		},
		"activateRole": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/roles/"+g.spareRole+"/activate"), map[string]any{"reason": "t"}, nil)
		},
		"deleteRole": func() *apitest.Response { return k.Do(t, "DELETE", w.tp("/roles/"+g.spareRole), nil, nil) },
		"createServiceAccount": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{g.strongRole}}, nil)
		},
		"updateServiceAccount": func() *apitest.Response {
			id, _ := w.sa(t, g.weakRole)
			return k.Do(t, "PATCH", w.tp("/service-accounts/"+id), map[string]any{"roles": []string{g.weakRole, g.strongRole}}, nil)
		},
		"createMember": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{g.strongRole}}, nil)
		},
		"updateMember": func() *apitest.Response {
			id, _ := w.member(t, g.weakRole)
			return k.Do(t, "PATCH", w.tp("/members/"+id), map[string]any{"roles": []string{g.weakRole, g.strongRole}}, nil)
		},
		"deleteMember": func() *apitest.Response { return k.Do(t, "DELETE", w.tp("/members/"+g.strongMember), nil, nil) },
		"createVirtualKey": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": g.strongSA}, nil)
		},
		"createSubjectMapping": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/subject-mappings"), map[string]any{"issuer": map[string]any{"scope": "tenant", "id": w.api.SeedTokenIssuer(t, w.tenant.ID)},
				"subject": apitest.UniqueName("s"), "service_account_id": g.strongSA}, nil)
		},
		"resetMemberPassword": func() *apitest.Response {
			return k.Do(t, "POST", w.tp("/members/"+g.strongMember+"/reset-password"), map[string]any{"reason": "t"}, nil)
		},
		"updateTokenIssuer": func() *apitest.Response {
			issuer := w.api.SeedTokenIssuer(t, w.tenant.ID)
			w.api.SeedSubjectMapping(t, w.tenant.ID, issuer, g.strongSA)
			return g.issuerManager(t).Do(t, "PATCH", w.tp("/token-issuers/"+issuer), map[string]any{"jwks_uri": "https://attacker.example.test/jwks"}, nil)
		},
		// The platform operations: nobody is weaker than the one role that may use them (checked below).
		"createUser": nil, "updateUser": nil, "createPlatformKey": nil, "resetUserPassword": nil,
	}
	classes := grantClassification()

	seen := map[string]bool{}
	for _, op := range adminapi.Operations {
		seen[op.ID] = true
		c, ok := classes[op.ID]
		if !ok {
			t.Errorf("the operation %s is not in the classification of section 5.4: say whether it confers, removes, contains or none", op.ID)
			continue
		}
		if c != grantConfers && c != grantRemoves {
			if _, has := drivers[op.ID]; has {
				t.Errorf("%s is classified %s and has a driver", op.ID, c)
			}
			continue
		}
		drive, has := drivers[op.ID]
		if !has {
			t.Errorf("%s is classified %s and has no driver", op.ID, c)
			continue
		}
		if drive == nil {
			// No weaker principal can use it: every role that holds its permission holds every permission of every platform role.
			var all []string
			for _, r := range authz.Roles() {
				if r.Scope == authz.ScopePlatform {
					all = append(all, r.Permissions...)
				}
			}
			holders := rolesHolding(op.Permission, authz.ScopePlatform)
			for _, h := range holders {
				role, _ := authz.RoleByName(h)
				have := map[string]bool{}
				for _, p := range role.Permissions {
					have[p] = true
				}
				for _, p := range all {
					if !have[p] {
						t.Errorf("%s: the role %s may use it and lacks %s, which another platform role holds: drive the guard with a weaker principal", op.ID, h, p)
					}
				}
			}
			continue
		}
		if r := drive(); r.Status != 403 || r.Problem() != "escalation_refused" {
			t.Errorf("%s (%s) driven by a weaker principal: %d %s, want 403 escalation_refused", op.ID, c, r.Status, r.Body)
		}
	}
	for id := range classes {
		if !seen[id] {
			t.Errorf("the classification names %s, which is not an operation of the document", id)
		}
	}
	for id := range drivers {
		if !seen[id] {
			t.Errorf("a driver for %s, which is not an operation of the document", id)
		}
	}
}

// TestSeparationOfDuties is T-895.
func TestSeparationOfDuties(t *testing.T) {
	w := newRW(t)
	identityPerms := []string{authz.ServiceAccountRead, authz.ServiceAccountWrite, authz.VirtualKeyRead, authz.VirtualKeyWrite, authz.VirtualKeyRevoke, authz.MemberRead, authz.MemberWrite, authz.RoleRead}
	w.role(t, "identity-only", identityPerms...)
	actors := []struct {
		name  string
		perms []string
		who   apitest.Doer
	}{
		{"credential-manager", mustRole(t, authz.RoleCredentialManager), w.api.AsRole(t, w.tenant.ID, authz.RoleCredentialManager)},
		{"identity-only", append([]string{authz.IdentityRead}, identityPerms...), w.api.AsRole(t, w.tenant.ID, "identity-only")},
		{"usage-viewer", mustRole(t, authz.RoleUsageViewer), w.api.AsRole(t, w.tenant.ID, authz.RoleUsageViewer)},
	}
	p := probeParams(t, w.api, w.tenant.ID)
	held := func(perms []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range perms {
			m[x] = true
		}
		return m
	}
	for i, a := range actors {
		own := held(a.perms)
		others := map[string]bool{}
		for j, b := range actors {
			if i != j {
				for _, x := range b.perms {
					others[x] = true
				}
			}
		}
		did, refused := 0, 0
		for _, op := range adminapi.Operations {
			perm, ok := authz.Lookup(op.Permission)
			if !ok || perm.Scope != authz.ScopeTenant {
				continue
			}
			r := apitest.Probe(t, a.who, op, p)
			switch {
			case own[op.Permission]:
				if apitest.Refused(r) {
					t.Errorf("%s is refused its own operation %s: %d %s", a.name, op.ID, r.Status, r.Body)
				}
				did++
			case others[op.Permission]:
				if !apitest.Refused(r) {
					t.Errorf("%s is not refused %s (permission %s, held by another of the three): %d %s", a.name, op.ID, op.Permission, r.Status, r.Body)
				}
				refused++
			}
		}
		if did < 4 || refused < 4 {
			t.Errorf("%s: %d own operations and %d refused ones were checked: the test proves nothing", a.name, did, refused)
		}
	}
	// And each does something real.
	cm, idm, uv := actors[0].who, actors[1].who, actors[2].who
	if r := cm.Do(t, http.MethodPost, w.tp("/credentials"), map[string]any{"provider": "openai", "label": apitest.UniqueName("sep"), "secret": "sk-" + apitest.Canary("secret")}, nil); r.Status != 201 && r.Status != 502 && r.Status != 422 {
		t.Errorf("the credential manager registers a credential: %d %s", r.Status, r.Body)
	}
	if r := idm.Do(t, http.MethodPost, w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sep"), "roles": []string{"identity-only"}}, nil); r.Status != 201 {
		t.Errorf("the identity manager makes a service account: %d %s", r.Status, r.Body)
	}
	if r := uv.Do(t, http.MethodGet, w.tp("/usage-records"), nil, nil); r.Status != 200 {
		t.Errorf("the usage viewer lists usage: %d %s", r.Status, r.Body)
	}
}

func mustRole(t *testing.T, name string) []string {
	t.Helper()
	r, ok := authz.RoleByName(name)
	if !ok {
		t.Fatalf("no role %s", name)
	}
	return r.Permissions
}
