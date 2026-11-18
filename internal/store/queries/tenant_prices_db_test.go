package queries_test

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// priceListStatements is the part of T-323 that covers the 16 statements of sprint 5.4: the active
// currency of a tenant and its price lists. It runs them in the session of the tenant, in the order
// a handler does, and deletes the scheduled version it writes.
func priceListStatements(c context.Context, t *testing.T, d *dbtest.Database, admin, gateway *store.DB, tid tenant.ID, called map[string]bool) {
	t.Helper()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	d.SeedProvider(t, "pl-provider")
	modelID := d.SeedModel(t, "pl-provider", "pl-model")
	var versionID string
	start := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	must("active currency on the gateway pool", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		called["GetTenantActiveCurrency"] = true
		cur, err := queries.New(tx).GetTenantActiveCurrency(c)
		if err != nil || cur.ActiveCurrency != "" || cur.ActiveCurrencyPrevious != "" || cur.ActiveCurrencyEffectiveFrom != nil {
			t.Errorf("GetTenantActiveCurrency: %+v %v", cur, err)
		}
		called["LoadTenantPriceVersions"] = true
		if rows, err := queries.New(tx).LoadTenantPriceVersions(c, []string{"EUR"}); err != nil || len(rows) != 0 {
			t.Errorf("LoadTenantPriceVersions: %v %v", rows, err)
		}
		return nil
	}))
	must("price lists", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["LockTenantActiveCurrency"] = true
		lock, err := q.LockTenantActiveCurrency(c, tid.String())
		if err != nil || lock.ID != tid.String() || lock.DbNow.IsZero() {
			t.Errorf("LockTenantActiveCurrency: %+v %v", lock, err)
		}
		called["SetTenantActiveCurrency"] = true
		set, err := q.SetTenantActiveCurrency(c, queries.SetTenantActiveCurrencyParams{ID: tid.String()})
		if err != nil || set.ActiveCurrency != "" || set.ActiveCurrencyEffectiveFrom != nil {
			t.Errorf("SetTenantActiveCurrency: %+v %v", set, err)
		}
		called["LockTenantPrices"] = true
		if err := q.LockTenantPrices(c); err != nil {
			t.Errorf("LockTenantPrices: %v", err)
		}
		called["ResolveTenantPriceModel"] = true
		model, err := q.ResolveTenantPriceModel(c, queries.ResolveTenantPriceModelParams{Provider: "pl-provider", Model: "pl-model"})
		if err != nil || model.ID != modelID {
			t.Errorf("ResolveTenantPriceModel: %+v %v", model, err)
		}
		called["CurrencyHasList"] = true
		if has, err := q.CurrencyHasList(c, "EUR"); err != nil || has {
			t.Errorf("CurrencyHasList before: %v %v", has, err)
		}
		called["CountTenantPriceVersions"] = true
		if n, err := q.CountTenantPriceVersions(c); err != nil || n != 0 {
			t.Errorf("CountTenantPriceVersions before: %d %v", n, err)
		}
		called["CreateTenantPriceVersion"] = true
		v, err := q.CreateTenantPriceVersion(c, queries.CreateTenantPriceVersionParams{ModelID: modelID, ValidFrom: start, Kind: "price", Currency: "EUR", Note: "n"})
		if err != nil {
			t.Fatalf("CreateTenantPriceVersion: %v", err)
		}
		versionID = v.ID
		called["CreateTenantPriceRates"] = true
		if n, err := q.CreateTenantPriceRates(c, queries.CreateTenantPriceRatesParams{PriceVersionID: v.ID,
			Categories: []string{"input", "output"}, Thresholds: []int64{0, 0}, Rates: []string{"1", "2.5"}}); err != nil || n != 2 {
			t.Errorf("CreateTenantPriceRates: %d %v", n, err)
		}
		return nil
	}))
	must("reads", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if n, err := q.CountTenantPriceVersions(c); err != nil || n != 1 {
			t.Errorf("CountTenantPriceVersions after: %d %v", n, err)
		}
		if has, err := q.CurrencyHasList(c, "EUR"); err != nil || !has {
			t.Errorf("CurrencyHasList after: %v %v", has, err)
		}
		called["TenantPriceAt"] = true
		at, err := q.TenantPriceAt(c, queries.TenantPriceAtParams{Currency: "EUR", ModelID: modelID, At: start})
		if err != nil || at.ID != versionID || at.Kind != "price" {
			t.Errorf("TenantPriceAt: %+v %v", at, err)
		}
		called["GetTenantPriceVersion"] = true
		got, err := q.GetTenantPriceVersion(c, versionID)
		if err != nil || got.Provider != "pl-provider" || got.Model != "pl-model" || got.Currency != "EUR" || got.InForce ||
			len(got.Rates) != 2 || got.Rates[1] != "2.500000000000" && got.Rates[1] != "2.5" {
			t.Errorf("GetTenantPriceVersion: %+v %v", got, err)
		}
		called["ListTenantPriceVersions"] = true
		list, err := q.ListTenantPriceVersions(c, queries.ListTenantPriceVersionsParams{AfterID: "00000000-0000-0000-0000-000000000000", PageSize: 10})
		if err != nil || len(list) != 1 || list[0].ID != versionID {
			t.Errorf("ListTenantPriceVersions: %+v %v", list, err)
		}
		called["ListTenantPriceLists"] = true
		lists, err := q.ListTenantPriceLists(c)
		if err != nil || len(lists) != 1 || lists[0].Currency != "EUR" || lists[0].Versions != 1 || lists[0].ModelsPriced != 0 {
			t.Errorf("ListTenantPriceLists: %+v %v", lists, err)
		}
		called["ListBudgetCurrenciesForTenantScope"] = true
		cur, err := q.ListBudgetCurrenciesForTenantScope(c, queries.ListBudgetCurrenciesForTenantScopeParams{ModelID: &modelID})
		if err != nil || len(cur) != 1 || cur[0] != "EUR" {
			t.Errorf("ListBudgetCurrenciesForTenantScope: %v %v", cur, err)
		}
		rows, err := q.LoadTenantPriceVersions(c, []string{"EUR"})
		if err != nil || len(rows) != 1 || rows[0].ID != versionID {
			t.Errorf("LoadTenantPriceVersions: %+v %v", rows, err)
		}
		called["DeleteTenantPriceVersion"] = true
		if n, err := q.DeleteTenantPriceVersion(c, versionID); err != nil || n != 1 {
			t.Errorf("DeleteTenantPriceVersion: %d %v", n, err)
		}
		return nil
	}))
}
