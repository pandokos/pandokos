package migrations_test

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestTenantPriceListsUpgrade is T-765: a database at 0011 with tenants, rules, ceilings and usage rows:
// after 0012 no row changed, every tenant has the default currency, the usage rows have no price source
// (they read as the catalogue's or empty), the inserts of the statements of 5.3 (a usage record without
// price_source, a tenant, a rule) are accepted, and the migration applied twice is refused.
func TestTenantPriceListsUpgrade(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 11)
	super := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	var a, b string
	for i, dst := range []*string{&a, &b} {
		if err := super.QueryRow(ctx, `INSERT INTO pandokos.tenant (name) VALUES ($1) RETURNING id::text`, []string{"upgrade-a", "upgrade-b"}[i]).Scan(dst); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value, currency) VALUES ($1::uuid, 'tenant', 'cost_per_month', 1000, 'EUR')`, *dst)
		exec(`INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value) VALUES ($1::uuid, 'tenant', 'requests_per_day', 10)`, *dst)
		exec(`INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value, currency) VALUES ($1::uuid, 'cost_per_day', 5000, 'USD')`, *dst)
		exec(`INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation, outcome, duration_ms,
				input_source, output_source, cost_status, cost, currency, price_version_id)
			VALUES ($1::uuid, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', '0192f0f0-0000-7000-8000-0000000000a2', 'openai', 'openai', 'chat.completions',
				'ok', 1, 'none', 'none', 'known', 0.5, 'USD', pandokos.uuid_v7())`, *dst)
	}
	exec(`INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES (NULL, 'requests_per_minute', 1000)`)

	snapshot := func(tbl, drop string) string {
		t.Helper()
		var s string
		if err := super.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) - $1::text[] ORDER BY to_jsonb(t)::text)::text, '[]') FROM pandokos.`+tbl+` t`, splitCols(drop)).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	tables := map[string]string{"limit_rule": "", "platform_limit_rule": "", "usage_record": "price_source", "tenant": "active_currency,active_currency_previous,active_currency_effective_from"}
	before := map[string]string{}
	for tbl, drop := range tables {
		before[tbl] = snapshot(tbl, drop)
	}

	d.Migrate(t)

	for tbl, drop := range tables {
		if got := snapshot(tbl, drop); got != before[tbl] {
			t.Errorf("%s changed by the migration:\n%s\n%s", tbl, before[tbl], got)
		}
	}
	var n int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.tenant WHERE active_currency <> '' OR active_currency_previous <> '' OR active_currency_effective_from IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d tenants without the default currency (%v)", n, err)
	}
	if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.usage_record WHERE price_source IS NOT NULL`).Scan(&n); err != nil || n != 2 && n != 0 {
		t.Errorf("%d rows with a price source (%v)", n, err)
	}
	if n != 0 {
		t.Errorf("%d old rows were given a price source: they read as the catalogue's through NULL", n)
	}

	t.Run("the statements of 5.3 are accepted", func(*testing.T) {
		exec(`INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation, outcome, duration_ms,
				input_source, output_source, cost_status) VALUES ($1::uuid, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', '0192f0f0-0000-7000-8000-0000000000a2',
				'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none')`, a)
		exec(`INSERT INTO pandokos.tenant (name) VALUES ('upgrade-after')`)
		exec(`INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value) VALUES ($1::uuid, 'tenant', 'tokens_per_day', 5)`, a)
		exec(`INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES ($1::uuid, 'tokens_per_day', 5)`, b)
	})

	t.Run("the migration applied twice is refused", func(t *testing.T) {
		if _, err := d.Connect(t, "owner").Exec(ctx, migrationSQL(t, 12)); err == nil {
			t.Error("migration 0012 ran twice")
		}
		var versions int
		if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.schema_migration`).Scan(&versions); err != nil || versions != 14 {
			t.Errorf("the ledger holds %d migrations (%v), want 14", versions, err)
		}
		d.Migrate(t) // the migrator applies what is missing: nothing
	})
}

func splitCols(s string) []string {
	if s == "" {
		return []string{}
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
