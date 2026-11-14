package admin_test

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/ledger"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/testutil"
)

// usageWorld is two tenants and a ledger writer on the same database, to write the records the
// list reads.
type usageWorld struct {
	c      *cat
	writer *ledger.Writer
	logs   *testutil.LogBuffer
}

func newUsageWorld(t *testing.T) *usageWorld {
	t.Helper()
	c := newCat(t)
	logs := &testutil.LogBuffer{}
	w, err := ledger.New(ledger.Options{DB: c.api.DB.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(logs, "debug"),
		BufferSize: 100, BatchSize: 50, FlushInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run(ctx(t))
	t.Cleanup(func() { w.Close(ctx(t)) })
	return &usageWorld{c: c, writer: w, logs: logs}
}

// write sends the records through the writer and waits until they are committed.
func (u *usageWorld) write(t *testing.T, recs ...usage.Record) {
	t.Helper()
	want := u.writer.Stats().Written + int64(len(recs))
	for _, r := range recs {
		if !u.writer.Admit() {
			t.Fatal("the writer refused an admission")
		}
		u.writer.Record(r)
	}
	testutil.Eventually(t, 10*time.Second, "the records to be written", func() bool {
		return u.writer.Stats().Written >= want || u.writer.Stats().Rejected > 0
	})
	if got := u.writer.Stats(); got.Written < want {
		t.Fatalf("the ledger rejected a record: %d written, %d rejected\n%s", got.Written, got.Rejected, u.logs.String())
	}
}

func tenantID(t testing.TB, id string) tenant.ID {
	t.Helper()
	v, err := tenant.ParseID(id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func decimal(t testing.TB, s string) pricing.Decimal {
	t.Helper()
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// usageRecords returns records of a tenant that cover the mixes of section 5.7a, newest first as
// the list must return them: start times 10, 20, 30, 40 and 50 minutes ago.
func usageRecords(t testing.TB, tid tenant.ID, now time.Time) []usage.Record {
	t.Helper()
	at := func(ago time.Duration) usage.Record {
		r := testutil.ValidRecord(tid)
		r.StartedAt = now.Add(-ago).UTC().Truncate(time.Microsecond)
		r.ID = usage.NewID(r.StartedAt)
		return r
	}
	yes := true
	ttfb, reqBytes, respBytes := 55*time.Millisecond, int64(300), int64(1200)

	r1 := at(10 * time.Minute) // anthropic, all provider, cache usage, metadata, a trace
	r1.Provider, r1.APIFamily, r1.Operation, r1.Model = "anthropic", "anthropic", "messages", "claude-sonnet-4-5"
	r1.Stream, r1.TTFB, r1.RequestBytes, r1.ResponseBytes = &yes, &ttfb, &reqBytes, &respBytes
	r1.TraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	r1.Usage = family.Usage{InputTokens: 2310, OutputTokens: 50, CacheReadTokens: 2000, CacheWriteTokens: 300,
		Extra:    map[string]int64{"cache_write_1h_tokens": 200, "web_search_requests": 2},
		Reported: map[string]int64{"input_tokens": 10, "cache_creation_input_tokens": 300, "cache_read_input_tokens": 2000, "output_tokens": 50, "cache_creation.ephemeral_1h_input_tokens": 200}}
	r1.Cost = pricing.Cost{Known: true, Amount: decimal(t, "0.002505"), Currency: "USD", VersionID: testutil.RandomUUID(), Fallbacks: []pricing.Category{pricing.CategoryCacheWrite1h}}
	r1.CostStatus = "known"
	r1.Metadata = map[string]string{"team": "alpha", "job": "nightly", "note": "synthetic"}
	r1.MetadataDropped = 2

	r2 := at(20 * time.Minute) // anthropic, input from the provider and output estimated, cancelled
	r2.Provider, r2.APIFamily, r2.Operation = "anthropic", "anthropic", "messages"
	r2.Stream, r2.Outcome = &yes, usage.OutcomeCancelled
	r2.InputSource, r2.OutputSource = usage.SourceProvider, usage.SourceEstimated
	r2.Usage = family.Usage{InputTokens: 12, Reported: map[string]int64{"input_tokens": 12, "output_tokens": 1}}
	r2.Estimated = family.Usage{OutputTokens: 3}
	r2.TraceID = ""

	r3 := at(30 * time.Minute) // openai, both estimated, cost unknown
	r3.InputSource, r3.OutputSource = usage.SourceEstimated, usage.SourceEstimated
	r3.Usage = family.Usage{}
	r3.Estimated = family.Usage{InputTokens: 5, OutputTokens: 7}
	r3.Cost = pricing.Cost{Reason: pricing.ReasonNoPrice}
	r3.CostStatus = "unknown"

	r4 := at(40 * time.Minute) // token counting: no sources, no cost, no stream, no TTFB
	r4.Provider, r4.APIFamily, r4.Operation = "anthropic", "anthropic", "messages.count_tokens"
	r4.InputSource, r4.OutputSource = usage.SourceNone, usage.SourceNone
	r4.Usage, r4.Cost, r4.CostStatus = family.Usage{}, pricing.Cost{}, "none"
	r4.TTFB, r4.Stream = nil, nil
	r4.Metadata = map[string]string{"a": "1"}

	r5 := at(50 * time.Minute) // refused before anything: most columns absent
	r5.Provider, r5.APIFamily = "anthropic", "anthropic"
	r5.Outcome, r5.ErrorCode, r5.UpstreamStatus = usage.OutcomeRejectedPolicy, "model_not_allowed", 0
	r5.CredentialID, r5.Endpoint, r5.Model, r5.ModelID, r5.UpstreamModel = "", "", "", "", ""
	r5.Stream, r5.TTFB, r5.RequestBytes, r5.ResponseBytes = nil, nil, nil, nil
	r5.InputSource, r5.OutputSource = usage.SourceNone, usage.SourceNone
	r5.Usage, r5.Cost, r5.CostStatus = family.Usage{}, pricing.Cost{}, "none"
	return []usage.Record{r1, r2, r3, r4, r5}
}

// expectedItem is the representation of section 5.7a of a record.
func expectedItem(r usage.Record) adminapi.UsageRecord {
	provIn, provOut := r.InputSource == usage.SourceProvider, r.OutputSource == usage.SourceProvider
	estIn, estOut := r.InputSource == usage.SourceEstimated, r.OutputSource == usage.SourceEstimated
	n := func(ok bool, v int64) int {
		if !ok {
			return 0
		}
		return int(v)
	}
	counters := func(m map[string]int64) []adminapi.UsageCounter {
		var out []adminapi.UsageCounter
		if provIn || provOut {
			for k, v := range m {
				out = append(out, adminapi.UsageCounter{Name: k, Value: int(v)})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	var meta []adminapi.MetadataEntry
	for k, v := range r.Metadata {
		meta = append(meta, adminapi.MetadataEntry{Key: k, Value: v})
	}
	sort.Slice(meta, func(i, j int) bool { return meta[i].Key < meta[j].Key })
	var fallbacks []string
	for _, c := range r.Cost.Fallbacks {
		fallbacks = append(fallbacks, string(c))
	}
	var status, ttfb, reqBytes, respBytes int
	status = r.UpstreamStatus
	if r.TTFB != nil {
		ttfb = int(r.TTFB.Milliseconds())
	}
	if r.RequestBytes != nil {
		reqBytes = int(*r.RequestBytes)
	}
	if r.ResponseBytes != nil {
		respBytes = int(*r.ResponseBytes)
	}
	reason := ""
	if r.CostStatus == "unknown" {
		reason = r.Cost.Reason
	}
	cost := adminapi.Null[string]()
	if r.CostStatus == "known" {
		cost = adminapi.Value(r.Cost.Amount.String())
	}
	return adminapi.UsageRecord{
		ID: r.ID, RequestID: r.RequestID, TraceID: r.TraceID, StartedAt: r.StartedAt, DurationMs: int(r.Duration.Milliseconds()),
		PrincipalKind: r.PrincipalKind, PrincipalID: r.PrincipalID, KeyID: r.KeyID, CredentialID: r.CredentialID, Funding: r.Funding,
		Provider: r.Provider, ApiFamily: r.APIFamily, Operation: r.Operation, Endpoint: r.Endpoint, Model: r.Model, ModelID: r.ModelID, UpstreamModel: r.UpstreamModel,
		Stream: r.Stream != nil && *r.Stream, Outcome: string(r.Outcome), ErrorCode: r.ErrorCode, UpstreamStatus: status, TtfbMs: ttfb,
		RequestBytes: reqBytes, ResponseBytes: respBytes, InputSource: string(r.InputSource), OutputSource: string(r.OutputSource),
		InputTokens: n(provIn, r.Usage.InputTokens), OutputTokens: n(provOut, r.Usage.OutputTokens), CacheReadTokens: n(provIn, r.Usage.CacheReadTokens),
		CacheWriteTokens: n(provIn, r.Usage.CacheWriteTokens), ReasoningTokens: n(provOut, r.Usage.ReasoningTokens),
		EstimatedInputTokens: n(estIn, r.Estimated.InputTokens), EstimatedOutputTokens: n(estOut, r.Estimated.OutputTokens),
		OtherUnits: counters(r.Usage.Extra), ProviderUsage: counters(r.Usage.Reported),
		CostStatus: r.CostStatus, CostReason: reason, Cost: cost, Currency: r.Cost.Currency, PriceVersionID: r.Cost.VersionID,
		CostFallbacks: fallbacks, Metadata: meta, MetadataDropped: r.MetadataDropped,
	}
}

// sameItem compares a listed item with the expected one: the same members, a cost equal as a
// number, and slices without a difference between empty and absent.
func sameItem(t *testing.T, got, want adminapi.UsageRecord) {
	t.Helper()
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("%s: started_at %v, want %v", want.ID, got.StartedAt, want.StartedAt)
	}
	got.StartedAt, want.StartedAt = time.Time{}, time.Time{}
	switch {
	case want.Cost.Null:
		if !got.Cost.Set || !got.Cost.Null {
			t.Errorf("%s: cost %+v, want null", want.ID, got.Cost)
		}
	default:
		if !got.Cost.Set || got.Cost.Null || decimal(t, got.Cost.Value).Cmp(decimal(t, want.Cost.Value)) != 0 {
			t.Errorf("%s: cost %+v, want %s", want.ID, got.Cost, want.Cost.Value)
		}
	}
	got.Cost, want.Cost = adminapi.Nullable[string]{}, adminapi.Nullable[string]{}
	norm := func(r *adminapi.UsageRecord) {
		if len(r.OtherUnits) == 0 {
			r.OtherUnits = nil
		}
		if len(r.ProviderUsage) == 0 {
			r.ProviderUsage = nil
		}
		if len(r.Metadata) == 0 {
			r.Metadata = nil
		}
		if len(r.CostFallbacks) == 0 {
			r.CostFallbacks = nil
		}
	}
	norm(&got)
	norm(&want)
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("record %s differs\n got %s\nwant %s", want.ID, g, w)
	}
}

func listUsage(t testing.TB, c *cat, key, tenantID string, q url.Values) *apitest.Response {
	t.Helper()
	path := "/v1/tenants/" + tenantID + "/usage-records"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.do(t, "GET", path, key, nil)
}

func usagePage(t testing.TB, c *cat, key, tenantID string, q url.Values) adminapi.UsageRecordList {
	t.Helper()
	var out adminapi.UsageRecordList
	ok(t, listUsage(t, c, key, tenantID, q), 200, &out)
	return out
}

func ids(items []adminapi.UsageRecord) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// TestListUsageRecords is T-546: records written by the ledger writer come back newest first,
// every member equal to its record, in pages, filtered, bounded.
func TestListUsageRecords(t *testing.T) {
	u := newUsageWorld(t)
	c := u.c
	now := time.Now()
	recs := usageRecords(t, tenantID(t, c.a.ID), now)
	old := testutil.ValidRecord(tenantID(t, c.a.ID)) // 30 hours old: outside the default window
	old.StartedAt = now.Add(-30 * time.Hour).UTC().Truncate(time.Microsecond)
	old.ID = usage.NewID(old.StartedAt)
	u.write(t, append(append([]usage.Record(nil), recs...), old)...)

	all := url.Values{"from": {now.Add(-48 * time.Hour).Format(time.RFC3339Nano)}, "to": {now.Add(time.Minute).Format(time.RFC3339Nano)}}

	t.Run("newest first, every member equal to its record", func(t *testing.T) {
		page := usagePage(t, c, c.ka, c.a.ID, all)
		if len(page.Items) != 6 || page.NextCursor != "" {
			t.Fatalf("%d items, cursor %q", len(page.Items), page.NextCursor)
		}
		want := append(append([]usage.Record(nil), recs...), old)
		for i, r := range want {
			if page.Items[i].ID != r.ID {
				t.Fatalf("item %d is %s, want %s (newest first)", i, page.Items[i].ID, r.ID)
			}
			sameItem(t, page.Items[i], expectedItem(r))
		}
	})

	t.Run("the shape of the sources and the nullable members", func(t *testing.T) {
		page := usagePage(t, c, c.ka, c.a.ID, all)
		mixed := page.Items[1]
		if mixed.InputSource != "provider" || mixed.OutputSource != "estimated" || mixed.InputTokens != 12 || mixed.OutputTokens != 0 || mixed.EstimatedInputTokens != 0 || mixed.EstimatedOutputTokens != 3 {
			t.Errorf("the mixed record: %+v", mixed)
		}
		counting := page.Items[3]
		if counting.Operation != "messages.count_tokens" || counting.Stream || counting.TtfbMs != 0 || counting.CostStatus != "none" || !counting.Cost.Null {
			t.Errorf("the counting record: %+v", counting)
		}
		refused := page.Items[4]
		if refused.Stream || refused.UpstreamStatus != 0 || refused.RequestBytes != 0 || refused.Model != "" || refused.CredentialID != "" || refused.ErrorCode != "model_not_allowed" {
			t.Errorf("the refused record: %+v", refused)
		}
		if known := page.Items[0]; known.CostStatus != "known" || !known.Cost.Set || known.Cost.Null || known.MetadataDropped != 2 {
			t.Errorf("the known record: %+v", known)
		}
		raw := listUsage(t, c, c.ka, c.a.ID, all)
		var doc struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw.Body, &doc); err != nil {
			t.Fatal(err)
		}
		if string(doc.Items[3]["cost"]) != "null" || string(doc.Items[0]["cost"]) == "null" {
			t.Errorf("cost members: %s and %s", doc.Items[0]["cost"], doc.Items[3]["cost"])
		}
		for _, k := range []string{"other_units", "provider_usage", "metadata", "cost_fallbacks"} {
			for i, it := range doc.Items {
				if string(it[k]) == "null" || len(it[k]) == 0 {
					t.Errorf("item %d: %s is %q, want an array", i, k, it[k])
				}
			}
		}
	})

	t.Run("three pages by cursor", func(t *testing.T) {
		q := url.Values{"from": all["from"], "to": all["to"], "limit": {"2"}}
		var got []string
		pages := 0
		for {
			page := usagePage(t, c, c.ka, c.a.ID, q)
			pages++
			got = append(got, ids(page.Items)...)
			if page.NextCursor == "" {
				break
			}
			if len(page.Items) != 2 {
				t.Fatalf("page %d has %d items", pages, len(page.Items))
			}
			q.Set("cursor", page.NextCursor)
			if pages > 5 {
				t.Fatal("too many pages")
			}
		}
		want := ids(usagePage(t, c, c.ka, c.a.ID, all).Items)
		if pages != 3 || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%d pages, ids %v, want %v", pages, got, want)
		}
	})

	t.Run("pages over the default window by cursor", func(t *testing.T) {
		// No from, no to: the window is resolved on the first request and the cursor carries it.
		for name, q := range map[string]url.Values{
			"no from, no to": {"limit": {"2"}},
			"only from":      {"from": {now.Add(-2 * time.Hour).Format(time.RFC3339Nano)}, "limit": {"2"}},
			"only to":        {"to": {now.Add(time.Minute).Format(time.RFC3339Nano)}, "limit": {"2"}},
		} {
			var got []string
			pages := 0
			for {
				page := usagePage(t, c, c.ka, c.a.ID, q)
				pages++
				got = append(got, ids(page.Items)...)
				if page.NextCursor == "" {
					break
				}
				q.Set("cursor", page.NextCursor)
				if pages > 5 {
					t.Fatalf("%s: too many pages", name)
				}
			}
			want := ids(usagePage(t, c, c.ka, c.a.ID, url.Values{"from": all["from"], "to": all["to"]}).Items)[:5]
			if pages != 3 || strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s: %d pages, ids %v, want %v", name, pages, got, want)
			}
		}
	})

	t.Run("a cursor of the default window refuses another explicit window", func(t *testing.T) {
		first := usagePage(t, c, c.ka, c.a.ID, url.Values{"limit": {"2"}})
		if first.NextCursor == "" {
			t.Fatal("no cursor")
		}
		for name, q := range map[string]url.Values{
			"another from":     {"from": {now.Add(-3 * time.Hour).Format(time.RFC3339Nano)}},
			"another to":       {"to": {now.Add(time.Hour).Format(time.RFC3339Nano)}},
			"another provider": {"provider": {"anthropic"}},
		} {
			q.Set("cursor", first.NextCursor)
			p := expect(t, listUsage(t, c, c.ka, c.a.ID, q), 400, adminapi.ProblemCodeInvalidRequest)
			if len(p.Errors) != 1 || p.Errors[0].Field != "cursor" {
				t.Errorf("%s: errors %+v, want one on cursor", name, p.Errors)
			}
		}
	})

	t.Run("the default window is the last 24 hours", func(t *testing.T) {
		page := usagePage(t, c, c.ka, c.a.ID, nil)
		if len(page.Items) != 5 {
			t.Fatalf("%d items, want the five of the last hour: %v", len(page.Items), ids(page.Items))
		}
		for _, it := range page.Items {
			if it.ID == old.ID {
				t.Error("a record of 30 hours ago is in the default window")
			}
		}
		withFrom := usagePage(t, c, c.ka, c.a.ID, url.Values{"from": {now.Add(-48 * time.Hour).Format(time.RFC3339)}})
		if len(withFrom.Items) != 6 {
			t.Errorf("from alone (to defaults to now): %d items", len(withFrom.Items))
		}
	})

	t.Run("to is excluded and from is included", func(t *testing.T) {
		second := recs[1].StartedAt
		q := url.Values{"from": {recs[3].StartedAt.Format(time.RFC3339Nano)}, "to": {second.Format(time.RFC3339Nano)}}
		page := usagePage(t, c, c.ka, c.a.ID, q)
		if strings.Join(ids(page.Items), ",") != recs[2].ID+","+recs[3].ID {
			t.Errorf("window [%s, %s): %v", recs[3].StartedAt.Format(time.RFC3339Nano), second.Format(time.RFC3339Nano), ids(page.Items))
		}
	})

	t.Run("provider filter", func(t *testing.T) {
		q := url.Values{"from": all["from"], "to": all["to"], "provider": {"anthropic"}}
		page := usagePage(t, c, c.ka, c.a.ID, q)
		if strings.Join(ids(page.Items), ",") != recs[0].ID+","+recs[1].ID+","+recs[3].ID+","+recs[4].ID {
			t.Errorf("anthropic: %v", ids(page.Items))
		}
		q.Set("provider", "synthetic-no-such-provider")
		if page := usagePage(t, c, c.ka, c.a.ID, q); len(page.Items) != 0 || page.Items == nil {
			t.Errorf("an unknown provider: %+v (an empty array is expected)", page)
		}
	})

	t.Run("the window is at most 31 days", func(t *testing.T) {
		to := now.Truncate(time.Second)
		ok31 := url.Values{"from": {to.Add(-31 * 24 * time.Hour).Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
		usagePage(t, c, c.ka, c.a.ID, ok31)
		tooWide := url.Values{"from": {to.Add(-31*24*time.Hour - time.Second).Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
		p := expect(t, listUsage(t, c, c.ka, c.a.ID, tooWide), 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) != 1 || p.Errors[0].Field != "from" {
			t.Errorf("errors %+v, want one on from", p.Errors)
		}
	})

	t.Run("bad times, an inverted window and bad cursors are 400", func(t *testing.T) {
		for name, tc := range map[string]struct {
			q     url.Values
			field string
		}{
			"from is not a time":     {url.Values{"from": {"yesterday"}}, "from"},
			"to is not a time":       {url.Values{"to": {"2026-13-45T00:00:00Z"}}, "to"},
			"from without a zone":    {url.Values{"from": {"2026-11-19T00:00:00"}}, "from"},
			"inverted":               {url.Values{"from": {"2026-11-20T00:00:00Z"}, "to": {"2026-11-19T00:00:00Z"}}, "from"},
			"empty":                  {url.Values{"from": {"2026-11-20T00:00:00Z"}, "to": {"2026-11-20T00:00:00Z"}}, "from"},
			"a cursor that is junk":  {url.Values{"cursor": {"not-a-cursor"}}, "cursor"},
			"a provider that is bad": {url.Values{"provider": {"Bad_Provider"}}, "provider"},
		} {
			p := expect(t, listUsage(t, c, c.ka, c.a.ID, tc.q), 400, adminapi.ProblemCodeInvalidRequest)
			found := false
			for _, e := range p.Errors {
				found = found || e.Field == tc.field
			}
			if !found {
				t.Errorf("%s: errors %+v, want one on %s", name, p.Errors, tc.field)
			}
		}
		first := usagePage(t, c, c.ka, c.a.ID, url.Values{"from": all["from"], "to": all["to"], "limit": {"2"}})
		if first.NextCursor == "" {
			t.Fatal("no cursor")
		}
		cursorCase := map[string]url.Values{
			"another from":     {"from": {now.Add(-47 * time.Hour).Format(time.RFC3339Nano)}, "to": all["to"], "limit": {"2"}, "cursor": {first.NextCursor}},
			"another to":       {"from": all["from"], "to": {now.Add(2 * time.Minute).Format(time.RFC3339Nano)}, "limit": {"2"}, "cursor": {first.NextCursor}},
			"another provider": {"from": all["from"], "to": all["to"], "limit": {"2"}, "provider": {"anthropic"}, "cursor": {first.NextCursor}},
			"tampered":         {"from": all["from"], "to": all["to"], "limit": {"2"}, "cursor": {"x" + first.NextCursor}},
			"truncated":        {"from": all["from"], "to": all["to"], "limit": {"2"}, "cursor": {first.NextCursor[:len(first.NextCursor)/2]}},
		}
		// Crafted cursors: each is well formed JSON but carries something no page of ours makes.
		id := first.Items[len(first.Items)-1].ID
		ts := now.UTC().Format(time.RFC3339Nano)
		fromTs, toTs := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano)
		long := strings.Repeat("a", 65)
		type cur struct {
			S string `json:"s"`
			N string `json:"n"`
			I string `json:"i"`
			F string `json:"f,omitempty"`
			T string `json:"t,omitempty"`
			P string `json:"p,omitempty"`
		}
		crafted := map[string]cur{
			"no window":           {S: "usage", N: ts, I: id},
			"a window of 40 days": {S: "usage", N: ts, I: id, F: now.Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339Nano), T: toTs},
			"an empty window":     {S: "usage", N: ts, I: id, F: toTs, T: toTs},
			"an inverted window":  {S: "usage", N: ts, I: id, F: toTs, T: fromTs},
			"the legacy sort key": {S: "usage:" + fromTs + "|" + toTs + "|", N: ts, I: id, F: fromTs, T: toTs},
			"a long from":         {S: "usage", N: ts, I: id, F: long, T: toTs},
			"a long to":           {S: "usage", N: ts, I: id, F: fromTs, T: long},
			"a long provider":     {S: "usage", N: ts, I: id, F: fromTs, T: toTs, P: long},
		}
		for name, c := range crafted {
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			cursorCase[name] = url.Values{"cursor": {base64.RawURLEncoding.EncodeToString(raw)}}
		}
		for name, q := range cursorCase {
			p := expect(t, listUsage(t, c, c.ka, c.a.ID, q), 400, adminapi.ProblemCodeInvalidRequest)
			if len(p.Errors) != 1 || p.Errors[0].Field != "cursor" {
				t.Errorf("%s: errors %+v, want one on cursor", name, p.Errors)
			}
		}
		for _, limit := range []string{"0", "201", "-1", "x"} {
			expect(t, listUsage(t, c, c.ka, c.a.ID, url.Values{"limit": {limit}}), 400, adminapi.ProblemCodeInvalidRequest)
		}
	})

	t.Run("a read writes no audit row", func(t *testing.T) {
		count := func() (n int) {
			if err := c.api.Super(t).QueryRow(ctx(t), `SELECT (SELECT count(*) FROM pandokos.audit_event) + (SELECT count(*) FROM pandokos.platform_audit_event)`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := count()
		usagePage(t, c, c.ka, c.a.ID, all)
		if count() != before {
			t.Error("a list of usage records wrote an audit row")
		}
	})
}

// TestListUsageRecordsIsolation is T-547: another tenant's key sees nothing of the tenant, the
// platform key and the inference role are refused.
func TestListUsageRecordsIsolation(t *testing.T) {
	u := newUsageWorld(t)
	c := u.c
	now := time.Now()
	aRecs := usageRecords(t, tenantID(t, c.a.ID), now)
	bRecs := usageRecords(t, tenantID(t, c.b.ID), now)
	u.write(t, append(append([]usage.Record(nil), aRecs...), bRecs...)...)
	window := url.Values{"from": {now.Add(-2 * time.Hour).Format(time.RFC3339Nano)}, "to": {now.Add(time.Minute).Format(time.RFC3339Nano)}}

	t.Run("each tenant lists its own", func(t *testing.T) {
		a := usagePage(t, c, c.ka, c.a.ID, window)
		b := usagePage(t, c, c.kb, c.b.ID, window)
		if strings.Join(ids(a.Items), ",") != ids2(aRecs) || strings.Join(ids(b.Items), ",") != ids2(bRecs) {
			t.Errorf("a: %v\nb: %v", ids(a.Items), ids(b.Items))
		}
	})
	t.Run("another tenant's administrator: 404, also with a cursor", func(t *testing.T) {
		expect(t, listUsage(t, c, c.kb, c.a.ID, window), 404, adminapi.ProblemCodeNotFound)
		q := url.Values{"from": window["from"], "to": window["to"], "limit": {"2"}}
		cursor := usagePage(t, c, c.ka, c.a.ID, q).NextCursor
		if cursor == "" {
			t.Fatal("no cursor")
		}
		q.Set("cursor", cursor)
		expect(t, listUsage(t, c, c.kb, c.a.ID, q), 404, adminapi.ProblemCodeNotFound)
		// On its own list a cursor taken from A's list shows none of A's rows.
		r := listUsage(t, c, c.kb, c.b.ID, q)
		for _, rec := range aRecs {
			if strings.Contains(string(r.Body), rec.ID) || strings.Contains(string(r.Body), rec.RequestID) {
				t.Errorf("tenant B's list holds a row of tenant A: %s", rec.ID)
			}
		}
		if r.Status != 200 && r.Status != 400 {
			t.Errorf("status %d", r.Status)
		}
	})
	t.Run("a platform key: 403", func(t *testing.T) {
		expect(t, listUsage(t, c, c.plat, c.a.ID, window), 403, adminapi.ProblemCodeForbidden)
	})
	t.Run("the inference role: 403", func(t *testing.T) {
		client := c.api.Client(c.ka)
		sa, err := client.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: c.a.ID, Body: adminapi.ServiceAccountCreate{Name: "worker", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}
		k, err := client.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: c.a.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID}})
		if err != nil {
			t.Fatal(err)
		}
		expect(t, listUsage(t, c, k.Secret, c.a.ID, window), 403, adminapi.ProblemCodeForbidden)
	})
	t.Run("no key: 401", func(t *testing.T) {
		expect(t, listUsage(t, c, "", c.a.ID, window), 401, adminapi.ProblemCodeUnauthenticated)
	})
	t.Run("a suspended tenant: 403 tenant_suspended", func(t *testing.T) {
		if r := c.platform(t, "POST", "/v1/tenants/"+c.a.ID+"/suspend", nil); r.Status/100 != 2 {
			t.Fatalf("suspend: %d %s", r.Status, r.Body)
		}
		expect(t, listUsage(t, c, c.ka, c.a.ID, window), 403, adminapi.ProblemCodeTenantSuspended)
	})
}

// ids2 joins the IDs of records, newest first, as the list returns them.
func ids2(recs []usage.Record) string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return strings.Join(out, ",")
}
