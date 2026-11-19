package admin_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestTenantPriceAudit is T-751: each creation, deletion and currency change writes one row in the tenant's
// log with the members of 5.9, in the transaction of the change (a failure injected after the write leaves
// neither); nothing in the platform's log; the note in no audit row and no log line.
func TestTenantPriceAudit(t *testing.T) {
	w := newPW(t)
	api := w.api
	prov, all := w.cat.Provider.Name, w.cat.All
	path := w.pricesPath(w.a)
	const note = "a-confidential-note-7f3a"
	seen := seenIDs(t, api)
	actor := *me(t, api, w.ka)

	row := func(rows []auditRow, action string) auditRow {
		t.Helper()
		if len(rows) != 1 || rows[0].Action != action {
			t.Fatalf("rows %+v, want one %s", rows, action)
		}
		r := rows[0]
		if r.Log != "tenant" || r.TenantID != w.a.ID || r.ActorType != "service_account" || r.ActorID != actor.ID || r.KeyID != actor.KeyID || r.Outcome != "success" {
			t.Errorf("row %+v: the tenant principal in the tenant's log", r)
		}
		return r
	}

	first := w.createPrice(t, priceBody(all, prov, "USD", tpRates("input", "0", "2.50", "output", "0", "10"), map[string]any{"note": note}))
	r := row(newRows(t, api, seen), "tenant_price.create")
	if r.ResourceType != "tenant_price_version" || r.ResourceID != first.ID {
		t.Errorf("row %+v", r)
	}
	for _, k := range []string{"provider", "model", "model_id", "currency", "kind", "rates", "valid_from"} {
		if _, ok := r.Detail[k]; !ok {
			t.Errorf("detail %v has no %s", r.Detail, k)
		}
	}
	if _, ok := r.Detail["previous_version"]; ok {
		t.Errorf("a first version has no previous one: %v", r.Detail)
	}

	second := w.createPrice(t, priceBody(all, prov, "USD", nil, map[string]any{"valid_from": scheduled(4, 0)}))
	r = row(newRows(t, api, seen), "tenant_price.create")
	if r.Detail["previous_version"] != first.ID {
		t.Errorf("detail %v: the version in force at the start is named", r.Detail)
	}

	decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "DELETE", path+"/"+second.ID, nil), 200)
	r = row(newRows(t, api, seen), "tenant_price.delete")
	if r.ResourceID != second.ID || r.Detail["currency"] != "USD" || r.Detail["kind"] != "price" || r.Detail["model_id"] != all.ID || r.Detail["provider"] != prov {
		t.Errorf("row %+v", r)
	}

	w.createPrice(t, priceBody(all, prov, "EUR", nil, map[string]any{"valid_from": scheduled(3, 0)}))
	newRows(t, api, seen)
	decodeInto[adminapi.ActiveCurrency](t, w.do(t, w.ka, "PUT", "/v1/tenants/"+w.a.ID+"/active-currency", map[string]any{"currency": "EUR"}), 200)
	r = row(newRows(t, api, seen), "tenant.active_currency_update")
	for _, k := range []string{"from", "to", "in_force", "effective_from", "inactive_limits"} {
		if _, ok := r.Detail[k]; !ok {
			t.Errorf("detail %v has no %s", r.Detail, k)
		}
	}
	for _, x := range auditRows(t, api) {
		if x.Log == "platform" && strings.HasPrefix(x.Action, "tenant_price.") {
			t.Errorf("the platform's log holds %+v", x)
		}
		if strings.Contains(fmt.Sprintf("%+v", x), note) {
			t.Errorf("the note is in an audit row: %+v", x)
		}
	}
	if strings.Contains(api.Logs.String(), note) {
		t.Error("the note is in a log line")
	}
	if v := decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "GET", path+"/"+first.ID, nil), 200); v.Note != note {
		t.Errorf("the note is stored with the version: %q", v.Note)
	}

	t.Run("a failure after the write leaves neither the change nor its row", func(t *testing.T) {
		boom := errors.New("injected")
		rowsBefore := len(auditRows(t, api))
		restore := admin.SetAfterTenantPriceInsert(func() error { return boom })
		before := api.SnapshotTenant(t, w.a.ID)
		expect(t, w.do(t, w.ka, "POST", path, priceBody(all, prov, "USD", nil, map[string]any{"valid_from": scheduled(8, 0)})), 500, adminapi.ProblemCodeInternal)
		restore()
		if after := api.SnapshotTenant(t, w.a.ID); after != before || len(auditRows(t, api)) != rowsBefore {
			t.Error("a version or an audit row survived the failed creation")
		}
		// A change of the currency that fails after the write: the tenant row and the log are as they were.
		w.createPrice(t, priceBody(all, prov, "GBP", nil, map[string]any{"valid_from": scheduled(3, 0)}))
		rowsBefore = len(auditRows(t, api))
		columns := func() string {
			var s string
			if err := api.Super(t).QueryRow(ctx(t), `SELECT active_currency || '|' || active_currency_previous || '|' || COALESCE(active_currency_effective_from::text, '') FROM pandokos.tenant WHERE id = $1::uuid`, w.a.ID).Scan(&s); err != nil {
				t.Fatal(err)
			}
			return s
		}
		cols := columns()
		restore = admin.SetAfterActiveCurrencySet(func() error { return boom })
		expect(t, w.do(t, w.ka, "PUT", "/v1/tenants/"+w.a.ID+"/active-currency", map[string]any{"currency": "GBP"}), 500, adminapi.ProblemCodeInternal)
		restore()
		if columns() != cols || len(auditRows(t, api)) != rowsBefore {
			t.Error("the currency or an audit row survived the failed change")
		}
	})
}
