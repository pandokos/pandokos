package repo

import (
	"regexp"
	"strings"
	"testing"
)

// section returns the text of the section of a Markdown document whose heading line matches re, up
// to the next heading of the same or a higher level; "" when there is none.
func section(doc string, re *regexp.Regexp) string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		m := regexp.MustCompile(`^(#+) `).FindStringSubmatch(l)
		if m == nil || !re.MatchString(l) {
			continue
		}
		level := len(m[1])
		var out []string
		for _, next := range lines[i+1:] {
			if h := regexp.MustCompile(`^(#+) `).FindStringSubmatch(next); h != nil && len(h[1]) <= level {
				break
			}
			out = append(out, next)
		}
		return strings.Join(out, "\n")
	}
	return ""
}

func mentionsAll(t *testing.T, where, text string, words ...string) {
	t.Helper()
	low := strings.ToLower(text)
	for _, w := range words {
		if !strings.Contains(low, strings.ToLower(w)) {
			t.Errorf("%s does not mention %q", where, w)
		}
	}
}

// TestLimitsGuideIntegrity is T-R45: the operations guide of limits has the section on the two
// quotas with the two recipes, the time zone per tenant and its change rule, the currency check,
// price overrides in another currency, the four states and the unlimited-traffic row; the guide of
// tokens names limit rules for a subject mapping in its first section.
func TestLimitsGuideIntegrity(t *testing.T) {
	guide := read(t, "docs/operations/limits.md")

	t.Run("two quotas", func(t *testing.T) {
		s := section(guide, regexp.MustCompile(`(?i)^#+ .*two quotas`))
		if s == "" {
			t.Fatal(`docs/operations/limits.md has no section "Two quotas"`)
		}
		mentionsAll(t, `the section "Two quotas"`, s,
			"provider", "429", "credential", "tier", // the quota the provider grants, which Pandokos meets as the provider's own 429
			"tier", "scoped", // the recipe that protects a provider tier: a rule of the tenant scoped to the provider
			"batch", "interactive", "starv", "pinned", // the recipe that keeps a batch key from starving an interactive one
			"default credential")
		// "Two quotas" comes after "Levels and subjects".
		if levels, two := strings.Index(guide, "Levels and subjects"), strings.Index(strings.ToLower(guide), "two quotas"); levels < 0 || two < levels {
			t.Errorf(`"Two quotas" (%d) is not after "Levels and subjects" (%d)`, two, levels)
		}
	})

	t.Run("periods and the time zone of a tenant", func(t *testing.T) {
		s := section(guide, regexp.MustCompile(`(?i)^#+ .*periods and the time zone`))
		if s == "" {
			t.Fatal(`docs/operations/limits.md has no section "Periods and the time zone"`)
		}
		mentionsAll(t, `the section "Periods and the time zone"`, s,
			"limits.time_zone", "limit time-zone", "next month", "whole", "effective_from",
			"Asia/Tokyo", "15:00", "nine", // the worked example: the Tokyo day that began at 15:00 UTC counts its nine earlier hours
			"Retry-After", "limit:write", "tenant:write")
	})

	t.Run("the currency check and price overrides in another currency", func(t *testing.T) {
		s := section(guide, regexp.MustCompile(`(?i)^#+ .*budgets`))
		if s == "" {
			t.Fatal(`docs/operations/limits.md has no section "Budgets"`)
		}
		mentionsAll(t, `the section "Budgets"`, s,
			"currency", "400", "no model of this scope has a price", "set by the platform", "every tenant",
			"override", "another currency", "imports", "5.4")
	})

	t.Run("how a request was treated, and the row of unlimited traffic", func(t *testing.T) {
		mentionsAll(t, "docs/operations/limits.md", guide,
			"limit_state", "unlimited", "limited", "unverified", "not_evaluated",
			"limit.unlimited_traffic", "limit.delete", "cause", "key_revoked", "token_issuer_deleted")
	})

	t.Run("what is not there yet no longer lists the zone per tenant", func(t *testing.T) {
		s := section(guide, regexp.MustCompile(`(?i)^#+ .*what is not there yet`))
		if s == "" {
			t.Fatal(`docs/operations/limits.md has no section "What is not there yet"`)
		}
		if strings.Contains(strings.ToLower(s), "zone per tenant") || strings.Contains(strings.ToLower(s), "time zone per tenant") {
			t.Errorf("the zone per tenant is still listed as not there:\n%s", s)
		}
		mentionsAll(t, `the section "What is not there yet"`, s, "price list", "5.4")
	})

	t.Run("tokens.md names limit rules for a subject mapping in its first section", func(t *testing.T) {
		tokens := read(t, "docs/operations/tokens.md")
		s := section(tokens, regexp.MustCompile(`(?i)^## 1\. `))
		if s == "" {
			t.Fatal("docs/operations/tokens.md has no first section")
		}
		mentionsAll(t, "section 1 of docs/operations/tokens.md", s, "limit rule", "subject mapping", "service account")
		mentionsAll(t, "docs/operations/tokens.md", tokens, "deleting a mapping", "limit rules")
	})

	t.Run("keys.md, catalogue.md", func(t *testing.T) {
		mentionsAll(t, "docs/operations/keys.md", read(t, "docs/operations/keys.md"), "revok", "limit rules", "deleted")
		mentionsAll(t, "docs/operations/catalogue.md", read(t, "docs/operations/catalogue.md"), "limit rules reference", "retire the model", "disable the provider")
	})
}

