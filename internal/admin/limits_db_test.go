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

func newLW(t *testing.T) *lw {
	t.Helper()
	w := &lw{iw: newIW(t)}
	w.cat = w.api.Catalog(t)
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

	t.Run("a rule of the same subject, scope and metric is a 409; a budget has one currency", func(t *testing.T) {
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
		w.create(t, map[string]any{"metric": "cost_per_day", "amount": "5", "currency": "EUR", "provider": w.cat.Provider.Name, "model": w.cat.All.Name})
		expect(t, w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "cost_per_day", "amount": "7", "currency": "USD", "provider": w.cat.Provider.Name, "model": w.cat.All.Name}), 409, adminapi.ProblemCodeConflict)
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
