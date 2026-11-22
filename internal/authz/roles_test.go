package authz

// The tests of sprint 4.3 for the catalogue, the ten built-in roles, the permissions that come
// from data (custom roles, support actions) and the rule against escalation: T-846 to T-851.

import (
	"bufio"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
)

func names(perms []Permission) []string {
	var out []string
	for _, p := range perms {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func resourceOf(name string) string { return name[:strings.IndexByte(name, ':')] }

// TestCatalogueAdditions is T-846.
func TestCatalogueAdditions(t *testing.T) {
	added := []struct {
		name  string
		scope Scope
	}{
		{"role:read", ScopeTenant}, {"role:write", ScopeTenant}, {"role:delete", ScopeTenant},
		{"support_action:read", ScopeTenant}, {"support_action:end", ScopeTenant}, {"tenant_setting:write", ScopeTenant},
		{"tenant_support:open", ScopePlatform}, {"tenant_support:read", ScopePlatform},
	}
	for _, a := range added {
		p, ok := Lookup(a.name)
		if !ok || p.Scope != a.scope || p.Description == "" {
			t.Errorf("%s: %+v, %v, want scope %s and a description", a.name, p, ok, a.scope)
		}
	}
	count := map[Scope]int{}
	var inTenant, support []Permission
	for _, p := range Catalogue() {
		count[p.Scope]++
		if p.InTenant {
			inTenant = append(inTenant, p)
		}
		if p.Support {
			support = append(support, p)
		}
	}
	if count[ScopeTenant] != 34 || count[ScopePlatform] != 21 || count[ScopeSelf] != 2 || len(Catalogue()) != 57 {
		t.Errorf("scopes %v (%d permissions), want 34 tenant, 21 platform, 2 self (57)", count, len(Catalogue()))
	}
	wantInTenant := []string{"tenant_credential:read", "tenant_credential:revoke", "tenant_support:open", "tenant_support:read"}
	if got := names(inTenant); !reflect.DeepEqual(got, wantInTenant) {
		t.Errorf("InTenant is %v, want %v", got, wantInTenant)
	}
	wantSupport := []string{"member:read", "member:write", "role:delete", "role:read", "role:write", "service_account:read", "service_account:write",
		"support_action:read", "virtual_key:read", "virtual_key:revoke", "virtual_key:write"}
	if got := names(support); !reflect.DeepEqual(got, wantSupport) {
		t.Errorf("Support is %v, want %v", got, wantSupport)
	}
	if got := SupportPermissions(); !reflect.DeepEqual(got, wantSupport) {
		t.Errorf("SupportPermissions() is %v, want %v", got, wantSupport)
	}
	for _, p := range inTenant {
		if p.Scope != ScopePlatform {
			t.Errorf("%s has InTenant and scope %s", p.Name, p.Scope)
		}
	}
	for _, p := range support {
		if p.Scope != ScopeTenant {
			t.Errorf("%s has Support and scope %s", p.Name, p.Scope)
		}
	}
	// No permission that reads usage, prices, limits, secrets, models or the tenant's settings can
	// be carried by a support action, and no platform permission names such a resource but the two
	// credential-oversight permissions of AZ-08.
	forbidden := map[string]bool{"usage": true, "price_list": true, "limit": true, "credential": true, "model": true,
		"token_issuer": true, "subject_mapping": true, "tenant_setting": true}
	for _, p := range Catalogue() {
		if (p.Support || p.Scope == ScopePlatform) && forbidden[resourceOf(p.Name)] {
			t.Errorf("%s has the resource %s, which a support action or the platform must never reach", p.Name, resourceOf(p.Name))
		}
	}
	if got := TenantPermissions(); len(got) != 34 || !sort.StringsAreSorted(got) {
		t.Errorf("TenantPermissions: %d names, sorted %v", len(got), sort.StringsAreSorted(got))
	}
}

type goldenLine struct {
	scope       Scope
	members     bool
	permissions []string
}

// readGolden parses the golden file without any helper of the package under test.
func readGolden(t *testing.T) (order []string, lines map[string]goldenLine) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "roles.golden"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines = map[string]goldenLine{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) != 4 || (cols[1] != "tenant" && cols[1] != "platform") || (cols[2] != "yes" && cols[2] != "no") {
			t.Fatalf("roles.golden: malformed line %q", sc.Text())
		}
		perms := strings.Fields(cols[3])
		if !sort.StringsAreSorted(perms) {
			t.Errorf("roles.golden: the permissions of %s are not sorted", cols[0])
		}
		if _, dup := lines[cols[0]]; dup {
			t.Fatalf("roles.golden: %s twice", cols[0])
		}
		order = append(order, cols[0])
		lines[cols[0]] = goldenLine{Scope(cols[1]), cols[2] == "yes", perms}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return order, lines
}

// diff returns what is in a and not in b.
func diff(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

// TestBuiltinRolesGolden is T-847.
func TestBuiltinRolesGolden(t *testing.T) {
	order, golden := readGolden(t)
	roles := Roles()
	if len(roles) != 10 || len(order) != 10 {
		t.Fatalf("%d roles and %d golden lines, want ten of each", len(roles), len(order))
	}
	for i, r := range roles {
		if r.Name != order[i] {
			t.Errorf("role %d is %s, the golden file has %s there", i, r.Name, order[i])
		}
		g, ok := golden[r.Name]
		if !ok {
			t.Errorf("role %s is not in the golden file", r.Name)
			continue
		}
		if r.Scope != g.scope || r.Members != g.members {
			t.Errorf("%s: scope %s members %v, the golden file says %s %v", r.Name, r.Scope, r.Members, g.scope, g.members)
		}
		if extra := diff(r.Permissions, g.permissions); len(extra) > 0 {
			t.Errorf("%s holds %v, which the golden file does not give it", r.Name, extra)
		}
		if missing := diff(g.permissions, r.Permissions); len(missing) > 0 {
			t.Errorf("%s lacks %v, which the golden file gives it", r.Name, missing)
		}
	}
	var members []string
	for _, r := range roles {
		if !r.Members {
			members = append(members, r.Name)
		}
	}
	if !reflect.DeepEqual(members, []string{RoleInference}) {
		t.Errorf("the roles a person cannot hold are %v, want only inference", members)
	}

	// By construction: the golden file is checked against the catalogue, not against itself.
	owner := append([]string{IdentityRead}, TenantPermissions()...)
	sort.Strings(owner)
	if !reflect.DeepEqual(golden[RoleOwner].permissions, owner) {
		t.Errorf("owner (golden) is not every tenant permission and identity:read: %v", diff(owner, golden[RoleOwner].permissions))
	}
	var admin []string
	for _, p := range golden[RoleOwner].permissions {
		if p != SupportActionEnd {
			admin = append(admin, p)
		}
	}
	if !reflect.DeepEqual(golden[RoleTenantAdmin].permissions, admin) {
		t.Errorf("tenant-admin (golden) is not owner without support_action:end")
	}
	readOnly := []string{IdentityRead}
	for _, p := range TenantPermissions() {
		if strings.HasSuffix(p, ":read") {
			readOnly = append(readOnly, p)
		}
	}
	sort.Strings(readOnly)
	if !reflect.DeepEqual(golden[RoleReadOnly].permissions, readOnly) || len(readOnly) != 13 {
		t.Errorf("read-only (golden) is %v, want every tenant permission that ends in :read and identity:read (%v)", golden[RoleReadOnly].permissions, readOnly)
	}
	for _, g := range []string{RoleOwner, RoleTenantAdmin, RoleReadOnly} {
		if r, _ := RoleByName(g); !reflect.DeepEqual(r.Permissions, golden[g].permissions) {
			t.Errorf("%s in code is %v", g, r.Permissions)
		}
	}
	// No platform role holds a tenant permission and the reverse.
	for name, g := range golden {
		for _, p := range g.permissions {
			perm, ok := Lookup(p)
			switch {
			case !ok:
				t.Errorf("%s holds %s, which is not in the catalogue", name, p)
			case perm.Scope == ScopeSelf && p != IdentityRead:
				t.Errorf("%s holds the self permission %s", name, p)
			case g.scope == ScopePlatform && perm.Scope == ScopeTenant, g.scope == ScopeTenant && perm.Scope == ScopePlatform:
				t.Errorf("%s (%s) holds the %s permission %s", name, g.scope, perm.Scope, p)
			}
		}
		// inference is the one role without identity:read (§5.2: "unchanged"; maintainer's decision at part A).
		if !contains(g.permissions, IdentityRead) && name != RoleInference {
			t.Errorf("%s lacks identity:read", name)
		}
	}
}

// TestEffectiveGranted is T-848 (the Effective test of sprint 1.3 keeps its name, T-204).
func TestEffectiveGranted(t *testing.T) {
	a := id(1)
	support := []string{MemberRead, MemberWrite, ServiceAccountRead}
	cases := []struct {
		name string
		p    Principal
		want []string
	}{
		{"granted counts for a service account with a tenant role", Principal{Kind: KindServiceAccount, TenantID: a, TenantState: tenant.StateActive, Roles: []string{RoleInference}, Granted: []string{MemberRead}},
			[]string{CredentialUse, MemberRead, ModelInvoke}},
		{"granted counts for a member", Principal{Kind: KindUser, TenantID: a, TenantState: tenant.StateActive, Roles: []string{RoleUsageViewer, "custom"}, Granted: []string{RoleRead}},
			[]string{IdentityRead, LimitRead, ModelRead, PriceListRead, RoleRead, SessionSelf, UsageRead}},
		{"granted does not count for a platform principal", Principal{Kind: KindPlatform, Roles: []string{RolePlatformAuditor}, Granted: []string{MemberRead, RoleRead}},
			[]string{CatalogRead, IdentityRead, PlatformIssuerRead, PlatformKeyRead, PlatformLimitRead, TenantRead, TenantSupportRead, UserRead}},
		{"granted does not count for a platform user", Principal{Kind: KindUser, Roles: []string{RolePlatformAuditor}, Granted: []string{MemberRead}},
			[]string{CatalogRead, IdentityRead, PlatformIssuerRead, PlatformKeyRead, PlatformLimitRead, SessionSelf, TenantRead, TenantSupportRead, UserRead}},
		{"a principal without tenant roles gets nothing from granted", Principal{Kind: KindServiceAccount, TenantID: a, Roles: nil, Granted: []string{MemberRead}}, nil},
		{"an unknown name, a platform and a self permission are dropped", Principal{Kind: KindServiceAccount, TenantID: a, Roles: []string{RoleInference},
			Granted: []string{"ghost:thing", TenantRead, IdentityRead, SessionSelf, "not-a-permission", MemberRead}},
			[]string{CredentialUse, MemberRead, ModelInvoke}},
		{"the scope list intersects granted", Principal{Kind: KindServiceAccount, TenantID: a, Roles: []string{RoleInference}, Granted: []string{MemberRead, RoleRead}, Scopes: []string{RoleRead, ModelInvoke, TenantRead}},
			[]string{ModelInvoke, RoleRead}},
		{"an empty scope list restricts to nothing", Principal{Kind: KindServiceAccount, TenantID: a, Roles: []string{RoleInference}, Granted: []string{MemberRead}, Scopes: []string{}}, nil},
		{"a user holds the two self permissions whatever it has", Principal{Kind: KindUser, TenantID: a, Granted: []string{MemberRead}},
			[]string{IdentityRead, SessionSelf}},
		{"a support principal holds granted and the Support attribute, and nothing from Roles", Principal{Kind: KindPlatform, ID: "k", TenantID: a, SupportActionID: "act", Roles: []string{RoleTenantAdmin, RolePlatformAdmin},
			Granted: append([]string{UsageRead, CredentialRead, TenantRead, TenantSettingWrite, SupportActionEnd}, support...)},
			[]string{MemberRead, MemberWrite, ServiceAccountRead}},
		{"a support principal of a user adds the self permissions", Principal{Kind: KindUser, ID: "u", TenantID: a, SupportActionID: "act", Roles: []string{RoleOwner},
			Granted: append([]string{UsageRead}, support...)},
			[]string{IdentityRead, MemberRead, MemberWrite, ServiceAccountRead, SessionSelf}},
		{"a support principal is limited by the scope list", Principal{Kind: KindPlatform, ID: "k", TenantID: a, SupportActionID: "act", Granted: support, Scopes: []string{MemberRead, UsageRead}},
			[]string{MemberRead}},
		{"a support principal with nothing granted holds nothing", Principal{Kind: KindPlatform, ID: "k", TenantID: a, SupportActionID: "act", Roles: []string{RoleOwner}}, nil},
	}
	for _, c := range cases {
		got := c.p.Effective()
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, want)
		}
	}
}