// TestIntegrityReference is T-R46: the reference of the administration API lists the two
// operations, the two audit actions and the four causes; the database guide has the section of
// migration 0011 with the upgrade note; the usage guide names limit_state.
func TestIntegrityReference(t *testing.T) {
	api := read(t, "docs/reference/admin-api.md")
	for _, op := range []string{"getTenantTimeZone", "setTenantTimeZone"} {
		if !strings.Contains(api, "`"+op+"`") {
			t.Errorf("docs/reference/admin-api.md does not list the operation %s", op)
		}
	}
	for _, action := range []string{"tenant.time_zone_update", "limit.unlimited_traffic"} {
		if !strings.Contains(api, "`"+action+"`") && !strings.Contains(api, action) {
			t.Errorf("docs/reference/admin-api.md does not list the audit action %s", action)
		}
	}
	for _, cause := range []string{"key_revoked", "subject_mapping_deleted", "token_issuer_deleted", "platform_issuer_deleted"} {
		if !strings.Contains(api, cause) {
			t.Errorf("docs/reference/admin-api.md does not name the cause %s", cause)
		}
	}
	mentionsAll(t, "docs/reference/admin-api.md", api, "limit_rules_deleted")

	db := read(t, "docs/operations/database.md")
	s := section(db, regexp.MustCompile(`^## 16\. `))
	if s == "" {
		t.Fatal("docs/operations/database.md has no section 16")
	}
	if !strings.Contains(s, "0011") {
		t.Error("section 16 of docs/operations/database.md does not name migration 0011")
	}
	mentionsAll(t, "section 16 of docs/operations/database.md", s,
		"stop, migrate, start", "delete_platform_issuer_limit_rules", "record_unlimited_traffic",
		"limit.delete", "ACCESS EXCLUSIVE", "row-level security", "tenant_self_update")
	if strings.Index(db, "## 15. ") > strings.Index(db, "## 16. ") {
		t.Error("section 16 of docs/operations/database.md does not follow section 15")
	}

	mentionsAll(t, "docs/operations/usage.md", read(t, "docs/operations/usage.md"), "limit_state", "not_evaluated", "unverified")
}

