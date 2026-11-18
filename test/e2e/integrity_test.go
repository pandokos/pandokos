package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

// listedRules returns the subject IDs and the IDs of the rules `limit list` shows for a tenant.
func (p *proc) listedRules(t *testing.T, adminFile, tenantID string) (ids, subjects []string) {
	t.Helper()
	out := p.mustCLI(t, adminFile, "limit", "list", "--tenant", tenantID, "-o", "json", "--all")
	var doc struct {
		Items []struct {
			ID        string `json:"id"`
			SubjectID string `json:"subject_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("limit list: %v\n%s", err, out)
	}
	for _, i := range doc.Items {
		ids, subjects = append(ids, i.ID), append(subjects, i.SubjectID)
	}
	return ids, subjects
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (p *proc) serviceAccountID(t *testing.T, adminFile, tenantID string) string {
	t.Helper()
	out := p.mustCLI(t, adminFile, "service-account", "list", "--tenant", tenantID, "-o", "json")
	var doc struct {
		Items []struct {
			ID    string   `json:"id"`
			Roles []string `json:"roles"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	for _, i := range doc.Items {
		for _, r := range i.Roles {
			if r == "inference" {
				return i.ID
			}
		}
	}
	t.Fatalf("no inference service account in %s", out)
	return ""
}

// TestRuleIntegrity is T-724: the binary and the CLI. A key with a rule is revoked and its rule is
// gone; a mapping with a rule is deleted; a model with a rule cannot be deleted, and can once the
// rule is; a tenant's time zone is set and read; the usage records show how the limits treated each
// request; the audit log holds the rules deleted and one row of unlimited traffic.
func TestRuleIntegrity(t *testing.T) {
	p, _, boot := limitsProc(t, "info")
	tn := p.newLimitsTenant(t, boot, "integrity")
	keyID, _ := p.newInferenceKey(t, tn.admin, tn.idStr)
	sa := p.serviceAccountID(t, tn.admin, tn.idStr)

	t.Run("a key with a rule is revoked and limit list no longer shows it", func(t *testing.T) {
		out := p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "requests_per_day", "--limit", "5", "--subject", "key", "--key", keyID)
		rule := limitRuleID(t, out)
		if ids, subjects := p.listedRules(t, tn.admin, tn.idStr); !contains(ids, rule) || !contains(subjects, keyID) {
			t.Fatalf("the rule is not listed: %v %v", ids, subjects)
		}
		p.mustCLI(t, tn.admin, "key", "revoke", "--tenant", tn.idStr, "--reason", "leaked", keyID)
		if ids, _ := p.listedRules(t, tn.admin, tn.idStr); contains(ids, rule) {
			t.Errorf("the rule of a revoked key is still listed: %v", ids)
		}
		got := auditActions(t, p.db, `SELECT action || '/' || COALESCE(detail ->> 'cause', '') FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND resource_id = $2`, tn.idStr, rule)
		if !contains(got, "limit.delete/key_revoked") {
			t.Errorf("audit rows %v", got)
		}
		// Creating a rule for the revoked key is refused.
		if code, _, errs := p.cli(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "requests_per_day", "--limit", "5", "--subject", "key", "--key", keyID); code == 0 || !strings.Contains(errs, "invalid_request") {
			t.Errorf("a rule for a revoked key: exit %d %q", code, errs)
		}
	})

	t.Run("a mapping with a rule is deleted", func(t *testing.T) {
		issuer := p.db.SeedTrustedIssuer(t, tn.id, "e2e-issuer")
		mapping := p.db.SeedSubjectMapping(t, tn.id, issuer, false, sa, "e2e-subject")
		out := p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "requests_per_day", "--limit", "5", "--subject", "key", "--key", mapping)
		rule := limitRuleID(t, out)
		p.mustCLI(t, tn.admin, "subject-mapping", "delete", "--tenant", tn.idStr, mapping)
		if ids, _ := p.listedRules(t, tn.admin, tn.idStr); contains(ids, rule) {
			t.Errorf("the rule of a deleted mapping is still listed")
		}
		got := auditActions(t, p.db, `SELECT action || '/' || COALESCE(detail ->> 'cause', '') FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND resource_id = $2`, tn.idStr, rule)
		if !contains(got, "limit.delete/subject_mapping_deleted") {
			t.Errorf("audit rows %v", got)
		}
	})

	t.Run("a model with a rule cannot be deleted, and can once the rule is", func(t *testing.T) {
		p.mustCLI(t, boot, "model", "create", "openai/e2e-doomed", "--availability", "all")
		out := p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "tokens_per_day", "--limit", "1000", "--provider", "openai", "--model", "e2e-doomed")
		rule := limitRuleID(t, out)
		code, _, errs := p.cli(t, boot, "model", "delete", "openai/e2e-doomed")
		if code == 0 || !strings.Contains(errs, "invalid_state") || strings.Contains(errs, tn.idStr) {
			t.Errorf("deleting a model that a rule names: exit %d, %q", code, errs)
		}
		p.mustCLI(t, tn.admin, "limit", "delete", "--tenant", tn.idStr, rule)
		p.mustCLI(t, boot, "model", "delete", "openai/e2e-doomed")
	})

	t.Run("a tenant's zone is set and read", func(t *testing.T) {
		out := p.mustCLI(t, tn.admin, "limit", "time-zone", "set", "--tenant", tn.idStr, "Europe/Rome")
		if !strings.Contains(out, "time_zone: Europe/Rome") || !strings.Contains(out, "effective_from: ") {
			t.Errorf("set:\n%s", out)
		}
		out = p.mustCLI(t, tn.admin, "limit", "time-zone", "get", "--tenant", tn.idStr)
		if !strings.Contains(out, "time_zone: Europe/Rome") || !strings.Contains(out, "in_force: ") {
			t.Errorf("get:\n%s", out)
		}
		out = p.mustCLI(t, boot, "tenant", "get", tn.idStr)
		if !strings.Contains(out, "time_zone: Europe/Rome") {
			t.Errorf("tenant get:\n%s", out)
		}
		if code, _, errs := p.cli(t, tn.admin, "limit", "time-zone", "set", "--tenant", tn.idStr, "Mars/Olympus"); code == 0 || !strings.Contains(errs, "invalid_request") {
			t.Errorf("an unknown zone: exit %d %q", code, errs)
		}
	})

	t.Run("usage list shows limited and unlimited; the audit log holds one unlimited-traffic row", func(t *testing.T) {
		free := p.newLimitsTenant(t, boot, "integrity-free") // no rule
		limited := p.newLimitsTenant(t, boot, "integrity-limited")
		p.db.SeedLimitRule(t, limited.id, "tenant", "", "requests_per_day", 1000)
		for i := 0; i < 3; i++ {
			if status, text := p.openaiCall(t, free.key); status != 200 {
				t.Fatalf("free: %d\n%s", status, text)
			}
		}
		if status, text := p.openaiCall(t, limited.key); status != 200 {
			t.Fatalf("limited: %d\n%s", status, text)
		}
		state := func(tn limitsTenant, n int) []string {
			var states []string
			testutil.Eventually(t, 15*time.Second, "the usage records", func() bool {
				out := p.mustCLI(t, tn.admin, "usage", "list", "--tenant", tn.idStr, "-o", "json", "--all")
				var doc struct {
					Items []struct {
						LimitState string `json:"limit_state"`
					} `json:"items"`
				}
				if err := json.Unmarshal([]byte(out), &doc); err != nil {
					t.Fatal(err)
				}
				states = nil
				for _, i := range doc.Items {
					states = append(states, i.LimitState)
				}
				return len(states) >= n
			})
			return states
		}
		for _, s := range state(free, 3) {
			if s != "unlimited" {
				t.Errorf("a tenant without a rule: limit_state %q", s)
			}
		}
		for _, s := range state(limited, 1) {
			if s != "limited" {
				t.Errorf("a tenant with a rule: limit_state %q", s)
			}
		}
		out := p.mustCLI(t, free.admin, "usage", "list", "--tenant", free.idStr, "--limit-state", "unlimited")
		if !strings.Contains(out, "unlimited") {
			t.Errorf("usage list --limit-state unlimited:\n%s", out)
		}
		testutil.Eventually(t, 10*time.Second, "the unlimited-traffic row", func() bool {
			return len(auditActions(t, p.db, `SELECT action FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'limit.unlimited_traffic'`, free.idStr)) >= 1
		})
		time.Sleep(300 * time.Millisecond)
		if got := auditActions(t, p.db, `SELECT action FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'limit.unlimited_traffic'`, free.idStr); len(got) != 1 {
			t.Errorf("%d unlimited-traffic rows for three requests of one day, want 1", len(got))
		}
		if got := auditActions(t, p.db, `SELECT action FROM pandokos.platform_audit_event WHERE action = 'limit.unlimited_traffic' AND resource_id = $1`, free.idStr); len(got) != 1 {
			t.Errorf("%d rows in the platform log", len(got))
		}
		if got := auditActions(t, p.db, `SELECT action FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'limit.unlimited_traffic'`, limited.idStr); len(got) != 0 {
			t.Errorf("a tenant with a rule has %d unlimited-traffic rows", len(got))
		}
	})
}