// TestDecideSupport is T-849: Decide over the whole catalogue for the support principals of both kinds.
func TestDecideSupport(t *testing.T) {
	a, b := id(1), id(2)
	granted := []string{MemberRead, MemberWrite, ServiceAccountRead, VirtualKeyWrite, RoleRead, UsageRead, CredentialRead, TenantSettingWrite}
	holds := map[string]bool{MemberRead: true, MemberWrite: true, ServiceAccountRead: true, VirtualKeyWrite: true, RoleRead: true} // granted and Support
	mk := func(kind Kind, state tenant.State) Principal {
		return Principal{Kind: kind, ID: "x", TenantID: a, TenantName: "acme", TenantState: state, SupportActionID: "act", Granted: granted,
			Roles: []string{RolePlatformAdmin, RoleOwner}}
	}
	for _, kind := range []Kind{KindPlatform, KindUser} {
		for _, perm := range Catalogue() {
			// Own tenant, active.
			p := mk(kind, tenant.StateActive)
			want := Deny
			switch {
			case perm.Scope == ScopeTenant && holds[perm.Name]:
				want = Allow
			case perm.Scope == ScopeSelf && kind == KindUser:
				want = Allow
			}
			if got := Decide(p, perm.Name, Target{TenantID: a}); got != want {
				t.Errorf("%s, own tenant, %s: %d, want %d", kind, perm.Name, got, want)
			}
			// Another tenant: a tenant permission is 404 (whatever the action holds), the rest as before.
			other := Deny
			switch {
			case perm.Scope == ScopeTenant:
				other = DenyNotFound
			case perm.Scope == ScopeSelf && kind == KindUser:
				other = Allow
			}
			if got := Decide(p, perm.Name, Target{TenantID: b}); got != other {
				t.Errorf("%s, another tenant, %s: %d, want %d", kind, perm.Name, got, other)
			}
			// A suspended tenant: every permission that would reach it is tenant_suspended.
			if got := Decide(mk(kind, tenant.StateSuspended), perm.Name, Target{TenantID: a}); perm.Scope == ScopePlatform {
				if got != Deny {
					t.Errorf("%s, suspended, %s: %d, want deny", kind, perm.Name, got)
				}
			} else if got != DenyTenantState {
				t.Errorf("%s, suspended, %s: %d, want tenant_suspended", kind, perm.Name, got)
			}
			// No platform permission is ever allowed to a principal that has a tenant.
			if perm.Scope == ScopePlatform {
				for _, tgt := range []Target{{}, {TenantID: a}, {TenantID: b}} {
					if Decide(p, perm.Name, tgt) == Allow {
						t.Errorf("%s with an action is allowed the platform permission %s", kind, perm.Name)
					}
				}
			}
		}
	}
	// A permission outside the action is refused even when the principal's roles would hold it.
	if got := Decide(mk(KindPlatform, tenant.StateActive), ModelInvoke, Target{TenantID: a}); got != Deny {
		t.Errorf("model:invoke under an action that does not list it: %d", got)
	}
	// A platform key without an action is denied for every tenant permission, as before.
	bare := Principal{Kind: KindPlatform, ID: "x", Roles: []string{RolePlatformAdmin}}
	for _, perm := range Catalogue() {
		if perm.Scope == ScopeTenant && Decide(bare, perm.Name, Target{TenantID: a}) != Deny {
			t.Errorf("a platform key without an action is allowed %s", perm.Name)
		}
	}
	// The tenant of the action is the principal's: a key with an action but no tenant set is denied.
	noTenant := Principal{Kind: KindPlatform, ID: "x", SupportActionID: "act", Granted: granted}
	if got := Decide(noTenant, MemberRead, Target{TenantID: a}); got != Deny {
		t.Errorf("an action without a tenant: %d", got)
	}
}

