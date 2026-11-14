package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func (f *fakeAPI) GetCapability(_ context.Context, in adminapi.GetCapabilityInput) (*adminapi.Capability, error) {
	return pick[adminapi.Capability](f, "getCapability"), f.rec("getCapability", in)
}

func (f *fakeAPI) ListUsageRecords(_ context.Context, in adminapi.ListUsageRecordsInput) (*adminapi.UsageRecordList, error) {
	f.mu.Lock()
	pages, ok := f.usagePages[in.Cursor]
	f.mu.Unlock()
	if ok {
		return &pages, f.rec("listUsageRecords", in)
	}
	return pick[adminapi.UsageRecordList](f, "listUsageRecords"), f.rec("listUsageRecords", in)
}

var capability = adminapi.Capability{
	Provider: "anthropic", Model: "claude-sonnet-4-5", Callable: false, Reasons: []string{"credential_disabled"},
	Checks: adminapi.CapabilityChecks{ProviderAvailable: true, ModelAllowed: true, Credential: adminapi.CredentialAvailabilityDisabled,
		CredentialID: cid, CredentialValidated: true, Budget: adminapi.BudgetAvailabilityNotEnforced},
	CheckedAt: t0,
}

// TestCapabilityAndUsageCommands is T-549: the two commands call their operations with the parsed
// inputs, --all follows the cursors, and the output names no secret.
func TestCapabilityAndUsageCommands(t *testing.T) {
	t.Run("capability check", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getCapability"] = &capability
		out, _ := mustRun(t, r, "capability", "check", "--tenant", tid, "--provider", "anthropic", "--model", "claude-sonnet-4-5")
		want := adminapi.GetCapabilityInput{Tenant: tid, Provider: "anthropic", Model: "claude-sonnet-4-5"}
		if got := r.f.only(t, "getCapability"); !reflect.DeepEqual(got, want) {
			t.Errorf("the API received %+v, want %+v", got, want)
		}
		for _, w := range []string{"anthropic", "claude-sonnet-4-5", "credential_disabled", "disabled", cid} {
			if !strings.Contains(out, w) {
				t.Errorf("%q is missing from the output:\n%s", w, out)
			}
		}
		testutil.AssertNoCanary(t, "the output", out)
	})
	t.Run("capability check, as JSON; the exit status is 0 whether or not callable", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getCapability"] = &capability
		code, out, errs := r.run(t, "capability", "check", "-o", "json", "--tenant", tid, "--provider", "anthropic", "--model", "claude-sonnet-4-5")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		var got adminapi.Capability
		if err := json.Unmarshal([]byte(out), &got); err != nil || got.Callable || got.Checks.CredentialID != cid {
			t.Errorf("%v: %s", err, out)
		}
		ok := capability
		ok.Callable, ok.Reasons = true, []string{}
		r2 := newRig(t)
		r2.f.out["getCapability"] = &ok
		if code, _, errs := r2.run(t, "capability", "check", "--tenant", tid, "--provider", "anthropic", "--model", "claude-sonnet-4-5"); code != 0 {
			t.Errorf("a callable answer: exit %d: %s", code, errs)
		}
	})
	t.Run("the tenant of the key is the default", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getMe"] = &adminapi.Me{Tenant: &adminapi.TenantRef{ID: tid, Name: "acme"}}
		mustRun(t, r, "capability", "check", "--provider", "openai", "--model", "gpt-4o-mini")
		if got := r.f.only(t, "getCapability").(adminapi.GetCapabilityInput); got.Tenant != tid {
			t.Errorf("tenant %q", got.Tenant)
		}
	})
	t.Run("provider and model are required", func(t *testing.T) {
		for _, args := range [][]string{
			{"capability", "check", "--tenant", tid, "--model", "m"},
			{"capability", "check", "--tenant", tid, "--provider", "p"},
			{"capability", "check", "--tenant", tid},
			{"capability", "check", "--tenant", tid, "--provider", "p", "--model", "m", "extra"},
		} {
			r := newRig(t)
			code, _, errs := r.run(t, args...)
			if code != 2 || errs == "" {
				t.Errorf("%v: exit %d, stderr %q (a usage error is expected)", args, code, errs)
			}
			if n := len(r.f.calls("getCapability")); n != 0 {
				t.Errorf("%v: the API was called %d times", args, n)
			}
		}
	})
	t.Run("an API refusal is reported without secrets", func(t *testing.T) {
		r := newRig(t)
		r.f.fail["getCapability"] = &problemErr{adminapi.Problem{Type: "urn:pandokos:problem:forbidden", Status: 403, Code: adminapi.ProblemCodeForbidden, Title: "Forbidden", Detail: "The credential does not allow this operation.", RequestID: "req403"}}
		code, _, errs := r.run(t, "capability", "check", "--tenant", tid, "--provider", "p", "--model", "m")
		if code != 1 || !strings.Contains(errs, "error: forbidden: The credential does not allow this operation. (request req403)") {
			t.Errorf("exit %d: %s", code, errs)
		}
		testutil.AssertAbsent(t, "stderr", errs, r.key)
	})

	t.Run("usage list", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listUsageRecords"] = &adminapi.UsageRecordList{Items: []adminapi.UsageRecord{usageRecord(usageID(1))}, NextCursor: "c1"}
		out, errs := mustRun(t, r, "usage", "list", "--tenant", tid, "--from", "2026-11-19T00:00:00Z", "--to", "2026-11-20T00:00:00Z", "--provider", "anthropic", "--limit", "7")
		want := adminapi.ListUsageRecordsInput{Tenant: tid, From: "2026-11-19T00:00:00Z", To: "2026-11-20T00:00:00Z", Provider: "anthropic", Limit: 7}
		if got := r.f.only(t, "listUsageRecords"); !reflect.DeepEqual(got, want) {
			t.Errorf("the API received %+v, want %+v", got, want)
		}
		if !strings.Contains(out, "anthropic") || !strings.Contains(out, "messages") {
			t.Errorf("the output:\n%s", out)
		}
		if !strings.Contains(errs, "more: --cursor c1") {
			t.Errorf("no hint for the next page: %q", errs)
		}
		testutil.AssertNoCanary(t, "the output", out+errs)
	})
	t.Run("usage list without flags sends none", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "usage", "list", "--tenant", tid)
		got := r.f.only(t, "listUsageRecords").(adminapi.ListUsageRecordsInput)
		if got.Tenant != tid || got.From != "" || got.To != "" || got.Provider != "" || got.Cursor != "" || (got.Limit != 0 && got.Limit != 50) {
			t.Errorf("the API received %+v", got)
		}
	})
	t.Run("--all follows the cursors", func(t *testing.T) {
		r := newRig(t)
		r.f.usagePages = map[string]adminapi.UsageRecordList{
			"":   {Items: []adminapi.UsageRecord{usageRecord(usageID(11))}, NextCursor: "c1"},
			"c1": {Items: []adminapi.UsageRecord{usageRecord(usageID(12))}, NextCursor: "c2"},
			"c2": {Items: []adminapi.UsageRecord{usageRecord(usageID(13))}},
		}
		out, errs := mustRun(t, r, "usage", "list", "--tenant", tid, "--provider", "anthropic", "--all")
		calls := r.f.calls("listUsageRecords")
		if len(calls) != 3 {
			t.Fatalf("%d pages were requested, want 3", len(calls))
		}
		for i, cursor := range []string{"", "c1", "c2"} {
			in := calls[i].(adminapi.ListUsageRecordsInput)
			if in.Cursor != cursor || in.Provider != "anthropic" || in.Tenant != tid {
				t.Errorf("page %d: %+v", i, in)
			}
		}
		for _, id := range []string{usageID(11), usageID(12), usageID(13)} {
			if !strings.Contains(out, id) {
				t.Errorf("%s is missing:\n%s", id, out)
			}
		}
		if strings.Contains(errs, "more:") {
			t.Errorf("--all printed a hint: %q", errs)
		}
		code, jsonOut, _ := r.run(t, "usage", "list", "-o", "json", "--tenant", tid, "--all")
		var doc struct {
			Items []adminapi.UsageRecord `json:"items"`
		}
		if code != 0 || json.Unmarshal([]byte(jsonOut), &doc) != nil || len(doc.Items) != 3 {
			t.Errorf("JSON output: exit %d, %q", code, jsonOut)
		}
	})
	t.Run("--cursor continues a list", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "usage", "list", "--tenant", tid, "--cursor", "c9")
		if got := r.f.only(t, "listUsageRecords").(adminapi.ListUsageRecordsInput); got.Cursor != "c9" {
			t.Errorf("cursor %q", got.Cursor)
		}
	})
	t.Run("a tenant given by name is looked up", func(t *testing.T) {
		r := newRig(t)
		r.f.pages = map[string]adminapi.TenantList{"": {Items: []adminapi.Tenant{tenant}}}
		mustRun(t, r, "usage", "list", "--tenant", "acme")
		if got := r.f.only(t, "listUsageRecords").(adminapi.ListUsageRecordsInput); got.Tenant != tid {
			t.Errorf("tenant %q", got.Tenant)
		}
	})
	t.Run("the output names no secret", func(t *testing.T) {
		r := newRig(t)
		rec := usageRecord(usageID(21))
		rec.Metadata = []adminapi.MetadataEntry{{Key: "team", Value: "alpha"}}
		r.f.out["listUsageRecords"] = &adminapi.UsageRecordList{Items: []adminapi.UsageRecord{rec}}
		out, errs := mustRun(t, r, "usage", "list", "--tenant", tid)
		testutil.AssertAbsent(t, "the output", out+errs, r.key, fixtures.CanaryProviderKey, fixtures.CanaryCredential)
	})
}

func usageID(n int) string { return fmt.Sprintf("0192f0f0-0000-7000-8000-0000000000%02d", n) }

func usageRecord(id string) adminapi.UsageRecord {
	return adminapi.UsageRecord{
		ID: id, RequestID: "0123456789abcdef0123456789abcdef", StartedAt: t0, Provider: "anthropic", ApiFamily: "anthropic", Operation: "messages",
		Model: "claude-sonnet-4-5", Outcome: "ok", InputSource: "provider", OutputSource: "provider", InputTokens: 12, OutputTokens: 7,
		CostStatus: "known", Cost: adminapi.Value("0.000078"), Currency: "USD",
		OtherUnits: []adminapi.UsageCounter{}, ProviderUsage: []adminapi.UsageCounter{}, CostFallbacks: []string{}, Metadata: []adminapi.MetadataEntry{},
	}
}
