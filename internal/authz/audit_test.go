package authz

import (
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
)

// TestAuditPermissions is T-943: audit:read (tenant scope, no support attribute) and
// platform_audit:read (platform scope, no attribute), and the roles that hold them.
func TestAuditPermissions(t *testing.T) {
	holders := func(perm string) []string {
		var out []string
		for _, r := range Roles() {
			for _, p := range r.Permissions {
				if p == perm {
					out = append(out, r.Name)
				}
			}
		}
		return out
	}

	p, ok := Lookup(AuditRead)
	if !ok || AuditRead != "audit:read" {
		t.Fatalf("audit:read is not in the catalogue: %+v %v", p, ok)
	}
	if p.Scope != ScopeTenant || p.InTenant || p.Support || p.Description == "" {
		t.Errorf("audit:read is %+v, want a tenant permission with a description and not Support", p)
	}
	if want := []string{RoleOwner, RoleTenantAdmin, RoleAuditor}; !equalSets(holders(AuditRead), want) {
		t.Errorf("the roles holding audit:read are %v, want %v", holders(AuditRead), want)
	}

	pp, ok := Lookup(PlatformAuditRead)
	if !ok || PlatformAuditRead != "platform_audit:read" {
		t.Fatalf("platform_audit:read is not in the catalogue: %+v %v", pp, ok)
	}
	if pp.Scope != ScopePlatform || pp.InTenant || pp.Support || pp.Description == "" {
		t.Errorf("platform_audit:read is %+v, want a platform permission with a description and no attribute", pp)
	}
	if want := []string{RolePlatformAdmin, RolePlatformAuditor}; !equalSets(holders(PlatformAuditRead), want) {
		t.Errorf("the roles holding platform_audit:read are %v, want %v", holders(PlatformAuditRead), want)
	}

	a := id(1)
	for _, role := range []string{RoleOwner, RoleTenantAdmin, RoleAuditor} {
		if d := Decide(sa(a, tenant.StateActive, []string{role}, nil), AuditRead, Target{TenantID: a}); d != Allow {
			t.Errorf("%s: audit:read is %v, want Allow", role, d)
		}
	}
	for _, role := range []string{RoleReadOnly, RoleUsageViewer, RoleDeveloper, RoleCredentialManager, RoleInference} {
		if d := Decide(sa(a, tenant.StateActive, []string{role}, nil), AuditRead, Target{TenantID: a}); d == Allow {
			t.Errorf("%s may read the audit log", role)
		}
	}
	// A tenant principal never holds a platform permission, and the other way round.
	for _, r := range Roles() {
		if r.Scope == ScopeTenant {
			if d := Decide(sa(a, tenant.StateActive, []string{r.Name}, nil), PlatformAuditRead, Target{}); d == Allow {
				t.Errorf("the tenant role %s reads the platform audit log", r.Name)
			}
		}
	}
	for _, role := range []string{RolePlatformAdmin, RolePlatformAuditor} {
		if d := Decide(Principal{Kind: KindPlatform, ID: "p", Roles: []string{role}}, PlatformAuditRead, Target{}); d != Allow {
			t.Errorf("%s: platform_audit:read is %v, want Allow", role, d)
		}
		if d := Decide(Principal{Kind: KindPlatform, ID: "p", Roles: []string{role}}, AuditRead, Target{TenantID: a}); d == Allow {
			t.Errorf("%s reads a tenant's audit log without a support action", role)
		}
	}
	scoped := Principal{Kind: KindPlatform, ID: "p", Roles: []string{RolePlatformAdmin}, Scopes: []string{CatalogRead}}
	if d := Decide(scoped, PlatformAuditRead, Target{}); d == Allow {
		t.Errorf("a platform key scoped to catalog:read reads the platform audit log: %v", d)
	}

	// The permissions a support action may carry are the eleven of sprint 4.3 and, of sprint 6.4,
	// event_destination:read (the export of events is usage data and stays out).
	support := SupportPermissions()
	if len(support) != 12 {
		t.Errorf("SupportPermissions() has %d names, want the eleven of sprint 4.3 and event_destination:read: %v", len(support), support)
	}
	for _, s := range support {
		if s == EventRead || s == EventDestinationWrite || s == EventDestinationDelete || s == PlatformEventRead {
			t.Errorf("SupportPermissions() holds %s", s)
		}
	}
	for _, s := range support {
		if s == AuditRead || s == PlatformAuditRead {
			t.Errorf("SupportPermissions() holds %s", s)
		}
	}
	if !reflect.DeepEqual(support, sortedCopy(support)) {
		t.Error("SupportPermissions() is not sorted")
	}
}

func sortedCopy(in []string) []string {
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