// TestCanGrant is T-850.
func TestCanGrant(t *testing.T) {
	a := id(1)
	admin := Principal{Kind: KindServiceAccount, TenantID: a, TenantState: tenant.StateActive, Roles: []string{RoleTenantAdmin}}
	custom := Principal{Kind: KindServiceAccount, TenantID: a, TenantState: tenant.StateActive, Roles: []string{RoleInference, "reader"}, Granted: []string{MemberRead, RoleWrite}}
	scoped := Principal{Kind: KindServiceAccount, TenantID: a, TenantState: tenant.StateActive, Roles: []string{RoleOwner}, Scopes: []string{MemberRead, RoleRead}}
	platform := Principal{Kind: KindPlatform, ID: "k", Roles: []string{RolePlatformAdmin}}
	auditor := Principal{Kind: KindPlatform, ID: "k", Roles: []string{RolePlatformAuditor}}
	support := Principal{Kind: KindPlatform, ID: "k", TenantID: a, SupportActionID: "act", Roles: []string{RolePlatformAdmin}, Granted: []string{MemberWrite}}
	supportUser := Principal{Kind: KindUser, ID: "u", TenantID: a, SupportActionID: "act", Granted: []string{MemberWrite}}
	cases := []struct {
		name string
		p    Principal
		set  []string
		want bool
	}{
		{"an empty set", admin, nil, true},
		{"a subset", admin, []string{MemberRead, UsageRead, CredentialWrite}, true},
		{"one missing permission", admin, []string{MemberRead, SupportActionEnd}, false},
		{"the owner holds it", Principal{Kind: KindServiceAccount, TenantID: a, Roles: []string{RoleOwner}}, []string{SupportActionEnd}, true},
		{"an unknown name", admin, []string{"ghost:thing"}, false},
		{"a custom role's permissions", custom, []string{MemberRead, RoleWrite, ModelInvoke}, true},
		{"a custom role's permissions, one more", custom, []string{MemberRead, RoleWrite, UsageRead}, false},
		{"a scoped key inside its scopes", scoped, []string{MemberRead, RoleRead}, true},
		{"a scoped key outside its scopes", scoped, []string{MemberRead, MemberWrite}, false},
		{"self permissions are ignored", admin, []string{IdentityRead, SessionSelf, MemberRead}, true},
		{"self permissions alone", Principal{Kind: KindServiceAccount, TenantID: a}, []string{IdentityRead, SessionSelf}, true},
		{"a platform administrator, a platform permission", platform, []string{TenantRead, TenantSupportOpen}, true},
		{"a platform auditor, a platform permission it lacks", auditor, []string{TenantRead, TenantSupportOpen}, false},
		{"a tenant principal, a platform permission", admin, []string{TenantRead}, false},
		{"a platform principal, a tenant permission", platform, []string{MemberRead}, false},
		{"a support principal grants any tenant set", support, []string{UsageRead, CredentialWrite, SupportActionEnd, MemberRead}, true},
		{"a support principal of a user grants any tenant set", supportUser, []string{UsageRead, SupportActionEnd}, true},
		{"a support principal grants no platform permission", support, []string{TenantRead}, false},
		{"a support principal grants no platform permission among tenant ones", support, []string{MemberRead, PlatformKeyWrite}, false},
		{"a support principal and an unknown name", support, []string{"ghost:thing"}, false},
	}
	for _, c := range cases {
		if got := c.p.CanGrant(c.set); got != c.want {
			t.Errorf("%s: CanGrant(%v) = %v, want %v", c.name, c.set, got, c.want)
		}
	}

	// The property: for a principal without a support action, CanGrant is true exactly when every
	// named permission that is not a self permission is in Effective().
	seed := rand.Int63() //nolint:gosec // a test seed, logged
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // a test generator
	all := Catalogue()
	roleNames := []string{RoleOwner, RoleTenantAdmin, RoleCredentialManager, RoleUsageViewer, RoleAuditor, RoleDeveloper, RoleInference, RoleReadOnly, RolePlatformAdmin, RolePlatformAuditor, "ghost"}
	pick := func(n int, from func() string) []string {
		var out []string
		for i := rng.Intn(n + 1); i > 0; i-- {
			out = append(out, from())
		}
		return out
	}
	permName := func() string {
		if rng.Intn(20) == 0 {
			return "ghost:thing"
		}
		return all[rng.Intn(len(all))].Name
	}
	for i := 0; i < 3000; i++ {
		p := Principal{Roles: pick(3, func() string { return roleNames[rng.Intn(len(roleNames))] }), Granted: pick(5, permName)}
		switch rng.Intn(3) {
		case 0:
			p.Kind = KindPlatform
		case 1:
			p.Kind, p.TenantID = KindServiceAccount, a
		default:
			p.Kind = KindUser
			if rng.Intn(2) == 0 {
				p.TenantID = a
			}
		}
		if rng.Intn(3) == 0 {
			p.Scopes = pick(8, permName)
			if p.Scopes == nil {
				p.Scopes = []string{}
			}
		}
		// Bias the set towards what the principal holds so that both outcomes are common.
		set := pick(4, permName)
		if eff := p.Effective(); len(eff) > 0 && rng.Intn(2) == 0 {
			set = pick(4, func() string { return eff[rng.Intn(len(eff))] })
		}
		want := true
		held := map[string]bool{}
		for _, e := range p.Effective() {
			held[e] = true
		}
		for _, name := range set {
			perm, ok := Lookup(name)
			if !ok || (perm.Scope != ScopeSelf && !held[name]) {
				want = false
			}
		}
		if got := p.CanGrant(set); got != want {
			t.Fatalf("CanGrant(%v) = %v for %+v (effective %v), want %v", set, got, p, p.Effective(), want)
		}
	}
}

