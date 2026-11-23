package admin_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestTenantExternalReference is T-1044: create, update, read, the validation, the audit detail and
// the copy that TenantDeletion shows.
func TestTenantExternalReference(t *testing.T) {
	api := apitest.New(t)
	plat := api.PlatformCaller()

	t.Run("create, read and update", func(t *testing.T) {
		name := apitest.UniqueName("ref")
		r := plat.Do(t, "POST", "/v1/tenants", map[string]any{"name": name, "external_reference": "cust-42"}, nil)
		var out struct {
			Tenant adminapi.Tenant `json:"tenant"`
		}
		r.JSON(t, &out)
		if r.Status != 201 || out.Tenant.ExternalReference != "cust-42" {
			t.Fatalf("create: %d %s", r.Status, r.Body)
		}
		id := out.Tenant.ID
		if got := decode[adminapi.Tenant](t, plat.Do(t, "GET", "/v1/tenants/"+id, nil, nil), 200); got.ExternalReference != "cust-42" {
			t.Errorf("read: %q", got.ExternalReference)
		}
		for _, l := range decode[adminapi.TenantList](t, plat.Do(t, "GET", "/v1/tenants", nil, nil), 200).Items {
			if l.ID == id && l.ExternalReference != "cust-42" {
				t.Errorf("list: %q", l.ExternalReference)
			}
		}
		upd := decode[adminapi.Tenant](t, plat.Do(t, "PATCH", "/v1/tenants/"+id, map[string]any{"external_reference": "cust-43"}, nil), 200)
		if upd.ExternalReference != "cust-43" {
			t.Errorf("update: %q", upd.ExternalReference)
		}
		// A reference that is cleared is empty; one that is not given is left alone.
		if got := decode[adminapi.Tenant](t, plat.Do(t, "PATCH", "/v1/tenants/"+id, map[string]any{"display_name": "Named"}, nil), 200); got.ExternalReference != "cust-43" {
			t.Errorf("an update of another member changed the reference: %q", got.ExternalReference)
		}
		if got := decode[adminapi.Tenant](t, plat.Do(t, "PATCH", "/v1/tenants/"+id, map[string]any{"external_reference": ""}, nil), 200); got.ExternalReference != "" {
			t.Errorf("cleared: %q", got.ExternalReference)
		}
		// A tenant created with none has none.
		x, _ := api.Tenant(t, apitest.UniqueName("noref"))
		if x.ExternalReference != "" {
			t.Errorf("default: %q", x.ExternalReference)
		}
		// Two tenants may be one customer.
		if r := plat.Do(t, "POST", "/v1/tenants", map[string]any{"name": apitest.UniqueName("ref"), "external_reference": "cust-42"}, nil); r.Status != 201 {
			t.Errorf("a second tenant with the same reference: %d %s", r.Status, r.Body)
		}
	})

	t.Run("a tenant's own key cannot change it", func(t *testing.T) {
		x, key := api.Tenant(t, apitest.UniqueName("mine"))
		r := api.Do(t, "PATCH", "/v1/tenants/"+x.ID, key, map[string]any{"external_reference": "mine"}, nil)
		if r.Status != 403 && r.Status != 404 {
			t.Errorf("a tenant's key set its reference: %d %s", r.Status, r.Body)
		}
		if got := decode[adminapi.Tenant](t, plat.Do(t, "GET", "/v1/tenants/"+x.ID, nil, nil), 200); got.ExternalReference != "" {
			t.Errorf("the reference became %q", got.ExternalReference)
		}
	})

	t.Run("validation", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("valid"))
		for what, ref := range map[string]string{"256 characters": strings.Repeat("a", 256), "a newline": "a\nb", "a tab": "a\tb", "a DEL": "a\x7fb"} {
			r := plat.Do(t, "PATCH", "/v1/tenants/"+x.ID, map[string]any{"external_reference": ref}, nil)
			if r.Status != 400 || r.Problem() != "invalid_request" || !hasField(problemOf(t, r), "external_reference") {
				t.Errorf("update with %s: %d %s", what, r.Status, r.Body)
			}
			c := plat.Do(t, "POST", "/v1/tenants", map[string]any{"name": apitest.UniqueName("bad"), "external_reference": ref}, nil)
			if c.Status != 400 {
				t.Errorf("create with %s: %d", what, c.Status)
			}
		}
		for what, ref := range map[string]string{"255 characters": strings.Repeat("a", 255), "255 multibyte characters": strings.Repeat("é", 255), "symbols": "ACME / 42 (EU)"} {
			if r := plat.Do(t, "PATCH", "/v1/tenants/"+x.ID, map[string]any{"external_reference": ref}, nil); r.Status != 200 {
				t.Errorf("%s: %d %s", what, r.Status, r.Body)
			}
		}
	})

	t.Run("the audit detail", func(t *testing.T) {
		name := apitest.UniqueName("audited")
		var out struct {
			Tenant adminapi.Tenant `json:"tenant"`
		}
		plat.Do(t, "POST", "/v1/tenants", map[string]any{"name": name, "external_reference": "cust-A"}, nil).JSON(t, &out)
		plat.Do(t, "PATCH", "/v1/tenants/"+out.Tenant.ID, map[string]any{"external_reference": "cust-B"}, nil)
		var create, update map[string]any
		for _, row := range api.DB.PlatformAuditRows(t) {
			if row.ResourceID == out.Tenant.ID && row.Action == "tenant.create" {
				create = row.Detail
			}
			if row.ResourceID == out.Tenant.ID && row.Action == "tenant.update" {
				update = row.Detail
			}
		}
		if create == nil || update == nil {
			t.Fatalf("audit rows: create %v, update %v", create, update)
		}
		if !strings.Contains(toJSON(create), "cust-A") || !strings.Contains(toJSON(update), "cust-B") {
			t.Errorf("the audit rows do not show the reference: %v / %v", create, update)
		}
	})

	t.Run("the copy shown by TenantDeletion", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("copied"))
		plat.Do(t, "PATCH", "/v1/tenants/"+x.ID, map[string]any{"external_reference": "cust-at-request"}, nil)
		d := decode[adminapi.TenantDeletion](t, plat.Do(t, "POST", "/v1/tenants/"+x.ID+"/deletion", map[string]any{"reason": apitest.Reason("close"), "confirm_name": x.Name}, nil), 200)
		if d.ExternalReference != "cust-at-request" {
			t.Errorf("the request answered %q", d.ExternalReference)
		}
		plat.Do(t, "PATCH", "/v1/tenants/"+x.ID, map[string]any{"external_reference": "cust-changed-later"}, nil)
		got := decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", "/v1/tenants/"+x.ID+"/deletion", nil, nil), 200)
		if got.ExternalReference != "cust-at-request" {
			t.Errorf("the deletion shows %q after a later change, want the copy made at the request", got.ExternalReference)
		}
		for _, l := range decode[adminapi.TenantDeletionList](t, plat.Do(t, "GET", "/v1/tenant-deletions", nil, nil), 200).Items {
			if l.TenantID == x.ID && l.ExternalReference != "cust-at-request" {
				t.Errorf("the list shows %q", l.ExternalReference)
			}
		}
	})
}

func toJSON(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprint(v), "\n", " "), "\t", " ")))
		b.WriteString(";")
	}
	return b.String()
}
