package isolation

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// The canaries of the tests of what the platform does not see: improbable amounts, token counts and
// rates that must appear in no answer to the platform, no row of its audit log, no row it can read.
const (
	canaryTokens = 7654321
	canaryRate   = "6.54321"
)

var canaryTexts = []string{"7654321", "6.54321"}

// seedCanaries gives a tenant usage records, limit events and prices whose figures are canaries.
func seedCanaries(t *testing.T, d *dbtest.Database, id tenant.ID, provider, model string) {
	t.Helper()
	now := time.Now().UTC()
	d.SeedUsage(t, id,
		dbtest.UsageRow{StartedAt: now.Add(-time.Hour), KeyID: "k1", Provider: provider, Model: model, InputTokens: canaryTokens, OutputTokens: canaryTokens + 1, Cost: canaryRate, Currency: "EUR"},
		dbtest.UsageRow{StartedAt: now.Add(-2 * time.Hour), KeyID: "k1", Provider: provider, Model: model, InputTokens: 1, Cost: "0.65432", Currency: "EUR"})
	if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency,
			period_start, period_end, percent, limit_value, observed)
		VALUES ($1::uuid, 'threshold_reached', '00000000-0000-4000-8000-0000000000a1', 'tenant', 'key', 'k1', $4, $2, 'tokens_per_day', '',
			date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', 80, 9000000, $3)`, id.String(), model, float64(canaryTokens), provider); err != nil {
		t.Fatal(err)
	}
	d.SeedTenantPrice(t, id, "EUR", provider, model, now.Add(2*time.Hour), dbtest.Rate{Category: "input", Rate: canaryRate}, dbtest.Rate{Category: "output", Rate: canaryRate})
}

