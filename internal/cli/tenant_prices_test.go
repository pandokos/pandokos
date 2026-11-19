package cli_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// The operations of sprint 5.4 on the fake.

func (f *fakeAPI) ListPriceLists(_ context.Context, in adminapi.ListPriceListsInput) (*adminapi.TenantPriceListList, error) {
	return pick[adminapi.TenantPriceListList](f, "listPriceLists"), f.rec("listPriceLists", in)
}
func (f *fakeAPI) ListPriceListVersions(_ context.Context, in adminapi.ListPriceListVersionsInput) (*adminapi.TenantPriceVersionList, error) {
	return pick[adminapi.TenantPriceVersionList](f, "listPriceListVersions"), f.rec("listPriceListVersions", in)
}
func (f *fakeAPI) CreatePriceListVersion(_ context.Context, in adminapi.CreatePriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	return pick[adminapi.TenantPriceVersion](f, "createPriceListVersion"), f.rec("createPriceListVersion", in)
}
func (f *fakeAPI) GetPriceListVersion(_ context.Context, in adminapi.GetPriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	return pick[adminapi.TenantPriceVersion](f, "getPriceListVersion"), f.rec("getPriceListVersion", in)
}
func (f *fakeAPI) DeletePriceListVersion(_ context.Context, in adminapi.DeletePriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	return pick[adminapi.TenantPriceVersion](f, "deletePriceListVersion"), f.rec("deletePriceListVersion", in)
}
func (f *fakeAPI) GetActiveCurrency(_ context.Context, in adminapi.GetActiveCurrencyInput) (*adminapi.ActiveCurrency, error) {
	return pick[adminapi.ActiveCurrency](f, "getActiveCurrency"), f.rec("getActiveCurrency", in)
}
func (f *fakeAPI) SetActiveCurrency(_ context.Context, in adminapi.SetActiveCurrencyInput) (*adminapi.ActiveCurrency, error) {
	return pick[adminapi.ActiveCurrency](f, "setActiveCurrency"), f.rec("setActiveCurrency", in)
}

