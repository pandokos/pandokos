package dbtest

import (
	"context"
	"testing"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The helpers below write limit rules through the admin role, in a tenant session or in the
// platform scope, so that every test that uses them also exercises the grants and the policies.
// Plain SQL is allowed in a test seed, as in the other seeds of this package.

// SeedLimitRule inserts a rule of a tenant. subjectID is empty for the subject kind "tenant".
func (d *Database) SeedLimitRule(tb testing.TB, id tenant.ID, subjectKind, subjectID, metric string, value int64) {
	tb.Helper()
	adm := d.Open(tb, store.RoleAdmin)
	ctx, cancel := ctx30()
	defer cancel()
	err := adm.WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		_, err := tx.Exec(c, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value)
			VALUES ($1::uuid, $2, $3, $4, $5)`, id.String(), subjectKind, subjectID, metric, value)
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
			WHERE subject_kind = $2 AND subject_id = $3 AND metric = $4`, id.String(), subjectKind, subjectID, metric, value)
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
// default of every tenant. A ceiling that exists is changed.
func (d *Database) SeedPlatformLimit(tb testing.TB, id *tenant.ID, metric string, value int64) {
	tb.Helper()
	adm := d.Open(tb, store.RoleAdmin)
	ctx, cancel := ctx30()
	defer cancel()
	err := adm.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var arg any
		if id != nil {
			arg = id.String()
		}
		tag, err := tx.Exec(c, `UPDATE pandokos.platform_limit_rule SET limit_value = $3
			WHERE tenant_id IS NOT DISTINCT FROM $1::uuid AND metric = $2`, arg, metric, value)
		if err != nil || tag.RowsAffected() > 0 {
			return err
		}
		_, err = tx.Exec(c, `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES ($1::uuid, $2, $3)`,
			arg, metric, value)
		return err
	})
	if err != nil {
		tb.Fatalf("seed platform limit: %v", err)
	}
}
