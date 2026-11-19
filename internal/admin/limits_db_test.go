package admin_test

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// lw is the world of the tests of limit rules: two tenants with an administrator each, a
// catalogue, and for tenant A a virtual key and a subject mapping, for B a service account and a key.
type lw struct {
	*iw
	cat        apitest.Catalogue
	keyA, keyB string // virtual key IDs
	mappingA   string
	baseA      string
}

func newLW(t *testing.T) *lw { return newLWWith(t, true) }

// newLWWith builds the world of the tests of limit rules; priced says whether its models carry
// prices (the budgets of most tests need one).
func newLWWith(t *testing.T, priced bool) *lw {
	t.Helper()
	w := &lw{iw: newIW(t)}
	w.cat = w.api.Catalog(t)
	if priced {
		w.price(t)
	}
	return w.finish(t)
}

// price gives the models of the world prices. The budgets of these tests name a currency, and
// since sprint 5.3 a budget is refused when no price that can be in force for its scope is in its
// currency: one model of the world is priced in EUR and another in USD.
func (w *lw) price(t *testing.T) {
	t.Helper()
	usd := decodeInto[adminapi.Model](t, w.do(t, w.plat, "POST", "/v1/models", map[string]any{"provider": w.cat.Provider.Name, "name": apitest.UniqueName("synthetic-usd"), "availability": "all"}), 201)
	w.api.DB.SeedPrice(t, w.cat.Provider.Name, w.cat.All.Name, "EUR", time.Time{})
	w.api.DB.SeedPrice(t, w.cat.Provider.Name, usd.Name, "USD", time.Time{})
}

func (w *lw) finish(t *testing.T) *lw {
	t.Helper()
	mk := func(tenantID, sa, key string) string {
		k := decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, key, "POST", "/v1/tenants/"+tenantID+"/virtual-keys", map[string]any{"service_account_id": sa}), 201)
		return k.Key.ID
	}
	w.keyA, w.keyB = mk(w.a.ID, w.saA.ID, w.ka), mk(w.b.ID, w.saB.ID, w.kb)
	issuer := w.api.SeedTokenIssuer(t, w.a.ID)
	w.mappingA = w.api.SeedSubjectMapping(t, w.a.ID, issuer, w.saA.ID)
	w.baseA = "/v1/tenants/" + w.a.ID + "/limits"
	return w
}

func (w *lw) create(t *testing.T, body map[string]any) adminapi.LimitRule {
	t.Helper()
	return decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "POST", w.baseA, body), 201)
}

func ruleIDs(l adminapi.LimitRuleList) []string {
	var out []string
	for _, r := range l.Items {
		out = append(out, r.ID)
	}
	return out
}

