package cli_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/internal/tenantexport"
)

func (f *fakeAPI) RequestTenantDeletion(_ context.Context, in adminapi.RequestTenantDeletionInput) (*adminapi.TenantDeletion, error) {
	return pick[adminapi.TenantDeletion](f, "requestTenantDeletion"), f.rec("requestTenantDeletion", in)
}
func (f *fakeAPI) CancelTenantDeletion(_ context.Context, in adminapi.CancelTenantDeletionInput) (*adminapi.Tenant, error) {
	return pick[adminapi.Tenant](f, "cancelTenantDeletion"), f.rec("cancelTenantDeletion", in)
}
func (f *fakeAPI) GetTenantDeletion(_ context.Context, in adminapi.GetTenantDeletionInput) (*adminapi.TenantDeletion, error) {
	return pick[adminapi.TenantDeletion](f, "getTenantDeletion"), f.rec("getTenantDeletion", in)
}
func (f *fakeAPI) ListTenantDeletions(_ context.Context, in adminapi.ListTenantDeletionsInput) (*adminapi.TenantDeletionList, error) {
	return pick[adminapi.TenantDeletionList](f, "listTenantDeletions"), f.rec("listTenantDeletions", in)
}
func (f *fakeAPI) GetDeletedTenantAuditHead(_ context.Context, in adminapi.GetDeletedTenantAuditHeadInput) (*adminapi.AuditHead, error) {
	return pick[adminapi.AuditHead](f, "getDeletedTenantAuditHead"), f.rec("getDeletedTenantAuditHead", in)
}
func (f *fakeAPI) ExportDeletedTenantAuditEvents(_ context.Context, in adminapi.ExportDeletedTenantAuditEventsInput) (*adminapi.Stream, error) {
	return f.stream("exportDeletedTenantAuditEvents", in, "application/x-ndjson")
}
func (f *fakeAPI) ListKEKVersions(_ context.Context, in adminapi.ListKEKVersionsInput) (*adminapi.KEKVersionList, error) {
	return pick[adminapi.KEKVersionList](f, "listKEKVersions"), f.rec("listKEKVersions", in)
}

// chainOf makes the NDJSON export of a chain with the given actions, with the hashes of auditchain, the
// details given for some of them, and its head.
func chainOf(t *testing.T, scope string, actions []string, details map[int]string) (lines []string, head adminapi.AuditHead) {
	t.Helper()
	prev := make([]byte, 32)
	head.AnchorHash = hex.EncodeToString(prev)
	head.AnchorSeq = 1
	for i, action := range actions {
		n := i + 1
		detail := `{}`
		if d, ok := details[n]; ok {
			detail = d
		}
		resource := "r" + fmt.Sprint(n)
		if strings.HasPrefix(action, "tenant.delete") {
			resource = tid
		}
		r := auditchain.Row{Scope: scope, Seq: int64(n), ID: fmt.Sprintf("0192f0f0-4444-7000-8000-%012d", n), OccurredAt: time.Date(2026, 11, 3, 10, 0, n, 0, time.UTC),
			ActorType: "platform", Action: action, ResourceType: "tenant", ResourceID: resource, Outcome: "success", RequestID: strings.Repeat("a", 32), Detail: []byte(detail), PrevHash: prev}
		h, err := auditchain.Hash(r)
		if err != nil {
			t.Fatal(err)
		}
		e := adminapi.AuditEvent{Seq: n, ID: r.ID, OccurredAt: r.OccurredAt, ActorType: r.ActorType, Action: r.Action, ResourceType: r.ResourceType, ResourceID: r.ResourceID,
			Outcome: r.Outcome, RequestID: r.RequestID, Detail: r.Detail, PrevHash: hex.EncodeToString(prev), Hash: hex.EncodeToString(h[:])}
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
		prev = h[:]
		head.Seq, head.Hash = n, e.Hash
	}
	return lines, head
}

// verifyRig is a fake with the data of a completed deletion that verifies: the deletion, its record in the
// platform's chain, that chain, and the kept log of the tenant.
type verifyRig struct {
	*rig
	keptLines, platformLines []string
	deletion                 adminapi.TenantDeletion
	record                   adminapi.AuditEvent
	keptHead, platformHead   adminapi.AuditHead
	noRecord                 bool // the platform log has no row for the completion
}

