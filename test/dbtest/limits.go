package dbtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The helpers below write limit rules through the admin role, in a tenant session or in the
// platform scope, so that every test that uses them also exercises the grants and the policies.
// Plain SQL is allowed in a test seed, as in the other seeds of this package.

// LimitOptions are the columns of a limit rule that sprint 5.2 added; the zero value is a rule
// for every provider and model that enforces and has no thresholds, as in sprint 5.1.
type LimitOptions struct {
	Provider, Model string // the scope; Model needs Provider
	Currency        string // three capital letters for a cost metric, empty otherwise
	NoEnforce       bool   // enforce = false: the rule only records its thresholds; needs Thresholds
	Thresholds      []int  // percentages, 1 to 100, at most four
}

func (o LimitOptions) thresholds() []int16 {
	out := make([]int16, len(o.Thresholds))
	for i, p := range o.Thresholds {
		out[i] = int16(p)
	}
	return out
}

func firstOptions(opts []LimitOptions) LimitOptions {
	if len(opts) == 0 {
		return LimitOptions{}
	}
	return opts[0]
}

// SeedLimitRule inserts a rule of a tenant. subjectID is empty for the subject kind "tenant".
// The optional LimitOptions give the scope, the currency, the enforcement and the thresholds.
func (d *Database) SeedLimitRule(tb testing.TB, id tenant.ID, subjectKind, subjectID, metric string, value int64, opts ...LimitOptions) {
	tb.Helper()
	o := firstOptions(opts)
	adm := d.Open(tb, store.RoleAdmin)
	ctx, cancel := ctx30()
	defer cancel()
	err := adm.WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		_, err := tx.Exec(c, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value,
				provider, model, currency, enforce, thresholds)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::smallint[])`,
			id.String(), subjectKind, subjectID, metric, value, o.Provider, o.Model, o.Currency, !o.NoEnforce, o.thresholds())
		return err
	})
	if err != nil {
		tb.Fatalf("seed limit rule: %v", err)
	}
}

// SetLimitRule changes the value of an existing rule of a tenant.
func (d *Database) SetLimitRule(tb testing.TB, id tenant.ID, subjectKind, subjectID, metric string, value int64) {
	tb.Helper()
	adm := d.Open(tb, store.RoleAdmin)
	ctx, cancel := ctx30()
	defer cancel()
	err := adm.WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		tag, err := tx.Exec(c, `UPDATE pandokos.limit_rule SET limit_value = $5
			WHERE tenant_id = $1::uuid AND subject_kind = $2 AND subject_id = $3 AND metric = $4`, id.String(), subjectKind, subjectID, metric, value)
		if err == nil && tag.RowsAffected() != 1 {
			tb.Errorf("set limit rule: %d rows changed", tag.RowsAffected())
		}
		return err
	})
	if err != nil {
		tb.Fatalf("set limit rule: %v", err)
	}
}

// DeleteLimitRule removes a rule of a tenant.
func (d *Database) DeleteLimitRule(tb testing.TB, id tenant.ID, subjectKind, subjectID, metric string) {
	tb.Helper()
	adm := d.Open(tb, store.RoleAdmin)
	ctx, cancel := ctx30()
	defer cancel()
	err := adm.WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		_, err := tx.Exec(c, `DELETE FROM pandokos.limit_rule WHERE subject_kind = $1 AND subject_id = $2 AND metric = $3`,
			subjectKind, subjectID, metric)
		return err
	})
	if err != nil {
		tb.Fatalf("delete limit rule: %v", err)
	}
}

// SeedPlatformLimit sets the platform ceiling of a metric: for one tenant, or, when id is nil, the
// default of every tenant. A ceiling of the same scope and metric that exists is changed. The
// optional LimitOptions give the scope, the currency, the enforcement and the thresholds.
func (d *Database) SeedPlatformLimit(tb testing.TB, id *tenant.ID, metric string, value int64, opts ...LimitOptions) {
	tb.Helper()
	o := firstOptions(opts)
	adm := d.Open(tb, store.RoleAdmin)
	ctx, cancel := ctx30()
	defer cancel()
	err := adm.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var arg any
		if id != nil {
			arg = id.String()
		}
		tag, err := tx.Exec(c, `UPDATE pandokos.platform_limit_rule SET limit_value = $3, enforce = $7, thresholds = $8::smallint[]
			WHERE tenant_id IS NOT DISTINCT FROM $1::uuid AND metric = $2 AND provider = $4 AND model = $5 AND currency = $6`,
			arg, metric, value, o.Provider, o.Model, o.Currency, !o.NoEnforce, o.thresholds())
		if err != nil || tag.RowsAffected() > 0 {
			return err
		}
		_, err = tx.Exec(c, `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value, provider, model, currency, enforce, thresholds)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::smallint[])`,
			arg, metric, value, o.Provider, o.Model, o.Currency, !o.NoEnforce, o.thresholds())
		return err
	})
	if err != nil {
		tb.Fatalf("seed platform limit: %v", err)
	}
}

// UsageRow is one usage record for SeedUsage. The zero value is a request that was served: it
// opened a credential, so a quota or a budget counts it.
type UsageRow struct {
	StartedAt             time.Time
	PrincipalID           string // a service account ID; a random one when empty
	KeyID                 string
	Provider, Model       string // "openai" and "" when empty
	NoCredential          bool   // no credential was opened for the request: it is not counted
	InputTokens           int64  // reported by the provider; both zero: no token figure
	OutputTokens          int64
	EstimatedInputTokens  int64 // estimated by Pandokos; used only when the provider reported nothing
	EstimatedOutputTokens int64
	Cost                  string // a decimal in Currency; empty: the cost is not known
	Currency              string
	Outcome               string // "ok" when empty
}

// SeedUsage inserts usage records of a tenant on the gateway role, in a session of that tenant,
// the way the ledger writer does. The partitions of the months of the rows must exist.
func (d *Database) SeedUsage(tb testing.TB, id tenant.ID, rows ...UsageRow) {
	tb.Helper()
	gw := d.Open(tb, store.RoleGateway)
	ctx, cancel := ctx30()
	defer cancel()
	err := gw.WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		for i, r := range rows {
			if r.StartedAt.IsZero() {
				return fmt.Errorf("row %d has no start time", i)
			}
			provider, outcome := r.Provider, r.Outcome
			if provider == "" {
				provider = "openai"
			}
			if outcome == "" {
				outcome = "ok"
			}
			var principal any
			if r.PrincipalID != "" {
				principal = r.PrincipalID
			}
			var cost, currency any
			if r.Cost != "" {
				cost, currency = r.Cost, r.Currency
			}
			var in, out, ein, eout any
			inSource, outSource := "none", "none"
			switch {
			case r.InputTokens != 0:
				in, inSource = r.InputTokens, "provider"
			case r.EstimatedInputTokens != 0:
				ein, inSource = r.EstimatedInputTokens, "estimated"
			}
			switch {
			case r.OutputTokens != 0:
				out, outSource = r.OutputTokens, "provider"
			case r.EstimatedOutputTokens != 0:
				eout, outSource = r.EstimatedOutputTokens, "estimated"
			}
			_, err := tx.Exec(c, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind,
					principal_id, key_id, credential_id, provider, api_family, operation, model, outcome, duration_ms,
					input_source, output_source, input_tokens, output_tokens, estimated_input_tokens, estimated_output_tokens,
					cost_status, cost, currency, price_version_id)
				VALUES ($1::uuid, pandokos.uuid_v7(), $2, md5(random()::text), 'service_account',
					COALESCE($3::text, gen_random_uuid()::text), $4, CASE WHEN $5 THEN NULL ELSE gen_random_uuid() END,
					$6, $6, 'chat.completions', $7, $8, 1, $9, $10, $11::bigint, $12::bigint, $13::bigint, $14::bigint,
					CASE WHEN $15::numeric IS NULL THEN 'none' ELSE 'known' END, $15::numeric, $16::text,
					CASE WHEN $15::numeric IS NULL THEN NULL ELSE gen_random_uuid() END)`,
				id.String(), r.StartedAt.UTC(), principal, r.KeyID, r.NoCredential, provider, r.Model, outcome,
				inSource, outSource, in, out, ein, eout, cost, currency)
			if err != nil {
				return fmt.Errorf("row %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("seed usage: %v", err)
	}
}