// TestLimits is T-675: the seven tenant operations on limit rules.
func TestLimits(t *testing.T) {
	w := newLW(t)

	t.Run("creation with the defaults, and the wire form", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.b.ID)
		r := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "requests_per_day", "limit": 1000})
		rule := decodeInto[adminapi.LimitRule](t, r, 201)
		if rule.SubjectKind != adminapi.LimitSubjectKindTenant || rule.SubjectID != "" || rule.Provider != "" || rule.Model != "" || rule.Metric != adminapi.LimitMetricRequestsPerDay ||
			rule.Limit != 1000 || rule.Amount != "" || rule.Currency != "" || !rule.Enforce || rule.Thresholds == nil || len(rule.Thresholds) != 0 ||
			len(rule.ID) != 36 || rule.CreatedAt.IsZero() || rule.UpdatedAt.Before(rule.CreatedAt) {
			t.Errorf("created: %+v", rule)
		}
		raw := w.do(t, w.ka, "GET", w.baseA+"/"+rule.ID, nil)
		if !strings.Contains(string(raw.Body), `"thresholds":[]`) || !strings.Contains(string(raw.Body), `"amount":""`) || !strings.Contains(string(raw.Body), `"enforce":true`) {
			t.Errorf("the wire form: %s", raw.Body)
		}
		if got := decodeInto[adminapi.LimitRule](t, raw, 200); !reflect.DeepEqual(got, rule) {
			t.Errorf("get after create:\n%+v\n%+v", got, rule)
		}
		if w.api.SnapshotTenant(t, w.b.ID) != before {
			t.Error("a creation in A changed tenant B")
		}
	})

	t.Run("a budget: the amount, the currency, soft thresholds stored ascending", func(t *testing.T) {
		rule := w.create(t, map[string]any{"metric": "cost_per_month", "amount": "250.50", "currency": "EUR", "enforce": false, "thresholds": []int{100, 80}})
		if rule.Limit != 0 || rule.Amount != "250.5" || rule.Currency != "EUR" || rule.Enforce || !reflect.DeepEqual(rule.Thresholds, []int{80, 100}) {
			t.Errorf("created: %+v", rule)
		}
		big := w.create(t, map[string]any{"metric": "cost_per_day", "amount": "1000000000", "currency": "USD", "thresholds": []int{50}})
		if big.Amount != "1000000000" || !big.Enforce {
			t.Errorf("the largest amount: %+v", big)
		}
		small := w.create(t, map[string]any{"metric": "cost_per_day", "amount": "0.000001", "currency": "EUR", "provider": w.cat.Provider.Name})
		if small.Amount != "0.000001" {
			t.Errorf("the smallest amount: %+v", small)
		}
		if r := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "tokens_per_month", "limit": 1000000000000, "provider": w.cat.Provider.Name, "model": w.cat.All.Name}); r.Status != 201 {
			t.Errorf("the largest count: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("each rule of the validation, with its field", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.a.ID)
		count := func(extra map[string]any) map[string]any {
			b := map[string]any{"metric": "requests_per_month", "limit": 10}
			for k, v := range extra {
				if v == nil {
					delete(b, k)
				} else {
					b[k] = v
				}
			}
			return b
		}
		cost := func(extra map[string]any) map[string]any {
			b := map[string]any{"metric": "cost_per_month", "amount": "10", "currency": "EUR"}
			for k, v := range extra {
				if v == nil {
					delete(b, k)
				} else {
					b[k] = v
				}
			}
			return b
		}
		for _, c := range []struct {
			what  string
			body  map[string]any
			field string
		}{
			{"an unknown metric", count(map[string]any{"metric": "tokens_per_hour"}), "metric"},
			{"no metric", count(map[string]any{"metric": nil}), "metric"},
			{"an unknown subject kind", count(map[string]any{"subject_kind": "team"}), "subject_kind"},
			{"a count metric without a limit", count(map[string]any{"limit": nil}), "limit"},
			{"a limit of 0", count(map[string]any{"limit": 0}), "limit"},
			{"a limit above 10^12", count(map[string]any{"limit": 1000000000001}), "limit"},
			{"a negative limit", count(map[string]any{"limit": -5}), "limit"},
			{"an amount on a count metric", count(map[string]any{"amount": "10"}), "amount"},
			{"a currency on a count metric", count(map[string]any{"currency": "EUR"}), "currency"},
			{"a cost metric without an amount", cost(map[string]any{"amount": nil}), "amount"},
			{"a cost metric without a currency", cost(map[string]any{"currency": nil}), "currency"},
			{"a limit on a cost metric", cost(map[string]any{"limit": 5}), "limit"},
			{"an amount of 0", cost(map[string]any{"amount": "0"}), "amount"},
			{"an amount above 10^9", cost(map[string]any{"amount": "1000000000.000001"}), "amount"},
			{"seven fraction digits", cost(map[string]any{"amount": "0.0000001"}), "amount"},
			{"a negative amount", cost(map[string]any{"amount": "-1"}), "amount"},
			{"an amount that is not a number", cost(map[string]any{"amount": "ten"}), "amount"},
			{"an amount with an exponent", cost(map[string]any{"amount": "1e3"}), "amount"},
			{"a lower-case currency", cost(map[string]any{"currency": "eur"}), "currency"},
			{"a four-letter currency", cost(map[string]any{"currency": "EURO"}), "currency"},
			{"a rate metric that does not enforce", map[string]any{"metric": "requests_per_minute", "limit": 5, "enforce": false, "thresholds": []int{50}}, "enforce"},
			{"a rate metric with thresholds", map[string]any{"metric": "tokens_per_minute", "limit": 5, "thresholds": []int{50}}, "thresholds"},
			{"a soft rule without thresholds", count(map[string]any{"enforce": false}), "thresholds"},
			{"a repeated threshold", count(map[string]any{"thresholds": []int{50, 50}}), "thresholds"},
			{"a threshold of 0", count(map[string]any{"thresholds": []int{0}}), "thresholds"},
			{"a threshold of 101", count(map[string]any{"thresholds": []int{101}}), "thresholds"},
			{"five thresholds", count(map[string]any{"thresholds": []int{10, 20, 30, 40, 50}}), "thresholds"},
			{"the tenant with a subject ID", count(map[string]any{"subject_kind": "tenant", "subject_id": w.saA.ID}), "subject_id"},
			{"a service account without an ID", count(map[string]any{"subject_kind": "service_account"}), "subject_id"},
			{"a key without an ID", count(map[string]any{"subject_kind": "key"}), "subject_id"},
			{"a service account that does not exist", count(map[string]any{"subject_kind": "service_account", "subject_id": "0192f0f0-0000-7000-8000-0000000000ee"}), "subject_id"},
			{"a key that does not exist", count(map[string]any{"subject_kind": "key", "subject_id": "tnosuchkeyid000000"}), "subject_id"},
			{"a model without a provider", count(map[string]any{"model": w.cat.All.Name}), "model"},
			{"a provider that does not exist", count(map[string]any{"provider": "no-such-provider"}), "provider"},
			{"a model that does not exist", count(map[string]any{"provider": w.cat.Provider.Name, "model": "no-such-model"}), "model"},
			{"a model the tenant cannot see", count(map[string]any{"provider": w.cat.Provider.Name, "model": w.cat.Selected.Name}), "model"},
			{"a provider name that is not a name", count(map[string]any{"provider": "Not A Name"}), "provider"},
		} {
			r := w.do(t, w.ka, "POST", w.baseA, c.body)
			if r.Status != 400 {
				t.Errorf("%s: status %d, want 400 on %s\n%s", c.what, r.Status, c.field, r.Body)
				continue
			}
			p := problem(t, r)
			if p.Code != adminapi.ProblemCodeInvalidRequest || !namesMember(p, c.field) {
				t.Errorf("%s: %+v does not name %s", c.what, p, c.field)
			}
		}
		if w.api.SnapshotTenant(t, w.a.ID) != before {
			t.Error("a refused creation changed a row")
		}
		for what, body := range map[string]any{"an unknown member": count(map[string]any{"tenant_id": w.b.ID}), "an array": []any{}, "null": nil, "no body": ""} {
			if r := w.do(t, w.ka, "POST", w.baseA, body); r.Status != 400 && r.Status != 415 {
				t.Errorf("%s: status %d\n%s", what, r.Status, r.Body)
			}
		}
	})

	t.Run("a subject or a scope of another tenant is refused as one that does not exist", func(t *testing.T) {
		body := func(kind, id string) map[string]any {
			return map[string]any{"metric": "requests_per_day", "limit": 5, "subject_kind": kind, "subject_id": id}
		}
		elsewhere := w.do(t, w.ka, "POST", w.baseA, body("service_account", w.saB.ID))
		nowhere := w.do(t, w.ka, "POST", w.baseA, body("service_account", "0192f0f0-0000-7000-8000-0000000000ee"))
		expect(t, elsewhere, 400, adminapi.ProblemCodeInvalidRequest)
		sameProblem(t, "a service account of another tenant and one that does not exist", elsewhere, nowhere)
		sameProblem(t, "a key of another tenant and one that does not exist", w.do(t, w.ka, "POST", w.baseA, body("key", w.keyB)), w.do(t, w.ka, "POST", w.baseA, body("key", "tnosuchkeyid000000")))
		// The subjects of the tenant are accepted: a service account, a virtual key, a subject mapping.
		for _, c := range []struct{ kind, id, metric string }{
			{"service_account", w.saA.ID, "requests_per_day"}, {"key", w.keyA, "requests_per_day"}, {"key", w.mappingA, "requests_per_day"},
		} {
			rule := w.create(t, map[string]any{"metric": c.metric, "limit": 5, "subject_kind": c.kind, "subject_id": c.id})
			if string(rule.SubjectKind) != c.kind || rule.SubjectID != c.id {
				t.Errorf("%s %s: %+v", c.kind, c.id, rule)
			}
		}
		// A model the tenant cannot see is the same as a model that is not there.
		hidden := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "requests_per_month", "limit": 5, "provider": w.cat.Provider.Name, "model": w.cat.Selected.Name})
		missing := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "requests_per_month", "limit": 5, "provider": w.cat.Provider.Name, "model": "no-such-model"})
		sameProblem(t, "a model that is not visible and one that is not there", hidden, missing)
	})

	t.Run("a rule of the same subject, scope and metric is a 409; a budget is one per currency", func(t *testing.T) {
		first := map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": w.cat.Provider.Name}
		w.create(t, first)
		expect(t, w.do(t, w.ka, "POST", w.baseA, first), 409, adminapi.ProblemCodeConflict)
		// Another scope, subject or metric is another rule.
		for _, b := range []map[string]any{
			{"metric": "tokens_per_day", "limit": 5},
			{"metric": "tokens_per_month", "limit": 5, "provider": w.cat.Provider.Name},
			{"metric": "tokens_per_day", "limit": 5, "provider": w.cat.Provider.Name, "model": w.cat.All.Name},
			{"metric": "tokens_per_day", "limit": 5, "provider": w.cat.Provider.Name, "subject_kind": "service_account", "subject_id": w.saA.ID},
		} {
			if r := w.do(t, w.ka, "POST", w.baseA, b); r.Status != 201 {
				t.Errorf("%v: %d\n%s", b, r.Status, r.Body)
			}
		}
		// The model has a price in both currencies (the second starts later, which counts), so that the second budget passes the currency check
		// of 5.8 and meets the conflict (a budget has one currency).
		w.api.DB.SeedPrice(t, w.cat.Provider.Name, w.cat.All.Name, "USD", time.Now().Add(24*time.Hour))
		w.create(t, map[string]any{"metric": "cost_per_day", "amount": "5", "currency": "EUR", "provider": w.cat.Provider.Name, "model": w.cat.All.Name})
		// Sprint 5.4 (5.7): a rule is one per subject, scope, metric and currency. A budget in another currency
		// coexists (that is how a change of currency is prepared); the same currency twice is the 409.
		if r := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "cost_per_day", "amount": "7", "currency": "USD", "provider": w.cat.Provider.Name, "model": w.cat.All.Name}); r.Status != 201 {
			t.Errorf("a budget in another currency: %d\n%s", r.Status, r.Body)
		}
		expect(t, w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "cost_per_day", "amount": "9", "currency": "EUR", "provider": w.cat.Provider.Name, "model": w.cat.All.Name}), 409, adminapi.ProblemCodeConflict)
		// The same rule in another tenant is fine.
		if r := w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/limits", map[string]any{"metric": "tokens_per_day", "limit": 5}); r.Status != 201 {
			t.Errorf("the same rule in tenant B: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("pagination, sorting and filters", func(t *testing.T) {
		pw := newLW(t)
		base := pw.baseA
		for i, m := range []string{"requests_per_day", "requests_per_month", "tokens_per_day", "tokens_per_month", "concurrent_requests"} {
			decodeInto[adminapi.LimitRule](t, pw.do(t, pw.ka, "POST", base, map[string]any{"metric": m, "limit": 10 + i}), 201)
		}
		decodeInto[adminapi.LimitRule](t, pw.do(t, pw.ka, "POST", base, map[string]any{"metric": "requests_per_day", "limit": 3, "subject_kind": "key", "subject_id": pw.keyA}), 201)
		decodeInto[adminapi.LimitRule](t, pw.do(t, pw.ka, "POST", base, map[string]any{"metric": "requests_per_day", "limit": 4, "provider": pw.cat.Provider.Name, "model": pw.cat.All.Name}), 201)
		list := func(query string) adminapi.LimitRuleList {
			return decodeInto[adminapi.LimitRuleList](t, pw.do(t, pw.ka, "GET", base+query, nil), 200)
		}
		all := list("")
		if len(all.Items) != 7 {
			t.Fatalf("%d rules, want 7", len(all.Items))
		}
		asc, desc := ruleIDs(all), ruleIDs(list("?sort=-created_at"))
		for i := range asc {
			if asc[i] != desc[len(desc)-1-i] {
				t.Fatalf("the descending order is not the reverse of the ascending: %v / %v", asc, desc)
			}
		}
		if !sort.StringsAreSorted(asc) {
			t.Errorf("the default order is the creation order (time-ordered IDs): %v", asc)
		}
		for _, s := range []string{"created_at", "-created_at"} {
			var got []string
			cursor := ""
			for pages := 0; ; pages++ {
				l := list("?sort=" + s + "&limit=3" + cursor)
				if len(l.Items) > 3 || pages > 4 {
					t.Fatalf("sort %s: a page of %d items, %d pages", s, len(l.Items), pages)
				}
				got = append(got, ruleIDs(l)...)
				if l.NextCursor == "" {
					break
				}
				cursor = "&cursor=" + l.NextCursor
			}
			want := asc
			if s == "-created_at" {
				want = desc
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("sort %s: the pages give %v, the whole list %v", s, got, want)
			}
		}
		count := func(q string) int { return len(list(q).Items) }
		if n := count("?metric=requests_per_day"); n != 3 {
			t.Errorf("metric filter: %d, want 3", n)
		}
		if n := count("?subject_kind=key&subject_id=" + pw.keyA); n != 1 {
			t.Errorf("subject filter: %d, want 1", n)
		}
		if n := count("?provider=" + pw.cat.Provider.Name); n != 1 {
			t.Errorf("provider filter: %d, want 1", n)
		}
		if n := count("?provider=" + pw.cat.Provider.Name + "&model=" + pw.cat.All.Name); n != 1 {
			t.Errorf("model filter: %d, want 1", n)
		}
		if n := count("?metric=cost_per_day"); n != 0 {
			t.Errorf("a metric nobody has: %d", n)
		}
		for _, q := range []string{"?metric=tokens_per_hour", "?sort=name", "?limit=0", "?limit=1000", "?cursor=garbage", "?subject_kind=team"} {
			if r := pw.do(t, pw.ka, "GET", base+q, nil); r.Status != 400 {
				t.Errorf("%s: status %d, want 400\n%s", q, r.Status, r.Body)
			}
		}
		if l := decodeInto[adminapi.LimitRuleList](t, pw.do(t, pw.kb, "GET", "/v1/tenants/"+pw.b.ID+"/limits", nil), 200); len(l.Items) != 0 {
			t.Errorf("tenant B lists %d rules of A", len(l.Items))
		}
	})

	t.Run("get, and the ID of a rule that is not there", func(t *testing.T) {
		rule := w.create(t, map[string]any{"metric": "requests_per_minute", "limit": 100})
		if r := w.do(t, w.ka, "GET", w.baseA+"/"+rule.ID, nil); r.Status != 200 {
			t.Errorf("get: %d", r.Status)
		}
		missing := w.do(t, w.ka, "GET", w.baseA+"/0192f0f0-0000-7000-8000-0000000000ee", nil)
		expect(t, missing, 404, adminapi.ProblemCodeNotFound)
		other := w.do(t, w.kb, "GET", w.baseA+"/"+rule.ID, nil) // B's key on A's tenant path: the same 404 as for a tenant that does not exist
		expect(t, other, 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.kb, "GET", "/v1/tenants/"+w.b.ID+"/limits/"+rule.ID, nil), 404, adminapi.ProblemCodeNotFound)
		if r := w.do(t, w.ka, "GET", w.baseA+"/not-an-id", nil); r.Status != 404 {
			t.Errorf("a malformed ID: %d", r.Status)
		}
	})

	t.Run("update: only limit or amount, enforce and thresholds, the result must be valid", func(t *testing.T) {
		rule := w.create(t, map[string]any{"metric": "requests_per_day", "limit": 100, "provider": w.cat.Provider.Name})
		path := w.baseA + "/" + rule.ID
		up := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "PATCH", path, map[string]any{"limit": 250}), 200)
		want := rule
		want.Limit, want.UpdatedAt = 250, up.UpdatedAt
		if !reflect.DeepEqual(up, want) || up.UpdatedAt.Before(rule.UpdatedAt) {
			t.Errorf("updated:\n%+v\nwant\n%+v", up, want)
		}
		up = decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "PATCH", path, map[string]any{"enforce": false, "thresholds": []int{90, 50}}), 200)
		if up.Enforce || !reflect.DeepEqual(up.Thresholds, []int{50, 90}) || up.Limit != 250 {
			t.Errorf("soft: %+v", up)
		}
		// Nothing sent that changes is a 200 and the rule is the same.
		same := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "PATCH", path, map[string]any{"limit": 250}), 200)
		if !reflect.DeepEqual(same, up) {
			t.Errorf("a PATCH that changes nothing changed the rule:\n%+v\n%+v", same, up)
		}
		up = decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "PATCH", path, map[string]any{"enforce": true, "thresholds": []int{}}), 200)
		if !up.Enforce || len(up.Thresholds) != 0 {
			t.Errorf("enforce again: %+v", up)
		}
		budget := w.create(t, map[string]any{"metric": "cost_per_month", "amount": "10", "currency": "EUR", "provider": w.cat.Provider.Name})
		bp := w.baseA + "/" + budget.ID
		if b := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "PATCH", bp, map[string]any{"amount": "12.250"}), 200); b.Amount != "12.25" || b.Currency != "EUR" {
			t.Errorf("a new amount: %+v", b)
		}
		for what, c := range map[string]struct {
			path  string
			body  map[string]any
			field string
		}{
			"nothing to change":              {path, map[string]any{}, ""},
			"a limit of 0":                   {path, map[string]any{"limit": 0}, "limit"},
			"an amount on a count rule":      {path, map[string]any{"amount": "5"}, "amount"},
			"a limit on a budget":            {bp, map[string]any{"limit": 5}, "limit"},
			"an amount of 0":                 {bp, map[string]any{"amount": "0"}, "amount"},
			"seven fraction digits":          {bp, map[string]any{"amount": "0.0000001"}, "amount"},
			"a soft rule without thresholds": {path, map[string]any{"enforce": false}, "thresholds"},
			"a repeated threshold":           {path, map[string]any{"thresholds": []int{5, 5}}, "thresholds"},
			"a threshold of 101":             {path, map[string]any{"thresholds": []int{101}}, "thresholds"},
			"a change of the subject":        {path, map[string]any{"subject_kind": "key"}, ""},
			"a change of the metric":         {path, map[string]any{"metric": "tokens_per_day"}, ""},
			"a change of the currency":       {bp, map[string]any{"currency": "USD"}, ""},
			"a change of the provider":       {path, map[string]any{"provider": "other"}, ""},
		} {
			r := w.do(t, w.ka, "PATCH", c.path, c.body)
			if r.Status != 400 {
				t.Errorf("%s: status %d, want 400\n%s", what, r.Status, r.Body)
				continue
			}
			if p := problem(t, r); c.field != "" && !namesMember(p, c.field) {
				t.Errorf("%s: %+v does not name %s", what, p, c.field)
			}
		}
		rate := w.create(t, map[string]any{"metric": "concurrent_requests", "limit": 3})
		if r := w.do(t, w.ka, "PATCH", w.baseA+"/"+rate.ID, map[string]any{"enforce": false, "thresholds": []int{50}}); r.Status != 400 {
			t.Errorf("a rate rule made soft: %d\n%s", r.Status, r.Body)
		}
		expect(t, w.do(t, w.kb, "PATCH", path, map[string]any{"limit": 1}), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.ka, "PATCH", w.baseA+"/0192f0f0-0000-7000-8000-0000000000ee", map[string]any{"limit": 1}), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("delete returns the rule as it was", func(t *testing.T) {
		rule := w.create(t, map[string]any{"metric": "requests_per_month", "limit": 77, "provider": w.cat.Provider.Name})
		gone := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "DELETE", w.baseA+"/"+rule.ID, nil), 200)
		if !reflect.DeepEqual(gone, rule) {
			t.Errorf("delete returned\n%+v\nwant\n%+v", gone, rule)
		}
		expect(t, w.do(t, w.ka, "GET", w.baseA+"/"+rule.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.ka, "DELETE", w.baseA+"/"+rule.ID, nil), 404, adminapi.ProblemCodeNotFound)
		if r := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "requests_per_month", "limit": 78, "provider": w.cat.Provider.Name}); r.Status != 201 {
			t.Errorf("the scope of a deleted rule cannot be used again: %d", r.Status)
		}
		other := w.create(t, map[string]any{"metric": "requests_per_minute", "limit": 7, "provider": w.cat.Provider.Name})
		expect(t, w.do(t, w.kb, "DELETE", w.baseA+"/"+other.ID, nil), 404, adminapi.ProblemCodeNotFound)
		if r := w.do(t, w.ka, "GET", w.baseA+"/"+other.ID, nil); r.Status != 200 {
			t.Errorf("a refused delete removed the rule: %d", r.Status)
		}
	})

	t.Run("a change is notified to the gateways", func(t *testing.T) {
		conn := w.api.DB.Connect(t, "gateway")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, "LISTEN pandokos_cache"); err != nil {
			t.Fatal(err)
		}
		rule := w.create(t, map[string]any{"metric": "requests_per_minute", "limit": 12, "provider": w.cat.Provider.Name, "model": w.cat.All.Name})
		want := "limits:" + w.a.ID
		for _, step := range []func(){
			func() {},
			func() { w.do(t, w.ka, "PATCH", w.baseA+"/"+rule.ID, map[string]any{"limit": 13}) },
			func() { w.do(t, w.ka, "DELETE", w.baseA+"/"+rule.ID, nil) },
		} {
			step()
			n, err := conn.WaitForNotification(ctx)
			if err != nil || n.Payload != want {
				t.Fatalf("notification %+v, %v, want %s", n, err, want)
			}
		}
	})

	t.Run("5,000 rules at most, counted under a lock, with the bound lowered", func(t *testing.T) {
		restore := admin.SetMaxLimitRules(6)
		defer restore()
		cw := newLW(t)
		type combo struct {
			metric string
			scope  map[string]any
		}
		var combos []combo
		for _, m := range []string{"requests_per_day", "requests_per_month", "tokens_per_day", "tokens_per_month"} {
			combos = append(combos, combo{m, nil}, combo{m, map[string]any{"provider": cw.cat.Provider.Name}}, combo{m, map[string]any{"provider": cw.cat.Provider.Name, "model": cw.cat.All.Name}})
		}
		statuses := make([]int, len(combos))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, c := range combos {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body := map[string]any{"metric": c.metric, "limit": 10}
				for k, v := range c.scope {
					body[k] = v
				}
				<-start
				statuses[i] = cw.do(t, cw.ka, "POST", cw.baseA, body).Status
			}()
		}
		close(start)
		wg.Wait()
		created, refused := 0, 0
		for i, s := range statuses {
			switch s {
			case 201:
				created++
			case 409:
				refused++
			default:
				t.Errorf("combination %d: status %d", i, s)
			}
		}
		if created != 6 || refused != len(combos)-6 {
			t.Errorf("%d created and %d refused under a bound of 6, want 6 and %d", created, refused, len(combos)-6)
		}
		l := decodeInto[adminapi.LimitRuleList](t, cw.do(t, cw.ka, "GET", cw.baseA+"?limit=100", nil), 200)
		if len(l.Items) != 6 {
			t.Errorf("%d rules after the race, want 6", len(l.Items))
		}
		r := cw.do(t, cw.ka, "POST", cw.baseA, map[string]any{"metric": "concurrent_requests", "limit": 3})
		p := expect(t, r, 409, adminapi.ProblemCodeInvalidState)
		if strings.Contains(strings.ToLower(p.Detail), "6") {
			t.Errorf("the detail names the bound: %q (problem details are constant per code)", p.Detail)
		}
		// The platform rules of the tenant do not count against its rules.
		if r := cw.do(t, cw.plat, "POST", "/v1/platform-limits", map[string]any{"tenant": cw.a.ID, "metric": "requests_per_minute", "limit": 100}); r.Status != 201 {
			t.Errorf("a platform rule for the tenant: %d\n%s", r.Status, r.Body)
		}
		// Deleting one makes room.
		decodeInto[adminapi.LimitRule](t, cw.do(t, cw.ka, "DELETE", cw.baseA+"/"+l.Items[0].ID, nil), 200)
		if r := cw.do(t, cw.ka, "POST", cw.baseA, map[string]any{"metric": "concurrent_requests", "limit": 3}); r.Status != 201 {
			t.Errorf("after a deletion: %d\n%s", r.Status, r.Body)
		}
	})
}