func newVerifyRig(t *testing.T) *verifyRig {
	t.Helper()
	v := &verifyRig{rig: newRig(t)}
	v.keptLines, v.keptHead = chainOf(t, "tenant:"+tid, []string{"member.create", "role.create", "tenant.delete"}, nil)
	started, requested := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	removed := json.RawMessage(`{"service_account":3,"virtual_key":2}`)
	audit := adminapi.TenantDeletionAudit{AnchorSeq: 1, LastSeq: 3, Rows: 3, OriginalHead: strings.Repeat("ab", 32), ResealedHead: v.keptHead.Hash}
	v.deletion = adminapi.TenantDeletion{TenantID: tid, State: adminapi.TenantDeletionStateCompleted, Step: adminapi.TenantDeletionStepDone, Reason: "closing",
		RequestID: strings.Repeat("c", 32), RequestedAt: requested, NotBefore: requested, StartedAt: &started, CompletedAt: &started, Passes: 1, Removed: removed,
		KekVersions: []string{"local-0123456789abcdef"}, Audit: &audit, CompletionSeq: 2,
		BackupErasure: adminapi.TenantDeletionBackupErasure{State: adminapi.BackupErasureStatePending, PendingKekVersions: []string{"local-0123456789abcdef"}}}
	auditJSON, _ := json.Marshal(audit)
	detail := fmt.Sprintf(`{"name":"acme","requested_at":%q,"started_at":%q,"passes":1,"removed":%s,"kek_versions":["local-0123456789abcdef"],"usage":"pseudonymized","audit":%s,"residue":0}`,
		requested.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano), removed, auditJSON)
	v.platformLines, v.platformHead = chainOf(t, "platform", []string{"tenant.create", "tenant.delete_complete", "tenant.create"}, map[int]string{2: detail})
	_ = json.Unmarshal([]byte(v.platformLines[1]), &v.record)
	v.apply(t)
	return v
}

// apply loads the fields of the verifyRig into the fake.
func (v *verifyRig) apply(t *testing.T) {
	t.Helper()
	v.f.out["getTenantDeletion"] = &v.deletion
	items := []adminapi.AuditEvent{v.record}
	if v.noRecord {
		items = nil
	}
	v.f.out["listPlatformAuditEvents"] = &adminapi.AuditEventList{Items: items}
	v.f.out["getPlatformAuditHead"] = &v.platformHead
	v.f.out["getDeletedTenantAuditHead"] = &v.keptHead
	v.f.streamState().streams["exportPlatformAuditEvents"] = strings.Join(v.platformLines, "\n") + "\n"
	v.f.streamState().streams["exportDeletedTenantAuditEvents"] = strings.Join(v.keptLines, "\n") + "\n"
}

