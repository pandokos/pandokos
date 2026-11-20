package repo

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAmendedRecords is T-R47: ADR 0049, 0050 and 0052 are accepted, and each has an "Amendment
// (sprint 5.3)" section and a history line that names it; the records that only get a history line
// name the sprint.
func TestAmendedRecords(t *testing.T) {
	for _, adr := range []string{"0049-limit-rules", "0050-quotas-and-budgets-periods-attribution-currencies", "0052-administration-of-limits"} {
		t.Run(adr, func(t *testing.T) {
			doc := read(t, "docs/adr/"+adr+".md")
			if !regexp.MustCompile(`(?m)^- Status: accepted\s*$`).MatchString(doc) {
				t.Errorf("%s is not accepted", adr)
			}
			amend := regexp.MustCompile(`(?m)^## Amendment \(sprint 5\.3\)\s*$`)
			loc := amend.FindStringIndex(doc)
			if loc == nil {
				t.Fatalf("%s has no section \"Amendment (sprint 5.3)\"", adr)
			}
			history := regexp.MustCompile(`(?m)^## History\s*$`).FindStringIndex(doc)
			alternatives := regexp.MustCompile(`(?m)^## Alternatives considered\s*$`).FindStringIndex(doc)
			if history == nil || alternatives == nil || alternatives[0] >= loc[0] || loc[0] >= history[0] {
				t.Errorf("%s: the amendment is not between \"Alternatives considered\" and \"History\"", adr)
			}
			hist := doc[history[0]:]
			if !regexp.MustCompile(`(?m)^- .*amended by sprint 5\.3`).MatchString(hist) {
				t.Errorf("%s: no history line says it was amended by sprint 5.3", adr)
			}
		})
	}
	t.Run("0049 amendment content", func(t *testing.T) {
		doc := read(t, "docs/adr/0049-limit-rules.md")
		s := amendmentOf(t, doc)
		mentionsAll(t, "the amendment of ADR 0049", s, "service_account_id", "virtual_key_id", "subject_mapping_id", "provider_id", "model_id", "ON DELETE RESTRICT",
			"limit.delete", "limit_state", "limit.unlimited_traffic", "invalid_state", "0011")
	})
	t.Run("0050 amendment content", func(t *testing.T) {
		doc := read(t, "docs/adr/0050-quotas-and-budgets-periods-attribution-currencies.md")
		s := amendmentOf(t, doc)
		mentionsAll(t, "the amendment of ADR 0050", s, "tenant.time_zone", "limits.time_zone", "next month", "whole civil period", "currency", "5.4")
	})
	t.Run("0052 amendment content", func(t *testing.T) {
		doc := read(t, "docs/adr/0052-administration-of-limits.md")
		s := amendmentOf(t, doc)
		mentionsAll(t, "the amendment of ADR 0052", s, "cause", "cause_id", "tenant.time_zone_update", "limit.unlimited_traffic", "getTenantTimeZone", "setTenantTimeZone")
	})
	t.Run("history lines of the other records", func(t *testing.T) {
		for _, adr := range []string{"0013-ledger-and-audit", "0032-audit-log-first-version", "0026-schema-migrations", "0027-database-roles-and-session-context",
			"0039-usage-record-admission-and-ledger-writer", "0043-tenant-capability-check-and-usage-list", "0045-trusted-token-issuers", "0051-period-counters-and-the-ledger"} {
			doc := read(t, "docs/adr/"+adr+".md")
			history := regexp.MustCompile(`(?m)^## History\s*$`).FindStringIndex(doc)
			if history == nil || !regexp.MustCompile(`(?m)^- .*sprint 5\.3`).MatchString(doc[history[0]:]) {
				t.Errorf("%s has no history line that names sprint 5.3", adr)
			}
		}
	})
}

// amendmentOf returns the text of the amendment of sprint 5.3 of a record, or fails the test.
func amendmentOf(t *testing.T, doc string) string {
	t.Helper()
	loc := regexp.MustCompile(`(?m)^## Amendment \(sprint 5\.3\)`).FindStringIndex(doc)
	if loc == nil {
		t.Fatal(`no section "Amendment (sprint 5.3)"`)
	}
	return doc[loc[0]:]
}

// TestPriceAndUsageRecords is T-R50: ADR 0053 and 0054 exist and are in the index; ADR 0049, 0050 and
// 0052 each have an `Amendment (sprint 5.4)` section and a history line that names it.
func TestPriceAndUsageRecords(t *testing.T) {
	index := read(t, "docs/adr/README.md")
	for _, adr := range []string{"0053-tenant-price-lists-and-the-active-currency", "0054-usage-data-is-the-tenants"} {
		matches, _ := filepath.Glob(filepath.Join(root(t), "docs/adr", adr[:4]+"-*.md"))
		if len(matches) != 1 {
			t.Errorf("ADR %s: %d files", adr[:4], len(matches))
			continue
		}
		if !strings.Contains(index, filepath.Base(matches[0])) {
			t.Errorf("ADR %s is not in the index", adr[:4])
		}
	}
	for _, adr := range []string{"0049-limit-rules", "0050-quotas-and-budgets-periods-attribution-currencies", "0052-administration-of-limits"} {
		doc := read(t, "docs/adr/"+adr+".md")
		if !regexp.MustCompile(`(?m)^## Amendment \(sprint 5\.4\)`).MatchString(doc) {
			t.Errorf("%s has no section \"Amendment (sprint 5.4)\"", adr)
		}
		history := regexp.MustCompile(`(?m)^## History\s*$`).FindStringIndex(doc)
		if history == nil || !regexp.MustCompile(`(?m)^- .*amended by sprint 5\.4`).MatchString(doc[history[0]:]) {
			t.Errorf("%s has no history line that names sprint 5.4", adr)
		}
	}
}

// TestOperationalTelemetryRecord: ADR 0055 exists and is in the index, and the records it refines
// (0015, 0039, 0040, 0054) each have a history line that names it.
func TestOperationalTelemetryRecord(t *testing.T) {
	const adr = "0055-operational-telemetry-holds-no-tenant-data.md"
	doc := read(t, "docs/adr/"+adr)
	if !strings.Contains(read(t, "docs/adr/README.md"), adr) {
		t.Error("ADR 0055 is not in the index")
	}
	mentionsAll(t, "ADR 0055", doc, "Required", "url.path", "gen_ai.operation.name", "gen_ai.provider.name", "request_id", "#52", "OPS-12", "tenant_usage_details")
	for _, refined := range []string{"0015-observability", "0039-usage-record-admission-and-ledger-writer", "0040-traces", "0054-usage-data-is-the-tenants"} {
		text := read(t, "docs/adr/"+refined+".md")
		history := regexp.MustCompile(`(?m)^## History\s*$`).FindStringIndex(text)
		if history == nil || !strings.Contains(text[history[0]:], adr) {
			t.Errorf("%s has no history line that names ADR 0055", refined)
		}
	}
}
