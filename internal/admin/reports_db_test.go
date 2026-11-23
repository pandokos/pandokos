package admin_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// reportRow is a row of the usage report as the test reads it: the cost is a pointer, null when no
// cost is known.
type reportRow struct {
	PeriodStart, PeriodEnd                      time.Time
	Provider, Model, ModelID                    string
	PrincipalKind, PrincipalID, KeyID           string
	Currency, PriceSource                       string
	Requests, RequestsOK, RejectedLimit         int
	InputTokens, OutputTokens, CacheReadTokens  int
	CacheWriteTokens, ReasoningTokens           int
	EstimatedInputTokens, EstimatedOutputTokens int
	Cost                                        *string
	CostUnknown                                 int
}

type reportDoc struct {
	From, To time.Time
	Period   string
	TimeZone string
	GroupBy  []string
	Rows     []reportRow
}

func (r *reportRow) UnmarshalJSON(b []byte) error {
	var raw struct {
		PeriodStart           time.Time `json:"period_start"`
		PeriodEnd             time.Time `json:"period_end"`
		Provider              string    `json:"provider"`
		Model                 string    `json:"model"`
		ModelID               string    `json:"model_id"`
		PrincipalKind         string    `json:"principal_kind"`
		PrincipalID           string    `json:"principal_id"`
		KeyID                 string    `json:"key_id"`
		Currency              string    `json:"currency"`
		PriceSource           string    `json:"price_source"`
		Requests              int       `json:"requests"`
		RequestsOK            int       `json:"requests_ok"`
		RejectedLimit         int       `json:"rejected_limit"`
		InputTokens           int       `json:"input_tokens"`
		OutputTokens          int       `json:"output_tokens"`
		CacheReadTokens       int       `json:"cache_read_tokens"`
		CacheWriteTokens      int       `json:"cache_write_tokens"`
		ReasoningTokens       int       `json:"reasoning_tokens"`
		EstimatedInputTokens  int       `json:"estimated_input_tokens"`
		EstimatedOutputTokens int       `json:"estimated_output_tokens"`
		Cost                  *string   `json:"cost"`
		CostUnknown           int       `json:"cost_unknown"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*r = reportRow(raw)
	return nil
}

func (d *reportDoc) UnmarshalJSON(b []byte) error {
	var raw struct {
		From     time.Time   `json:"from"`
		To       time.Time   `json:"to"`
		Period   string      `json:"period"`
		TimeZone string      `json:"time_zone"`
		GroupBy  []string    `json:"group_by"`
		Rows     []reportRow `json:"rows"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*d = reportDoc(raw)
	return nil
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func reportOf(t *testing.T, w *pw, key, tenantID, query string) reportDoc {
	t.Helper()
	r := w.do(t, key, "GET", "/v1/tenants/"+tenantID+"/usage-report?"+query, nil)
	var doc reportDoc
	ok(t, r, 200, &doc)
	return doc
}

func rowOf(t *testing.T, rows []reportRow, match func(reportRow) bool) reportRow {
	t.Helper()
	var found []reportRow
	for _, r := range rows {
		if match(r) {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d rows match, want 1: %+v", len(found), rows)
	}
	return found[0]
}

func costOf(r reportRow) string {
	if r.Cost == nil {
		return "null"
	}
	return *r.Cost
}

// TestUsageReport is T-954: periods, groupings, the window and its clipping, the tenant's calendar
// (a month in Europe/Rome across the end of summer time, a day in Asia/Kathmandu, a pending change of
// zone), currencies and price sources kept apart, no cost, requests refused for a limit apart,
// filters, the bounds of the window and of the report, and a tenant with no records.
func TestUsageReport(t *testing.T) {
	w := newPW(t)
	d := w.api.DB
	rome, kathmandu, empty := w.idA, w.idB, "" // tenants A and B, and a third without records
	_ = empty
	d.SetTenantZone(t, rome, "Europe/Rome", "", time.Time{})
	d.SetTenantZone(t, kathmandu, "Asia/Kathmandu", "", time.Time{})
	d.SeedModel(t, "openai", "rpt-m1")
	d.SeedModel(t, "openai", "rpt-m2")
	d.SeedModel(t, "anthropic", "rpt-m3")
	pa, pb := "0192f0f0-0000-7000-8000-0000000000a1", "0192f0f0-0000-7000-8000-0000000000a2"
	row := func(started, model, provider, cost, currency, source string) dbtest.UsageRow {
		u := dbtest.UsageRow{StartedAt: at(started), PrincipalID: pa, KeyID: "tkeyaaaaaaaaaaaaa", Provider: provider, Model: model, InputTokens: 10, OutputTokens: 5, Cost: cost, Currency: currency, PriceSource: source}
		return u
	}
	d.SeedUsage(t, rome,
		row("2026-09-30T21:59:59Z", "rpt-m1", "openai", "1", "EUR", ""),        // September in Rome
		row("2026-09-30T22:00:00Z", "rpt-m1", "openai", "2", "EUR", ""),        // October, first second
		row("2026-10-15T10:00:00Z", "rpt-m2", "openai", "3", "USD", ""),        // another model and currency
		row("2026-10-25T00:30:00Z", "rpt-m1", "openai", "", "", ""),            // no cost
		row("2026-10-31T22:59:59Z", "rpt-m1", "openai", "4", "EUR", ""),        // October, last second (CET)
		row("2026-10-31T23:00:00Z", "rpt-m1", "openai", "8", "EUR", ""),        // November
		row("2026-10-20T12:00:00Z", "rpt-m1", "openai", "16", "EUR", "tenant"), // the tenant's own price list
		row("2026-10-21T12:00:00Z", "rpt-m3", "anthropic", "32", "EUR", ""),
		dbtest.UsageRow{StartedAt: at("2026-10-22T12:00:00Z"), PrincipalID: pb, KeyID: "tkeybbbbbbbbbbbbb", Provider: "openai", Model: "rpt-m1", Outcome: "rejected_limit"})
	d.SeedUsage(t, kathmandu,
		row("2026-10-14T18:14:59Z", "rpt-m1", "openai", "1", "EUR", ""), // 23:59:59 on the 14th in Kathmandu
		row("2026-10-14T18:15:00Z", "rpt-m1", "openai", "2", "EUR", ""), // midnight of the 15th
		row("2026-10-15T18:14:59Z", "rpt-m1", "openai", "4", "EUR", ""))

	t.Run("a month in Europe/Rome", func(t *testing.T) {
		for name, q := range map[string]string{
			"dates":    "from=2026-10-01&to=2026-11-01&period=month&group_by=model",
			"instants": "from=" + url.QueryEscape("2026-09-30T22:00:00Z") + "&to=" + url.QueryEscape("2026-10-31T23:00:00Z") + "&period=month&group_by=model",
		} {
			doc := reportOf(t, w, w.ka, w.a.ID, q)
			if doc.Period != "month" || doc.TimeZone != "Europe/Rome" || strings.Join(doc.GroupBy, ",") != "model" {
				t.Errorf("%s: %+v", name, doc)
			}
			if !doc.From.Equal(at("2026-09-30T22:00:00Z")) || !doc.To.Equal(at("2026-10-31T23:00:00Z")) {
				t.Errorf("%s: the window is %v to %v, want the October of Rome", name, doc.From, doc.To)
			}
			m1eur := rowOf(t, doc.Rows, func(r reportRow) bool {
				return r.Model == "rpt-m1" && r.Currency == "EUR" && r.PriceSource == "catalogue"
			})
			if m1eur.Requests != 2 || costOf(m1eur) != "6" && costOf(m1eur) != "6.000000000000000000" || m1eur.InputTokens != 20 || m1eur.OutputTokens != 10 {
				t.Errorf("%s: rpt-m1 EUR catalogue: %+v (cost %s), want 2 requests and 6 EUR: the records of 21:59:59 and 23:00:00 are outside", name, m1eur, costOf(m1eur))
			}
			if !m1eur.PeriodStart.Equal(at("2026-09-30T22:00:00Z")) || !m1eur.PeriodEnd.Equal(at("2026-10-31T23:00:00Z")) {
				t.Errorf("%s: the period is %v to %v: clipped to the window", name, m1eur.PeriodStart, m1eur.PeriodEnd)
			}
			if own := rowOf(t, doc.Rows, func(r reportRow) bool { return r.Model == "rpt-m1" && r.PriceSource == "tenant" }); own.Currency != "EUR" || own.Requests != 1 {
				t.Errorf("%s: the tenant's price source is not kept apart: %+v", name, own)
			}
			if usd := rowOf(t, doc.Rows, func(r reportRow) bool { return r.Model == "rpt-m2" }); usd.Currency != "USD" || costOf(usd) == "null" {
				t.Errorf("%s: %+v", name, usd)
			}
			// The record without a cost and the request refused for a limit have the same grouping
			// (no currency, no price source), so they share a row, in which the refused request is
			// counted apart from requests.
			none := rowOf(t, doc.Rows, func(r reportRow) bool { return r.Model == "rpt-m1" && r.Currency == "" })
			if none.Cost != nil || none.PriceSource != "" || none.Requests != 1 || none.RequestsOK != 1 || none.RejectedLimit != 1 {
				t.Errorf("%s: a record without a cost and a request refused for a limit: %+v (cost %s), want cost null, 1 request, 1 refused", name, none, costOf(none))
			}
			for _, r := range doc.Rows {
				if r.Currency == "" && r.Cost != nil {
					t.Errorf("%s: a cost without a currency: %+v", name, r)
				}
			}
		}
	})

	t.Run("a day in Asia/Kathmandu", func(t *testing.T) {
		doc := reportOf(t, w, w.kb, w.b.ID, "from=2026-10-14&to=2026-10-16&period=day")
		if doc.TimeZone != "Asia/Kathmandu" || len(doc.Rows) != 2 {
			t.Fatalf("%+v", doc)
		}
		first, second := doc.Rows[0], doc.Rows[1]
		if !first.PeriodStart.Equal(at("2026-10-13T18:15:00Z")) || !first.PeriodEnd.Equal(at("2026-10-14T18:15:00Z")) || first.Requests != 1 {
			t.Errorf("the first day: %+v", first)
		}
		if !second.PeriodStart.Equal(at("2026-10-14T18:15:00Z")) || !second.PeriodEnd.Equal(at("2026-10-15T18:15:00Z")) || second.Requests != 2 || costOf(second) != "6" && costOf(second) != "6.000000000000000000" {
			t.Errorf("the second day: %+v (cost %s): the record at 18:15:00 is in it, the one at 18:14:59 is not", second, costOf(second))
		}
	})

	t.Run("the periods and the groupings", func(t *testing.T) {
		total := reportOf(t, w, w.ka, w.a.ID, "from=2026-10-01&to=2026-11-01&period=total&group_by=provider,model")
		if total.Period != "total" || strings.Join(total.GroupBy, ",") != "provider,model" {
			t.Errorf("%+v", total)
		}
		for _, r := range total.Rows {
			if !r.PeriodStart.Equal(total.From) || !r.PeriodEnd.Equal(total.To) {
				t.Errorf("a total row has the period %v to %v", r.PeriodStart, r.PeriodEnd)
			}
		}
		if r := rowOf(t, total.Rows, func(r reportRow) bool { return r.Provider == "anthropic" }); r.Model != "rpt-m3" || r.Requests != 1 {
			t.Errorf("provider and model: %+v", r)
		}
		days := reportOf(t, w, w.ka, w.a.ID, "from=2026-10-24&to=2026-10-27&period=day&group_by=model")
		var starts []string
		for _, r := range days.Rows {
			starts = append(starts, r.PeriodStart.UTC().Format(time.RFC3339))
		}
		// 25 October is the day summer time ends: 25 hours, from 22:00Z of the 24th to 23:00Z of the 25th.
		if len(days.Rows) != 1 || !days.Rows[0].PeriodStart.Equal(at("2026-10-23T22:00:00Z")) && !days.Rows[0].PeriodStart.Equal(at("2026-10-24T22:00:00Z")) {
			t.Errorf("the days around the end of summer time: %v", starts)
		}
		caller := reportOf(t, w, w.ka, w.a.ID, "from=2026-10-01&to=2026-11-01&period=total&group_by=caller")
		pids := map[string]bool{}
		for _, r := range caller.Rows {
			if r.PrincipalKind != "service_account" || r.PrincipalID == "" || r.Model != "" || r.KeyID != "" {
				t.Errorf("grouped by caller only: %+v", r)
			}
			pids[r.PrincipalID] = true
		}
		if !pids[pa] || !pids[pb] {
			t.Errorf("the callers %v, want both", pids)
		}
		keys := reportOf(t, w, w.ka, w.a.ID, "from=2026-10-01&to=2026-11-01&period=total&group_by=key")
		for _, r := range keys.Rows {
			if r.KeyID == "" || r.PrincipalID != "" || r.Model != "" {
				t.Errorf("grouped by key only: %+v", r)
			}
		}
		if def := reportOf(t, w, w.ka, w.a.ID, "from=2026-10-01&to=2026-11-01&period=total"); strings.Join(def.GroupBy, ",") != "model" {
			t.Errorf("the default grouping is %v, want model", def.GroupBy)
		}
		if def := reportOf(t, w, w.ka, w.a.ID, "from=2026-10-01&to=2026-11-01"); def.Period != "month" {
			t.Errorf("the default period is %q, want month", def.Period)
		}
	})

	t.Run("filters", func(t *testing.T) {
		doc := reportOf(t, w, w.ka, w.a.ID, "from=2026-10-01&to=2026-11-01&period=total&group_by=provider,model&provider=anthropic")
		if len(doc.Rows) != 1 || doc.Rows[0].Provider != "anthropic" {
			t.Errorf("provider filter: %+v", doc.Rows)
		}
		doc = reportOf(t, w, w.ka, w.a.ID, "from=2026-10-01&to=2026-11-01&period=total&group_by=model&model=rpt-m2")
		if len(doc.Rows) != 1 || doc.Rows[0].Model != "rpt-m2" {
			t.Errorf("model filter: %+v", doc.Rows)
		}
	})

	t.Run("the default window is this month up to now", func(t *testing.T) {
		now := time.Now().UTC()
		d.SeedModel(t, "openai", "rpt-m4") // a model of its own: the fixture above has records of any month
		d.SeedUsage(t, w.idA, dbtest.UsageRow{StartedAt: now.Add(-time.Second), PrincipalID: pa, Provider: "openai", Model: "rpt-m4", Cost: "64", Currency: "EUR"})
		doc := reportOf(t, w, w.ka, w.a.ID, "group_by=model")
		if doc.Period != "month" || !doc.To.After(now.Add(-time.Minute)) || !doc.To.Before(now.Add(time.Minute)) || doc.From.After(now) || now.Sub(doc.From) > 32*24*time.Hour {
			t.Errorf("the default window is %v to %v", doc.From, doc.To)
		}
		r := rowOf(t, doc.Rows, func(r reportRow) bool { return r.Model == "rpt-m4" && r.Currency == "EUR" })
		if costOf(r) != "64" && costOf(r) != "64.000000000000000000" {
			t.Errorf("the record of this month is not in the default window: %+v", r)
		}
	})

	t.Run("the window and the report are bounded", func(t *testing.T) {
		if r := w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/usage-report?from=2025-10-01&to=2026-11-04&period=total", nil); r.Status != 200 {
			t.Errorf("400 days: %d %s, want 200", r.Status, r.Body)
		}
		r := w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/usage-report?from=2025-10-01&to=2026-11-05&period=total", nil)
		p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) == 0 {
			t.Errorf("401 days: no field named: %+v", p)
		}
		for name, q := range map[string]string{
			"from after to":  "from=2026-11-01&to=2026-10-01",
			"from equals to": "from=2026-10-01&to=2026-10-01",
			"a bad date":     "from=2026-13-01",
			"a bad period":   "period=week",
			"a bad grouping": "group_by=tenant",
			"a repeated":     "group_by=model,model",
		} {
			if r := w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/usage-report?"+q, nil); r.Status != 400 {
				t.Errorf("%s: %d, want 400", name, r.Status)
			}
		}
		// More than 10000 rows: ten thousand and one keys in one period.
		if _, err := w.api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, key_id, provider, api_family, operation, outcome,
				duration_ms, input_source, output_source, cost_status)
			SELECT $1::uuid, pandokos.uuid_v7(), timestamptz '2026-08-10 12:00:00+00' + g * interval '1 second', md5(g::text), 'service_account', $2, 'tk' || g, 'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none'
			  FROM generate_series(1, 10001) g`, w.a.ID, pa); err != nil {
			t.Fatal(err)
		}
		r = w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/usage-report?from=2026-08-01&to=2026-09-01&period=total&group_by=key", nil)
		p = expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) != 1 || p.Errors[0].Field != "group_by" || p.Errors[0].Message != "the report has more than 10000 rows: narrow the window, the period or the grouping" {
			t.Errorf("the bound of rows: %+v", p.Errors)
		}
		if doc := reportOf(t, w, w.ka, w.a.ID, "from=2026-08-01&to=2026-09-01&period=total&group_by=model"); len(doc.Rows) != 1 || doc.Rows[0].Requests != 10001 {
			t.Errorf("the same window by model: %+v", doc.Rows)
		}
	})

	t.Run("a tenant with no records", func(t *testing.T) {
		tn, key := w.api.Tenant(t, "report-empty")
		r := w.do(t, key, "GET", "/v1/tenants/"+tn.ID+"/usage-report?from=2026-10-01&to=2026-11-01", nil)
		if r.Status != 200 || !strings.Contains(string(r.Body), `"rows":[]`) {
			t.Errorf("%d %s, want rows: []", r.Status, r.Body)
		}
	})

	t.Run("a pending change of zone", func(t *testing.T) {
		tn, key := w.api.Tenant(t, "report-pending")
		tid := tenantID(t, tn.ID)
		// Asia/Kolkata until 2026-10-10 00:00Z, Europe/Rome from then on.
		d.SetTenantZone(t, tid, "Europe/Rome", "Asia/Kolkata", at("2026-10-10T00:00:00Z"))
		d.SeedUsage(t, tid, dbtest.UsageRow{StartedAt: at("2026-10-08T12:00:00Z"), Model: "", Provider: "openai"}, dbtest.UsageRow{StartedAt: at("2026-10-11T12:00:00Z"), Provider: "openai"})
		doc := reportOf(t, w, key, tn.ID, "from=2026-10-07T00:00:00Z&to=2026-10-13T00:00:00Z&period=day&group_by=provider")
		if doc.TimeZone != "Europe/Rome" {
			t.Errorf("the zone in force at the end of the window is %q, want Europe/Rome", doc.TimeZone)
		}
		var sum int
		for i, r := range doc.Rows {
			sum += r.Requests
			if i > 0 && r.PeriodStart.Before(doc.Rows[i-1].PeriodEnd) {
				t.Errorf("periods overlap: %v after %v", r.PeriodStart, doc.Rows[i-1].PeriodEnd)
			}
		}
		if sum != 2 {
			t.Errorf("the rows sum to %d requests, want 2: %+v", sum, doc.Rows)
		}
		// A day before the change starts at a midnight of Kolkata (18:30Z), a day after it at one of Rome (22:00Z in October).
		for _, r := range doc.Rows {
			h := r.PeriodStart.UTC().Format("15:04")
			if r.PeriodEnd.Before(at("2026-10-10T00:00:00Z")) || r.PeriodEnd.Equal(at("2026-10-10T00:00:00Z")) {
				if h != "18:30" {
					t.Errorf("a day before the change starts at %s UTC, want a midnight of Asia/Kolkata (18:30)", h)
				}
			}
			if !r.PeriodStart.Before(at("2026-10-11T00:00:00Z")) && !r.PeriodStart.Before(at("2026-10-10T22:00:00Z")) && h != "22:00" {
				t.Errorf("a day after the change starts at %s UTC, want a midnight of Europe/Rome (22:00)", h)
			}
		}
	})
	_ = fmt.Sprint
}

// TestLimitUsage is T-956: what a day or month rule has used in its current period, equal to the sum
// over the ledger that limits.Sum makes; a per-minute rule is 409; another tenant's rule is 404.
func TestLimitUsage(t *testing.T) {
	w := newPW(t)
	d := w.api.DB
	d.SeedModel(t, "openai", "lu-m1")
	d.SeedModel(t, "openai", "lu-m2")
	d.SeedPrice(t, "openai", "lu-m1", "EUR", time.Time{}) // a cost rule needs a price in its currency
	sa := w.saA.ID
	keyA := decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/virtual-keys", map[string]any{"service_account_id": sa}), 201).Key.ID
	now := time.Now().UTC()
	type rec struct {
		started time.Time
		in, out int64
		cost    string
		model   string
		key     string
	}
	recs := []rec{
		{now.Add(-3 * time.Second), 10, 5, "1.50", "lu-m1", keyA},
		{now.Add(-2 * time.Second), 20, 5, "2.25", "lu-m2", keyA},
		{now.Add(-1 * time.Second), 30, 5, "4", "lu-m1", "tkeybbbbbbbbbbbbb"},
		{now.Add(-26 * time.Hour), 40, 5, "8", "lu-m1", keyA},
		{now.AddDate(0, 0, -20), 50, 5, "16", "lu-m2", keyA},
	}
	for _, r := range recs {
		d.SeedUsage(t, w.idA, dbtest.UsageRow{StartedAt: r.started, PrincipalID: sa, KeyID: r.key, Provider: "openai", Model: r.model, InputTokens: r.in, OutputTokens: r.out, Cost: r.cost, Currency: "EUR"})
	}
	base := "/v1/tenants/" + w.a.ID + "/limits"
	create := func(body map[string]any) adminapi.LimitRule {
		t.Helper()
		body["enforce"] = true
		return decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "POST", base, body), 201)
	}
	type tc struct {
		name   string
		body   map[string]any
		filter func(rec) bool
		tokens bool
		cost   bool
	}
	inWindow := func(from, to time.Time) func(rec) bool {
		return func(r rec) bool { return !r.started.Before(from) && r.started.Before(to) }
	}
	_ = inWindow
	cases := []tc{
		{"requests per day", map[string]any{"metric": "requests_per_day", "limit": 1000}, nil, false, false},
		{"tokens per day", map[string]any{"metric": "tokens_per_day", "limit": 100000}, nil, true, false},
		{"cost per day", map[string]any{"metric": "cost_per_day", "amount": "100", "currency": "EUR"}, nil, false, true},
		{"requests per month", map[string]any{"metric": "requests_per_month", "limit": 1000}, nil, false, false},
		{"tokens per month", map[string]any{"metric": "tokens_per_month", "limit": 100000}, nil, true, false},
		{"cost per month", map[string]any{"metric": "cost_per_month", "amount": "100", "currency": "EUR"}, nil, false, true},
		{"a key-scoped rule", map[string]any{"metric": "requests_per_day", "limit": 1000, "subject_kind": "key", "subject_id": keyA}, func(r rec) bool { return r.key == keyA }, false, false},
		{"a model-scoped rule", map[string]any{"metric": "tokens_per_day", "limit": 1000, "provider": "openai", "model": "lu-m1"}, func(r rec) bool { return r.model == "lu-m1" }, true, false},
	}
	for _, c := range cases {
		rule := create(c.body)
		u := decodeInto[adminapi.LimitUsage](t, w.do(t, w.ka, "GET", base+"/"+rule.ID+"/usage", nil), 200)
		if u.RuleID != rule.ID || u.Metric != string(rule.Metric) || u.AsOf.Before(now) || !u.AsOf.Before(now.Add(time.Minute)) {
			t.Errorf("%s: %+v", c.name, u)
		}
		if !u.PeriodStart.Before(u.PeriodEnd) || u.AsOf.Before(u.PeriodStart) || !u.AsOf.Before(u.PeriodEnd) {
			t.Errorf("%s: as_of %v is not in the period %v to %v", c.name, u.AsOf, u.PeriodStart, u.PeriodEnd)
		}
		var requests, tokens int64
		cost := 0.0
		for _, r := range recs {
			if r.started.Before(u.PeriodStart) || !r.started.Before(u.PeriodEnd) || c.filter != nil && !c.filter(r) {
				continue
			}
			requests++
			tokens += r.in + r.out
			var f float64
			if _, err := fmt.Sscanf(r.cost, "%g", &f); err != nil {
				t.Fatal(err)
			}
			cost += f
		}
		switch {
		case c.cost:
			var got float64
			if _, err := fmt.Sscanf(u.UsedAmount, "%g", &got); err != nil {
				t.Fatal(err)
			}
			if u.Currency != "EUR" || u.UsedAmount == "" || got != cost || u.Used != 0 {
				t.Errorf("%s: used_amount %q used %d currency %q, want %v EUR", c.name, u.UsedAmount, u.Used, u.Currency, cost)
			}
		case c.tokens:
			if int64(u.Used) != tokens || u.UsedAmount != "" {
				t.Errorf("%s: used %d (amount %q), want %d tokens", c.name, u.Used, u.UsedAmount, tokens)
			}
		default:
			if int64(u.Used) != requests || u.UsedAmount != "" {
				t.Errorf("%s: used %d (amount %q), want %d requests", c.name, u.Used, u.UsedAmount, requests)
			}
		}
	}
	t.Run("a per-minute rule is 409", func(t *testing.T) {
		rule := create(map[string]any{"metric": "requests_per_minute", "limit": 10})
		expect(t, w.do(t, w.ka, "GET", base+"/"+rule.ID+"/usage", nil), 409, adminapi.ProblemCodeInvalidState)
	})
	t.Run("another tenant's rule is 404", func(t *testing.T) {
		other := decodeInto[adminapi.LimitRule](t, w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/limits", map[string]any{"metric": "requests_per_day", "limit": 10, "enforce": true}), 201)
		r1 := w.do(t, w.ka, "GET", base+"/"+other.ID+"/usage", nil)
		r2 := w.do(t, w.ka, "GET", base+"/0192f0f0-0000-7000-8000-00000000000a/usage", nil)
		expect(t, r1, 404, adminapi.ProblemCodeNotFound)
		sameProblem(t, "another tenant's rule and a missing one", r1, r2)
	})
	t.Run("usage:read is the permission", func(t *testing.T) {
		// A rule of its own scope: the tenant already has a requests_per_day rule above.
		rule := create(map[string]any{"metric": "tokens_per_month", "limit": 10, "provider": "openai", "model": "lu-m2"})
		viewer := w.api.AsRole(t, w.a.ID, "usage-viewer")
		if r := viewer.Do(t, "GET", base+"/"+rule.ID+"/usage", nil, nil); r.Status != 200 {
			t.Errorf("a usage-viewer: %d %s", r.Status, r.Body)
		}
		inference := w.api.AsRole(t, w.a.ID, "inference")
		if r := inference.Do(t, "GET", base+"/"+rule.ID+"/usage", nil, nil); r.Status != 403 {
			t.Errorf("an inference key: %d", r.Status)
		}
	})
}

// TestReportAndAuditRoutes is T-944: each of the twelve operations of sprint 6.2 is allowed to a
// built-in role exactly when the role holds its permission; a platform key (administrator and
// auditor) is refused on every tenant operation, also with a support action that carries every
// permission a support action may carry; a tenant key is refused on the platform operations.
func TestReportAndAuditRoutes(t *testing.T) {
	w := newPW(t)
	tenantOps := []string{"getUsageReport", "exportUsageRecords", "getLimitUsage", "listAuditEvents", "getAuditHead", "exportAuditEvents", "getMetadataKeys", "setMetadataKeys", "clearMetadataKeys"}
	platformOps := []string{"listPlatformAuditEvents", "getPlatformAuditHead", "exportPlatformAuditEvents"}
	byID := map[string]adminapi.Operation{}
	for _, op := range adminapi.Operations {
		byID[op.ID] = op
	}
	wantPermission := map[string]string{
		"getUsageReport": "usage:read", "exportUsageRecords": "usage:read", "getLimitUsage": "usage:read", "getMetadataKeys": "usage:read",
		"setMetadataKeys": "tenant_setting:write", "clearMetadataKeys": "tenant_setting:write",
		"listAuditEvents": "audit:read", "getAuditHead": "audit:read", "exportAuditEvents": "audit:read",
		"listPlatformAuditEvents": "platform_audit:read", "getPlatformAuditHead": "platform_audit:read", "exportPlatformAuditEvents": "platform_audit:read",
	}
	for id, perm := range wantPermission {
		if byID[id].ID == "" || byID[id].Permission != perm {
			t.Fatalf("%s declares %q, the design says %q", id, byID[id].Permission, perm)
		}
	}
	params := probeParams(t, w.api, w.a.ID)
	call := func(who apitest.Doer, id string) *apitest.Response {
		t.Helper()
		return apitest.Probe(t, who, byID[id], params)
	}

	holds := func(role authz.Role, perm string) bool {
		for _, p := range role.Permissions {
			if p == perm {
				return true
			}
		}
		return false
	}
	for _, role := range authz.Roles() {
		if role.Scope != authz.ScopeTenant {
			continue
		}
		who := w.api.AsRole(t, w.a.ID, role.Name)
		for _, id := range tenantOps {
			r := call(who, id)
			if want := holds(role, wantPermission[id]); want == apitest.Refused(r) || r.Status == 401 {
				t.Errorf("%s as %s: %d %s, want allowed %v", id, role.Name, r.Status, r.Body, want)
			}
		}
		for _, id := range platformOps {
			if r := call(who, id); !apitest.Refused(r) {
				t.Errorf("%s as the tenant role %s: %d, want 403", id, role.Name, r.Status)
			}
		}
	}

	for _, role := range []string{"platform-admin", "platform-auditor"} {
		key := platformKeyOf(t, w.api, role)
		for _, id := range tenantOps {
			if r := call(key, id); !apitest.Refused(r) {
				t.Errorf("%s as a %s key: %d %s, want 403", id, role, r.Status, r.Body)
			}
		}
		for _, id := range platformOps {
			if r := call(key, id); apitest.Refused(r) || r.Status == 401 {
				t.Errorf("%s as a %s key: %d %s, want allowed", id, role, r.Status, r.Body)
			}
		}
	}

	t.Run("a support action does not open the reports or the audit log", func(t *testing.T) {
		plat := w.api.PlatformCaller()
		apitest.OpenSupport(t, plat, w.a.ID, authz.SupportPermissions()...)
		for _, id := range tenantOps {
			if r := call(plat, id); !apitest.Refused(r) {
				t.Errorf("%s with a support action of every Support permission: %d %s, want 403", id, r.Status, r.Body)
			}
		}
		// Opening an action with audit:read is refused: it is not a permission that a support action may carry.
		r := plat.Do(t, "POST", "/v1/tenant-support/"+w.a.ID+"/actions", map[string]any{"reason": apitest.Reason("audit"), "permissions": []string{authz.AuditRead}}, nil)
		expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		r = plat.Do(t, "POST", "/v1/tenant-support/"+w.a.ID+"/actions", map[string]any{"reason": apitest.Reason("usage"), "permissions": []string{authz.UsageRead}}, nil)
		expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
	})
}

// platformKeyOf makes a platform key with the role and returns a caller that holds it.
func platformKeyOf(t *testing.T, api *apitest.API, role string) *apitest.Caller {
	t.Helper()
	var key struct {
		Secret string `json:"secret"`
	}
	ok(t, api.Do(t, "POST", "/v1/platform-keys", api.PlatformKey(), map[string]any{"description": apitest.UniqueName("as-" + role), "role": role}, nil), 201, &key)
	return &apitest.Caller{API: api, Key: key.Secret, Name: role}
}