// TestLifecycleCommands is T-1010: every new command sends the flags it is given, and no other; the
// composites do what §5.5 and §5.11 say; tenant-export schema needs no server.
func TestLifecycleCommands(t *testing.T) {
	t.Run("tenant delete and the three commands of the deletion", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "tenant", "delete", tid, "--reason", "end of contract", "--confirm-name", "acme")
		want := adminapi.RequestTenantDeletionInput{Tenant: tid, Body: adminapi.TenantDeletionRequest{Reason: "end of contract", ConfirmName: "acme"}}
		if got := r.f.only(t, "requestTenantDeletion"); !reflect.DeepEqual(got, want) {
			t.Errorf("the API received %+v, want %+v", got, want)
		}
		for _, args := range [][]string{{"tenant", "delete", tid}, {"tenant", "delete", tid, "--reason", "r"}, {"tenant", "delete", tid, "--confirm-name", "acme"}, {"tenant", "delete", "--reason", "r", "--confirm-name", "acme"}} {
			r := newRig(t)
			if code, _, errs := r.run(t, args...); code != 2 || errs == "" {
				t.Errorf("%v: exit %d (%s), want a usage error", args, code, errs)
			}
			if len(r.f.calls("requestTenantDeletion")) != 0 {
				t.Errorf("%v: the API was called", args)
			}
		}

		r = newRig(t)
		mustRun(t, r, "tenant", "deletion", "get", tid)
		if got := r.f.only(t, "getTenantDeletion"); !reflect.DeepEqual(got, adminapi.GetTenantDeletionInput{Tenant: tid}) {
			t.Errorf("get: %+v", got)
		}
		r = newRig(t)
		mustRun(t, r, "tenant", "deletion", "cancel", tid, "--reason", "changed our mind")
		if got := r.f.only(t, "cancelTenantDeletion"); !reflect.DeepEqual(got, adminapi.CancelTenantDeletionInput{Tenant: tid, Body: adminapi.RequiredReason{Reason: "changed our mind"}}) {
			t.Errorf("cancel: %+v", got)
		}
		if code, _, _ := newRig(t).run(t, "tenant", "deletion", "cancel", tid); code != 2 {
			t.Errorf("a cancel without a reason: exit %d, want 2", code)
		}
		r = newRig(t)
		mustRun(t, r, "tenant", "deletion", "list")
		if got := r.f.only(t, "listTenantDeletions"); !reflect.DeepEqual(got, adminapi.ListTenantDeletionsInput{Limit: 50}) { // the router fills the default page size
			t.Errorf("list without flags: %+v", got)
		}
		r = newRig(t)
		mustRun(t, r, "tenant", "deletion", "list", "--state", "completed")
		if got := r.f.only(t, "listTenantDeletions").(adminapi.ListTenantDeletionsInput); got.State != adminapi.TenantDeletionStateCompleted {
			t.Errorf("list --state completed: %+v", got)
		}
		if code, _, _ := newRig(t).run(t, "tenant", "deletion", "list", "--state", "later"); code != 2 {
			t.Errorf("a state that does not exist: exit %d, want 2", code)
		}
		// The output of get is JSON on request, and never names a salt.
		r = newRig(t)
		r.f.out["getTenantDeletion"] = &adminapi.TenantDeletion{TenantID: tid, State: adminapi.TenantDeletionStateScheduled, Step: adminapi.TenantDeletionStepBegin, Reason: "x"}
		out, _ := mustRun(t, r, "tenant", "deletion", "get", tid, "-o", "json")
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil || m["state"] != "scheduled" || strings.Contains(out, "salt") {
			t.Errorf("get -o json: %v %s", err, out)
		}
	})

	t.Run("the kept log and the key versions", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "tenant", "deletion", "audit-head", tid)
		if got := r.f.only(t, "getDeletedTenantAuditHead"); !reflect.DeepEqual(got, adminapi.GetDeletedTenantAuditHeadInput{Tenant: tid}) {
			t.Errorf("audit-head: %+v", got)
		}
		lines, _ := chainOf(t, "tenant:"+tid, []string{"a.b", "c.d", "tenant.delete"}, nil)
		r = newRig(t)
		r.f.streamState().streams["exportDeletedTenantAuditEvents"] = strings.Join(lines, "\n") + "\n"
		out, _ := mustRun(t, r, "tenant", "deletion", "audit-export", tid)
		if out != strings.Join(lines, "\n")+"\n" {
			t.Errorf("audit-export wrote %q", out)
		}
		file := filepath.Join(t.TempDir(), "kept.ndjson")
		mustRun(t, r, "tenant", "deletion", "audit-export", tid, "--out", file)
		if b, err := os.ReadFile(file); err != nil || string(b) != strings.Join(lines, "\n")+"\n" {
			t.Errorf("audit-export --out: %v %q", err, b)
		}
		if len(r.f.calls("exportDeletedTenantAuditEvents")) != 2 {
			t.Errorf("%d exports", len(r.f.calls("exportDeletedTenantAuditEvents")))
		}
		r = newRig(t)
		r.f.out["listKEKVersions"] = &adminapi.KEKVersionList{Items: []adminapi.KEKVersion{{ID: "local-0123456789abcdef", Provider: "local", State: adminapi.KEKVersionStateActive, ErasuresPending: 2}}}
		out, _ = mustRun(t, r, "kek-version", "list")
		if got := r.f.only(t, "listKEKVersions"); !reflect.DeepEqual(got, adminapi.ListKEKVersionsInput{}) {
			t.Errorf("kek-version list: %+v", got)
		}
		if !strings.Contains(out, "local-0123456789abcdef") || !strings.Contains(out, "active") {
			t.Errorf("kek-version list printed %q", out)
		}
	})

	t.Run("tenant deletion verify prints the line of section 5.5", func(t *testing.T) {
		v := newVerifyRig(t)
		code, out, errs := v.run(t, "tenant", "deletion", "verify", tid)
		want := fmt.Sprintf("deletion of %s verified: record at seq 2, 3 audit rows kept, head %s", tid, v.keptHead.Hash)
		if code != 0 || strings.TrimSpace(out) != want {
			t.Fatalf("exit %d\nstdout: %q\nstderr: %s\nwant: %s", code, out, errs, want)
		}
		for _, op := range []string{"getTenantDeletion", "listPlatformAuditEvents", "getPlatformAuditHead", "exportPlatformAuditEvents", "getDeletedTenantAuditHead", "exportDeletedTenantAuditEvents"} {
			if len(v.f.calls(op)) == 0 {
				t.Errorf("verify did not call %s", op)
			}
		}
		if got := v.f.only(t, "listPlatformAuditEvents").(adminapi.ListPlatformAuditEventsInput); got.Action != "tenant.delete_complete" || got.ResourceID != tid {
			t.Errorf("the platform row was asked for with %+v, want the action tenant.delete_complete and the tenant as resource", got)
		}
	})

	t.Run("tenant deletion verify names each failure", func(t *testing.T) {
		tamper := func(lines []string) []string {
			out := append([]string(nil), lines...)
			out[0] = strings.Replace(out[0], `"outcome":"success"`, `"outcome":"failure"`, 1)
			return out
		}
		for name, c := range map[string]struct {
			change func(v *verifyRig)
			want   string
		}{
			"the record is not in the platform log": {func(v *verifyRig) { v.noRecord = true }, "record_missing"},
			"the record is at another place":        {func(v *verifyRig) { v.deletion.CompletionSeq = 3 }, "record_mismatch"},
			"the record holds other counts":         {func(v *verifyRig) { v.deletion.Passes = 2 }, "record_mismatch"},
			"the platform chain is broken":          {func(v *verifyRig) { v.platformLines = tamper(v.platformLines) }, "platform_chain_break"},
			"the kept log is broken":                {func(v *verifyRig) { v.keptLines = tamper(v.keptLines) }, "tenant_chain_break"},
			"the head is not the resealed head":     {func(v *verifyRig) { v.deletion.Audit.ResealedHead = strings.Repeat("ee", 32) }, "head_mismatch"},
		} {
			v := newVerifyRig(t)
			c.change(v)
			v.apply(t)
			code, out, errs := v.run(t, "tenant", "deletion", "verify", tid)
			if code != 1 || !strings.Contains(out+errs, c.want) || strings.Contains(out, "verified:") {
				t.Errorf("%s: exit %d\nstdout: %s\nstderr: %s\nwant exit 1 and %s", name, code, out, errs, c.want)
			}
		}
	})

	t.Run("a deletion that is not completed does not verify", func(t *testing.T) {
		v := newVerifyRig(t)
		v.deletion.State, v.deletion.Step = adminapi.TenantDeletionStateRunning, adminapi.TenantDeletionStepUsage
		v.apply(t)
		code, out, errs := v.run(t, "tenant", "deletion", "verify", tid)
		if code != 1 || strings.Contains(out, "verified:") || !strings.Contains(out+errs, "completed") {
			t.Errorf("a running deletion: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
		}
	})

	t.Run("tenant-export schema needs no server and prints the embedded schema", func(t *testing.T) {
		code, out, errs := runWith(t, nil, "tenant-export", "schema")
		if code != 0 || errs != "" || out != string(tenantexport.Schema) {
			t.Errorf("exit %d, stderr %q, the output is the embedded schema: %v", code, errs, out == string(tenantexport.Schema))
		}
	})

	t.Run("tenant-export sends nothing for a wrong call", func(t *testing.T) {
		for _, args := range [][]string{{"tenant-export"}, {"tenant-export", "--tenant", tid}, {"tenant-export", "--out", "x"}, {"tenant-export", "--tenant", tid, "--out", "x", "--usage-from", "yesterday"}} {
			r := newRig(t)
			if code, _, errs := r.run(t, args...); code != 2 || errs == "" {
				t.Errorf("%v: exit %d (%s), want a usage error", args, code, errs)
			}
			if len(r.f.got) != 0 {
				t.Errorf("%v: the API was called: %v", args, r.f.got)
			}
		}
		// A directory that exists is not written into.
		dir := t.TempDir()
		r := newRig(t)
		if code, _, errs := r.run(t, "tenant-export", "--tenant", tid, "--out", dir); code == 0 || errs == "" {
			t.Errorf("an export into a directory that exists: exit %d (%s)", code, errs)
		}
	})
}
