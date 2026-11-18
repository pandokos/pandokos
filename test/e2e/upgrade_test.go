package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
)

// TestUpgradeWithRules is T-725: `pandokos migrate` on a database at 0010 that enforces a quota.
// After the upgrade, and after a restart, the quota still refuses, with the count of the ledger.
func TestUpgradeWithRules(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 10)

	// The world of 5.2, written as 5.2 wrote it: a tenant, an inference service account with a key, a
	// quota of three requests a day (named by the old columns) and three requests in the ledger.
	tn := d.SeedTenant(t, "upgrade")
	sa := d.SeedServiceAccount(t, tn, "workload")
	key, keyID, hash, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	super := d.Super(t)
	ctx := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'inference')`, []any{tn.String(), sa}},
		{`INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, []any{tn.String(), sa, keyID, hash[:]}},
		{`INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value) VALUES ($1::uuid, 'tenant', '', 'requests_per_day', 3)`, []any{tn.String()}},
	} {
		if _, err := super.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	// The ledger holds three requests that opened a credential (the gateway role cannot be used here: the
	// binary of this tree refuses a database at 0010).
	if _, err := super.Exec(ctx, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, key_id,
			credential_id, provider, api_family, operation, outcome, duration_ms, input_source, output_source, input_tokens, cost_status)
		SELECT $1::uuid, pandokos.uuid_v7(), now() - g * interval '1 second', md5(random()::text), 'service_account', $2, '', gen_random_uuid(),
			'openai', 'openai', 'chat.completions', 'ok', 1, 'provider', 'none', 10, 'none'
		  FROM generate_series(1, 3) g`, tn.String(), sa); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	// The upgrade: stop, migrate, start.
	code, out, errs := runBinary(t, d.Env(t), "migrate")
	if code != 0 || !strings.Contains(out, "applied 0011_limit_integrity") || !strings.Contains(out, "applied 0012_tenant_price_lists") || !strings.Contains(out, "schema is at version 12") {
		t.Fatalf("migrate: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	var kept int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.limit_rule WHERE tenant_id = $1::uuid AND subject_kind = 'tenant' AND metric = 'requests_per_day' AND limit_value = 3`, tn.String()).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("the rule did not survive the upgrade: %d %v", kept, err)
	}

	refuses := func(t *testing.T, label string, first bool) {
		t.Helper()
		up := fakeprovider.New(t)
		p := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true})
		if first { // the platform key, the endpoint and the models are made once; a restart finds them
			p.prepareUpstream(t)
		}
		status, text := p.chatCall(t, key.Reveal(), fakeprovider.ModelBasic, false)
		assertPandokosError(t, label, status, text, 429, "quota_exceeded")
		for _, want := range []string{"X-Pandokos-Limit-Metric: requests_per_day", "X-Pandokos-Limit: 3", "X-Pandokos-Limit-Remaining: 0", "X-Pandokos-Limit-Subject: tenant"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: the refusal lacks %q\n%s", label, want, text)
			}
		}
		if got := p.stop(t); got != 0 {
			t.Errorf("%s: the process exited with %d", label, got)
		}
	}
	refuses(t, "after the upgrade", true)
	refuses(t, "after a restart", false)
}
