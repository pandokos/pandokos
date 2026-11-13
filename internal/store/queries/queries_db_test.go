package queries_test

import (
	"context"
	"crypto/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

const zeroID = "00000000-0000-0000-0000-000000000000" // AfterID when there is no cursor: the statement casts it

func usec(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func equalTime(t *testing.T, what string, got *time.Time, want *time.Time) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s: got %v, want %v", what, got, want)
	case !got.Equal(*want):
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

// TestEveryStatement is T-230: each of the 28 generated methods is called through a store
// helper, directly and through the pooler. Identifiers, times, null times, arrays, null arrays,
// hashes and JSON go in and come back unchanged.
func TestEveryStatement(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		called := map[string]bool{}
		mark := func(name string) { called[name] = true }
		admin := d.Open(t, store.RoleAdmin)
		gateway := d.Open(t, store.RoleGateway)
		c := ctx(t)

		// Tenants, in platform scope.
		var ten queries.PandokosTenant
		must := func(what string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
		}
		must("tenant statements", admin.Platform(c, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			var err error
			mark("CreateTenant")
			if ten, err = q.CreateTenant(c, queries.CreateTenantParams{Name: "tenant-230", DisplayName: "Tenant ünï 230"}); err != nil {
				return err
			}
			if ten.State != "provisioning" || ten.Name != "tenant-230" || ten.DisplayName != "Tenant ünï 230" || len(ten.ID) != 36 || ten.CreatedAt.IsZero() {
				t.Errorf("CreateTenant: %+v", ten)
			}
			mark("GetTenant")
			got, err := q.GetTenant(c, ten.ID)
			if err != nil || got != ten {
				t.Errorf("GetTenant: %+v %v", got, err)
			}
			mark("GetTenantForUpdate")
			if got, err = q.GetTenantForUpdate(c, ten.ID); err != nil || got != ten {
				t.Errorf("GetTenantForUpdate: %+v %v", got, err)
			}
			mark("UpdateTenant")
			upd, err := q.UpdateTenant(c, queries.UpdateTenantParams{ID: ten.ID, SetName: true, Name: "tenant-230b"})
			if err != nil || upd.Name != "tenant-230b" || upd.DisplayName != ten.DisplayName {
				t.Errorf("UpdateTenant (name only): %+v %v", upd, err)
			}
			upd, err = q.UpdateTenant(c, queries.UpdateTenantParams{ID: ten.ID, SetDisplayName: true, DisplayName: "", SetName: true, Name: "tenant-230"})
			if err != nil || upd.Name != "tenant-230" || upd.DisplayName != "" {
				t.Errorf("UpdateTenant (display name to empty): %+v %v", upd, err)
			}
			mark("SetTenantState")
			act, err := q.SetTenantState(c, queries.SetTenantStateParams{ID: ten.ID, State: "active"})
			if err != nil || act.State != "active" {
				t.Errorf("SetTenantState: %+v %v", act, err)
			}
			ten = act
			// A second tenant, so that lists have something to order.
			second, err := q.CreateTenant(c, queries.CreateTenantParams{Name: "aaa-first", DisplayName: ""})
			if err != nil {
				return err
			}
			mark("ListTenants")
			for _, sortBy := range []string{"created_at", "-created_at", "name", "-name"} {
				all, err := q.ListTenants(c, queries.ListTenantsParams{AfterID: zeroID, Sort: sortBy, PageSize: 10})
				if err != nil || len(all) != 2 {
					t.Errorf("ListTenants %s: %d rows, %v", sortBy, len(all), err)
					return nil
				}
				wantFirst := map[string]string{"created_at": ten.ID, "-created_at": second.ID, "name": second.ID, "-name": ten.ID}[sortBy]
				if all[0].ID != wantFirst {
					t.Errorf("ListTenants %s: first is %s, want %s", sortBy, all[0].ID, wantFirst)
				}
				next, err := q.ListTenants(c, queries.ListTenantsParams{Sort: sortBy, PageSize: 10, After: true, AfterID: all[0].ID, AfterName: all[0].Name})
				if err != nil || len(next) != 1 || next[0].ID != all[1].ID {
					t.Errorf("ListTenants %s after the first: %+v %v", sortBy, next, err)
				}
			}
			byName, err := q.ListTenants(c, queries.ListTenantsParams{AfterID: zeroID, Sort: "created_at", PageSize: 10, Name: "tenant-230", State: "active"})
			if err != nil || len(byName) != 1 || byName[0].ID != ten.ID {
				t.Errorf("ListTenants by name and state: %+v %v", byName, err)
			}
			if none, err := q.ListTenants(c, queries.ListTenantsParams{AfterID: zeroID, Sort: "created_at", PageSize: 10, State: "suspended"}); err != nil || len(none) != 0 {
				t.Errorf("ListTenants by another state: %+v %v", none, err)
			}
			if one, err := q.ListTenants(c, queries.ListTenantsParams{AfterID: zeroID, Sort: "name", PageSize: 1}); err != nil || len(one) != 1 {
				t.Errorf("ListTenants with a page size of 1: %+v %v", one, err)
			}
			return nil
		}))
		tid, err := tenant.ParseID(ten.ID)
		must("ParseID", err)

		// Service accounts, role assignments, keys, in the tenant's session.
		from, until := usec(time.Now().Add(-time.Hour)), usec(time.Now().Add(24*time.Hour))
		var sa, sa2 queries.CreateServiceAccountRow
		hash := make([]byte, 32)
		if _, err := rand.Read(hash); err != nil {
			t.Fatal(err)
		}
		hash[0], hash[1], hash[2] = 0x00, '\\', '\''
		scopes := []string{"model:invoke", "virtual_key:read", `odd, "value" {x}`, "é"}
		expires := usec(time.Now().Add(48 * time.Hour))
		must("tenant statements", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			var err error
			mark("CreateServiceAccount")
			if sa, err = q.CreateServiceAccount(c, queries.CreateServiceAccountParams{TenantID: ten.ID, Name: "bot", Description: "a bot, ünï", ValidFrom: &from, ValidUntil: &until}); err != nil {
				return err
			}
			equalTime(t, "valid_from", sa.ValidFrom, &from)
			equalTime(t, "valid_until", sa.ValidUntil, &until)
			if sa.State != "active" || sa.Description != "a bot, ünï" || sa.TenantID != ten.ID {
				t.Errorf("CreateServiceAccount: %+v", sa)
			}
			if sa2, err = q.CreateServiceAccount(c, queries.CreateServiceAccountParams{TenantID: ten.ID, Name: "bare"}); err != nil {
				return err
			}
			if sa2.ValidFrom != nil || sa2.ValidUntil != nil {
				t.Errorf("null times came back as %v %v", sa2.ValidFrom, sa2.ValidUntil)
			}
			mark("GetServiceAccount")
			got, err := q.GetServiceAccount(c, sa.ID)
			if err != nil || got.ID != sa.ID || got.Roles == nil || len(got.Roles) != 0 {
				t.Errorf("GetServiceAccount: %+v %v (roles must be an empty array, not null)", got, err)
			}
			mark("CreateRoleAssignment")
			for _, role := range []string{"tenant-admin", "inference"} {
				if err := q.CreateRoleAssignment(c, queries.CreateRoleAssignmentParams{TenantID: ten.ID, ServiceAccountID: sa.ID, Role: role}); err != nil {
					return err
				}
			}
			if got, err = q.GetServiceAccount(c, sa.ID); err != nil || !reflect.DeepEqual(got.Roles, []string{"inference", "tenant-admin"}) {
				t.Errorf("roles after two assignments: %+v %v", got.Roles, err)
			}
			mark("LockServiceAccount")
			lock, err := q.LockServiceAccount(c, sa.ID)
			if err != nil || lock.ID != sa.ID || lock.State != "active" {
				t.Errorf("LockServiceAccount: %+v %v", lock, err)
			}
			mark("ListServiceAccounts")
			for _, sortBy := range []string{"created_at", "-created_at", "name", "-name"} {
				all, err := q.ListServiceAccounts(c, queries.ListServiceAccountsParams{AfterID: zeroID, Sort: sortBy, PageSize: 10})
				if err != nil || len(all) != 2 {
					t.Errorf("ListServiceAccounts %s: %d rows, %v", sortBy, len(all), err)
					return nil
				}
				wantFirst := map[string]string{"created_at": sa.ID, "-created_at": sa2.ID, "name": sa2.ID, "-name": sa.ID}[sortBy]
				if all[0].ID != wantFirst {
					t.Errorf("ListServiceAccounts %s: first is %s, want %s", sortBy, all[0].ID, wantFirst)
				}
				next, err := q.ListServiceAccounts(c, queries.ListServiceAccountsParams{Sort: sortBy, PageSize: 10, After: true, AfterID: all[0].ID, AfterName: all[0].Name})
				if err != nil || len(next) != 1 || next[0].ID != all[1].ID {
					t.Errorf("ListServiceAccounts %s after the first: %+v %v", sortBy, next, err)
				}
			}
			if byName, err := q.ListServiceAccounts(c, queries.ListServiceAccountsParams{AfterID: zeroID, Sort: "name", PageSize: 10, Name: "bot", State: "active"}); err != nil || len(byName) != 1 || !reflect.DeepEqual(byName[0].Roles, []string{"inference", "tenant-admin"}) {
				t.Errorf("ListServiceAccounts by name: %+v %v", byName, err)
			}
			mark("UpdateServiceAccount")
			newFrom := usec(time.Now().Add(-2 * time.Hour))
			n, err := q.UpdateServiceAccount(c, queries.UpdateServiceAccountParams{ID: sa.ID,
				SetName: true, Name: "bot2", SetDescription: true, Description: "", SetValidFrom: true, ValidFrom: &newFrom, SetValidUntil: true, ValidUntil: nil})
			if err != nil || n != 1 {
				t.Errorf("UpdateServiceAccount: %d %v", n, err)
			}
			got, err = q.GetServiceAccount(c, sa.ID)
			if err != nil || got.Name != "bot2" || got.Description != "" || got.ValidUntil != nil {
				t.Errorf("after the update: %+v %v", got, err)
			}
			equalTime(t, "valid_from after the update", got.ValidFrom, &newFrom)
			if n, err := q.UpdateServiceAccount(c, queries.UpdateServiceAccountParams{ID: sa.ID}); err != nil || n != 1 {
				t.Errorf("an update that sets nothing: %d %v", n, err)
			}
			if got2, err := q.GetServiceAccount(c, sa.ID); err != nil || got2.Name != "bot2" || got2.ValidFrom == nil {
				t.Errorf("an update that sets nothing changed the row: %+v %v", got2, err)
			}
			mark("SetServiceAccountState")
			if n, err := q.SetServiceAccountState(c, queries.SetServiceAccountStateParams{ID: sa.ID, State: "disabled"}); err != nil || n != 1 {
				t.Errorf("SetServiceAccountState: %d %v", n, err)
			}
			if n, err := q.SetServiceAccountState(c, queries.SetServiceAccountStateParams{ID: sa.ID, State: "active"}); err != nil || n != 1 {
				t.Errorf("SetServiceAccountState back: %d %v", n, err)
			}
			mark("DeleteRoleAssignments")
			if n, err := q.DeleteRoleAssignments(c, sa2.ID); err != nil || n != 0 {
				t.Errorf("DeleteRoleAssignments of an account without roles: %d %v", n, err)
			}
			if err := q.CreateRoleAssignment(c, queries.CreateRoleAssignmentParams{TenantID: ten.ID, ServiceAccountID: sa2.ID, Role: "inference"}); err != nil {
				return err
			}
			if n, err := q.DeleteRoleAssignments(c, sa2.ID); err != nil || n != 1 {
				t.Errorf("DeleteRoleAssignments: %d %v", n, err)
			}
			return nil
		}))

		var scoped, unscoped queries.CreateVirtualKeyRow
		const keyScoped, keyUnscoped = "tabcdefghijklmnop", "tabcdefghijklmnoq"
		must("key statements", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			var err error
			mark("CreateVirtualKey")
			if scoped, err = q.CreateVirtualKey(c, queries.CreateVirtualKeyParams{TenantID: ten.ID, ServiceAccountID: sa.ID, KeyID: keyScoped,
				Description: "ci", SecretHash: hash, Scoped: true, Scopes: scopes, ExpiresAt: &expires}); err != nil {
				return err
			}
			if !scoped.Scoped || !reflect.DeepEqual(scoped.Scopes, scopes) || scoped.State != "active" || scoped.RevokedAt != nil {
				t.Errorf("CreateVirtualKey (scoped): %+v", scoped)
			}
			equalTime(t, "expires_at", scoped.ExpiresAt, &expires)
			if unscoped, err = q.CreateVirtualKey(c, queries.CreateVirtualKeyParams{TenantID: ten.ID, ServiceAccountID: sa.ID, KeyID: keyUnscoped,
				SecretHash: hash, Scoped: false}); err != nil {
				return err
			}
			if unscoped.Scoped || unscoped.Scopes == nil || len(unscoped.Scopes) != 0 || unscoped.ExpiresAt != nil {
				t.Errorf("CreateVirtualKey (not scoped, no expiry): %+v (a null array must come back as scoped=false and an empty array)", unscoped)
			}
			mark("GetVirtualKey")
			got, err := q.GetVirtualKey(c, keyScoped)
			if err != nil || !reflect.DeepEqual(got.Scopes, scopes) || got.ServiceAccountID != sa.ID || got.Description != "ci" || got.State != "active" {
				t.Errorf("GetVirtualKey: %+v %v", got, err)
			}
			mark("ListVirtualKeys")
			for _, sortBy := range []string{"created_at", "-created_at"} {
				all, err := q.ListVirtualKeys(c, queries.ListVirtualKeysParams{AfterID: zeroID, ServiceAccountID: zeroID, Sort: sortBy, PageSize: 10})
				if err != nil || len(all) != 2 {
					t.Errorf("ListVirtualKeys %s: %d rows, %v", sortBy, len(all), err)
					return nil
				}
				wantFirst := map[string]string{"created_at": keyScoped, "-created_at": keyUnscoped}[sortBy]
				if all[0].KeyID != wantFirst {
					t.Errorf("ListVirtualKeys %s: first is %s, want %s", sortBy, all[0].KeyID, wantFirst)
				}
				next, err := q.ListVirtualKeys(c, queries.ListVirtualKeysParams{ServiceAccountID: zeroID, Sort: sortBy, PageSize: 10, After: true, AfterID: all[0].ID})
				if err != nil || len(next) != 1 || next[0].ID != all[1].ID {
					t.Errorf("ListVirtualKeys %s after the first: %+v %v", sortBy, next, err)
				}
			}
			if bySA, err := q.ListVirtualKeys(c, queries.ListVirtualKeysParams{AfterID: zeroID, Sort: "created_at", PageSize: 10, ByServiceAccount: true, ServiceAccountID: sa2.ID}); err != nil || len(bySA) != 0 {
				t.Errorf("ListVirtualKeys of another service account: %+v %v", bySA, err)
			}
			if bySA, err := q.ListVirtualKeys(c, queries.ListVirtualKeysParams{AfterID: zeroID, Sort: "created_at", PageSize: 10, ByServiceAccount: true, ServiceAccountID: sa.ID, State: "active"}); err != nil || len(bySA) != 2 {
				t.Errorf("ListVirtualKeys of the service account: %+v %v", bySA, err)
			}
			return nil
		}))

		// The key is read the way the gateway reads it: the lookup without a tenant, then the
		// principal in the tenant's session.
		mark("LookupVirtualKeyTenant")
		var looked, missing string
		must("lookup", gateway.Unscoped(c, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			var err error
			if looked, err = q.LookupVirtualKeyTenant(c, keyScoped); err != nil {
				return err
			}
			missing, err = q.LookupVirtualKeyTenant(c, "tzzzzzzzzzzzzzzzz")
			return err
		}))
		if looked != ten.ID || missing != "" {
			t.Errorf("lookup: %q and %q, want %q and an empty string", looked, missing, ten.ID)
		}
		mark("GetKeyPrincipal")
		must("principal", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			p, err := queries.New(tx).GetKeyPrincipal(c, keyScoped)
			if err != nil {
				return err
			}
			if string(p.SecretHash) != string(hash) || !p.Scoped || !reflect.DeepEqual(p.Scopes, scopes) || p.ServiceAccountID != sa.ID ||
				p.ServiceAccountName != "bot2" || p.ServiceAccountState != "active" || p.TenantID != ten.ID || p.TenantName != "tenant-230" ||
				p.TenantState != "active" || !reflect.DeepEqual(p.Roles, []string{"inference", "tenant-admin"}) || p.RevokedAt != nil {
				t.Errorf("GetKeyPrincipal: %+v", p)
			}
			equalTime(t, "key expires_at", p.ExpiresAt, &expires)
			if p.ValidUntil != nil || p.ValidFrom == nil {
				t.Errorf("validity of the service account: %v %v", p.ValidFrom, p.ValidUntil)
			}
			up, err := queries.New(tx).GetKeyPrincipal(c, keyUnscoped)
			if err != nil || up.Scoped || up.Scopes == nil || len(up.Scopes) != 0 || up.ExpiresAt != nil {
				t.Errorf("GetKeyPrincipal (not scoped): %+v %v", up, err)
			}
			return nil
		}))
		mark("RevokeVirtualKey")
		must("revoke", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			if n, err := q.RevokeVirtualKey(c, keyScoped); err != nil || n != 1 {
				t.Errorf("RevokeVirtualKey: %d %v", n, err)
			}
			if n, err := q.RevokeVirtualKey(c, keyScoped); err != nil || n != 0 {
				t.Errorf("a second RevokeVirtualKey: %d %v", n, err)
			}
			got, err := q.GetVirtualKey(c, keyScoped)
			if err != nil || got.State != "revoked" || got.RevokedAt == nil {
				t.Errorf("after the revocation: %+v %v", got, err)
			}
			if rev, err := q.ListVirtualKeys(c, queries.ListVirtualKeysParams{AfterID: zeroID, ServiceAccountID: zeroID, Sort: "created_at", PageSize: 10, State: "revoked"}); err != nil || len(rev) != 1 || rev[0].KeyID != keyScoped {
				t.Errorf("ListVirtualKeys by state: %+v %v", rev, err)
			}
			return nil
		}))

		// Platform keys.
		const platformKey = "pabcdefghijklmnop"
		pexpires := usec(time.Now().Add(time.Hour))
		must("platform key statements", admin.Platform(c, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			mark("LockPlatformKeys")
			if err := q.LockPlatformKeys(c); err != nil {
				return err
			}
			mark("CountUsablePlatformKeys")
			if n, err := q.CountUsablePlatformKeys(c); err != nil || n.Usable != 0 || n.Bootstrap != 0 {
				t.Errorf("CountUsablePlatformKeys on an empty table: %+v %v", n, err)
			}
			mark("CreatePlatformKey")
			created, err := q.CreatePlatformKey(c, queries.CreatePlatformKeyParams{KeyID: platformKey, Description: "boot", Role: "platform-admin", SecretHash: hash, Bootstrap: true, ExpiresAt: &pexpires})
			if err != nil || created.State != "active" || !created.Bootstrap {
				t.Errorf("CreatePlatformKey: %+v %v", created, err)
			}
			if _, err := q.CreatePlatformKey(c, queries.CreatePlatformKeyParams{KeyID: "pabcdefghijklmnoq", Role: "platform-admin", SecretHash: hash}); err != nil {
				return err
			}
			if n, err := q.CountUsablePlatformKeys(c); err != nil || n.Usable != 2 || n.Bootstrap != 1 {
				t.Errorf("CountUsablePlatformKeys: %+v %v", n, err)
			}
			mark("GetPlatformKey")
			got, err := q.GetPlatformKey(c, platformKey)
			if err != nil || string(got.SecretHash) != string(hash) || got.Role != "platform-admin" || got.Description != "boot" {
				t.Errorf("GetPlatformKey: %+v %v", got, err)
			}
			equalTime(t, "platform key expires_at", got.ExpiresAt, &pexpires)
			mark("ListPlatformKeys")
			for _, sortBy := range []string{"created_at", "-created_at"} {
				all, err := q.ListPlatformKeys(c, queries.ListPlatformKeysParams{AfterID: zeroID, Sort: sortBy, PageSize: 10})
				if err != nil || len(all) != 2 {
					t.Errorf("ListPlatformKeys %s: %d rows, %v", sortBy, len(all), err)
					return nil
				}
				next, err := q.ListPlatformKeys(c, queries.ListPlatformKeysParams{Sort: sortBy, PageSize: 10, After: true, AfterID: all[0].ID})
				if err != nil || len(next) != 1 || next[0].ID != all[1].ID {
					t.Errorf("ListPlatformKeys %s after the first: %+v %v", sortBy, next, err)
				}
			}
			mark("RevokePlatformKey")
			if n, err := q.RevokePlatformKey(c, platformKey); err != nil || n != 1 {
				t.Errorf("RevokePlatformKey: %d %v", n, err)
			}
			if n, err := q.CountUsablePlatformKeys(c); err != nil || n.Usable != 1 || n.Bootstrap != 0 {
				t.Errorf("CountUsablePlatformKeys after the revocation: %+v %v", n, err)
			}
			if rev, err := q.ListPlatformKeys(c, queries.ListPlatformKeysParams{AfterID: zeroID, Sort: "created_at", PageSize: 10, State: "revoked"}); err != nil || len(rev) != 1 {
				t.Errorf("ListPlatformKeys by state: %+v %v", rev, err)
			}
			return nil
		}))

		// Audit rows: JSON goes in as text and comes back unchanged.
		const detail = `{"a": 1, "b": [1, 2], "c": "é\"x", "d": {"e": null}}`
		mark("InsertAuditEvent")
		must("audit", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			return queries.New(tx).InsertAuditEvent(c, queries.InsertAuditEventParams{TenantID: ten.ID, ActorType: "service_account", ActorID: sa.ID, ActorName: "bot2",
				KeyID: keyScoped, Action: "virtual_key.revoke", ResourceType: "virtual_key", ResourceID: keyScoped, Outcome: "success", RequestID: "req-230", Reason: "because, ünï", Detail: detail})
		}))
		mark("InsertPlatformAuditEvent")
		must("platform audit", admin.Platform(c, func(c context.Context, tx store.Tx) error {
			return queries.New(tx).InsertPlatformAuditEvent(c, queries.InsertPlatformAuditEventParams{ActorType: "host", ActorName: "pandokos init",
				Action: "platform_key.bootstrap", ResourceType: "platform_key", ResourceID: platformKey, Outcome: "success", RequestID: "req-230", Detail: "{}"})
		}))
		super := d.Super(t)
		var back, reason string
		var occurred time.Time
		if err := super.QueryRow(c, `SELECT detail::text, reason, occurred_at FROM pandokos.audit_event WHERE request_id = 'req-230'`).Scan(&back, &reason, &occurred); err != nil {
			t.Fatal(err)
		}
		if reason != "because, ünï" || time.Since(occurred) > time.Minute || time.Since(occurred) < -time.Minute {
			t.Errorf("audit row: reason %q, occurred_at %v", reason, occurred)
		}
		var equal bool
		if err := super.QueryRow(c, `SELECT detail = $1::text::jsonb FROM pandokos.audit_event WHERE request_id = 'req-230'`, detail).Scan(&equal); err != nil || !equal {
			t.Errorf("the JSON detail came back as %s (%v)", back, err)
		}
		var n int
		if err := super.QueryRow(c, `SELECT count(*) FROM pandokos.platform_audit_event WHERE request_id = 'req-230' AND actor_type = 'host'`).Scan(&n); err != nil || n != 1 {
			t.Errorf("platform audit rows: %d %v", n, err)
		}

		// The 28 statements, and nothing was prepared on the server.
		var names []string
		typ := reflect.TypeOf(&queries.Queries{})
		for i := 0; i < typ.NumMethod(); i++ {
			if name := typ.Method(i).Name; name != "WithTx" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) != 28 {
			t.Errorf("the generated package has %d statements, want 28: %v", len(names), names)
		}
		for _, name := range names {
			if !called[name] {
				t.Errorf("the statement %s is not exercised by this test", name)
			}
		}
		for _, db := range []*store.DB{admin, gateway} {
			for i := 0; i < 8; i++ {
				must("prepared statements", db.Unscoped(c, func(c context.Context, tx store.Tx) error {
					var prepared int
					if err := tx.QueryRow(c, "SELECT count(*) FROM pg_catalog.pg_prepared_statements").Scan(&prepared); err != nil {
						return err
					}
					if prepared != 0 {
						t.Errorf("%d prepared statements on a connection", prepared)
					}
					return nil
				}))
			}
		}
	})
}
