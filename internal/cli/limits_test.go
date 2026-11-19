package cli_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// The operations of sprint 5.2 on the fake.

func (f *fakeAPI) ListLimits(_ context.Context, in adminapi.ListLimitsInput) (*adminapi.LimitRuleList, error) {
	return pick[adminapi.LimitRuleList](f, "listLimits"), f.rec("listLimits", in)
}
func (f *fakeAPI) CreateLimit(_ context.Context, in adminapi.CreateLimitInput) (*adminapi.LimitRule, error) {
	return pick[adminapi.LimitRule](f, "createLimit"), f.rec("createLimit", in)
}
func (f *fakeAPI) GetLimit(_ context.Context, in adminapi.GetLimitInput) (*adminapi.LimitRule, error) {
	return pick[adminapi.LimitRule](f, "getLimit"), f.rec("getLimit", in)
}
func (f *fakeAPI) UpdateLimit(_ context.Context, in adminapi.UpdateLimitInput) (*adminapi.LimitRule, error) {
	return pick[adminapi.LimitRule](f, "updateLimit"), f.rec("updateLimit", in)
}
func (f *fakeAPI) DeleteLimit(_ context.Context, in adminapi.DeleteLimitInput) (*adminapi.LimitRule, error) {
	return pick[adminapi.LimitRule](f, "deleteLimit"), f.rec("deleteLimit", in)
}
func (f *fakeAPI) ListTenantPlatformLimits(_ context.Context, in adminapi.ListTenantPlatformLimitsInput) (*adminapi.TenantPlatformLimitRuleList, error) {
	return pick[adminapi.TenantPlatformLimitRuleList](f, "listTenantPlatformLimits"), f.rec("listTenantPlatformLimits", in)
}
func (f *fakeAPI) ListLimitEvents(_ context.Context, in adminapi.ListLimitEventsInput) (*adminapi.LimitEventList, error) {
	return pick[adminapi.LimitEventList](f, "listLimitEvents"), f.rec("listLimitEvents", in)
}
func (f *fakeAPI) ListPlatformLimits(_ context.Context, in adminapi.ListPlatformLimitsInput) (*adminapi.PlatformLimitRuleList, error) {
	return pick[adminapi.PlatformLimitRuleList](f, "listPlatformLimits"), f.rec("listPlatformLimits", in)
}
func (f *fakeAPI) CreatePlatformLimit(_ context.Context, in adminapi.CreatePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return pick[adminapi.PlatformLimitRule](f, "createPlatformLimit"), f.rec("createPlatformLimit", in)
}
func (f *fakeAPI) GetPlatformLimit(_ context.Context, in adminapi.GetPlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return pick[adminapi.PlatformLimitRule](f, "getPlatformLimit"), f.rec("getPlatformLimit", in)
}
func (f *fakeAPI) UpdatePlatformLimit(_ context.Context, in adminapi.UpdatePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return pick[adminapi.PlatformLimitRule](f, "updatePlatformLimit"), f.rec("updatePlatformLimit", in)
}
func (f *fakeAPI) DeletePlatformLimit(_ context.Context, in adminapi.DeletePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return pick[adminapi.PlatformLimitRule](f, "deletePlatformLimit"), f.rec("deletePlatformLimit", in)
}

const (
	ruleID  = "0192f0f0-0000-7000-8000-000000000040"
	keyIDOf = "tabcdefghijklmnop"
)

var limitRule = adminapi.LimitRule{ID: ruleID, SubjectKind: adminapi.LimitSubjectKindTenant, Provider: "openai", Metric: adminapi.LimitMetricCostPerMonth,
	Amount: "250.5", Currency: "EUR", Enforce: false, Thresholds: []int{80, 100}, CreatedAt: t0, UpdatedAt: t0}