// TestPriceListCommands is T-763: the commands and flags of section 5.14 call their operations; --end
// excludes --rate; --default and a currency are exclusive; --model splits at the first slash.
func TestPriceListCommands(t *testing.T) {
	priceID := "0192f0f0-0000-7000-8000-0000000000c1"
	call := func(t *testing.T, id string, args ...string) any {
		t.Helper()
		r := newRig(t)
		mustRun(t, r, args...)
		return r.f.only(t, id)
	}
	check := func(t *testing.T, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the API received\n %+v\nwant\n %+v", got, want)
		}
	}
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	t.Run("lists, list, get and delete", func(t *testing.T) {
		check(t, call(t, "listPriceLists", "price-list", "lists", "--tenant", tid), adminapi.ListPriceListsInput{Tenant: tid})
		check(t, call(t, "listPriceListVersions", "price-list", "list", "--tenant", tid), adminapi.ListPriceListVersionsInput{Tenant: tid, Limit: 50})
		check(t, call(t, "listPriceListVersions", "price-list", "list", "--tenant", tid, "--currency", "EUR", "--model", "openai/gpt-4o", "--at", from.Format(time.RFC3339)),
			adminapi.ListPriceListVersionsInput{Tenant: tid, Limit: 50, Currency: "EUR", Provider: "openai", Model: "gpt-4o", At: from.Format(time.RFC3339)})
		check(t, call(t, "listPriceListVersions", "price-list", "list", "--tenant", tid, "--provider", "openai"), adminapi.ListPriceListVersionsInput{Tenant: tid, Limit: 50, Provider: "openai"})
		check(t, call(t, "listPriceListVersions", "price-list", "list", "--tenant", tid, "--model", "meta/llama/3"), adminapi.ListPriceListVersionsInput{Tenant: tid, Limit: 50, Provider: "meta", Model: "llama/3"}) // the first slash
		check(t, call(t, "getPriceListVersion", "price-list", "get", "--tenant", tid, priceID), adminapi.GetPriceListVersionInput{Tenant: tid, Price: priceID})
		check(t, call(t, "deletePriceListVersion", "price-list", "delete", "--tenant", tid, priceID), adminapi.DeletePriceListVersionInput{Tenant: tid, Price: priceID})
	})

	t.Run("set", func(t *testing.T) {
		check(t, call(t, "createPriceListVersion", "price-list", "set", "--tenant", tid, "--currency", "EUR", "--model", "openai/gpt-4o",
			"--rate", "input=2.5", "--rate", "output=10", "--rate", "input@200000=5", "--valid-from", from.Format(time.RFC3339), "--note", "negotiated"),
			adminapi.CreatePriceListVersionInput{Tenant: tid, Body: adminapi.TenantPriceCreate{Provider: "openai", Model: "gpt-4o", Currency: "EUR", ValidFrom: &from, Note: "negotiated",
				Rates: []adminapi.PriceRate{
					{Category: adminapi.PriceCategoryInput, Rate: "2.5"}, {Category: adminapi.PriceCategoryOutput, Rate: "10"}, {Category: adminapi.PriceCategoryInput, Threshold: 200000, Rate: "5"}}}})
		check(t, call(t, "createPriceListVersion", "price-list", "set", "--tenant", tid, "--currency", "EUR", "--model", "openai/gpt-4o", "--end", "--valid-from", from.Format(time.RFC3339)),
			adminapi.CreatePriceListVersionInput{Tenant: tid, Body: adminapi.TenantPriceCreate{Provider: "openai", Model: "gpt-4o", Currency: "EUR", Kind: adminapi.TenantPriceKindEnd, ValidFrom: &from}})
		check(t, call(t, "createPriceListVersion", "price-list", "set", "--tenant", tid, "--currency", "USD", "--model", "openai/gpt-4o", "--rate", "input=1"),
			adminapi.CreatePriceListVersionInput{Tenant: tid, Body: adminapi.TenantPriceCreate{Provider: "openai", Model: "gpt-4o", Currency: "USD", Rates: []adminapi.PriceRate{{Category: adminapi.PriceCategoryInput, Rate: "1"}}}})
		r := newRig(t)
		for name, args := range map[string][]string{
			"--end with --rate":                {"price-list", "set", "--tenant", tid, "--currency", "EUR", "--model", "openai/gpt-4o", "--end", "--rate", "input=1"},
			"no rate and no --end":             {"price-list", "set", "--tenant", tid, "--currency", "EUR", "--model", "openai/gpt-4o"},
			"no currency":                      {"price-list", "set", "--tenant", tid, "--model", "openai/gpt-4o", "--rate", "input=1"},
			"no model":                         {"price-list", "set", "--tenant", tid, "--currency", "EUR", "--rate", "input=1"},
			"a model without a slash":          {"price-list", "set", "--tenant", tid, "--currency", "EUR", "--model", "gpt-4o", "--rate", "input=1"},
			"a rate without a value":           {"price-list", "set", "--tenant", tid, "--currency", "EUR", "--model", "openai/gpt-4o", "--rate", "input"},
			"a bad time":                       {"price-list", "set", "--tenant", tid, "--currency", "EUR", "--model", "openai/gpt-4o", "--rate", "input=1", "--valid-from", "tomorrow"},
			"get without an ID":                {"price-list", "get", "--tenant", tid},
			"an unknown subcommand":            {"price-list", "frobnicate"},
			"list with --model and --provider": {"price-list", "list", "--tenant", tid, "--model", "openai/gpt-4o", "--provider", "openai"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("createPriceListVersion")) + len(r.f.calls("getPriceListVersion")); n != 0 {
			t.Errorf("a refused command reached the API %d times", n)
		}
	})

	t.Run("currency get and set", func(t *testing.T) {
		check(t, call(t, "getActiveCurrency", "price-list", "currency", "get", "--tenant", tid), adminapi.GetActiveCurrencyInput{Tenant: tid})
		check(t, call(t, "setActiveCurrency", "price-list", "currency", "set", "--tenant", tid, "EUR"), adminapi.SetActiveCurrencyInput{Tenant: tid, Body: adminapi.ActiveCurrencySet{Currency: "EUR"}})
		check(t, call(t, "setActiveCurrency", "price-list", "currency", "set", "--tenant", tid, "--default"), adminapi.SetActiveCurrencyInput{Tenant: tid, Body: adminapi.ActiveCurrencySet{Currency: ""}})
		r := newRig(t)
		for name, args := range map[string][]string{
			"a currency and --default": {"price-list", "currency", "set", "--tenant", tid, "--default", "EUR"},
			"neither":                  {"price-list", "currency", "set", "--tenant", tid},
			"two currencies":           {"price-list", "currency", "set", "--tenant", tid, "EUR", "GBP"},
			"get with an argument":     {"price-list", "currency", "get", "--tenant", tid, "EUR"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("setActiveCurrency")) + len(r.f.calls("getActiveCurrency")); n != 0 {
			t.Errorf("a refused command reached the API %d times", n)
		}
	})

	t.Run("currency set prints the resource and the limits that become inactive", func(t *testing.T) {
		effective := from
		r := newRig(t)
		r.f.out["setActiveCurrency"] = &adminapi.ActiveCurrency{Currency: "EUR", InForce: "USD", EffectiveFrom: &effective,
			InactiveLimits: []adminapi.InactiveLimit{{Level: "tenant", RuleID: ruleID, Metric: adminapi.LimitMetricCostPerMonth, Currency: "USD"}}}
		out, _ := mustRun(t, r, "price-list", "currency", "set", "--tenant", tid, "EUR")
		for _, want := range []string{"currency: EUR", "in_force: USD", "effective_from: ", "limits that will be inactive:", ruleID, "cost_per_month", "USD", "tenant"} {
			if !strings.Contains(out, want) {
				t.Errorf("the output lacks %q:\n%s", want, out)
			}
		}
		r = newRig(t)
		r.f.out["setActiveCurrency"] = &adminapi.ActiveCurrency{Currency: "", InForce: "USD"}
		if out, _ := mustRun(t, r, "price-list", "currency", "set", "--tenant", tid, "--default"); strings.Contains(out, "limits that will be inactive:") {
			t.Errorf("a heading without limits:\n%s", out)
		}
	})

	t.Run("limit list shows active, tenant get the active currency, usage list -o json price_source", func(t *testing.T) {
		r := newRig(t)
		inactive := limitRule
		inactive.Active = false
		r.f.out["listLimits"] = &adminapi.LimitRuleList{Items: []adminapi.LimitRule{inactive}}
		out, _ := mustRun(t, r, "limit", "list", "--tenant", tid)
		if !strings.Contains(strings.ToLower(strings.SplitN(out, "\n", 2)[0]), "active") {
			t.Errorf("limit list has no column active:\n%s", out)
		}
		tn := tenant
		tn.ActiveCurrency, tn.ActiveCurrencyInForce = "EUR", "USD"
		r.f.out["getTenant"] = &tn
		out, _ = mustRun(t, r, "tenant", "get", tid)
		if !strings.Contains(out, "active_currency") || !strings.Contains(out, "EUR") {
			t.Errorf("tenant get lacks the active currency:\n%s", out)
		}
		rec := usageRecord(usageID(41))
		rec.PriceSource = "tenant"
		r.f.out["listUsageRecords"] = &adminapi.UsageRecordList{Items: []adminapi.UsageRecord{rec}}
		out, _ = mustRun(t, r, "usage", "list", "-o", "json", "--tenant", tid)
		if !strings.Contains(out, `"price_source": "tenant"`) && !strings.Contains(out, `"price_source":"tenant"`) {
			t.Errorf("usage list -o json lacks price_source:\n%s", out)
		}
	})
}
