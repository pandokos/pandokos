package cli_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
)

// The twelve operations of sprint 6.2 on the fake: each records its input and returns the canned
// output; an export answers with the stream the test gave (f.streams) and fails after its first
// bytes when f.streamFails names it.
type streamed struct {
	streams     map[string]string
	streamFails map[string]bool
}

var streams = struct {
	m map[*fakeAPI]*streamed
}{m: map[*fakeAPI]*streamed{}}

func (f *fakeAPI) streamState() *streamed {
	f.mu.Lock()
	defer f.mu.Unlock()
	if streams.m[f] == nil {
		streams.m[f] = &streamed{streams: map[string]string{}, streamFails: map[string]bool{}}
	}
	return streams.m[f]
}

func (f *fakeAPI) stream(id string, in any, contentType string) (*adminapi.Stream, error) {
	if err := f.rec(id, in); err != nil {
		return nil, err
	}
	st := f.streamState()
	f.mu.Lock()
	body, fails := st.streams[id], st.streamFails[id]
	f.mu.Unlock()
	return &adminapi.Stream{ContentType: contentType, Filename: "export.ndjson", Write: func(_ context.Context, w io.Writer) error {
		if fails {
			_, _ = io.WriteString(w, body[:len(body)/2])
			return errors.New("the database failed")
		}
		_, err := io.WriteString(w, body)
		return err
	}}, nil
}

func (f *fakeAPI) GetUsageReport(_ context.Context, in adminapi.GetUsageReportInput) (*adminapi.UsageReport, error) {
	return pick[adminapi.UsageReport](f, "getUsageReport"), f.rec("getUsageReport", in)
}
func (f *fakeAPI) ExportUsageRecords(_ context.Context, in adminapi.ExportUsageRecordsInput) (*adminapi.Stream, error) {
	ct := "application/x-ndjson"
	if in.Format == adminapi.ExportFormatCsv {
		ct = "text/csv; charset=utf-8"
	}
	return f.stream("exportUsageRecords", in, ct)
}
func (f *fakeAPI) GetLimitUsage(_ context.Context, in adminapi.GetLimitUsageInput) (*adminapi.LimitUsage, error) {
	return pick[adminapi.LimitUsage](f, "getLimitUsage"), f.rec("getLimitUsage", in)
}
func (f *fakeAPI) ListAuditEvents(_ context.Context, in adminapi.ListAuditEventsInput) (*adminapi.AuditEventList, error) {
	return pick[adminapi.AuditEventList](f, "listAuditEvents"), f.rec("listAuditEvents", in)
}
func (f *fakeAPI) GetAuditHead(_ context.Context, in adminapi.GetAuditHeadInput) (*adminapi.AuditHead, error) {
	return pick[adminapi.AuditHead](f, "getAuditHead"), f.rec("getAuditHead", in)
}
func (f *fakeAPI) ExportAuditEvents(_ context.Context, in adminapi.ExportAuditEventsInput) (*adminapi.Stream, error) {
	return f.stream("exportAuditEvents", in, "application/x-ndjson")
}
func (f *fakeAPI) ListPlatformAuditEvents(_ context.Context, in adminapi.ListPlatformAuditEventsInput) (*adminapi.AuditEventList, error) {
	return pick[adminapi.AuditEventList](f, "listPlatformAuditEvents"), f.rec("listPlatformAuditEvents", in)
}
func (f *fakeAPI) GetPlatformAuditHead(_ context.Context, in adminapi.GetPlatformAuditHeadInput) (*adminapi.AuditHead, error) {
	return pick[adminapi.AuditHead](f, "getPlatformAuditHead"), f.rec("getPlatformAuditHead", in)
}
func (f *fakeAPI) ExportPlatformAuditEvents(_ context.Context, in adminapi.ExportPlatformAuditEventsInput) (*adminapi.Stream, error) {
	return f.stream("exportPlatformAuditEvents", in, "application/x-ndjson")
}
func (f *fakeAPI) GetMetadataKeys(_ context.Context, in adminapi.GetMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	return pick[adminapi.MetadataKeys](f, "getMetadataKeys"), f.rec("getMetadataKeys", in)
}
func (f *fakeAPI) SetMetadataKeys(_ context.Context, in adminapi.SetMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	return pick[adminapi.MetadataKeys](f, "setMetadataKeys"), f.rec("setMetadataKeys", in)
}
func (f *fakeAPI) ClearMetadataKeys(_ context.Context, in adminapi.ClearMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	return pick[adminapi.MetadataKeys](f, "clearMetadataKeys"), f.rec("clearMetadataKeys", in)
}