// TestPlatformLimits is T-676: the five platform operations and the view of a tenant.
func TestPlatformLimits(t *testing.T) {
	w := newLW(t)
	base := "/v1/platform-limits"
	create := func(body map[string]any) adminapi.PlatformLimitRule {
		t.Helper()
		return decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", base, body), 201)
	}

	t.Run("a default and a row for one tenant", func(t *testing.T) {
		def := create(map[string]any{"metric": "requests_per_minute", "limit": 600})
		if def.Tenant != "" || def.Metric != adminapi.LimitMetricRequestsPerMinute || def.Limit != 600 || !def.Enforce || def.Thresholds == nil || len(def.ID) != 36 {
			t.Errorf("default: %+v", def)
		}
		row := create(map[string]any{"tenant": w.a.ID, "metric": "requests_per_minute", "limit": 5000})
		if row.Tenant != w.a.ID || row.Limit != 5000 {
			t.Errorf("a row for a tenant: %+v", row)
		}
		budget := create(map[string]any{"tenant": w.a.ID, "metric": "cost_per_month", "amount": "100.500", "currency": "USD", "provider": w.cat.Provider.Name, "enforce": false, "thresholds": []int{100, 90}})
		if budget.Amount != "100.5" || budget.Currency != "USD" || budget.Enforce || !reflect.DeepEqual(budget.Thresholds, []int{90, 100}) || budget.Provider != w.cat.Provider.Name {
			t.Errorf("a budget: %+v", budget)
		}
		if got := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "GET", base+"/"+row.ID, nil), 200); !reflect.DeepEqual(got, row) {
			t.Errorf("get:\n%+v\n%+v", got, row)
		}
		expect(t, w.do(t, w.plat, "POST", base, map[string]any{"metric": "requests_per_minute", "limit": 601}), 409, adminapi.ProblemCodeConflict)
		expect(t, w.do(t, w.plat, "POST", base, map[string]any{"tenant": w.a.ID, "metric": "requests_per_minute", "limit": 1}), 409, adminapi.ProblemCodeConflict)
		// The default and the row of a tenant are different rules; another tenant may have its own.
		if r := w.do(t, w.plat, "POST", base, map[string]any{"tenant": w.b.ID, "metric": "requests_per_minute", "limit": 1}); r.Status != 201 {
			t.Errorf("the same metric for another tenant: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("the same rules of validation, and a tenant that must exist", func(t *testing.T) {
		before := w.api.Snapshot(t)
		for _, c := range []struct {
			what  string
			body  map[string]any
			field string
		}{
			{"an unknown tenant", map[string]any{"tenant": "0192f0f0-0000-7000-8000-0000000000ee", "metric": "tokens_per_day", "limit": 5}, "tenant"},
			{"a tenant that is not an ID", map[string]any{"tenant": "acme", "metric": "tokens_per_day", "limit": 5}, "tenant"},
			{"an unknown metric", map[string]any{"metric": "tokens_per_hour", "limit": 5}, "metric"},
			{"a count without a limit", map[string]any{"metric": "tokens_per_day"}, "limit"},
			{"an amount on a count", map[string]any{"metric": "tokens_per_day", "limit": 5, "amount": "5"}, "amount"},
			{"a budget without a currency", map[string]any{"metric": "cost_per_day", "amount": "5"}, "currency"},
			{"an amount of 0", map[string]any{"metric": "cost_per_day", "amount": "0", "currency": "EUR"}, "amount"},
			{"a rate that does not enforce", map[string]any{"metric": "requests_per_minute", "limit": 5, "enforce": false, "thresholds": []int{50}}, "enforce"},
			{"a soft rule without thresholds", map[string]any{"metric": "tokens_per_day", "limit": 5, "enforce": false}, "thresholds"},
			{"a model without a provider", map[string]any{"metric": "tokens_per_day", "limit": 5, "model": w.cat.All.Name}, "model"},
			{"a provider that does not exist", map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": "no-such-provider"}, "provider"},
			{"a model that does not exist", map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": w.cat.Provider.Name, "model": "no-such-model"}, "model"},
		} {
			r := w.do(t, w.plat, "POST", base, c.body)
			if r.Status != 400 {
				t.Errorf("%s: status %d, want 400 on %s\n%s", c.what, r.Status, c.field, r.Body)
				continue
			}
			if p := problem(t, r); !namesMember(p, c.field) {
				t.Errorf("%s: %+v does not name %s", c.what, p, c.field)
			}
		}
		if w.api.Snapshot(t) != before {
			t.Error("a refused creation changed a row")
		}
		// For a platform rule a model need only exist, whoever can see it.
		if r := w.do(t, w.plat, "POST", base, map[string]any{"metric": "tokens_per_month", "limit": 5, "provider": w.cat.Provider.Name, "model": w.cat.Selected.Name}); r.Status != 201 {
			t.Errorf("a model that is not generally available: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("list, filters, pagination", func(t *testing.T) {
		pw := newLW(t)
		for _, b := range []map[string]any{
			{"metric": "requests_per_day", "limit": 1}, {"metric": "tokens_per_day", "limit": 2},
			{"tenant": pw.a.ID, "metric": "requests_per_day", "limit": 3}, {"tenant": pw.a.ID, "metric": "tokens_per_month", "limit": 4}, {"tenant": pw.b.ID, "metric": "requests_per_day", "limit": 5},
		} {
			decodeInto[adminapi.PlatformLimitRule](t, pw.do(t, pw.plat, "POST", base, b), 201)
		}
		list := func(q string) adminapi.PlatformLimitRuleList {
			return decodeInto[adminapi.PlatformLimitRuleList](t, pw.do(t, pw.plat, "GET", base+q, nil), 200)
		}
		if n := len(list("?limit=100").Items); n < 5 {
			t.Errorf("%d rules", n)
		}
		count := func(q string) int { return len(list(q).Items) }
		if n := count("?applies_to=default&metric=requests_per_day"); n < 1 {
			t.Errorf("the defaults: %d", n)
		}
		for _, it := range list("?applies_to=default&limit=100").Items {
			if it.Tenant != "" {
				t.Errorf("a row of a tenant among the defaults: %+v", it)
			}
		}
		if n := count("?applies_to=" + pw.a.ID); n != 2 {
			t.Errorf("rows of tenant A: %d, want 2", n)
		}
		if n := count("?applies_to=" + pw.a.ID + "&metric=tokens_per_month"); n != 1 {
			t.Errorf("rows of tenant A for a metric: %d, want 1", n)
		}
		var got []string
		cursor := ""
		for pages := 0; pages < 20; pages++ {
			l := list("?sort=created_at&limit=2" + cursor)
			for _, it := range l.Items {
				got = append(got, it.ID)
			}
			if l.NextCursor == "" {
				break
			}
			cursor = "&cursor=" + l.NextCursor
		}
		if !sort.StringsAreSorted(got) || len(got) < 5 {
			t.Errorf("the pages give %v", got)
		}
		for _, q := range []string{"?applies_to=nobody", "?metric=x", "?sort=name", "?limit=0", "?cursor=garbage"} {
			if r := pw.do(t, pw.plat, "GET", base+q, nil); r.Status != 400 {
				t.Errorf("%s: status %d, want 400", q, r.Status)
			}
		}
	})

	t.Run("update and delete", func(t *testing.T) {
		rule := create(map[string]any{"tenant": w.a.ID, "metric": "requests_per_month", "limit": 100})
		up := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "PATCH", base+"/"+rule.ID, map[string]any{"limit": 200}), 200)
		want := rule
		want.Limit, want.UpdatedAt = 200, up.UpdatedAt
		if !reflect.DeepEqual(up, want) {
			t.Errorf("updated:\n%+v\nwant\n%+v", up, want)
		}
		if r := w.do(t, w.plat, "PATCH", base+"/"+rule.ID, map[string]any{"tenant": w.b.ID}); r.Status != 400 {
			t.Errorf("a change of the tenant: %d", r.Status)
		}
		if r := w.do(t, w.plat, "PATCH", base+"/"+rule.ID, map[string]any{}); r.Status != 400 {
			t.Errorf("an empty update: %d", r.Status)
		}
		gone := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "DELETE", base+"/"+rule.ID, nil), 200)
		if !reflect.DeepEqual(gone, up) {
			t.Errorf("delete returned %+v", gone)
		}
		expect(t, w.do(t, w.plat, "GET", base+"/"+rule.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.plat, "DELETE", base+"/"+rule.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.plat, "PATCH", base+"/0192f0f0-0000-7000-8000-0000000000ee", map[string]any{"limit": 1}), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("a tenant sees the defaults and its own rows, and nothing of another tenant", func(t *testing.T) {
		pw := newLW(t)
		def := decodeInto[adminapi.PlatformLimitRule](t, pw.do(t, pw.plat, "POST", base, map[string]any{"metric": "concurrent_requests", "limit": 77, "provider": pw.cat.Provider.Name}), 201)
		own := decodeInto[adminapi.PlatformLimitRule](t, pw.do(t, pw.plat, "POST", base, map[string]any{"tenant": pw.a.ID, "metric": "requests_per_minute", "limit": 88}), 201)
		theirs := decodeInto[adminapi.PlatformLimitRule](t, pw.do(t, pw.plat, "POST", base, map[string]any{"tenant": pw.b.ID, "metric": "requests_per_minute", "limit": 99}), 201)
		l := decodeInto[adminapi.PlatformLimitRuleList](t, pw.do(t, pw.ka, "GET", "/v1/tenants/"+pw.a.ID+"/platform-limits?limit=200", nil), 200)
		seen := map[string]bool{}
		for _, it := range l.Items {
			seen[it.ID] = true
		}
		if !seen[def.ID] || !seen[own.ID] || seen[theirs.ID] {
			t.Errorf("tenant A sees default %v, own %v, B's %v", seen[def.ID], seen[own.ID], seen[theirs.ID])
		}
		for _, it := range l.Items {
			if it.Tenant != "" && it.Tenant != pw.a.ID {
				t.Errorf("a row of tenant %s in the view of A", it.Tenant)
			}
		}
		lb := decodeInto[adminapi.PlatformLimitRuleList](t, pw.do(t, pw.kb, "GET", "/v1/tenants/"+pw.b.ID+"/platform-limits?limit=200", nil), 200)
		for _, it := range lb.Items {
			if it.ID == own.ID {
				t.Error("tenant B sees a row of A")
			}
		}
		// The tenant cannot write them; the platform key does not read through the tenant path.
		expect(t, pw.do(t, pw.ka, "POST", base, map[string]any{"metric": "tokens_per_day", "limit": 5}), 403, adminapi.ProblemCodeForbidden)
		expect(t, pw.do(t, pw.plat, "GET", "/v1/tenants/"+pw.a.ID+"/platform-limits", nil), 403, adminapi.ProblemCodeForbidden)
		if r := pw.do(t, pw.ka, "GET", "/v1/tenants/"+pw.a.ID+"/platform-limits?limit=0", nil); r.Status != 400 {
			t.Errorf("limit=0: %d", r.Status)
		}
	})
}

// TestLimitEvents is T-680: the events a tenant's rules recorded, newest first.
func TestLimitEvents(t *testing.T) {
	w := newLW(t)
	base := "/v1/tenants/" + w.a.ID + "/limit-events"
	super := w.api.Super(t)
	rule1, rule2 := w.create(t, map[string]any{"metric": "tokens_per_day", "limit": 100}), w.create(t, map[string]any{"metric": "cost_per_month", "amount": "10", "currency": "EUR"})
	insert := func(tenantID, ruleID, kind string, percent int, metric, currency string, period time.Time) {
		t.Helper()
		time.Sleep(3 * time.Millisecond) // the identifiers are time-ordered: one event a millisecond
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, currency, period_start, period_end, percent, limit_value, observed)
			VALUES ($1::uuid, $2, $3::uuid, 'tenant', 'tenant', $4, $5, $6::timestamptz, $6::timestamptz + interval '1 day', $7, 100, 81.5)`, tenantID, kind, ruleID, metric, currency, period, percent); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		insert(w.a.ID, rule1.ID, "threshold_reached", 10+i, "tokens_per_day", "", day.AddDate(0, 0, i))
	}
	insert(w.a.ID, rule2.ID, "exhausted", 0, "cost_per_month", "EUR", day)
	insert(w.b.ID, rule1.ID, "threshold_reached", 50, "tokens_per_day", "", day) // another tenant's

	list := func(q string) adminapi.LimitEventList {
		return decodeInto[adminapi.LimitEventList](t, w.do(t, w.ka, "GET", base+q, nil), 200)
	}
	all := list("?limit=100")
	if len(all.Items) != 6 {
		t.Fatalf("%d events, want 6 (nothing of tenant B)", len(all.Items))
	}
	for i := 1; i < len(all.Items); i++ {
		if all.Items[i-1].ID < all.Items[i].ID {
			t.Errorf("not newest first: %s before %s", all.Items[i-1].ID, all.Items[i].ID)
		}
	}
	first := all.Items[len(all.Items)-1]
	if first.RuleID != rule1.ID || first.Kind != adminapi.LimitEventKind("threshold_reached") || first.Percent != 10 || first.Metric != adminapi.LimitMetricTokensPerDay ||
		first.Limit != 100 || first.Observed != "81.5" || first.RuleLevel != adminapi.LimitRuleLevel("tenant") || !first.PeriodStart.Equal(day) || first.OccurredAt.IsZero() {
		t.Errorf("an event: %+v", first)
	}
	for _, e := range all.Items {
		if e.Metric == adminapi.LimitMetricCostPerMonth && (e.Amount != "0.0001" || e.Currency != "EUR" || e.Limit != 0) {
			t.Errorf("a budget event shows its value as an amount in the currency: %+v", e)
		}
	}
	if n := len(list("?kind=exhausted").Items); n != 1 {
		t.Errorf("kind filter: %d, want 1", n)
	}
	if n := len(list("?kind=threshold_reached&limit=100").Items); n != 5 {
		t.Errorf("kind filter: %d, want 5", n)
	}
	if n := len(list("?rule=" + rule2.ID).Items); n != 1 {
		t.Errorf("rule filter: %d, want 1", n)
	}
	if n := len(list("?rule=0192f0f0-0000-7000-8000-0000000000ee").Items); n != 0 {
		t.Errorf("rule filter, none: %d", n)
	}
	var got []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		l := list("?limit=2" + cursor)
		if len(l.Items) > 2 {
			t.Fatalf("a page of %d", len(l.Items))
		}
		for _, e := range l.Items {
			got = append(got, e.ID)
		}
		if l.NextCursor == "" {
			break
		}
		cursor = "&cursor=" + l.NextCursor
	}
	if want := ruleIDsOfEvents(all); !reflect.DeepEqual(got, want) {
		t.Errorf("the pages give %v, the whole list %v", got, want)
	}
	for _, q := range []string{"?kind=whatever", "?limit=0", "?limit=1000", "?cursor=garbage", "?rule=not-an-id"} {
		if r := w.do(t, w.ka, "GET", base+q, nil); r.Status != 400 {
			t.Errorf("%s: status %d, want 400\n%s", q, r.Status, r.Body)
		}
	}
	if l := decodeInto[adminapi.LimitEventList](t, w.do(t, w.kb, "GET", "/v1/tenants/"+w.b.ID+"/limit-events?limit=100", nil), 200); len(l.Items) != 1 || l.Items[0].RuleID != rule1.ID {
		t.Errorf("tenant B sees %+v", l.Items)
	}
	expect(t, w.do(t, w.kb, "GET", base, nil), 404, adminapi.ProblemCodeNotFound)
	expect(t, w.do(t, w.plat, "GET", base, nil), 403, adminapi.ProblemCodeForbidden)
}

func ruleIDsOfEvents(l adminapi.LimitEventList) []string {
	var out []string
	for _, e := range l.Items {
		out = append(out, e.ID)
	}
	return out
}

// ruleRow is a limit rule as the database holds it, by the typed columns.
type ruleRow struct {
	kind                        string
	sa, vk, sm, provider, model string
}

func ruleRows(t *testing.T, w *lw, id string) ruleRow {
	t.Helper()
	var r ruleRow
	err := w.api.Super(t).QueryRow(ctx(t), `SELECT subject_kind, COALESCE(service_account_id::text, ''), COALESCE(virtual_key_id::text, ''),
			COALESCE(subject_mapping_id::text, ''), COALESCE(provider_id::text, ''), COALESCE(model_id::text, '') FROM pandokos.limit_rule WHERE id = $1::uuid`, id).
		Scan(&r.kind, &r.sa, &r.vk, &r.sm, &r.provider, &r.model)
	if err != nil {
		t.Fatalf("rule %s: %v", id, err)
	}
	return r
}

// TestLimitSubjectsTyped is T-713: creation with each kind of subject stores the typed column; a
// revoked key, a subject of another tenant and a missing one get one and the same 400; scope names
// resolve to identifiers and a model of another provider is refused; the resource shows the names
// and the identifiers; the list filters by subject, provider and model.
func TestLimitSubjectsTyped(t *testing.T) {
	w := newLW(t)
	super := w.api.Super(t)
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := super.QueryRow(ctx(t), sql, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	keyRow := scalar(`SELECT id::text FROM pandokos.virtual_key WHERE key_id = $1`, w.keyA)
	providerID := scalar(`SELECT id::text FROM pandokos.provider WHERE name = $1`, w.cat.Provider.Name)
	modelID := scalar(`SELECT id::text FROM pandokos.model WHERE id = $1::uuid`, w.cat.All.ID)

	t.Run("each kind of subject stores its typed column", func(t *testing.T) {
		tn := w.create(t, map[string]any{"metric": "requests_per_day", "limit": 5})
		if got := ruleRows(t, w, tn.ID); got != (ruleRow{kind: "tenant"}) {
			t.Errorf("tenant rule: %+v", got)
		}
		sa := w.create(t, map[string]any{"metric": "requests_per_day", "limit": 5, "subject_kind": "service_account", "subject_id": w.saA.ID})
		if got := ruleRows(t, w, sa.ID); got != (ruleRow{kind: "service_account", sa: w.saA.ID}) {
			t.Errorf("service account rule: %+v", got)
		}
		vk := w.create(t, map[string]any{"metric": "requests_per_day", "limit": 5, "subject_kind": "key", "subject_id": w.keyA})
		if got := ruleRows(t, w, vk.ID); got != (ruleRow{kind: "key", vk: keyRow}) {
			t.Errorf("virtual key rule: %+v (the row ID of the key, not its key ID)", got)
		}
		sm := w.create(t, map[string]any{"metric": "requests_per_day", "limit": 5, "subject_kind": "key", "subject_id": w.mappingA})
		if got := ruleRows(t, w, sm.ID); got != (ruleRow{kind: "key", sm: w.mappingA}) {
			t.Errorf("subject mapping rule: %+v", got)
		}
		for _, r := range []adminapi.LimitRule{sa, vk, sm} {
			if r.SubjectID == "" {
				t.Errorf("the resource shows no subject: %+v", r)
			}
		}
		if vk.SubjectID != w.keyA || sm.SubjectID != w.mappingA || sa.SubjectID != w.saA.ID {
			t.Errorf("the resources show %q %q %q, want the key ID, the mapping ID and the service account ID", vk.SubjectID, sm.SubjectID, sa.SubjectID)
		}
	})

	t.Run("a revoked key, a subject of another tenant and a missing one get the same 400", func(t *testing.T) {
		saX := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts", map[string]any{"name": "to-revoke", "roles": []string{"inference"}}), 201)
		k := decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/virtual-keys", map[string]any{"service_account_id": saX.ID}), 201)
		if r := w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/virtual-keys/"+k.Key.ID+"/revoke", map[string]any{}); r.Status != 200 {
			t.Fatalf("revoke: %d\n%s", r.Status, r.Body)
		}
		body := func(id string) map[string]any {
			return map[string]any{"metric": "tokens_per_day", "limit": 5, "subject_kind": "key", "subject_id": id}
		}
		revoked := w.do(t, w.ka, "POST", w.baseA, body(k.Key.ID))
		elsewhere := w.do(t, w.ka, "POST", w.baseA, body(w.keyB))
		nowhere := w.do(t, w.ka, "POST", w.baseA, body("tnosuchkeyid000000"))
		p := expect(t, revoked, 400, adminapi.ProblemCodeInvalidRequest)
		if !namesMember(p, "subject_id") {
			t.Errorf("the 400 for a revoked key does not name subject_id: %+v", p)
		}
		sameProblem(t, "a revoked key and a key of another tenant", revoked, elsewhere)
		sameProblem(t, "a revoked key and a key that does not exist", revoked, nowhere)
		if n := scalar(`SELECT count(*)::text FROM pandokos.limit_rule WHERE tenant_id = $1::uuid AND metric = 'tokens_per_day'`, w.a.ID); n != "0" {
			t.Errorf("%s rules were written by the refused requests", n)
		}
	})

	t.Run("scope names resolve to identifiers, a model of another provider is refused", func(t *testing.T) {
		prov := w.create(t, map[string]any{"metric": "tokens_per_month", "limit": 9, "provider": w.cat.Provider.Name})
		if got := ruleRows(t, w, prov.ID); got.provider != providerID || got.model != "" {
			t.Errorf("provider scope: %+v, want %s", got, providerID)
		}
		mod := w.create(t, map[string]any{"metric": "tokens_per_month", "limit": 9, "provider": w.cat.Provider.Name, "model": w.cat.All.Name, "subject_kind": "service_account", "subject_id": w.saA.ID})
		if got := ruleRows(t, w, mod.ID); got.provider != providerID || got.model != modelID {
			t.Errorf("model scope: %+v, want %s and %s", got, providerID, modelID)
		}
		// The resource shows the names and the identifiers.
		if prov.Provider != w.cat.Provider.Name || prov.ProviderID != providerID || prov.Model != "" || prov.ModelID != "" {
			t.Errorf("provider rule: %+v", prov)
		}
		if mod.Provider != w.cat.Provider.Name || mod.ProviderID != providerID || mod.Model != w.cat.All.Name || mod.ModelID != modelID {
			t.Errorf("model rule: %+v", mod)
		}
		if got := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "GET", w.baseA+"/"+mod.ID, nil), 200); got.ProviderID != mod.ProviderID || got.ModelID != mod.ModelID || got.Provider != mod.Provider {
			t.Errorf("get: %+v", got)
		}
		// A model of another provider.
		other := w.api.Catalog(t)
		r := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "requests_per_day", "limit": 1, "provider": w.cat.Provider.Name, "model": other.All.Name})
		p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		if !namesMember(p, "model") {
			t.Errorf("a model of another provider: %+v", p)
		}
		// The platform's rules have identifiers too.
		pl := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_month", "limit": 7, "provider": w.cat.Provider.Name, "model": w.cat.All.Name}), 201)
		if pl.ProviderID != providerID || pl.ModelID != modelID {
			t.Errorf("platform rule: %+v", pl)
		}
		var pp, pm string
		if err := super.QueryRow(ctx(t), `SELECT provider_id::text, model_id::text FROM pandokos.platform_limit_rule WHERE id = $1::uuid`, pl.ID).Scan(&pp, &pm); err != nil || pp != providerID || pm != modelID {
			t.Errorf("platform row %s %s %v", pp, pm, err)
		}
		r = w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_month", "limit": 7, "provider": w.cat.Provider.Name, "model": other.All.Name})
		expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
	})

	t.Run("the list filters by subject_id, provider and model", func(t *testing.T) {
		list := func(q string) adminapi.LimitRuleList {
			return decodeInto[adminapi.LimitRuleList](t, w.do(t, w.ka, "GET", w.baseA+q, nil), 200)
		}
		if l := list("?subject_kind=key&subject_id=" + w.keyA); len(l.Items) != 1 || l.Items[0].SubjectID != w.keyA {
			t.Errorf("by key ID: %+v", l.Items)
		}
		if l := list("?subject_kind=key&subject_id=" + w.mappingA); len(l.Items) != 1 || l.Items[0].SubjectID != w.mappingA {
			t.Errorf("by mapping ID: %+v", l.Items)
		}
		if l := list("?subject_kind=service_account&subject_id=" + w.saA.ID); len(l.Items) < 2 {
			t.Errorf("by service account: %d rules", len(l.Items))
		}
		if l := list("?provider=" + w.cat.Provider.Name + "&model=" + w.cat.All.Name); len(l.Items) != 1 || l.Items[0].ModelID != modelID {
			t.Errorf("by model: %+v", l.Items)
		}
		if l := list("?provider=" + w.cat.Provider.Name); len(l.Items) < 2 {
			t.Errorf("by provider: %d rules", len(l.Items))
		}
		if l := list("?provider=no-such-provider"); len(l.Items) != 0 {
			t.Errorf("by a provider that is not there: %+v", l.Items)
		}
	})
}

// TestLimitRulesByCurrency is T-755: two budgets that differ by currency are created for one subject and
// scope, for a tenant and for the platform; the same currency twice is 409 conflict; `active` on each rule
// before and after a switch; listTenantPlatformLimits sets it; the audit details name the currency.
func TestLimitRulesByCurrency(t *testing.T) {
	w := newPW(t)
	prov, model := w.cat.Provider.Name, w.cat.All
	w.api.DB.SeedPrice(t, prov, model.Name, "USD", time.Time{})
	w.createPrice(t, priceBody(model, prov, "EUR", nil, nil)) // the tenant's own list in EUR
	rules := "/v1/tenants/" + w.a.ID + "/limits"
	seen := seenIDs(t, w.api)
	mk := func(currency, amount string) map[string]any {
		return map[string]any{"metric": "cost_per_month", "amount": amount, "currency": currency, "provider": prov}
	}

	usd := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "POST", rules, mk("USD", "5")), 201)
	eur := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "POST", rules, mk("EUR", "7")), 201)
	if usd.ID == eur.ID || usd.Currency != "USD" || eur.Currency != "EUR" {
		t.Errorf("%+v %+v", usd, eur)
	}
	expect(t, w.do(t, w.ka, "POST", rules, mk("EUR", "9")), 409, adminapi.ProblemCodeConflict)
	expect(t, w.do(t, w.ka, "POST", rules, mk("USD", "9")), 409, adminapi.ProblemCodeConflict)
	rows := newRows(t, w.api, seen)
	if len(rows) != 2 || rows[0].Detail["currency"] == nil || rows[1].Detail["currency"] == nil || rows[0].Detail["currency"] == rows[1].Detail["currency"] {
		t.Errorf("the audit details name the currency: %+v", rows)
	}

	// The same for the platform: a ceiling for the tenant in both currencies, a conflict for the same one.
	pl := func(currency string) map[string]any {
		b := mk(currency, "50")
		b["tenant"] = w.a.ID
		return b
	}
	pUSD := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", pl("USD")), 201)
	decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", pl("EUR")), 201)
	expect(t, w.do(t, w.plat, "POST", "/v1/platform-limits", pl("EUR")), 409, adminapi.ProblemCodeConflict)
	_ = pUSD

	active := func(path string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		l := decodeInto[adminapi.LimitRuleList](t, w.do(t, w.ka, "GET", path, nil), 200)
		for _, r := range l.Items {
			out[r.Currency] = r.Active
		}
		return out
	}
	check := func(when, inForce string) {
		t.Helper()
		for _, path := range []string{rules, "/v1/tenants/" + w.a.ID + "/platform-limits"} {
			got := active(path)
			for _, cur := range []string{"USD", "EUR"} {
				if got[cur] != (cur == inForce) {
					t.Errorf("%s, %s: active by currency %v, want only %s", when, path, got, inForce)
				}
			}
		}
		one := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "GET", rules+"/"+eur.ID, nil), 200)
		if one.Active != (inForce == "EUR") {
			t.Errorf("%s: getLimit active %v", when, one.Active)
		}
	}
	check("before the switch (USD, the default)", "USD")
	w.api.DB.SetTenantCurrency(t, w.idA, "EUR", "", time.Time{})
	check("after the switch", "EUR")
	// A count rule is always active.
	count := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "POST", rules, map[string]any{"metric": "requests_per_day", "limit": 3}), 201)
	if !count.Active {
		t.Errorf("a count rule is inactive: %+v", count)
	}
	// The platform's own view of its ceilings does not carry active.
	raw := rawJSON(t, w.do(t, w.plat, "GET", "/v1/platform-limits/"+pUSD.ID, nil))
	if _, ok := raw["active"]; ok {
		t.Errorf("a platform ceiling in the platform's view carries active: %v", raw)
	}
}
