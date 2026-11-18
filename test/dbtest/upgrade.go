package dbtest

import (
	"fmt"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
)

// RuleAt0010 is one rule written against the schema of migration 0010, and what migration 0011
// must make of it (section 5.10).
type RuleAt0010 struct {
	Case     string
	Tenant   tenant.ID // the tenant of the rule; the zero ID for the platform default
	Platform bool      // a row of platform_limit_rule
	ID       string

	// Kept: the identifier columns the rule must have afterwards, by column name; a column that is
	// not in the map must be NULL.
	Kept map[string]string

	// Deleted: the rule is gone and its audit row has this cause and these fields (besides the
	// cause), by the key the detail uses. Cause is empty for a rule that is kept.
	Cause  string
	Fields map[string]any
}

// Upgrade0010 is what SeedRulesAt0010 wrote.
type Upgrade0010 struct {
	A, B  tenant.ID
	Rules []RuleAt0010
}

// SeedRulesAt0010 writes, as the superuser and in plain SQL, against the schema of migration 0010,
// one rule for each row of the table of section 5.10 in two tenants, and ceilings of the platform.
// It also writes the subjects, the provider and the model the rules name, and returns what each
// rule must become. d must be migrated to 10 and no further.
func (d *Database) SeedRulesAt0010(tb testing.TB) Upgrade0010 {
	tb.Helper()
	a, b := d.SeedTenant(tb, "upgrade-a"), d.SeedTenant(tb, "upgrade-b")
	up := Upgrade0010{A: a, B: b}

	// The catalogue: a model that exists (of a provider that exists) and names that do not.
	prov := d.ProviderID(tb, "openai")
	model := d.SeedModel(tb, "openai", "upgrade-model")

	noMarks := func(m []int16) []int16 {
		if m == nil {
			return []int16{}
		}
		return m
	}
	rule := func(t tenant.ID, kind, subjectID, provider, model, metric string, value int64, currency string, enforce bool, thresholds []int16) string {
		return d.seedScalar(tb, "seed rule at 0010", `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, provider, model, metric,
				limit_value, currency, enforce, thresholds)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::smallint[]) RETURNING id::text`,
			t.String(), kind, subjectID, provider, model, metric, value, currency, enforce, noMarks(thresholds))
	}
	ceiling := func(t *tenant.ID, provider, model, metric string, value int64, currency string, enforce bool, thresholds []int16) string {
		var arg any
		if t != nil {
			arg = t.String()
		}
		return d.seedScalar(tb, "seed ceiling at 0010", `INSERT INTO pandokos.platform_limit_rule (tenant_id, provider, model, metric,
				limit_value, currency, enforce, thresholds)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::smallint[]) RETURNING id::text`,
			arg, provider, model, metric, value, currency, enforce, noMarks(thresholds))
	}
	add := func(r RuleAt0010) { up.Rules = append(up.Rules, r) }

	for i, t := range []tenant.ID{a, b} {
		n := fmt.Sprintf("%d", i)
		sa := d.SeedServiceAccount(tb, t, "up-sa-"+n)
		keyID, keyRow := d.SeedVirtualKey(tb, t, sa)
		revokedID, _ := d.SeedVirtualKey(tb, t, sa, KeyRevoked())
		issuer := d.SeedTrustedIssuer(tb, t, "up-issuer-"+n)
		mapping := d.SeedSubjectMapping(tb, t, issuer, false, sa, "up-subject-"+n)
		other := b
		if i == 1 {
			other = a
		}
		otherSA := d.SeedServiceAccount(tb, other, "up-other-sa-"+n)
		otherKey, _ := d.SeedVirtualKey(tb, other, otherSA)
		ghost := "0192f0f0-0000-7000-8000-00000000000" + n

		// Kept: the subject designates something now.
		id := rule(t, "service_account", sa, "", "", "requests_per_minute", 10, "", true, nil)
		add(RuleAt0010{Case: "a service account of the tenant", Tenant: t, ID: id, Kept: map[string]string{"service_account_id": sa}})
		id = rule(t, "key", keyID, "", "", "tokens_per_minute", 20, "", true, nil)
		add(RuleAt0010{Case: "a key of the tenant, not revoked", Tenant: t, ID: id, Kept: map[string]string{"virtual_key_id": keyRow}})
		id = rule(t, "key", mapping, "", "", "concurrent_requests", 3, "", true, nil)
		add(RuleAt0010{Case: "a subject mapping of the tenant", Tenant: t, ID: id, Kept: map[string]string{"subject_mapping_id": mapping}})
		id = rule(t, "tenant", "", "openai", "upgrade-model", "requests_per_day", 100, "", true, nil)
		add(RuleAt0010{Case: "a provider and a model of the catalogue", Tenant: t, ID: id, Kept: map[string]string{"provider_id": prov, "model_id": model}})
		id = rule(t, "tenant", "", "anthropic", "", "cost_per_month", 5_000_000, "EUR", false, []int16{80, 100})
		add(RuleAt0010{Case: "a provider of the catalogue, a soft budget", Tenant: t, ID: id, Kept: map[string]string{"provider_id": d.ProviderID(tb, "anthropic")}})

		// Deleted: the subject is not there.
		id = rule(t, "service_account", ghost, "", "", "tokens_per_day", 1000, "", true, nil)
		add(RuleAt0010{Case: "a service account that does not exist", Tenant: t, ID: id, Cause: "migration_subject_missing",
			Fields: map[string]any{"subject_kind": "service_account", "subject_id": ghost, "metric": "tokens_per_day", "limit": float64(1000), "enforce": true}})
		id = rule(t, "key", "tnosuchkey0000000", "", "", "requests_per_day", 7, "", true, nil)
		add(RuleAt0010{Case: "a key that does not exist", Tenant: t, ID: id, Cause: "migration_subject_missing",
			Fields: map[string]any{"subject_kind": "key", "subject_id": "tnosuchkey0000000", "metric": "requests_per_day", "limit": float64(7), "enforce": true}})
		id = rule(t, "key", otherKey, "", "", "requests_per_month", 9, "", true, nil)
		add(RuleAt0010{Case: "a key of another tenant", Tenant: t, ID: id, Cause: "migration_subject_missing",
			Fields: map[string]any{"subject_kind": "key", "subject_id": otherKey, "metric": "requests_per_month", "limit": float64(9), "enforce": true}})
		id = rule(t, "key", revokedID, "", "", "tokens_per_month", 55, "", true, nil)
		add(RuleAt0010{Case: "a key that is revoked", Tenant: t, ID: id, Cause: "migration_key_revoked",
			Fields: map[string]any{"subject_kind": "key", "subject_id": revokedID, "metric": "tokens_per_month", "limit": float64(55), "enforce": true}})

		// Deleted: the scope names nothing.
		id = rule(t, "tenant", "", "ghost-provider", "", "tokens_per_minute", 500, "", true, nil)
		add(RuleAt0010{Case: "a provider that does not exist", Tenant: t, ID: id, Cause: "migration_scope_missing",
			Fields: map[string]any{"subject_kind": "tenant", "subject_id": "", "provider": "ghost-provider", "metric": "tokens_per_minute", "limit": float64(500), "enforce": true}})
		id = rule(t, "tenant", "", "openai", "ghost-model", "cost_per_day", 2_500_000, "USD", true, []int16{50})
		add(RuleAt0010{Case: "a model that does not exist", Tenant: t, ID: id, Cause: "migration_scope_missing",
			Fields: map[string]any{"subject_kind": "tenant", "subject_id": "", "provider": "openai", "model": "ghost-model", "metric": "cost_per_day",
				"amount": "2.5", "currency": "USD", "enforce": true, "thresholds": []any{float64(50)}}})
	}

	// The ceilings: a default and a row of a tenant that survive, and two that name nothing.
	id := ceiling(nil, "openai", "upgrade-model", "tokens_per_day", 9_000_000, "", true, nil)
	add(RuleAt0010{Case: "a platform default with a provider and a model", Platform: true, ID: id, Kept: map[string]string{"provider_id": prov, "model_id": model}})
	id = ceiling(&a, "", "", "requests_per_minute", 600, "", true, nil)
	add(RuleAt0010{Case: "a platform row of a tenant without a scope", Tenant: a, Platform: true, ID: id, Kept: map[string]string{}})
	id = ceiling(nil, "ghost-provider", "", "requests_per_day", 800, "", true, nil)
	add(RuleAt0010{Case: "a platform default whose provider does not exist", Platform: true, ID: id, Cause: "migration_scope_missing",
		Fields: map[string]any{"tenant": "", "provider": "ghost-provider", "metric": "requests_per_day", "limit": float64(800), "enforce": true}})
	id = ceiling(&b, "anthropic", "ghost-model", "cost_per_month", 7_000_000, "EUR", true, nil)
	add(RuleAt0010{Case: "a platform row of a tenant whose model does not exist", Tenant: b, Platform: true, ID: id, Cause: "migration_scope_missing",
		Fields: map[string]any{"tenant": b.String(), "provider": "anthropic", "model": "ghost-model", "metric": "cost_per_month", "amount": "7", "currency": "EUR", "enforce": true}})
	return up
}