// TestLimitCommands is T-684: every operation of sprint 5.2 has a command (with
// TestEveryOperationHasACommand, T-219), its flags map to the members of the body, a member that is
// not given is not sent, and the amount of a budget is passed as it was written.
func TestLimitCommands(t *testing.T) {
	call := func(t *testing.T, id string, args ...string) any {
		t.Helper()
		r := newRig(t)
		mustRun(t, r, args...)
		return r.f.only(t, id)
	}
	check := func(t *testing.T, what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the API received\n %+v\nwant\n %+v", what, got, want)
		}
	}
	// The default subject is the tenant: the command may send it or leave it out.
	create := func(t *testing.T, id string, args ...string) any {
		t.Helper()
		got := call(t, id, args...)
		if in, ok := got.(adminapi.CreateLimitInput); ok && in.Body.SubjectKind == adminapi.LimitSubjectKindTenant {
			in.Body.SubjectKind = ""
			return in
		}
		return got
	}

	t.Run("limit list, get, delete, platform, events", func(t *testing.T) {
		check(t, "list", call(t, "listLimits", "limit", "list", "--tenant", tid, "--limit", "5", "--sort", "-created_at", "--metric", "cost_per_day", "--provider", "openai", "--model", "gpt-4o"),
			adminapi.ListLimitsInput{Tenant: tid, Limit: 5, Sort: adminapi.SortMinusCreatedAt, Metric: adminapi.LimitMetricCostPerDay, Provider: "openai", Model: "gpt-4o"})
		check(t, "list defaults", call(t, "listLimits", "limit", "list", "--tenant", tid),
			adminapi.ListLimitsInput{Tenant: tid, Limit: 50, Sort: adminapi.SortCreatedAt})
		check(t, "get", call(t, "getLimit", "limit", "get", "--tenant", tid, ruleID), adminapi.GetLimitInput{Tenant: tid, Rule: ruleID})
		check(t, "delete", call(t, "deleteLimit", "limit", "delete", "--tenant", tid, ruleID), adminapi.DeleteLimitInput{Tenant: tid, Rule: ruleID})
		check(t, "platform", call(t, "listTenantPlatformLimits", "limit", "platform", "--tenant", tid, "--limit", "3"),
			adminapi.ListTenantPlatformLimitsInput{Tenant: tid, Limit: 3})
		check(t, "events", call(t, "listLimitEvents", "limit", "events", "--tenant", tid, "--kind", "threshold_reached", "--rule", ruleID, "--limit", "2"),
			adminapi.ListLimitEventsInput{Tenant: tid, Limit: 2, Kind: adminapi.LimitEventKind("threshold_reached"), Rule: ruleID})
		check(t, "events defaults", call(t, "listLimitEvents", "limit", "events", "--tenant", tid), adminapi.ListLimitEventsInput{Tenant: tid, Limit: 50})
	})

	t.Run("limit create", func(t *testing.T) {
		check(t, "a count quota of the tenant", create(t, "createLimit", "limit", "create", "--tenant", tid, "--metric", "requests_per_day", "--limit", "1000"),
			adminapi.CreateLimitInput{Tenant: tid, Body: adminapi.LimitRuleCreate{Metric: adminapi.LimitMetricRequestsPerDay, Limit: 1000, Enforce: true}})
		check(t, "a budget: the amount as written", create(t, "createLimit", "limit", "create", "--tenant", tid, "--metric", "cost_per_month", "--amount", "250.50", "--currency", "EUR"),
			adminapi.CreateLimitInput{Tenant: tid, Body: adminapi.LimitRuleCreate{Metric: adminapi.LimitMetricCostPerMonth, Amount: "250.50", Currency: "EUR", Enforce: true}})
		check(t, "a soft rule with thresholds", create(t, "createLimit", "limit", "create", "--tenant", tid, "--metric", "tokens_per_day", "--limit", "5000000", "--soft", "--threshold", "80", "--threshold", "100"),
			adminapi.CreateLimitInput{Tenant: tid, Body: adminapi.LimitRuleCreate{Metric: adminapi.LimitMetricTokensPerDay, Limit: 5000000, Enforce: false, Thresholds: []int{80, 100}}})
		check(t, "a scope", create(t, "createLimit", "limit", "create", "--tenant", tid, "--metric", "requests_per_minute", "--limit", "5", "--provider", "openai", "--model", "gpt-4o"),
			adminapi.CreateLimitInput{Tenant: tid, Body: adminapi.LimitRuleCreate{Metric: adminapi.LimitMetricRequestsPerMinute, Limit: 5, Provider: "openai", Model: "gpt-4o", Enforce: true}})
		check(t, "a service account by ID", create(t, "createLimit", "limit", "create", "--tenant", tid, "--metric", "concurrent_requests", "--limit", "2", "--subject", "service-account", "--service-account", sid),
			adminapi.CreateLimitInput{Tenant: tid, Body: adminapi.LimitRuleCreate{SubjectKind: adminapi.LimitSubjectKindServiceAccount, SubjectID: sid, Metric: adminapi.LimitMetricConcurrentRequests, Limit: 2, Enforce: true}})
		check(t, "a key", create(t, "createLimit", "limit", "create", "--tenant", tid, "--metric", "requests_per_month", "--limit", "9", "--subject", "key", "--key", keyIDOf),
			adminapi.CreateLimitInput{Tenant: tid, Body: adminapi.LimitRuleCreate{SubjectKind: adminapi.LimitSubjectKindKey, SubjectID: keyIDOf, Metric: adminapi.LimitMetricRequestsPerMonth, Limit: 9, Enforce: true}})

		r := newRig(t)
		r.f.out["listServiceAccounts"] = &adminapi.ServiceAccountList{Items: []adminapi.ServiceAccount{{ID: sid, TenantID: tid, Name: "bot", State: adminapi.ServiceAccountStateActive, CreatedAt: t0, UpdatedAt: t0}}}
		mustRun(t, r, "limit", "create", "--tenant", tid, "--metric", "concurrent_requests", "--limit", "2", "--subject", "service-account", "--service-account", "bot")
		if in, ok := r.f.only(t, "createLimit").(adminapi.CreateLimitInput); !ok || in.Body.SubjectID != sid {
			t.Errorf("a service account by name: %+v, want its ID", in)
		}
		// The amount is a string on the wire.
		r = newRig(t)
		mustRun(t, r, "limit", "create", "--tenant", tid, "--metric", "cost_per_day", "--amount", "0.000001", "--currency", "USD")
		var wire map[string]any
		if err := json.Unmarshal([]byte(r.f.postBody), &wire); err != nil || wire["amount"] != "0.000001" || wire["currency"] != "USD" {
			t.Errorf("the body %s: want the amount 0.000001 as a string (%v)", r.f.postBody, err)
		}
		if _, ok := wire["limit"]; ok {
			t.Errorf("a budget sends a limit: %s", r.f.postBody)
		}

		r = newRig(t)
		for name, args := range map[string][]string{
			"no metric":                        {"limit", "create", "--tenant", tid, "--limit", "5"},
			"both a limit and an amount":       {"limit", "create", "--tenant", tid, "--metric", "cost_per_day", "--limit", "5", "--amount", "5", "--currency", "EUR"},
			"neither a limit nor an amount":    {"limit", "create", "--tenant", tid, "--metric", "requests_per_day"},
			"a service account without one":    {"limit", "create", "--tenant", tid, "--metric", "requests_per_day", "--limit", "5", "--subject", "service-account"},
			"a key without a key":              {"limit", "create", "--tenant", tid, "--metric", "requests_per_day", "--limit", "5", "--subject", "key"},
			"an unknown subject":               {"limit", "create", "--tenant", tid, "--metric", "requests_per_day", "--limit", "5", "--subject", "team"},
			"a limit that is not a number":     {"limit", "create", "--tenant", tid, "--metric", "requests_per_day", "--limit", "many"},
			"a threshold that is not a number": {"limit", "create", "--tenant", tid, "--metric", "requests_per_day", "--limit", "5", "--soft", "--threshold", "half"},
			"get without an ID":                {"limit", "get", "--tenant", tid},
			"delete with two IDs":              {"limit", "delete", "--tenant", tid, ruleID, ruleID},
			"an unknown subcommand":            {"limit", "frobnicate"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("createLimit")) + len(r.f.calls("getLimit")) + len(r.f.calls("deleteLimit")); n != 0 {
			t.Errorf("a refused command reached the API %d times", n)
		}
	})

	t.Run("limit update", func(t *testing.T) {
		check(t, "the limit", call(t, "updateLimit", "limit", "update", "--tenant", tid, "--limit", "500", ruleID),
			adminapi.UpdateLimitInput{Tenant: tid, Rule: ruleID, Body: adminapi.LimitRuleUpdate{Limit: adminapi.Some(500)}})
		check(t, "the amount, as written", call(t, "updateLimit", "limit", "update", "--tenant", tid, "--amount", "12.50", ruleID),
			adminapi.UpdateLimitInput{Tenant: tid, Rule: ruleID, Body: adminapi.LimitRuleUpdate{Amount: adminapi.Some("12.50")}})
		check(t, "soft", call(t, "updateLimit", "limit", "update", "--tenant", tid, "--soft", "--threshold", "90", ruleID),
			adminapi.UpdateLimitInput{Tenant: tid, Rule: ruleID, Body: adminapi.LimitRuleUpdate{Enforce: adminapi.Some(false), Thresholds: adminapi.Some([]int{90})}})
		check(t, "enforce", call(t, "updateLimit", "limit", "update", "--tenant", tid, "--enforce", ruleID),
			adminapi.UpdateLimitInput{Tenant: tid, Rule: ruleID, Body: adminapi.LimitRuleUpdate{Enforce: adminapi.Some(true)}})
		check(t, "thresholds", call(t, "updateLimit", "limit", "update", "--tenant", tid, "--threshold", "50", "--threshold", "75", ruleID),
			adminapi.UpdateLimitInput{Tenant: tid, Rule: ruleID, Body: adminapi.LimitRuleUpdate{Thresholds: adminapi.Some([]int{50, 75})}})
		r := newRig(t)
		mustRun(t, r, "limit", "update", "--tenant", tid, "--no-thresholds", ruleID)
		if r.f.patchBody != `{"thresholds":[]}` {
			t.Errorf("--no-thresholds sends %s, want {\"thresholds\":[]}", r.f.patchBody)
		}
		r = newRig(t)
		mustRun(t, r, "limit", "update", "--tenant", tid, "--limit", "7", ruleID)
		if r.f.patchBody != `{"limit":7}` {
			t.Errorf("update --limit sends %s: only what is given", r.f.patchBody)
		}

		r = newRig(t)
		for name, args := range map[string][]string{
			"nothing to change":            {"limit", "update", "--tenant", tid, ruleID},
			"both soft and enforce":        {"limit", "update", "--tenant", tid, "--soft", "--enforce", ruleID},
			"thresholds and no thresholds": {"limit", "update", "--tenant", tid, "--threshold", "5", "--no-thresholds", ruleID},
			"both a limit and an amount":   {"limit", "update", "--tenant", tid, "--limit", "5", "--amount", "5", ruleID},
			"without an ID":                {"limit", "update", "--tenant", tid, "--limit", "5"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("updateLimit")); n != 0 {
			t.Errorf("a refused command reached updateLimit %d times", n)
		}
	})

	t.Run("platform-limit", func(t *testing.T) {
		check(t, "list", call(t, "listPlatformLimits", "platform-limit", "list", "--limit", "5", "--sort", "-created_at", "--metric", "tokens_per_month"),
			adminapi.ListPlatformLimitsInput{Limit: 5, Sort: adminapi.SortMinusCreatedAt, Metric: adminapi.LimitMetricTokensPerMonth})
		check(t, "get", call(t, "getPlatformLimit", "platform-limit", "get", ruleID), adminapi.GetPlatformLimitInput{Rule: ruleID})
		check(t, "delete", call(t, "deletePlatformLimit", "platform-limit", "delete", ruleID), adminapi.DeletePlatformLimitInput{Rule: ruleID})
		check(t, "a default", call(t, "createPlatformLimit", "platform-limit", "create", "--metric", "requests_per_minute", "--limit", "600"),
			adminapi.CreatePlatformLimitInput{Body: adminapi.PlatformLimitRuleCreate{Metric: adminapi.LimitMetricRequestsPerMinute, Limit: 600, Enforce: true}})
		check(t, "for a tenant, a budget, a scope", call(t, "createPlatformLimit", "platform-limit", "create", "--for-tenant", tid, "--metric", "cost_per_month", "--amount", "1000", "--currency", "USD", "--provider", "openai", "--soft", "--threshold", "90"),
			adminapi.CreatePlatformLimitInput{Body: adminapi.PlatformLimitRuleCreate{Tenant: tid, Metric: adminapi.LimitMetricCostPerMonth, Amount: "1000", Currency: "USD", Provider: "openai", Enforce: false, Thresholds: []int{90}}})
		check(t, "update", call(t, "updatePlatformLimit", "platform-limit", "update", "--limit", "900", "--no-thresholds", ruleID),
			adminapi.UpdatePlatformLimitInput{Rule: ruleID, Body: adminapi.LimitRuleUpdate{Limit: adminapi.Some(900), Thresholds: adminapi.Some([]int{})}})

		r := newRig(t)
		for name, args := range map[string][]string{
			"create without a metric":     {"platform-limit", "create", "--limit", "5"},
			"both a limit and an amount":  {"platform-limit", "create", "--metric", "cost_per_day", "--limit", "5", "--amount", "5", "--currency", "EUR"},
			"update that changes nothing": {"platform-limit", "update", ruleID},
			"get without an ID":           {"platform-limit", "get"},
			"an unknown subcommand":       {"platform-limit", "frobnicate"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
	})

	t.Run("the output is the API's", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getLimit"] = &limitRule
		out, _ := mustRun(t, r, "limit", "get", "-o", "json", "--tenant", tid, ruleID)
		var got adminapi.LimitRule
		if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, limitRule) {
			t.Errorf("limit get: %v\n%s", err, out)
		}
		text, _ := mustRun(t, r, "limit", "get", "--tenant", tid, ruleID)
		for _, want := range []string{ruleID, "cost_per_month", "250.5", "EUR", "80"} {
			if !strings.Contains(text, want) {
				t.Errorf("the text output of limit get lacks %q:\n%s", want, text)
			}
		}
		r.f.out["listLimits"] = &adminapi.LimitRuleList{Items: []adminapi.LimitRule{limitRule}}
		out, _ = mustRun(t, r, "limit", "list", "-o", "json", "--tenant", tid)
		var list struct{ Items []adminapi.LimitRule }
		if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0], limitRule) {
			t.Errorf("limit list: %v\n%s", err, out)
		}
	})
}

