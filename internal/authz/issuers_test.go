package authz

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
)

// TestIssuerPermissions is T-591: the nine permissions of sprint 4.1 with their scopes, who holds
// them, and that a principal that came from a token decides like one that came from a key.
func TestIssuerPermissions(t *testing.T) {
	want := []struct {
		name  string
		scope Scope
	}{
		{TokenIssuerRead, ScopeTenant}, {TokenIssuerWrite, ScopeTenant}, {TokenIssuerDelete, ScopeTenant},
		{SubjectMappingRead, ScopeTenant}, {SubjectMappingWrite, ScopeTenant}, {SubjectMappingDelete, ScopeTenant},
		{PlatformIssuerRead, ScopePlatform}, {PlatformIssuerWrite, ScopePlatform}, {PlatformIssuerDelete, ScopePlatform},
	}
	names := map[string]string{
		TokenIssuerRead: "token_issuer:read", TokenIssuerWrite: "token_issuer:write", TokenIssuerDelete: "token_issuer:delete",
		SubjectMappingRead: "subject_mapping:read", SubjectMappingWrite: "subject_mapping:write", SubjectMappingDelete: "subject_mapping:delete",
		PlatformIssuerRead: "platform_issuer:read", PlatformIssuerWrite: "platform_issuer:write", PlatformIssuerDelete: "platform_issuer:delete",
	}

	t.Run("nine permissions with their scopes, after usage:read, in this order", func(t *testing.T) {
		cat := Catalogue()
		first := -1
		for i, p := range cat {
			if p.Name == UsageRead {
				first = i + 1
			}
		}
		if first < 0 || len(cat) != first+9 {
			t.Fatalf("%d permissions, usage:read at %d: the nine are not the last nine", len(cat), first)
		}
		for i, w := range want {
			p := cat[first+i]
			if p.Name != names[w.name] || p.Scope != w.scope || p.Description == "" {
				t.Errorf("entry %d is %+v, want %s with scope %s and a description", first+i, p, names[w.name], w.scope)
			}
			if got, ok := Lookup(names[w.name]); !ok || got.Scope != w.scope {
				t.Errorf("Lookup(%s) = %+v %v", names[w.name], got, ok)
			}
		}
	})

	t.Run("the descriptions say what the permission gives", func(t *testing.T) {
		p, _ := Lookup(SubjectMappingWrite)
		if !strings.Contains(strings.ToLower(p.Description), "service account") || !strings.Contains(strings.ToLower(p.Description), "all its roles") {
			t.Errorf("subject_mapping:write says %q: it must say that it gives a token subject all the roles of a service account", p.Description)
		}
		d, _ := Lookup(PlatformIssuerDelete)
		if !strings.Contains(d.Description, "tenant list") || !strings.Contains(d.Description, "subject mappings") {
			t.Errorf("platform_issuer:delete says %q", d.Description)
		}
	})

	t.Run("who holds them", func(t *testing.T) {
		roles := map[string]Role{}
		for _, r := range Roles() {
			roles[r.Name] = r
		}
		has := func(role string, perm string) bool {
			for _, p := range roles[role].Permissions {
				if p == perm {
					return true
				}
			}
			return false
		}
		for _, w := range want {
			tenantAdmin, platformAdmin, inference := has(RoleTenantAdmin, w.name), has(RolePlatformAdmin, w.name), has(RoleInference, w.name)
			switch w.scope {
			case ScopeTenant:
				if !tenantAdmin || platformAdmin || inference {
					t.Errorf("%s: tenant-admin %v, platform-admin %v, inference %v, want true, false, false", w.name, tenantAdmin, platformAdmin, inference)
				}
			case ScopePlatform:
				if tenantAdmin || !platformAdmin || inference {
					t.Errorf("%s: tenant-admin %v, platform-admin %v, inference %v, want false, true, false", w.name, tenantAdmin, platformAdmin, inference)
				}
			}
		}
		if n := len(roles); n != 3 {
			t.Errorf("%d built-in roles: sprint 4.1 adds no role", n)
		}
	})

	t.Run("a token principal decides as a service account", func(t *testing.T) {
		a, b := id(1), id(2)
		token := sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil)
		token.KeyID, token.IssuerID, token.Subject = "0192f0f0-0000-7000-8000-0000000000c1", "0192f0f0-0000-7000-8000-0000000000a1", "workload"
		key := sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil)
		for _, w := range want {
			tgt := Target{}
			if w.scope == ScopeTenant {
				tgt = Target{TenantID: a}
			}
			if Decide(token, names[w.name], tgt) != Decide(key, names[w.name], tgt) {
				t.Errorf("%s: a token principal and a key principal decide differently", names[w.name])
			}
		}
		if Decide(token, names[TokenIssuerRead], Target{TenantID: a}) != Allow {
			t.Error("a tenant-admin token principal cannot read its issuers")
		}
		if Decide(token, names[TokenIssuerRead], Target{TenantID: b}) != DenyNotFound {
			t.Error("another tenant's issuers are not a 404")
		}
		if Decide(token, names[PlatformIssuerRead], Target{}) != Deny {
			t.Error("a tenant principal reads platform issuers")
		}
		inference := sa(a, tenant.StateActive, []string{RoleInference}, nil)
		inference.IssuerID, inference.Subject = token.IssuerID, token.Subject
		for _, w := range want {
			if Decide(inference, names[w.name], Target{TenantID: a}) == Allow {
				t.Errorf("an inference principal holds %s", names[w.name])
			}
		}
		if Decide(inference, ModelInvoke, Target{TenantID: a}) != Allow {
			t.Error("a token principal with the role inference cannot invoke a model")
		}
		// A suspended tenant is a decision, not an authentication failure.
		suspended := sa(a, tenant.StateSuspended, []string{RoleInference}, nil)
		suspended.IssuerID = token.IssuerID
		if got := Decide(suspended, ModelInvoke, Target{TenantID: a}); got != DenyTenantState {
			t.Errorf("a token principal of a suspended tenant: %v, want DenyTenantState", got)
		}
		// A token principal has no scope list and no model list: Effective is the union of its roles.
		if eff := inference.Effective(); len(eff) != 2 {
			t.Errorf("effective permissions of a token principal with the role inference: %v", eff)
		}
	})

	t.Run("the permission reference lists them", func(t *testing.T) {
		root := repoRoot(t)
		b, err := os.ReadFile(filepath.Join(root, "docs", "reference", "permissions.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, w := range want {
			var line string
			for _, l := range strings.Split(text, "\n") {
				if strings.Contains(l, names[w.name]) {
					line = l
					break
				}
			}
			if line == "" || !strings.Contains(line, string(w.scope)) {
				t.Errorf("permissions.md has no line with %s and its scope %s", names[w.name], w.scope)
			}
		}
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(l, "subject_mapping:write") && !strings.Contains(strings.ToLower(l), "virtual_key:write") && !strings.Contains(strings.ToLower(l), "all its roles") && !strings.Contains(strings.ToLower(l), "as sensitive") {
				t.Errorf("permissions.md does not say that subject_mapping:write is as sensitive as virtual_key:write: %q", l)
			}
		}
	})
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(b), "module github.com/pandokos/pandokos\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("the repository root was not found")
		}
		dir = parent
	}
}