// TestTenantPriceGuide is T-R48: catalogue.md has the section on tenant price lists and the active
// currency (lists per currency, precedence, end, the change rule, what imports and overrides do not
// touch); limits.md section 5 says which budgets apply, how a change is prepared and what a ceiling
// measures; the telemetry and gateway guides name the key and no longer list the removed fields as
// always present.
func TestTenantPriceGuide(t *testing.T) {
	cat := read(t, "docs/operations/catalogue.md")
	s := section(cat, regexp.MustCompile(`(?i)^#+ .*tenant price lists and the active currency`))
	if s == "" {
		t.Fatal(`docs/operations/catalogue.md has no section "Tenant price lists and the active currency"`)
	}
	mentionsAll(t, "the catalogue section on tenant price lists", s, "list", "currency", "end", "next month", "time zone", "import", "override", "never convert", "price_list:write", "tenant_price", "active_currency")

	lim := read(t, "docs/operations/limits.md")
	b := section(lim, regexp.MustCompile(`(?i)^#+ 5\. Budgets`))
	if b == "" {
		t.Fatal(`docs/operations/limits.md has no section 5 "Budgets"`)
	}
	mentionsAll(t, "limits.md section 5", b, "active currency", "inactive", "active", "prepare", "ceiling", "tokens", "tenant's price", "no_price", "listTenantModels", "pricing.default_currency")

	for _, doc := range []string{"docs/operations/telemetry.md", "docs/operations/gateway.md"} {
		text := read(t, doc)
		mentionsAll(t, doc, text, "telemetry.tenant_usage_details")
		if regexp.MustCompile(`(?m)^\|\s*` + "`" + `(cost|reserved_tokens|limit_currency|price_currency)` + "`" + `\s*\|[^|]*\|\s*always`).MatchString(text) {
			t.Errorf("%s lists a removed field as always present", doc)
		}
	}
}

// TestTenantPriceReference is T-R49: admin-api.md lists the seven operations and the three audit
// actions; permissions.md the three permissions; configuration.md the two keys; database.md the section
// of migration 0012 with the upgrade notes; usage.md names price_source; the requirements have CAT-11 and
// the new wording of USE-06, USE-07 and AZ-06 (no "platform usage viewer"); the security model names
// issue #33; the epic file of epic 6 carries the two notes.
func TestTenantPriceReference(t *testing.T) {
	api := read(t, "docs/reference/admin-api.md")
	mentionsAll(t, "admin-api.md", api, "listPriceLists", "listPriceListVersions", "createPriceListVersion", "getPriceListVersion", "deletePriceListVersion",
		"getActiveCurrency", "setActiveCurrency", "tenant_price.create", "tenant_price.delete", "tenant.active_currency_update")
	mentionsAll(t, "permissions.md", read(t, "docs/reference/permissions.md"), "price_list:read", "price_list:write", "price_list:delete")
	mentionsAll(t, "configuration.md", read(t, "docs/operations/configuration.md"), "pricing.default_currency", "telemetry.tenant_usage_details")
	db := read(t, "docs/operations/database.md")
	mentionsAll(t, "database.md", db, "0012", "active currency", "no_price", "pricing.default_currency", "request log", "spans")
	mentionsAll(t, "usage.md", read(t, "docs/operations/usage.md"), "price_source")

	req := read(t, "docs/requirements/README.md")
	mentionsAll(t, "the requirements", req, "CAT-11")
	if strings.Contains(strings.ToLower(req), "platform usage viewer") {
		t.Error(`the requirements still name a "platform usage viewer"`)
	}
	for _, id := range []string{"USE-06", "USE-07", "AZ-06"} {
		line := ""
		for _, l := range strings.Split(req, "\n") {
			if strings.HasPrefix(l, "| "+id+" ") {
				line = l
			}
		}
		if line == "" {
			t.Errorf("the requirements have no row %s", id)
		}
	}
	mentionsAll(t, "the security model", read(t, "docs/architecture/security-model.md"), "#33")
	epic6 := read(t, "planning/epics/06-observability-reports-lifecycle/epic.md")
	mentionsAll(t, "the epic file of epic 6", epic6, "tenant_usage_details", "per installation", "never to cost or tokens")
}