// The operations of sprint 5.3 on the fake.

func (f *fakeAPI) GetTenantTimeZone(_ context.Context, in adminapi.GetTenantTimeZoneInput) (*adminapi.TenantTimeZone, error) {
	return pick[adminapi.TenantTimeZone](f, "getTenantTimeZone"), f.rec("getTenantTimeZone", in)
}
func (f *fakeAPI) SetTenantTimeZone(_ context.Context, in adminapi.SetTenantTimeZoneInput) (*adminapi.TenantTimeZone, error) {
	return pick[adminapi.TenantTimeZone](f, "setTenantTimeZone"), f.rec("setTenantTimeZone", in)
}

// TestIntegrityCommands is T-723: the commands of section 5.11 call their operations, with the
// members of the body their flags give; a zone and --default exclude each other.
func TestIntegrityCommands(t *testing.T) {
	effective := t0.Add(72 * time.Hour)
	call := func(t *testing.T, id string, args ...string) (any, string) {
		t.Helper()
		r := newRig(t)
		r.f.out["getTenantTimeZone"] = &adminapi.TenantTimeZone{TimeZone: "Europe/Rome", InForce: "UTC", EffectiveFrom: &effective}
		r.f.out["setTenantTimeZone"] = &adminapi.TenantTimeZone{TimeZone: "Europe/Rome", InForce: "UTC", EffectiveFrom: &effective}
		r.f.out["updateTenant"], r.f.out["listUsageRecords"] = &tenant, &adminapi.UsageRecordList{Items: []adminapi.UsageRecord{usageRecord(usageID(31))}}
		out, _ := mustRun(t, r, args...)
		return r.f.only(t, id), out
	}

	t.Run("limit time-zone get and set", func(t *testing.T) {
		got, out := call(t, "getTenantTimeZone", "limit", "time-zone", "get", "--tenant", tid)
		if !reflect.DeepEqual(got, adminapi.GetTenantTimeZoneInput{Tenant: tid}) {
			t.Errorf("get: %+v", got)
		}
		for _, want := range []string{"time_zone: Europe/Rome", "in_force: UTC", "effective_from: "} {
			if !lineWith(out, want) {
				t.Errorf("the resource lacks the line %q:\n%s", want, out)
			}
		}
		got, _ = call(t, "setTenantTimeZone", "limit", "time-zone", "set", "--tenant", tid, "Europe/Rome")
		if !reflect.DeepEqual(got, adminapi.SetTenantTimeZoneInput{Tenant: tid, Body: adminapi.TenantTimeZoneSet{TimeZone: "Europe/Rome"}}) {
			t.Errorf("set: %+v", got)
		}
		got, _ = call(t, "setTenantTimeZone", "limit", "time-zone", "set", "--tenant", tid, "--default")
		if !reflect.DeepEqual(got, adminapi.SetTenantTimeZoneInput{Tenant: tid, Body: adminapi.TenantTimeZoneSet{TimeZone: ""}}) {
			t.Errorf("set --default: %+v", got)
		}
		r := newRig(t)
		for name, args := range map[string][]string{
			"a zone and --default": {"limit", "time-zone", "set", "--tenant", tid, "--default", "Europe/Rome"},
			"neither":              {"limit", "time-zone", "set", "--tenant", tid},
			"two zones":            {"limit", "time-zone", "set", "--tenant", tid, "Europe/Rome", "Asia/Tokyo"},
			"get with an argument": {"limit", "time-zone", "get", "--tenant", tid, "Europe/Rome"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("setTenantTimeZone")) + len(r.f.calls("getTenantTimeZone")); n != 0 {
			t.Errorf("a refused command reached the API %d times", n)
		}
	})

	t.Run("tenant update accepts --time-zone alone", func(t *testing.T) {
		got, _ := call(t, "updateTenant", "tenant", "update", "--time-zone", "Asia/Tokyo", tid)
		if !reflect.DeepEqual(got, adminapi.UpdateTenantInput{Tenant: tid, Body: adminapi.TenantUpdate{TimeZone: adminapi.Some("Asia/Tokyo")}}) {
			t.Errorf("update: %+v", got)
		}
		r := newRig(t)
		r.f.out["updateTenant"] = &tenant
		mustRun(t, r, "tenant", "update", "--time-zone", "", tid)
		if r.f.patchBody != `{"time_zone":""}` {
			t.Errorf("an empty zone is the default: the body is %s", r.f.patchBody)
		}
		r = newRig(t)
		mustRun(t, r, "tenant", "update", "--name", "acme3", "--time-zone", "Europe/Rome", tid)
		if r.f.patchBody != `{"name":"acme3","time_zone":"Europe/Rome"}` {
			t.Errorf("body %s", r.f.patchBody)
		}
		if code, _, errs := r.run(t, "tenant", "update", tid); code != 2 || errs == "" {
			t.Errorf("an update with nothing to change: exit %d %q", code, errs)
		}
	})

	t.Run("tenant create --time-zone", func(t *testing.T) {
		r := newRig(t)
		r.f.out["createTenant"] = &adminapi.TenantCreated{Tenant: tenant}
		mustRun(t, r, "tenant", "create", "--time-zone", "Europe/Rome", "acme")
		in, ok := r.f.only(t, "createTenant").(adminapi.CreateTenantInput)
		if !ok || in.Body.TimeZone != "Europe/Rome" || in.Body.Name != "acme" {
			t.Errorf("create: %+v", in)
		}
	})

	t.Run("usage list --limit-state", func(t *testing.T) {
		got, out := call(t, "listUsageRecords", "usage", "list", "--tenant", tid, "--limit-state", "unlimited")
		in, ok := got.(adminapi.ListUsageRecordsInput)
		if !ok || in.LimitState != adminapi.LimitStateUnlimited || in.Tenant != tid {
			t.Errorf("usage list: %+v", got)
		}
		if !strings.Contains(out, "LIMITS") {
			t.Errorf("the table has no LIMITS column:\n%s", out)
		}
		r := newRig(t)
		if code, _, errs := r.run(t, "usage", "list", "--tenant", tid, "--limit-state", "sometimes"); code != 2 || errs == "" {
			t.Errorf("an unknown state: exit %d %q", code, errs)
		}
		if n := len(r.f.calls("listUsageRecords")); n != 0 {
			t.Errorf("an unknown state reached the API")
		}
		for _, s := range []string{"limited", "unlimited", "unverified", "not_evaluated"} {
			r := newRig(t)
			mustRun(t, r, "usage", "list", "--tenant", tid, "--limit-state", s)
			if in := r.f.only(t, "listUsageRecords").(adminapi.ListUsageRecordsInput); string(in.LimitState) != s {
				t.Errorf("%s: %+v", s, in)
			}
		}
	})

	t.Run("the tenant table shows the zone", func(t *testing.T) {
		r := newRig(t)
		tn := tenant
		tn.TimeZone, tn.TimeZoneInForce = "Europe/Rome", "UTC"
		tn.TimeZoneEffectiveFrom = &effective
		r.f.pages = map[string]adminapi.TenantList{"": {Items: []adminapi.Tenant{tn}}}
		out, _ := mustRun(t, r, "tenant", "list")
		if !strings.Contains(out, "Europe/Rome") {
			t.Errorf("the tenant list does not show the time zone:\n%s", out)
		}
	})
}
