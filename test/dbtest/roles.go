package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// CustomRole inserts an active custom role of the tenant (sprint 4.3) as the admin role, in the
// tenant's session, and returns its name. permissions are written as they are: a test that wants
// the set to be normalized passes it normalized.
func (d *Database) CustomRole(tb testing.TB, tid tenant.ID, name string, permissions ...string) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	err := d.admin(tb).WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		_, err := queries.New(tx).CreateTenantRole(ctx, queries.CreateTenantRoleParams{TenantID: tid.String(), Name: name, Permissions: permissions})
		return err
	})
	if err != nil {
		tb.Fatalf("seed custom role %s: %v", name, err)
	}
	return name
}

// SupportAction opens a support action in the tenant for the actor (actorType "platform" with the
// key ID, or "user" with the user ID), as the admin role, in one transaction that is the
// platform's and the tenant's (the pair of settings that store.DB.ActInTenant sets). It returns the
// ID of the action. The action lasts the duration; no audit row is written.
func (d *Database) SupportAction(tb testing.TB, tid tenant.ID, actorType, actorID, reason string, duration time.Duration, permissions ...string) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	var id string
	err := d.admin(tb).Provision(ctx, func(ctx context.Context, tx store.Tx, bind func(ctx context.Context, id tenant.ID) error) error {
		if err := bind(ctx, tid); err != nil {
			return err
		}
		q := queries.New(tx)
		if _, err := q.CloseOpenSupportActions(ctx, queries.CloseOpenSupportActionsParams{TenantID: tid.String(), ActorType: actorType, ActorID: actorID, ClosedByID: actorID}); err != nil {
			return err
		}
		row, err := q.OpenSupportAction(ctx, queries.OpenSupportActionParams{
			TenantID: tid.String(), ActorType: actorType, ActorID: actorID, ActorName: "seed", Reason: reason,
			Permissions: permissions, Seconds: int32(duration / time.Second), //nolint:gosec // a test duration
		})
		id = row.ID
		return err
	})
	if err != nil {
		tb.Fatalf("seed support action: %v", err)
	}
	return id
}

// ExpireSupportAction moves the opening and the expiry of the action into the past, so that it has
// expired a minute ago and its lifetime is unchanged, as the owner, in a transaction that disables
// the guard trigger of the table for itself and enables it again before the commit.
func (d *Database) ExpireSupportAction(tb testing.TB, id string) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	conn := d.Super(tb)
	tx, err := conn.Begin(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, st := range []struct {
		sql  string
		args []any
	}{
		{sql: `ALTER TABLE pandokos.support_action DISABLE TRIGGER support_action_guard`},
		{sql: `UPDATE pandokos.support_action SET opened_at = opened_at - (expires_at - now() + interval '1 minute'),
			expires_at = expires_at - (expires_at - now() + interval '1 minute') WHERE id = $1::uuid`, args: []any{id}},
		{sql: `ALTER TABLE pandokos.support_action ENABLE TRIGGER support_action_guard`},
	} {
		if _, err := tx.Exec(ctx, st.sql, st.args...); err != nil {
			tb.Fatalf("expire support action: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		tb.Fatal(err)
	}
}