// FuzzEffective is T-851.
func FuzzEffective(f *testing.F) {
	f.Add("tenant-admin,inference", "member:read,role:write", "", byte(1), true, false, false)
	f.Add("platform-admin", "member:read", "tenant:read,member:read", byte(0), false, true, false)
	f.Add("owner,ghost", "tenant:read,identity:read,ghost", "", byte(2), true, false, true)
	f.Add("", "", "", byte(2), false, true, false)
	f.Add("read-only,platform-auditor", "usage:read,support_action:end", "usage:read", byte(0), true, true, true)
	f.Fuzz(func(t *testing.T, roles, granted, scopes string, kind byte, hasTenant, hasScopes, support bool) {
		p := Principal{Roles: strings.Split(roles, ","), Granted: strings.Split(granted, ",")}
		if hasScopes {
			p.Scopes = strings.Split(scopes, ",")
		}
		switch kind % 3 {
		case 0:
			p.Kind = KindPlatform
		case 1:
			p.Kind = KindServiceAccount
			hasTenant = true // a service account always belongs to a tenant
		default:
			p.Kind = KindUser
		}
		if p.Kind == KindPlatform && !support {
			hasTenant = false // a platform key has a tenant only under a support action
		}
		if hasTenant {
			p.TenantID = id(7)
		}
		if support && p.Kind != KindServiceAccount && hasTenant {
			p.SupportActionID = "act"
		}
		got := p.Effective()
		if !sort.StringsAreSorted(got) {
			t.Fatalf("not sorted: %v", got)
		}
		for i, name := range got {
			perm, ok := Lookup(name)
			if !ok {
				t.Fatalf("%q is not in the catalogue", name)
			}
			if i > 0 && got[i-1] == name {
				t.Fatalf("%q twice", name)
			}
			if hasTenant && perm.Scope == ScopePlatform {
				t.Fatalf("a principal with a tenant holds the platform permission %s: %+v", name, p)
			}
			if !hasTenant && perm.Scope == ScopeTenant {
				t.Fatalf("a principal without a tenant holds the tenant permission %s: %+v", name, p)
			}
			if p.SupportActionID != "" && perm.Scope == ScopeTenant && !perm.Support {
				t.Fatalf("a support principal holds %s, which has no Support attribute", name)
			}
		}
	})
}