// chainExport makes the NDJSON export of a chain of n rows with the hashes of auditchain, and its head.
func chainExport(t *testing.T, scope string, n int) (lines []string, head adminapi.AuditHead) {
	t.Helper()
	prev := make([]byte, 32)
	head.AnchorHash = hex.EncodeToString(prev)
	head.AnchorSeq = 1
	for i := 1; i <= n; i++ {
		r := auditchain.Row{Scope: scope, Seq: int64(i), ID: fmt.Sprintf("0192f0f0-2222-7000-8000-%012d", i), OccurredAt: time.Date(2026, 11, 3, 10, 0, i, 0, time.UTC),
			ActorType: "user", ActorID: "u", ActorName: "Ada", Action: "member.create", ResourceType: "member", ResourceID: fmt.Sprint(i), Outcome: "success",
			RequestID: strings.Repeat("a", 32), Detail: []byte(`{"n": 1.50}`), PrevHash: prev}
		h, err := auditchain.Hash(r)
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		e := adminapi.AuditEvent{Seq: i, ID: r.ID, OccurredAt: r.OccurredAt, ActorType: r.ActorType, ActorID: r.ActorID, ActorName: r.ActorName, Action: r.Action, ResourceType: r.ResourceType,
			ResourceID: r.ResourceID, Outcome: r.Outcome, RequestID: r.RequestID, Detail: r.Detail, PrevHash: hex.EncodeToString(prev), Hash: hex.EncodeToString(h[:])}
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
		prev = h[:]
		head.Seq, head.Hash = i, e.Hash
	}
	return lines, head
}