// usageTables lists the tables that hold a tenant's usage and prices: the ledger, each of its
// partitions by name, the limit events and the two price tables.
func usageTables(t *testing.T, super *pgx.Conn) []string {
	t.Helper()
	rows, err := super.Query(ctx(t), `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND (c.relname = 'usage_record' OR c.relname LIKE 'usage_record\_%') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 3 {
		t.Fatalf("the ledger and its partitions: %v", names)
	}
	return append(names, "limit_event", "tenant_price_version", "tenant_price_rate")
}

// TestPlatformSeesNoTenantUsage is T-746: with usage records, limit events and prices of two tenants
// present, each runtime role in the platform scope and without scope, direct and pooled, reads zero rows
// or is refused on usage_record, on each of its partitions by name, on limit_event and on both price
// tables; the SECURITY DEFINER functions of the schema are the eleven of 5.3 with the result types the
// test pins, and the body of none names one of these tables except ensure_usage_partitions (partitions,
// no row).
func TestPlatformSeesNoTenantUsage(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "blind-a"), d.SeedTenant(t, "blind-b")
		model := seedModel(t, d, "blind-model")
		super := d.Super(t)
		seedCanaries(t, d, a, "openai", "blind-model")
		seedCanaries(t, d, b, "openai", "blind-model")
		tables := usageTables(t, super)

		t.Run("each runtime role reads nothing in the platform scope and without scope", func(t *testing.T) {
			blind := []session{{name: "platform", setting: "app.scope", value: "platform"}, noSession, {name: "empty", setting: "app.tenant_id", value: ""}}
			for _, role := range runtimeRoles {
				conn := d.Connect(t, role)
				for _, tbl := range tables {
					// The control: the tenant's own session reads its rows where the role may read the table at all.
					for _, s := range blind {
						var n int
						err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
							return tx.QueryRow(c, `SELECT count(*) FROM `+table(tbl)).Scan(&n)
						})
						if err != nil && sqlstate(err) != insufficientPrivilege {
							t.Errorf("%s reads %s in %s: %v, want no row or 42501", role, tbl, s.name, err)
						}
						if err == nil && n != 0 {
							t.Errorf("%s reads %d rows of %s in the session %s", role, n, tbl, s.name)
						}
					}
				}
			}
			// The rows are there: the superuser counts them, and a tenant's own session reads its share.
			for _, tbl := range []string{"usage_record", "limit_event", "tenant_price_version", "tenant_price_rate"} {
				var n int
				if err := super.QueryRow(ctx(t), `SELECT count(*) FROM `+table(tbl)).Scan(&n); err != nil || n < 2 {
					t.Errorf("%s holds %d rows (%v): the test proves nothing", tbl, n, err)
				}
			}
			var n int
			if err := inSession(t, d.Connect(t, "admin"), sessions(a)[0], func(c context.Context, tx pgx.Tx) error {
				return tx.QueryRow(c, `SELECT count(*) FROM pandokos.tenant_price_version`).Scan(&n)
			}); err != nil || n != 1 {
				t.Errorf("tenant A reads %d price versions (%v), want its own one", n, err)
			}
		})

		t.Run("the definer functions are the eleven of sprint 5.3, with the result types pinned", func(t *testing.T) {
			rows, err := super.Query(ctx(t), `SELECT p.proname, pg_get_function_result(p.oid), p.prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = 'pandokos' AND p.prosecdef ORDER BY 1`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			want := map[string]string{
				"delete_platform_issuer_limit_rules": "bigint",
				"ensure_usage_partitions":            "record",
				"issuer_directory_sync":              "trigger",
				"issuer_lookup":                      "TABLE(scope text, tenant_id uuid, issuer_id uuid)",
				"platform_issuer_mapping_count":      "bigint",
				"platform_issuer_tenant_lookup":      "uuid",
				"record_auth_failure":                "void",
				"record_token_auth_failure":          "void",
				"record_unlimited_traffic":           "boolean",
				"virtual_key_directory_add":          "trigger",
				"virtual_key_lookup":                 "uuid",
			}
			got := map[string]string{}
			for rows.Next() {
				var name, result, body string
				if err := rows.Scan(&name, &result, &body); err != nil {
					t.Fatal(err)
				}
				got[name] = result
				for _, tbl := range append([]string{"usage_record", "limit_event"}, "tenant_price_version", "tenant_price_rate") {
					if strings.Contains(body, tbl) && name != "ensure_usage_partitions" {
						t.Errorf("the body of %s names %s", name, tbl)
					}
				}
				for _, tbl := range []string{"tenant_price_version", "tenant_price_rate", "limit_event"} {
					if strings.Contains(body, tbl) {
						t.Errorf("the body of %s names %s", name, tbl)
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("the definer functions:\n got %v\nwant %v", got, want)
			}
			_ = model
		})
	})
}

// TestPlatformAPISeesNoTenantUsage is T-747: tenant A has records, events and prices with canary amounts,
// token counts and rates. A platform-admin key calls every operation of platform scope, with A wherever a
// path names a tenant: no answer and no row of the platform's audit log contains a canary; every
// tenant-scoped operation on A answers 403.
func TestPlatformAPISeesNoTenantUsage(t *testing.T) {
	w := newAPIWorld(t)
	idA, err := tenant.ParseID(w.a.tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	seedCanaries(t, w.api.DB, idA, w.cat.Provider.Name, w.cat.All.Name)
	platformKey := w.api.PlatformKey()
	absent := func(what string, text string) {
		t.Helper()
		for _, c := range canaryTexts {
			if strings.Contains(text, c) {
				t.Errorf("%s contains the canary %s", what, c)
			}
		}
	}

	tenantOps, platformOps := 0, 0
	for _, op := range adminapi.Operations {
		switch scopeOf(t, op) {
		case authz.ScopeTenant:
			c := apitest.CallFor(t, op, w.fresh(t, w.a, op))
			r := w.api.Do(t, c.Method, c.Path, platformKey, c.Body, nil)
			expect403(t, op, r)
			absent(op.ID+" (a tenant operation, called with a platform key)", string(r.Body))
			tenantOps++
		}
	}
	for _, op := range adminapi.Operations {
		if scopeOf(t, op) != authz.ScopePlatform {
			continue
		}
		p := w.fresh(t, w.a, op)
		p.PlatformKey = "pabcdefghijklmnop" // a key that does not exist: revoking it harms nothing
		c := apitest.CallFor(t, op, p)
		r := w.api.Do(t, c.Method, c.Path, platformKey, c.Body, nil)
		absent(fmt.Sprintf("%s (%d)", op.ID, r.Status), string(r.Body)+fmt.Sprint(r.Header))
		platformOps++
	}
	if tenantOps < 45 || platformOps < 40 {
		t.Errorf("%d tenant operations and %d platform operations were called: too few", tenantOps, platformOps)
	}
	rows, err := w.api.Super(t).Query(ctx(t), `SELECT t::text FROM pandokos.platform_audit_event t`)
	if err != nil {
		t.Fatal(err)
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) == 0 {
		t.Fatal("the platform's audit log is empty: the test proves nothing")
	}
	for _, text := range texts {
		absent("a row of the platform's audit log", text)
	}
	if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, w.a.tenant.ID) }) {
		t.Log("no row of the platform's log names tenant A: the calls on A changed nothing it logs")
	}
}
