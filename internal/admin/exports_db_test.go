package admin_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/test/apitest"
)

// poison installs the injections of the export tests in a database: a flag table that makes a read
// of a row fail, and a delay per row, both as an extra condition of the tenant policy of the table
// (the isolation predicate stays as it was). The failure is a database error in the middle of a
// page, the way a lost connection or a full disk would be.
func poison(t *testing.T, api *apitest.API, table, policyColumn string) (failAt func(n int64), slow func(d time.Duration)) {
	t.Helper()
	super := api.Super(t)
	for _, q := range []string{
		`CREATE TABLE public.probe_flag (fail_at bigint NOT NULL DEFAULT 0, delay_ms double precision NOT NULL DEFAULT 0)`,
		`INSERT INTO public.probe_flag VALUES (0, 0)`,
		`GRANT SELECT ON public.probe_flag TO PUBLIC`,
		`CREATE FUNCTION public.probe_row(n bigint) RETURNS boolean LANGUAGE plpgsql VOLATILE COST 100000 AS $$
			DECLARE f public.probe_flag;
			BEGIN
			  SELECT * INTO f FROM public.probe_flag;
			  IF f.delay_ms > 0 THEN PERFORM pg_sleep(f.delay_ms / 1000.0); END IF;
			  IF f.fail_at > 0 AND n = f.fail_at THEN RAISE EXCEPTION 'probe: the read fails here'; END IF;
			  RETURN true;
			END $$`,
		`GRANT EXECUTE ON FUNCTION public.probe_row(bigint) TO PUBLIC`,
	} {
		if _, err := super.Exec(ctx(t), q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := super.Exec(ctx(t), fmt.Sprintf(`ALTER POLICY tenant_isolation ON pandokos.%s USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND public.probe_row(%s))`, table, policyColumn)); err != nil {
		t.Fatal(err)
	}
	set := func(sql string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx(t), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	return func(n int64) { set(`UPDATE public.probe_flag SET fail_at = $1`, n) },
		func(d time.Duration) {
			set(`UPDATE public.probe_flag SET delay_ms = $1`, float64(d)/float64(time.Millisecond))
		}
}

// get makes a raw request on the API with a key and returns the response without reading its body.
func get(t *testing.T, api *apitest.API, key, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx(t), "GET", api.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func ndjsonLines(t *testing.T, body io.Reader) ([][]byte, error) {
	t.Helper()
	var out [][]byte
	r := bufio.NewReaderSize(body, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && err == nil {
			out = append(out, bytes.TrimSuffix(line, []byte("\n")))
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) == 0 {
				return out, nil
			}
			return out, err
		}
	}
}

// bulkAudit writes n rows into a tenant's log as the owner, through the seal trigger.
func bulkAudit(t *testing.T, api *apitest.API, tenantID string, n int) {
	t.Helper()
	if _, err := api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
		SELECT $1::uuid, 'platform', 'bulk', 'bulk.row', 'test', g::text, 'success', jsonb_build_object('n', g, 'price', 1.50) FROM generate_series(1, $2) g`, tenantID, n); err != nil {
		t.Fatal(err)
	}
}

// TestAuditExport is T-945: NDJSON with its content type and disposition, oldest first, from_seq
// and to_seq, from the anchor to the head read at the start, and a failure after the first byte ends
// the transfer without its terminator and says so in the log; before the first byte it is a problem.
func TestAuditExport(t *testing.T) {
	w, _ := auditWorld(t)
	failAt, _ := poison(t, w.api, "audit_event", "seq")
	bulkAudit(t, w.api, w.a.ID, 2300)
	// The planner reads the rows in the order of the index and stops at the page only when it knows the table; without
	// statistics it may evaluate the failing row of the probe while it looks for the first page.
	if _, err := w.api.Super(t).Exec(ctx(t), `ANALYZE pandokos.audit_event`); err != nil {
		t.Fatal(err)
	}
	path := "/v1/tenants/" + w.a.ID + "/audit-events/export"
	head := decodeInto[adminapi.AuditHead](t, w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/audit-head", nil), 200)

	t.Run("oldest first from the anchor to the head, with its headers", func(t *testing.T) {
		resp := get(t, w.api, w.ka, path)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
			t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if d := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(d, "attachment;") || !strings.Contains(d, `filename="audit-`) || !strings.Contains(d, ".ndjson") {
			t.Errorf("Content-Disposition %q", d)
		}
		lines, err := ndjsonLines(t, resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != head.Seq {
			t.Fatalf("%d lines, want %d (the anchor to the head)", len(lines), head.Seq)
		}
		for i, l := range lines {
			var e adminapi.AuditEvent
			if err := json.Unmarshal(l, &e); err != nil || e.Seq != head.AnchorSeq+i {
				t.Fatalf("line %d: %v seq %d, want %d", i, err, e.Seq, head.AnchorSeq+i)
			}
		}
	})

	t.Run("from_seq and to_seq", func(t *testing.T) {
		resp := get(t, w.api, w.ka, path+"?from_seq=10&to_seq=1500")
		lines, err := ndjsonLines(t, resp.Body)
		if err != nil || len(lines) != 1491 {
			t.Fatalf("%d lines (%v), want 1491", len(lines), err)
		}
		var first, last adminapi.AuditEvent
		_ = json.Unmarshal(lines[0], &first)
		_ = json.Unmarshal(lines[len(lines)-1], &last)
		if first.Seq != 10 || last.Seq != 1500 {
			t.Errorf("from %d to %d, want 10 to 1500", first.Seq, last.Seq)
		}
		if r := w.do(t, w.ka, "GET", path+"?from_seq=0", nil); r.Status != 400 {
			t.Errorf("from_seq=0: %d, want 400", r.Status)
		}
		if r := w.do(t, w.ka, "GET", path+"?from_seq=20&to_seq=10", nil); r.Status != 400 {
			t.Errorf("from_seq after to_seq: %d, want 400", r.Status)
		}
	})

	t.Run("a row written during the export is not in it", func(t *testing.T) {
		resp := get(t, w.api, w.ka, path)
		r := bufio.NewReaderSize(resp.Body, 1<<20)
		first, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		_ = first
		decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts", map[string]any{"name": apitest.UniqueName("late"), "roles": []string{"inference"}}), 201)
		lines, err := ndjsonLines(t, r)
		if err != nil {
			t.Fatal(err)
		}
		var last adminapi.AuditEvent
		_ = json.Unmarshal(lines[len(lines)-1], &last)
		if last.Seq != head.Seq || len(lines)+1 != head.Seq {
			t.Errorf("the export ends at seq %d with %d lines, want the head read at the start (%d)", last.Seq, len(lines)+1, head.Seq)
		}
	})

	t.Run("the chain of an export verifies", func(t *testing.T) {
		resp := get(t, w.api, w.ka, path+"?to_seq="+strconv.Itoa(head.Seq))
		lines, err := ndjsonLines(t, resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var rows []auditchain.Row
		for _, l := range lines {
			var e adminapi.AuditEvent
			if err := json.Unmarshal(l, &e); err != nil {
				t.Fatal(err)
			}
			prev, _ := hexBytes(e.PrevHash)
			hash, _ := hexBytes(e.Hash)
			rows = append(rows, auditchain.Row{Scope: "tenant:" + w.a.ID, Seq: int64(e.Seq), ID: e.ID, OccurredAt: e.OccurredAt, ActorType: e.ActorType, ActorID: e.ActorID, ActorName: e.ActorName,
				KeyID: e.KeyID, Action: e.Action, ResourceType: e.ResourceType, ResourceID: e.ResourceID, Outcome: e.Outcome, RequestID: e.RequestID, Reason: e.Reason,
				SupportActionID: e.SupportActionID, Detail: e.Detail, PrevHash: prev, Hash: hash})
		}
		h, _ := hexBytes(head.Hash)
		ah, _ := hexBytes(head.AnchorHash)
		res := auditchain.Verify(func(yield func(auditchain.Row, error) bool) {
			for _, r := range rows {
				if !yield(r, nil) {
					return
				}
			}
		}, &auditchain.Head{Seq: int64(head.Seq), Hash: h, AnchorSeq: int64(head.AnchorSeq), AnchorHash: ah}, nil)
		if len(res.Breaks) != 0 || res.Rows != int64(len(rows)) {
			t.Errorf("Verify: %+v", res)
		}
	})

	t.Run("a failure after the first page aborts the transfer", func(t *testing.T) {
		failAt(1500) // the second page of 1000 rows
		defer failAt(0)
		resp := get(t, w.api, w.ka, path+"?to_seq="+strconv.Itoa(head.Seq))
		if resp.StatusCode != 200 {
			t.Fatalf("the status is %d: the first page was good", resp.StatusCode)
		}
		lines, err := ndjsonLines(t, resp.Body)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("the body ended with %v after %d lines, want an unexpected EOF: a truncated export must not look complete", err, len(lines))
		}
		testEventually(t, func() bool { return len(w.api.Logs.Find(t, "export aborted")) == 1 })
		line := w.api.Logs.Find(t, "export aborted")[0]
		if line["level"] != "WARN" || line["request_id"] == nil || line["error_class"] == nil || line["duration_ms"] == nil {
			t.Errorf("the line export aborted: %v", line)
		}
		if strings.Contains(w.api.Logs.String(), w.a.ID+"\",\"") && strings.Contains(fmt.Sprint(line), w.a.ID) {
			t.Errorf("the line names the tenant: %v", line)
		}
	})

	t.Run("a failure before the first byte is a problem", func(t *testing.T) {
		failAt(1)
		defer failAt(0)
		r := w.do(t, w.ka, "GET", path, nil)
		p := expect(t, r, 500, adminapi.ProblemCodeInternal)
		if p.RequestID == "" {
			t.Errorf("%+v", p)
		}
	})
}

func hexBytes(s string) ([]byte, error) {
	b := make([]byte, len(s)/2)
	_, err := fmt.Sscanf(s, "%x", &b)
	return b, err
}

func testEventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition did not come true in 5 s")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// bulkUsage writes n usage records of a tenant, one per second from start, as the owner.
func bulkUsage(t *testing.T, api *apitest.API, tenantID string, start time.Time, n int) {
	t.Helper()
	if _, err := api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, key_id, provider, api_family, operation, model, outcome,
			duration_ms, input_source, output_source, input_tokens, output_tokens, cost_status, metadata)
		SELECT $1::uuid, pandokos.uuid_v7(), $2::timestamptz + g * interval '1 second', md5(g::text), 'service_account', '0192f0f0-0000-7000-8000-0000000000a1', 'tkeyaaaaaaaaaaaaa',
		       'openai', 'openai', 'chat.completions', 'export-model', 'ok', g, 'provider', 'provider', g, 2, 'none', jsonb_build_object('team', 'alpha', 'n', g::text)
		  FROM generate_series(1, $3) g`, tenantID, start.UTC(), n); err != nil {
		t.Fatal(err)
	}
}

// TestUsageExport is T-957: NDJSON equal to the items of listUsageRecords for the window (oldest
// first), CSV with a header and one line per record, a text cell that starts with a formula
// character quoted in CSV and untouched in NDJSON, the window of 31 days, a short write timeout and
// a slow page, a failure after the first byte, the content disposition.
func TestUsageExport(t *testing.T) {
	d := newPW(t)
	api := apitest.NewWithServer(t, d.api.DB, func(s *http.Server) { s.WriteTimeout = time.Second })
	_ = api
	w := d
	failAt, slow := poison(t, w.api, "usage_record", "(extract(epoch FROM started_at) * 1000)::bigint % 100000000")
	_ = failAt
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	bulkUsage(t, w.api, w.a.ID, start, 1200)
	if _, err := w.api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation, model, outcome,
			duration_ms, input_source, output_source, cost_status, metadata)
		VALUES ($1::uuid, pandokos.uuid_v7(), $2, md5('formula'), 'service_account', '0192f0f0-0000-7000-8000-0000000000a1', 'openai', 'openai', 'chat.completions', '=cmd', 'ok', 1, 'none', 'none', 'none',
			jsonb_build_object('team', '=1+1', 'plus', '+1', 'minus', '-1', 'at', '@x', 'plain', 'a=b'))`, w.a.ID, start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	from, to := start.Add(-2*time.Hour), start.Add(2*time.Hour)
	q := "from=" + url.QueryEscape(from.Format(time.RFC3339)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339))
	base := "/v1/tenants/" + w.a.ID + "/usage-records"

	listed := func() []map[string]any {
		t.Helper()
		var all []map[string]any
		cursor := ""
		for i := 0; i < 100; i++ {
			p := base + "?limit=200&" + q
			if cursor != "" {
				p += "&cursor=" + url.QueryEscape(cursor)
			}
			var page struct {
				Items      []map[string]any `json:"items"`
				NextCursor string           `json:"next_cursor"`
			}
			ok(t, w.do(t, w.ka, "GET", p, nil), 200, &page)
			all = append(all, page.Items...)
			if page.NextCursor == "" {
				return all
			}
			cursor = page.NextCursor
		}
		t.Fatal("the pages do not end")
		return nil
	}

	t.Run("NDJSON equals the list, oldest first", func(t *testing.T) {
		items := listed()
		resp := get(t, w.api, w.ka, base+"/export?"+q)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
			t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, `filename="usage-`+w.a.ID) || !strings.HasSuffix(cd, `.ndjson"`) {
			t.Errorf("Content-Disposition %q", cd)
		}
		lines, err := ndjsonLines(t, resp.Body)
		if err != nil || len(lines) != len(items) || len(items) != 1201 {
			t.Fatalf("%d lines (%v), the list has %d, want 1201", len(lines), err, len(items))
		}
		for i, l := range lines {
			var got map[string]any
			if err := json.Unmarshal(l, &got); err != nil {
				t.Fatal(err)
			}
			if want := items[len(items)-1-i]; !reflect.DeepEqual(got, want) {
				t.Fatalf("line %d differs from the item of the list:\n%v\n%v", i, got, want)
			}
		}
		// A value that starts with = is not changed in NDJSON.
		found := false
		for _, l := range lines {
			found = found || bytes.Contains(l, []byte(`{"key":"team","value":"=1+1"}`)) // metadata is an array of entries
		}
		if !found {
			t.Error(`the NDJSON does not hold the entry team = "=1+1" as written`)
		}
	})

	t.Run("CSV: a header, a line per record, formulas quoted", func(t *testing.T) {
		resp := get(t, w.api, w.ka, base+"/export?format=csv&"+q)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Type"), "utf-8") {
			t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if cd := resp.Header.Get("Content-Disposition"); !strings.HasSuffix(cd, `.csv"`) {
			t.Errorf("Content-Disposition %q", cd)
		}
		recs, err := csv.NewReader(resp.Body).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1202 {
			t.Fatalf("%d lines, want a header and 1201", len(recs))
		}
		var want []string
		typ := reflect.TypeOf(adminapi.UsageRecord{})
		for i := 0; i < typ.NumField(); i++ {
			want = append(want, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
		}
		if !reflect.DeepEqual(recs[0], want) {
			t.Errorf("the header is %v, want the members of UsageRecord in schema order %v", recs[0], want)
		}
		col := func(name string) int {
			for i, h := range recs[0] {
				if h == name {
					return i
				}
			}
			t.Fatalf("no column %s", name)
			return -1
		}
		meta := col("metadata")
		var formula []string
		for _, r := range recs[1:] {
			if strings.Contains(r[meta], "team") && strings.Contains(r[meta], "1+1") {
				formula = r
			}
		}
		if formula == nil {
			t.Fatal("the record with a formula is not in the CSV")
		}
		var entries []struct{ Key, Value string }
		if err := json.Unmarshal([]byte(formula[meta]), &entries); err != nil {
			t.Fatalf("an array is its JSON text in one cell: %v %q", err, formula[meta])
		}
		m := map[string]string{}
		for _, e := range entries {
			m[e.Key] = e.Value
		}
		// The JSON text of the cell starts with {: only a text cell that starts with a formula character is quoted.
		if formula[col("model")] != "'=cmd" {
			t.Errorf("the model =cmd is %q in CSV, want '=cmd", formula[col("model")])
		}
		if m["team"] != "=1+1" {
			t.Errorf("a value inside a JSON cell is not changed: %q", m["team"])
		}
		for _, r := range recs[1:] {
			if cost := r[col("cost")]; cost != "" && strings.ContainsAny(cost[:1], "=+-@") && cost[0] != '-' {
				t.Errorf("a number is not quoted: %q", cost)
			}
		}
	})

	t.Run("the window is at most 31 days", func(t *testing.T) {
		ok31 := "from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z"
		if r := get(t, w.api, w.ka, base+"/export?"+ok31); r.StatusCode != 200 {
			t.Errorf("31 days: %d", r.StatusCode)
		}
		r := w.do(t, w.ka, "GET", base+"/export?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:01Z", nil)
		expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
	})

	t.Run("a short write timeout and a slow page: the export completes", func(t *testing.T) {
		slow(1200 * time.Microsecond) // a page of 1000 rows takes more than the write timeout of 1 s
		defer slow(0)
		client := &http.Client{Timeout: 30 * time.Second}
		req, _ := http.NewRequestWithContext(ctx(t), "GET", api.URL+base+"/export?"+q, nil)
		req.Header.Set("Authorization", "Bearer "+w.ka)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		lines, err := ndjsonLines(t, resp.Body)
		if err != nil || len(lines) != 1201 {
			t.Errorf("%d lines (%v) with a write timeout of 1 s, want 1201: every page extends the deadline", len(lines), err)
		}
	})

	t.Run("a failure after the first byte aborts the transfer", func(t *testing.T) {
		before := len(w.api.Logs.Find(t, "export aborted"))
		failAt(0)
		if _, err := w.api.Super(t).Exec(ctx(t), `UPDATE public.probe_flag SET fail_at = (extract(epoch FROM $1::timestamptz) * 1000)::bigint % 100000000`, start.Add(1100*time.Second)); err != nil {
			t.Fatal(err)
		}
		resp := get(t, w.api, w.ka, base+"/export?"+q)
		_, err := ndjsonLines(t, resp.Body)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("the body ended with %v, want an unexpected EOF", err)
		}
		testEventually(t, func() bool { return len(w.api.Logs.Find(t, "export aborted")) == before+1 })
	})
	_ = context.Background
}
