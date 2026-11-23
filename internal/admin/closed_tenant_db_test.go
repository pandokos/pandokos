package admin_test

import (
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestWritesRefusedForAClosedTenant is T-1000: a tenant that is being deleted or is deleted takes no
// row from a platform operation (409 invalid_state, one error on the tenant, nothing written), and its
// own principals are refused (403, then 401 once their keys are gone).
func TestWritesRefusedForAClosedTenant(t *testing.T) {
	api := apitest.New(t)
	plat := api.PlatformCaller()
	cat := api.Catalog(t)
	issuer := api.SeedPlatformIssuer(t)

	deleting, deletingKey := api.Tenant(t, apitest.UniqueName("closed-ing"))
	deleted, _ := api.Tenant(t, apitest.UniqueName("closed-ed"))
	open, _ := api.Tenant(t, apitest.UniqueName("closed-open"))
	api.DB.BeginDeletion(t, parseTenant(t, deleting.ID))
	api.DB.CompleteDeletion(t, parseTenant(t, deleted.ID))

	// The operations that write a row for a tenant, with a valid call each.
	writes := map[string]func(id string) *apitest.Response{
		"grantModel": func(id string) *apitest.Response {
			return plat.Do(t, "PUT", "/v1/models/"+cat.Selected.ID+"/grants/"+id, nil, nil)
		},
		"putPlatformIssuerTenant": func(id string) *apitest.Response {
			return plat.Do(t, "PUT", "/v1/platform-issuers/"+issuer+"/tenants/"+id, map[string]any{}, nil)
		},
		"createPlatformLimit": func(id string) *apitest.Response {
			return plat.Do(t, "POST", "/v1/platform-limits", map[string]any{"tenant": id, "metric": "requests_per_day", "limit": 1000, "enforce": true}, nil)
		},
		"openSupportAction": func(id string) *apitest.Response {
			return plat.Do(t, "POST", "/v1/tenant-support/"+id+"/actions", map[string]any{"reason": apitest.Reason("open"), "permissions": []string{authz.MemberRead}}, nil)
		},
	}
	// The platform operations that name a tenant and write no new row for it, or only change one that
	// exists: they are covered by their own tests. Any other one must be classified here.
	changes := map[string]bool{"updateTenant": true, "suspendTenant": true, "reactivateTenant": true, "requestTenantDeletion": true, "cancelTenantDeletion": true,
		"disableTenantCredential": true, "revokeTenantCredential": true, "closeSupportAction": true, "revokeModelGrant": true, "deletePlatformIssuerTenant": true}
	for _, op := range adminapi.Operations {
		if op.Method == "GET" || !strings.Contains(op.Path, "{tenant}") {
			continue
		}
		perm, ok := authz.Lookup(op.Permission)
		if !ok || perm.Scope != authz.ScopePlatform {
			continue
		}
		if writes[op.ID] == nil && !changes[op.ID] {
			t.Errorf("the platform operation %s names a tenant: say whether it writes a row for it (add a call to writes) or only changes one (changes)", op.ID)
		}
	}
	for id := range writes {
		found := false
		for _, op := range adminapi.Operations {
			found = found || op.ID == id
		}
		if !found {
			t.Errorf("the specification has no operation %s", id)
		}
	}

	rowCount := func(id string) int {
		return api.DB.Count(t, `SELECT (SELECT count(*) FROM pandokos.model_tenant_grant WHERE tenant_id = $1::uuid)
			+ (SELECT count(*) FROM pandokos.platform_issuer_tenant WHERE tenant_id = $1::uuid)
			+ (SELECT count(*) FROM pandokos.platform_limit_rule WHERE tenant_id = $1::uuid)
			+ (SELECT count(*) FROM pandokos.support_action WHERE tenant_id = $1::uuid)`, id)
	}
	for name, tn := range map[string]string{"deleting": deleting.ID, "deleted": deleted.ID} {
		for op, call := range writes {
			rows, audit := rowCount(tn), len(api.DB.PlatformAuditRows(t))
			r := call(tn)
			p := problemOf(t, r)
			if r.Status != 409 || p.Code != "invalid_state" || !hasField(p, "tenant") || !strings.Contains(string(r.Body), "being deleted or is deleted") {
				t.Errorf("%s for a tenant that is %s: %d %s, want 409 invalid_state with an error on tenant", op, name, r.Status, r.Body)
			}
			if got := rowCount(tn); got != rows {
				t.Errorf("%s wrote %d rows for a tenant that is %s", op, got-rows, name)
			}
			if got := len(api.DB.PlatformAuditRows(t)); got != audit {
				t.Errorf("%s wrote %d audit rows for a tenant that is %s", op, got-audit, name)
			}
		}
	}
	// The control: an open tenant takes the same calls.
	for op, call := range writes {
		if r := call(open.ID); r.Status/100 != 2 {
			t.Errorf("%s for an open tenant: %d %s", op, r.Status, r.Body)
		}
	}

	// A principal of the tenant being deleted is refused (it is suspended), and once the step of access
	// has removed its keys, it is no one.
	list := func() *apitest.Response {
		return api.Do(t, "GET", "/v1/tenants/"+deleting.ID+"/service-accounts", deletingKey, nil, nil)
	}
	if r := list(); r.Status != 403 || r.Problem() != "tenant_suspended" {
		t.Errorf("a key of a tenant being deleted: %d %s, want 403 tenant_suspended", r.Status, r.Body)
	}
	if got := api.DB.StepDeletion(t, parseTenant(t, deleting.ID), 100); got != "secrets" {
		t.Fatalf("the step access answered %q", got)
	}
	if r := list(); r.Status != 401 {
		t.Errorf("a key of a tenant whose access was removed: %d %s, want 401", r.Status, r.Body)
	}
	if r := api.Do(t, "GET", "/v1/me", deletingKey, nil, nil); r.Status != 401 {
		t.Errorf("getMe with such a key: %d, want 401", r.Status)
	}
}