// TestReportAuditCommands is T-970: every new command sends the flags it is given and none it is
// not given; --month computes the dates; usage export splits a long window into requests and writes
// its file through a temporary name; audit verify reads the head and then the export and prints the
// line of section 5.9.
func TestReportAuditCommands(t *testing.T) {
	t.Run("usage report sends what it is given", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "usage", "report", "--tenant", tid)
		if got := r.f.only(t, "getUsageReport"); !reflect.DeepEqual(got, adminapi.GetUsageReportInput{Tenant: tid}) {
			t.Errorf("without flags the API received %+v, want the tenant alone", got)
		}
		r = newRig(t)
		mustRun(t, r, "usage", "report", "--tenant", tid, "--from", "2026-10-01", "--to", "2026-11-01", "--period", "day", "--group-by", "model,key", "--provider", "openai", "--model", "gpt-4o")
		want := adminapi.GetUsageReportInput{Tenant: tid, From: "2026-10-01", To: "2026-11-01", Period: adminapi.ReportPeriodDay, GroupBy: "model,key", Provider: "openai", Model: "gpt-4o"}
		if got := r.f.only(t, "getUsageReport"); !reflect.DeepEqual(got, want) {
			t.Errorf("the API received %+v, want %+v", got, want)
		}
	})

	t.Run("--month computes the first days", func(t *testing.T) {
		for month, want := range map[string][2]string{"2026-10": {"2026-10-01", "2026-11-01"}, "2026-12": {"2026-12-01", "2027-01-01"}, "2028-02": {"2028-02-01", "2028-03-01"}} {
			r := newRig(t)
			mustRun(t, r, "usage", "report", "--tenant", tid, "--month", month)
			got := r.f.only(t, "getUsageReport").(adminapi.GetUsageReportInput)
			if got.From != want[0] || got.To != want[1] {
				t.Errorf("--month %s: %s to %s, want %s to %s", month, got.From, got.To, want[0], want[1])
			}
		}
		for _, args := range [][]string{
			{"--month", "2026-13"}, {"--month", "October"}, {"--month", "2026-10", "--from", "2026-10-01"}, {"--month", "2026-10", "--to", "2026-11-01"},
			{"--period", "week"}, {"--group-by", "tenant"},
		} {
			r := newRig(t)
			if code, _, errs := r.run(t, append([]string{"usage", "report", "--tenant", tid}, args...)...); code != 2 || errs == "" {
				t.Errorf("%v: exit %d (%s), want a usage error", args, code, errs)
			}
			if len(r.f.calls("getUsageReport")) != 0 {
				t.Errorf("%v: the API was called", args)
			}
		}
	})

	t.Run("limit usage and the metadata keys", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "limit", "usage", "--tenant", tid, sid)
		if got := r.f.only(t, "getLimitUsage"); !reflect.DeepEqual(got, adminapi.GetLimitUsageInput{Tenant: tid, Rule: sid}) {
			t.Errorf("limit usage: %+v", got)
		}
		r = newRig(t)
		mustRun(t, r, "metadata-keys", "get", "--tenant", tid)
		mustRun(t, r, "metadata-keys", "set", "--tenant", tid, "team", "project")
		mustRun(t, r, "metadata-keys", "clear", "--tenant", tid)
		if got := r.f.only(t, "getMetadataKeys"); !reflect.DeepEqual(got, adminapi.GetMetadataKeysInput{Tenant: tid}) {
			t.Errorf("metadata-keys get: %+v", got)
		}
		if got := r.f.only(t, "setMetadataKeys"); !reflect.DeepEqual(got, adminapi.SetMetadataKeysInput{Tenant: tid, Body: adminapi.MetadataKeysSet{Keys: []string{"team", "project"}}}) {
			t.Errorf("metadata-keys set: %+v", got)
		}
		if got := r.f.only(t, "clearMetadataKeys"); !reflect.DeepEqual(got, adminapi.ClearMetadataKeysInput{Tenant: tid}) {
			t.Errorf("metadata-keys clear: %+v", got)
		}
		// set with no key is an empty list: a policy that keeps nothing.
		r = newRig(t)
		mustRun(t, r, "metadata-keys", "set", "--tenant", tid)
		if got := r.f.only(t, "setMetadataKeys").(adminapi.SetMetadataKeysInput); len(got.Body.Keys) != 0 || got.Body.Keys == nil {
			t.Errorf("set without keys sent %+v, want an empty list", got.Body.Keys)
		}
	})

	t.Run("audit list, head and export send what they are given", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "audit", "list", "--tenant", tid)
		if got := r.f.only(t, "listAuditEvents"); !reflect.DeepEqual(got, adminapi.ListAuditEventsInput{Tenant: tid}) {
			t.Errorf("audit list without flags: %+v", got)
		}
		r = newRig(t)
		mustRun(t, r, "audit", "list", "--tenant", tid, "--action", "member.", "--resource-type", "member", "--actor-type", "user", "--outcome", "failure",
			"--request-id", strings.Repeat("a", 32), "--support-action-id", sid, "--from", "2026-11-01T00:00:00Z", "--limit", "5")
		got := r.f.only(t, "listAuditEvents").(adminapi.ListAuditEventsInput)
		if got.Tenant != tid || got.Action != "member." || got.ResourceType != "member" || got.ActorType != adminapi.AuditActorTypeUser || got.Outcome != adminapi.AuditOutcomeFailure ||
			got.RequestID != strings.Repeat("a", 32) || got.SupportActionID != sid || got.From != "2026-11-01T00:00:00Z" || got.Limit != 5 || got.To != "" || got.ActorID != "" {
			t.Errorf("audit list: %+v", got)
		}
		r = newRig(t)
		mustRun(t, r, "audit", "head", "--tenant", tid)
		if got := r.f.only(t, "getAuditHead"); !reflect.DeepEqual(got, adminapi.GetAuditHeadInput{Tenant: tid}) {
			t.Errorf("audit head: %+v", got)
		}
		r = newRig(t)
		r.f.streamState().streams["exportAuditEvents"] = "{\"seq\":3}\n"
		out := filepath.Join(t.TempDir(), "audit.ndjson")
		mustRun(t, r, "audit", "export", "--tenant", tid, "--from-seq", "3", "--to-seq", "9", "--out", out)
		if got := r.f.only(t, "exportAuditEvents"); !reflect.DeepEqual(got, adminapi.ExportAuditEventsInput{Tenant: tid, FromSeq: 3, ToSeq: 9}) {
			t.Errorf("audit export: %+v", got)
		}
		if b, err := os.ReadFile(out); err != nil || string(b) != "{\"seq\":3}\n" {
			t.Errorf("the file: %q %v", b, err)
		}
	})

	t.Run("platform-audit", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "platform-audit", "list", "--action", "tenant.", "--limit", "3")
		mustRun(t, r, "platform-audit", "head")
		r.f.streamState().streams["exportPlatformAuditEvents"] = "{\"seq\":1}\n"
		mustRun(t, r, "platform-audit", "export", "--from-seq", "1")
		if got := r.f.only(t, "listPlatformAuditEvents"); !reflect.DeepEqual(got, adminapi.ListPlatformAuditEventsInput{Action: "tenant.", Limit: 3}) {
			t.Errorf("platform-audit list: %+v", got)
		}
		if got := r.f.only(t, "getPlatformAuditHead"); !reflect.DeepEqual(got, adminapi.GetPlatformAuditHeadInput{}) {
			t.Errorf("platform-audit head: %+v", got)
		}
		if got := r.f.only(t, "exportPlatformAuditEvents"); !reflect.DeepEqual(got, adminapi.ExportPlatformAuditEventsInput{FromSeq: 1}) {
			t.Errorf("platform-audit export: %+v", got)
		}
	})

	t.Run("usage export splits a long window and writes its file through a temporary name", func(t *testing.T) {
		r := newRig(t)
		r.f.streamState().streams["exportUsageRecords"] = "{\"id\":\"x\"}\n"
		dir := t.TempDir()
		out := filepath.Join(dir, "usage.ndjson")
		// 70 days: 31, 31 and 8.
		mustRun(t, r, "usage", "export", "--tenant", tid, "--from", "2026-09-01T00:00:00Z", "--to", "2026-11-10T00:00:00Z", "--provider", "openai", "--out", out)
		calls := r.f.calls("exportUsageRecords")
		if len(calls) != 3 {
			t.Fatalf("%d requests, want 3", len(calls))
		}
		var prev time.Time
		for i, c := range calls {
			in := c.(adminapi.ExportUsageRecordsInput)
			from, err1 := time.Parse(time.RFC3339, in.From)
			to, err2 := time.Parse(time.RFC3339, in.To)
			if err1 != nil || err2 != nil || to.Sub(from) > 31*24*time.Hour || in.Provider != "openai" || in.Tenant != tid {
				t.Fatalf("request %d: %+v", i, in)
			}
			if i > 0 && !from.Equal(prev) {
				t.Errorf("request %d starts at %v, want the end of the one before (%v)", i, from, prev)
			}
			prev = to
		}
		if !prev.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("the last request ends at %v", prev)
		}
		if b, err := os.ReadFile(out); err != nil || string(b) != strings.Repeat("{\"id\":\"x\"}\n", 3) {
			t.Errorf("the file holds %q (%v), want the three bodies in order", b, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("the directory holds %d files after the export, want only the result", len(entries))
		}

		// A transfer that fails halfway leaves no file, and no temporary file either.
		r = newRig(t)
		r.f.streamState().streams["exportUsageRecords"] = strings.Repeat("{\"id\":\"x\"}\n", 100)
		r.f.streamState().streamFails["exportUsageRecords"] = true
		dir = t.TempDir()
		out = filepath.Join(dir, "usage.ndjson")
		if code, _, errs := r.run(t, "usage", "export", "--tenant", tid, "--from", "2026-10-01T00:00:00Z", "--to", "2026-10-02T00:00:00Z", "--out", out); code != 1 {
			t.Errorf("a failed transfer: exit %d (%s), want 1", code, errs)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("a failed export left %d files", len(entries))
		}
		// To standard output without --out; the format reaches the API.
		r = newRig(t)
		r.f.streamState().streams["exportUsageRecords"] = "id\nx\n"
		code, stdout, _ := r.run(t, "usage", "export", "--tenant", tid, "--from", "2026-10-01T00:00:00Z", "--to", "2026-10-02T00:00:00Z", "--format", "csv")
		if got := r.f.only(t, "exportUsageRecords").(adminapi.ExportUsageRecordsInput); code != 0 || stdout != "id\nx\n" || got.Format != adminapi.ExportFormatCsv {
			t.Errorf("csv to stdout: exit %d %q %+v", code, stdout, got)
		}
	})

	t.Run("audit verify", func(t *testing.T) {
		scope := "tenant:" + tid
		lines, head := chainExport(t, scope, 6)
		r := newRig(t)
		r.f.out["getAuditHead"] = &head
		r.f.streamState().streams["exportAuditEvents"] = strings.Join(lines, "\n") + "\n"
		code, out, errs := r.run(t, "audit", "verify", "--tenant", tid)
		if code != 0 || strings.TrimSpace(out) != "verified 6 rows, seq 1..6, head "+head.Hash {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		if len(r.f.calls("getAuditHead")) != 1 || len(r.f.calls("exportAuditEvents")) != 1 {
			t.Errorf("verify calls: head %d, export %d, want one each", len(r.f.calls("getAuditHead")), len(r.f.calls("exportAuditEvents")))
		}
		if got := r.f.calls("exportAuditEvents")[0].(adminapi.ExportAuditEventsInput); got.ToSeq != 6 {
			t.Errorf("the export is read up to the head: %+v", got)
		}
		// Offline, from a file, no server needed; with the expected head.
		file := filepath.Join(t.TempDir(), "export.ndjson")
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errs = runWith(t, nil, "audit", "verify", "--tenant", tid, "--file", file, "--expect-head", head.Hash)
		if code != 0 || !strings.HasPrefix(out, "verified 6 rows, seq 1..6, head ") {
			t.Errorf("offline: exit %d, stdout %q, stderr %q", code, out, errs)
		}
		code, out, _ = runWith(t, nil, "audit", "verify", "--tenant", tid, "--file", file, "--expect-head", strings.Repeat("0", 64))
		if code != 1 || !strings.Contains(out, "break at seq 6: expected_head_mismatch") {
			t.Errorf("a different expected head: exit %d, stdout %q", code, out)
		}
		// A changed row: the first break is named.
		changed := append([]string(nil), lines...)
		changed[2] = strings.Replace(changed[2], `"action":"member.create"`, `"action":"member.delete"`, 1)
		if err := os.WriteFile(file, []byte(strings.Join(changed, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = runWith(t, nil, "audit", "verify", "--tenant", tid, "--file", file)
		if code != 1 || !strings.Contains(out, "break at seq 3: hash_mismatch") {
			t.Errorf("a changed row: exit %d, stdout %q", code, out)
		}
		// A truncated last line and a line that is not JSON: invalid export, exit 1.
		for name, body := range map[string]string{
			"a truncated last line":   strings.Join(lines, "\n") + "\n" + lines[0][:20],
			"a line that is not JSON": strings.Join(lines[:2], "\n") + "\nthis is not json\n",
		} {
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, errs = runWith(t, nil, "audit", "verify", "--tenant", tid, "--file", file)
			if code != 1 || !strings.Contains(out+errs, "invalid export:") {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want exit 1 and invalid export", name, code, out, errs)
			}
		}
	})

	t.Run("platform-audit verify", func(t *testing.T) {
		lines, head := chainExport(t, "platform", 4)
		r := newRig(t)
		r.f.out["getPlatformAuditHead"] = &head
		r.f.streamState().streams["exportPlatformAuditEvents"] = strings.Join(lines, "\n") + "\n"
		code, out, errs := r.run(t, "platform-audit", "verify")
		if code != 0 || strings.TrimSpace(out) != "verified 4 rows, seq 1..4, head "+head.Hash {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
}
