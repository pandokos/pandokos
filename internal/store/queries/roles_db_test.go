package queries_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// roleStatements covers the 21 statements of sprint 4.3: custom roles in a tenant's session, and
// support actions in the platform scope that is also a tenant's session (what store.Provision
// gives; ActInTenant is the same pair of settings).
func roleStatements(c context.Context, t *testing.T, d *dbtest.Database, admin *store.DB, tid tenant.ID, tenantID, serviceAccountID string, called map[string]bool) {
	t.Helper()
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	perms := []string{"member:read", "role:read"}
	var roleID string
	must("role statements", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("CreateTenantRole")
		r, err := q.CreateTenantRole(c, queries.CreateTenantRoleParams{TenantID: tenantID, Name: "reader-230", Description: "reads, ünï", Permissions: perms})
		if err != nil {
			return err
		}
		if r.State != "active" || r.TenantID != tenantID || !reflect.DeepEqual(r.Permissions, perms) || len(r.ID) != 36 {
			t.Errorf("CreateTenantRole: %+v", r)
		}
		roleID = r.ID
		mark("GetTenantRole")
		got, err := q.GetTenantRole(c, "reader-230")
		if err != nil || got.ID != roleID || got.Members != 0 || got.ServiceAccounts != 0 || got.Description != "reads, ünï" {
			t.Errorf("GetTenantRole: %+v %v", got, err)
		}
		if _, err := q.GetTenantRole(c, "nobody-230"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetTenantRole of a role that does not exist: %v", err)
		}
		mark("CountTenantRoles")
		if n, err := q.CountTenantRoles(c); err != nil || n != 1 {
			t.Errorf("CountTenantRoles: %d %v", n, err)
		}
		mark("ListTenantRoles")
		list, err := q.ListTenantRoles(c, queries.ListTenantRolesParams{Sort: "name", PageSize: 10, AfterID: "00000000-0000-7000-8000-000000000000"})
		if err != nil || len(list) != 1 || list[0].Name != "reader-230" {
			t.Errorf("ListTenantRoles: %+v %v", list, err)
		}
		if none, err := q.ListTenantRoles(c, queries.ListTenantRolesParams{Sort: "-created_at", State: "inactive", PageSize: 10, AfterID: "00000000-0000-7000-8000-000000000000"}); err != nil || len(none) != 0 {
			t.Errorf("ListTenantRoles of another state: %+v %v", none, err)
		}
		mark("LockTenantRole")
		if l, err := q.LockTenantRole(c, "reader-230"); err != nil || l.ID != roleID || l.State != "active" || !reflect.DeepEqual(l.Permissions, perms) {
			t.Errorf("LockTenantRole: %+v %v", l, err)
		}
		mark("LockTenantRolesForAssignment")
		if ls, err := q.LockTenantRolesForAssignment(c, []string{"reader-230", "tenant-admin"}); err != nil || len(ls) != 1 || ls[0].Name != "reader-230" {
			t.Errorf("LockTenantRolesForAssignment: %+v %v", ls, err)
		}
		mark("UpdateTenantRole")
		if n, err := q.UpdateTenantRole(c, queries.UpdateTenantRoleParams{ID: roleID, Description: "changed", Permissions: []string{"member:read"}}); err != nil || n != 1 {
			t.Errorf("UpdateTenantRole: %d %v", n, err)
		}
		mark("SetTenantRoleState")
		if n, err := q.SetTenantRoleState(c, queries.SetTenantRoleStateParams{ID: roleID, State: "inactive"}); err != nil || n != 1 {
			t.Errorf("SetTenantRoleState: %d %v", n, err)
		}
		if n, err := q.SetTenantRoleState(c, queries.SetTenantRoleStateParams{ID: roleID, State: "active"}); err != nil || n != 1 {
			t.Errorf("SetTenantRoleState back: %d %v", n, err)
		}
		return nil
	}))

	// The role is given to the service account (the owner of the tables does it: the statements
	// that assign roles are those of the handlers).
	if _, err := d.Super(t).Exec(c, `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'reader-230')`, tenantID, serviceAccountID); err != nil {
		t.Fatal(err)
	}
	must("holders", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("CountRoleHolders")
		if h, err := q.CountRoleHolders(c, "reader-230"); err != nil || h.Members != 0 || h.ServiceAccounts != 1 {
			t.Errorf("CountRoleHolders: %+v %v", h, err)
		}
		mark("GetServiceAccountGrant")
		g, err := q.GetServiceAccountGrant(c, serviceAccountID)
		if err != nil || !reflect.DeepEqual(g.Granted, []string{"member:read"}) {
			t.Errorf("GetServiceAccountGrant: %+v %v", g, err)
		}
		mark("GetMemberGrant")
		if _, err := q.GetMemberGrant(c, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetMemberGrant of a user that is not a member: %v", err)
		}
		return nil
	}))
	// An assigned role is not deleted; the trigger is the backstop.
	must("assigned role", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		_, err := tx.Exec(c, `SAVEPOINT s`)
		if err != nil {
			return err
		}
		if _, err := queries.New(tx).DeleteTenantRole(c, roleID); err == nil {
			t.Error("a role that is assigned was deleted")
		}
		_, err = tx.Exec(c, `ROLLBACK TO SAVEPOINT s`)
		return err
	}))
	if _, err := d.Super(t).Exec(c, `DELETE FROM pandokos.role_assignment WHERE role = 'reader-230'`); err != nil {
		t.Fatal(err)
	}
	must("delete", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		mark("DeleteTenantRole")
		if n, err := queries.New(tx).DeleteTenantRole(c, roleID); err != nil || n != 1 {
			t.Errorf("DeleteTenantRole: %d %v", n, err)
		}
		return nil
	}))

	// Support actions: the transaction of the platform that is also the tenant's.
	var actionID, secondID string
	must("support actions", admin.Provision(c, func(c context.Context, tx store.Tx, bind func(context.Context, tenant.ID) error) error {
		if err := bind(c, tid); err != nil {
			return err
		}
		q := queries.New(tx)
		mark("CloseOpenSupportActions")
		if ids, err := q.CloseOpenSupportActions(c, queries.CloseOpenSupportActionsParams{TenantID: tenantID, ActorType: "platform", ActorID: "pkabcdefghijklmn", ClosedByID: "pkabcdefghijklmn"}); err != nil || len(ids) != 0 {
			t.Errorf("CloseOpenSupportActions with nothing open: %v %v", ids, err)
		}
		mark("OpenSupportAction")
		a, err := q.OpenSupportAction(c, queries.OpenSupportActionParams{TenantID: tenantID, ActorType: "platform", ActorID: "pkabcdefghijklmn", ActorName: "ops",
			KeyID: "pkabcdefghijklmn", Reason: "recovery, ünï", Permissions: []string{"member:read", "member:write"}, RequestID: "0123456789abcdef0123456789abcdef", Seconds: 600})
		if err != nil {
			return err
		}
		if a.ClosedAt != nil || a.ClosedBy != "" || a.TenantID != tenantID || a.Reason != "recovery, ünï" || !a.ExpiresAt.After(a.OpenedAt) || time.Until(a.ExpiresAt) > 11*time.Minute {
			t.Errorf("OpenSupportAction: %+v", a)
		}
		actionID = a.ID
		mark("TenantExists")
		if ok, err := q.TenantExists(c, tenantID); err != nil || !ok {
			t.Errorf("TenantExists: %v %v", ok, err)
		}
		if ok, err := q.TenantExists(c, "00000000-0000-7000-8000-000000000000"); err != nil || ok {
			t.Errorf("TenantExists of a tenant that does not exist: %v %v", ok, err)
		}
		mark("GetOpenSupportAction")
		o, err := q.GetOpenSupportAction(c, queries.GetOpenSupportActionParams{TenantID: tenantID, ActorType: "platform", ActorID: "pkabcdefghijklmn"})
		if err != nil || o.ID != actionID || len(o.Permissions) != 2 || o.TenantState != "active" || o.TenantName == "" {
			t.Errorf("GetOpenSupportAction: %+v %v", o, err)
		}
		mark("ListOpenSupportActionsOfActor")
		if l, err := q.ListOpenSupportActionsOfActor(c, queries.ListOpenSupportActionsOfActorParams{ActorType: "platform", ActorID: "pkabcdefghijklmn"}); err != nil || len(l) != 1 || l[0].ID != actionID {
			t.Errorf("ListOpenSupportActionsOfActor: %+v %v", l, err)
		}
		mark("GetSupportAction")
		if g, err := q.GetSupportAction(c, queries.GetSupportActionParams{ID: actionID, TenantID: tenantID}); err != nil || g.ID != actionID || g.ActorName != "ops" {
			t.Errorf("GetSupportAction: %+v %v", g, err)
		}
		mark("LockSupportAction")
		if l, err := q.LockSupportAction(c, queries.LockSupportActionParams{ID: actionID, TenantID: tenantID}); err != nil || l.ID != actionID {
			t.Errorf("LockSupportAction: %+v %v", l, err)
		}
		// Opening again closes the first first: one open action per actor and tenant.
		ids, err := q.CloseOpenSupportActions(c, queries.CloseOpenSupportActionsParams{TenantID: tenantID, ActorType: "platform", ActorID: "pkabcdefghijklmn", ClosedByID: "pkabcdefghijklmn"})
		if err != nil || len(ids) != 1 || ids[0].ID != actionID || ids[0].ClosedBy != "platform" {
			t.Errorf("CloseOpenSupportActions: %v %v", ids, err)
		}
		b, err := q.OpenSupportAction(c, queries.OpenSupportActionParams{TenantID: tenantID, ActorType: "platform", ActorID: "pkabcdefghijklmn", ActorName: "ops",
			KeyID: "pkabcdefghijklmn", Reason: "again", Permissions: []string{"member:read"}, Seconds: 600})
		if err != nil {
			return err
		}
		secondID = b.ID
		return nil
	}))
	must("close and list", admin.Provision(c, func(c context.Context, tx store.Tx, bind func(context.Context, tenant.ID) error) error {
		if err := bind(c, tid); err != nil {
			return err
		}
		q := queries.New(tx)
		first, err := q.GetSupportAction(c, queries.GetSupportActionParams{ID: actionID, TenantID: tenantID})
		if err != nil || first.ClosedAt == nil || first.ClosedBy != "platform" || first.ClosedByID != "pkabcdefghijklmn" {
			t.Errorf("the first action after it was closed by the second opening: %+v %v", first, err)
		}
		mark("ListSupportActions")
		open, err := q.ListSupportActions(c, queries.ListSupportActionsParams{TenantID: tenantID, State: "open", Sort: "-opened_at", PageSize: 10, AfterID: "00000000-0000-7000-8000-000000000000"})
		if err != nil || len(open) != 1 || open[0].ID != secondID {
			t.Errorf("ListSupportActions open: %+v %v", open, err)
		}
		closed, err := q.ListSupportActions(c, queries.ListSupportActionsParams{TenantID: tenantID, State: "closed", Sort: "opened_at", PageSize: 10, AfterID: "00000000-0000-7000-8000-000000000000"})
		if err != nil || len(closed) != 1 || closed[0].ID != actionID {
			t.Errorf("ListSupportActions closed: %+v %v", closed, err)
		}
		mark("CloseSupportAction")
		if n, err := q.CloseSupportAction(c, queries.CloseSupportActionParams{ID: secondID, ClosedBy: "tenant", ClosedByID: "sa", CloseReason: "done"}); err != nil || n != 1 {
			t.Errorf("CloseSupportAction: %d %v", n, err)
		}
		if n, err := q.CloseSupportAction(c, queries.CloseSupportActionParams{ID: secondID, ClosedBy: "tenant"}); err != nil || n != 0 {
			t.Errorf("CloseSupportAction of a closed action: %d %v", n, err)
		}
		return nil
	}))
}
