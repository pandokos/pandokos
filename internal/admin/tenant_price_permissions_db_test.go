package admin_test

import (
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestTenantPricePermissions is T-752: each of the seven operations needs its permission: refused to an
// inference key, to a key scoped without it and to a platform key; tenant-admin needs nothing else.
func TestTenantPricePermissions(t *testing.T) {
	w := newPW(t)
	api := w.api
	tn := w.a.ID
	mkKey := func(roles []string, scopes []string) string {
		sa := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+tn+"/service-accounts", map[string]any{"name": apitest.UniqueName("sa"), "roles": roles}), 201)
		body := map[string]any{"service_account_id": sa.ID}
		if scopes != nil {
			body["scopes"] = scopes
		}
		return decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", "/v1/tenants/"+tn+"/virtual-keys", body), 201).Secret
	}
	inference := mkKey([]string{"inference"}, nil)
	snap := api.SnapshotTenant(t, tn)
	price := func() string {
		return w.createPrice(t, priceBody(w.cat.All, w.cat.Provider.Name, "EUR", nil, map[string]any{"valid_from": scheduled(50, int(apitestN()))})).ID
	}

	for _, c := range []struct {
		op, perm string
		method   string
		path     func() string
		body     func() any
	}{
		{"listPriceLists", "price_list:read", "GET", func() string { return "/v1/tenants/" + tn + "/price-lists" }, func() any { return nil }},
		{"listPriceListVersions", "price_list:read", "GET", func() string { return "/v1/tenants/" + tn + "/prices" }, func() any { return nil }},
		{"getPriceListVersion", "price_list:read", "GET", func() string { return "/v1/tenants/" + tn + "/prices/" + price() }, func() any { return nil }},
		{"getActiveCurrency", "price_list:read", "GET", func() string { return "/v1/tenants/" + tn + "/active-currency" }, func() any { return nil }},
		{"createPriceListVersion", "price_list:write", "POST", func() string { return "/v1/tenants/" + tn + "/prices" },
			func() any {
				return priceBody(w.cat.All, w.cat.Provider.Name, "EUR", nil, map[string]any{"valid_from": scheduled(60, int(apitestN()))})
			}},
		{"setActiveCurrency", "price_list:write", "PUT", func() string { return "/v1/tenants/" + tn + "/active-currency" }, func() any { return map[string]any{"currency": ""} }},
		{"deletePriceListVersion", "price_list:delete", "DELETE", func() string { return "/v1/tenants/" + tn + "/prices/" + price() }, func() any { return nil }},
	} {
		t.Run(c.op, func(t *testing.T) {
			other := "price_list:delete"
			if c.perm == "price_list:delete" {
				other = "price_list:read"
			}
			scopedWithout := mkKey([]string{"tenant-admin"}, []string{other})
			scopedWith := mkKey([]string{"tenant-admin"}, []string{c.perm})
			for name, key := range map[string]string{"an inference key": inference, "a key scoped without the permission": scopedWithout, "a platform key": w.plat} {
				r := w.do(t, key, c.method, c.path(), c.body())
				if r.Status != 403 {
					t.Errorf("%s: %d, want 403\n%s", name, r.Status, r.Body)
				}
			}
			if r := w.do(t, scopedWith, c.method, c.path(), c.body()); r.Status != 200 && r.Status != 201 {
				t.Errorf("a key scoped to %s: %d\n%s", c.perm, r.Status, r.Body)
			}
			if r := w.do(t, w.ka, c.method, c.path(), c.body()); r.Status != 200 && r.Status != 201 {
				t.Errorf("the tenant administrator needs nothing else: %d\n%s", r.Status, r.Body)
			}
		})
	}
	_ = snap
}

var priceCounter int

// apitestN returns a number that differs per call, to space the starts of scheduled versions.
func apitestN() int64 { priceCounter++; return int64(priceCounter) * 7 }
