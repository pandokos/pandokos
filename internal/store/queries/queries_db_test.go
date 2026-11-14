package queries_test

import (
	"context"
	"crypto/rand"
	"errors"
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

// TestEveryStatement is T-230, extended by T-323 and by T-393: each of the 92 generated methods
// (28 of epic 1, the 19 of sprint 2.1 and the 45 of sprint 2.2) is called through a store helper,
// directly and through the pooler.
// Identifiers, times, null times, arrays, null arrays, hashes, sealed bytes and JSON go in and
// come back unchanged.
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
		mark("EnsureUsagePartitions")
		worker := d.Open(t, store.RoleWorker)
		must("usage partitions", worker.Platform(c, func(c context.Context, tx store.Tx) error {
			row, err := queries.New(tx).EnsureUsagePartitions(c, 3)
			if err == nil && row.DefaultInUse {
				err = errors.New("the default usage partition is in use")
			}
			return err
		}))
		mark("InsertUsageRecords")
		must("usage records", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			rec := `[{"id":"0192f0f0-0000-7000-8000-0000000000a1","started_at":"` + time.Now().UTC().Format(time.RFC3339Nano) +
				`","request_id":"0123456789abcdef0123456789abcdef","principal_kind":"service_account","principal_id":"0192f0f0-0000-7000-8000-0000000000a2",` +
				`"funding":"byok","provider":"openai","api_family":"openai","operation":"chat.completions","outcome":"ok","duration_ms":1,` +
				`"input_source":"none","output_source":"none","cost_status":"none","cost_reason":"","trace_id":"","key_id":"","endpoint":"","model":"",` +
				`"upstream_model":"","error_code":"","other_units":{},"provider_usage":{},"metadata":{},"metadata_dropped":0}]`
			n, err := queries.New(tx).InsertUsageRecords(c, rec)
			if err == nil && n != 1 {
				err = errors.New("InsertUsageRecords did not insert one row")
			}
			return err
		}))
		mark("RecordAuthFailure")
		must("auth failure", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			return queries.New(tx).RecordAuthFailure(c, queries.RecordAuthFailureParams{KeyID: keyScoped, Reason: "bad_secret", RequestID: "0123456789abcdef0123456789abcdef"})
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

		// Sprint 2.1: key-encryption key versions, data keys, credentials and the pin of a key.
		vaultStatements(c, t, admin, gateway, tid, ten.ID, sa.ID, hash, called)

		// Sprint 2.2: the catalogue, the model access and the prices.
		catalogStatements(c, t, admin, gateway, tid, ten.ID, sa.ID, hash, called)

		// The 95 statements, and nothing was prepared on the server.
		var names []string
		typ := reflect.TypeOf(&queries.Queries{})
		for i := 0; i < typ.NumMethod(); i++ {
			if name := typ.Method(i).Name; name != "WithTx" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) != 95 {
			t.Errorf("the generated package has %d statements, want 95: %v", len(names), names)
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

// vaultStatements is the part of T-323 that covers the vault and credential statements.
func vaultStatements(c context.Context, t *testing.T, admin, gateway *store.DB, tid tenant.ID, tenantID, serviceAccountID string, hash []byte, called map[string]bool) {
	t.Helper()
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	random := func(n int) []byte {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		b[0], b[1], b[2] = 0x00, '\\', '\'' // bytes that text protocols mangle
		return b
	}
	const kek = "local-0123456789abcdef"
	wrapped, sealed1, sealed2 := random(61), random(48), random(52)
	expires := usec(time.Now().Add(72 * time.Hour))
	var c1, c2, c3 queries.CreateCredentialRow

	must("vault statements", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("RegisterKEKVersion")
		for i := 0; i < 2; i++ { // the second call changes nothing
			if err := q.RegisterKEKVersion(c, queries.RegisterKEKVersionParams{ID: kek, Provider: "local"}); err != nil {
				return err
			}
		}
		mark("ListKEKVersions")
		versions, err := q.ListKEKVersions(c)
		if err != nil || !reflect.DeepEqual(versions, []queries.ListKEKVersionsRow{{ID: kek, Provider: "local"}}) {
			t.Errorf("ListKEKVersions: %+v %v", versions, err)
		}
		mark("CreateDataKey")
		if n, err := q.CreateDataKey(c, queries.CreateDataKeyParams{TenantID: tenantID, Version: 1, KekVersion: kek, WrappedKey: wrapped}); err != nil || n != 1 {
			t.Errorf("CreateDataKey: %d %v", n, err)
		}
		if n, err := q.CreateDataKey(c, queries.CreateDataKeyParams{TenantID: tenantID, Version: 1, KekVersion: kek, WrappedKey: random(61)}); err != nil || n != 0 {
			t.Errorf("a second CreateDataKey of the same version: %d %v, want 0 rows and no error", n, err)
		}
		mark("GetDataKey")
		dk, err := q.GetDataKey(c, 1)
		if err != nil || dk.Version != 1 || dk.KekVersion != kek || !reflect.DeepEqual(dk.WrappedKey, wrapped) {
			t.Errorf("GetDataKey: %+v %v (the first writer wins and the bytes come back unchanged)", dk, err)
		}
		if _, err := q.GetDataKey(c, 2); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetDataKey of a version that does not exist: %v", err)
		}

		mark("NewCredentialID")
		id1, err := q.NewCredentialID(c)
		if err != nil || len(id1) != 36 {
			t.Fatalf("NewCredentialID: %q %v", id1, err)
		}
		time.Sleep(3 * time.Millisecond) // the order of identifiers is the order of milliseconds
		id2, _ := q.NewCredentialID(c)
		time.Sleep(3 * time.Millisecond)
		id3, _ := q.NewCredentialID(c)
		if id1 == id2 || id2 == id3 || id1 >= id2 || id2 >= id3 {
			t.Errorf("identifiers are not new and increasing: %s %s %s", id1, id2, id3)
		}
		mark("CreateCredential")
		base := queries.CreateCredentialParams{TenantID: tenantID, KeyVersion: 1}
		p1, p2, p3 := base, base, base
		p1.ID, p1.Provider, p1.Label, p1.Description, p1.Secret, p1.Fingerprint, p1.Hint, p1.ExpiresAt, p1.Validated =
			id1, "openai", "main", "the first, ünï", sealed1, "0123456789abcdef0123456789abcdef", "wxyz", &expires, true
		p2.ID, p2.Provider, p2.Label, p2.Secret, p2.Fingerprint = id2, "openai", "second", sealed2, "fedcba9876543210fedcba9876543210"
		p3.ID, p3.Provider, p3.Label, p3.Secret, p3.Fingerprint = id3, "anthropic", "claude", sealed1, "00112233445566778899aabbccddeeff"
		if c1, err = q.CreateCredential(c, p1); err != nil {
			return err
		}
		if c2, err = q.CreateCredential(c, p2); err != nil {
			return err
		}
		if c3, err = q.CreateCredential(c, p3); err != nil {
			return err
		}
		equalTime(t, "expires_at", c1.ExpiresAt, &expires)
		if !c1.IsDefault || c2.IsDefault || !c3.IsDefault {
			t.Errorf("defaults: %v %v %v, want the first credential of each provider only", c1.IsDefault, c2.IsDefault, c3.IsDefault)
		}
		if c1.State != "active" || c1.Provider != "openai" || c1.Label != "main" || c1.Description != "the first, ünï" || c1.Fingerprint != p1.Fingerprint || c1.Hint != "wxyz" ||
			c1.ValidatedAt == nil || c1.RotatedAt != nil || c1.StateChangedBy != "tenant" || c1.StateReason != "" || c1.TenantID != tenantID || c1.KeyVersion != 1 {
			t.Errorf("CreateCredential: %+v", c1)
		}
		if c2.ValidatedAt != nil || c2.ExpiresAt != nil || c2.Hint != "" {
			t.Errorf("CreateCredential (not validated, no expiry): %+v", c2)
		}

		mark("GetCredential")
		got, err := q.GetCredential(c, id1)
		if err != nil || got.ID != id1 || got.Label != "main" || got.Fingerprint != p1.Fingerprint || !got.IsDefault || got.Hint != "wxyz" || got.ValidatedAt == nil {
			t.Errorf("GetCredential: %+v %v", got, err)
		}
		if _, err := q.GetCredential(c, "0192f0f0-0000-7000-8000-000000000bad"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetCredential of a credential that does not exist: %v", err)
		}
		mark("ListCredentials")
		for _, sortBy := range []string{"created_at", "-created_at"} {
			all, err := q.ListCredentials(c, queries.ListCredentialsParams{AfterID: zeroID, Sort: sortBy, PageSize: 10})
			if err != nil || len(all) != 3 {
				t.Fatalf("ListCredentials %s: %d rows, %v", sortBy, len(all), err)
			}
			want := map[string]string{"created_at": id1, "-created_at": id3}[sortBy]
			if all[0].ID != want {
				t.Errorf("ListCredentials %s: first is %s, want %s", sortBy, all[0].ID, want)
			}
			next, err := q.ListCredentials(c, queries.ListCredentialsParams{Sort: sortBy, PageSize: 10, After: true, AfterID: all[0].ID})
			if err != nil || len(next) != 2 || next[0].ID != all[1].ID {
				t.Errorf("ListCredentials %s after the first: %+v %v", sortBy, next, err)
			}
		}
		if byProvider, err := q.ListCredentials(c, queries.ListCredentialsParams{AfterID: zeroID, Sort: "created_at", PageSize: 10, Provider: "openai", State: "active"}); err != nil || len(byProvider) != 2 {
			t.Errorf("ListCredentials by provider and state: %+v %v", byProvider, err)
		}
		if none, err := q.ListCredentials(c, queries.ListCredentialsParams{AfterID: zeroID, Sort: "created_at", PageSize: 10, State: "revoked"}); err != nil || len(none) != 0 {
			t.Errorf("ListCredentials by another state: %+v %v", none, err)
		}
		if one, err := q.ListCredentials(c, queries.ListCredentialsParams{AfterID: zeroID, Sort: "created_at", PageSize: 1}); err != nil || len(one) != 1 {
			t.Errorf("ListCredentials with a page size of 1: %+v %v", one, err)
		}
		mark("LockCredential")
		lock, err := q.LockCredential(c, id1)
		if err != nil || lock != (queries.LockCredentialRow{ID: id1, Provider: "openai", Label: "main", State: "active", IsDefault: true, Fingerprint: p1.Fingerprint, Hint: "wxyz"}) {
			t.Errorf("LockCredential: %+v %v", lock, err)
		}
		mark("UpdateCredential")
		upd, err := q.UpdateCredential(c, queries.UpdateCredentialParams{ID: id2, SetLabel: true, Label: "renamed", SetExpiresAt: true, ExpiresAt: &expires})
		if err != nil || upd.Label != "renamed" || upd.Description != "" {
			t.Errorf("UpdateCredential (label and expiry): %+v %v", upd, err)
		}
		equalTime(t, "expires_at after the update", upd.ExpiresAt, &expires)
		upd, err = q.UpdateCredential(c, queries.UpdateCredentialParams{ID: id2, SetDescription: true, Description: "d, é", SetExpiresAt: true, ExpiresAt: nil})
		if err != nil || upd.Label != "renamed" || upd.Description != "d, é" || upd.ExpiresAt != nil {
			t.Errorf("UpdateCredential (description, expiry cleared): %+v %v", upd, err)
		}
		if upd, err = q.UpdateCredential(c, queries.UpdateCredentialParams{ID: id2}); err != nil || upd.Label != "renamed" || upd.Description != "d, é" {
			t.Errorf("an update that sets nothing: %+v %v", upd, err)
		}
		mark("ReplaceCredentialSecret")
		rep, err := q.ReplaceCredentialSecret(c, queries.ReplaceCredentialSecretParams{ID: id1, KeyVersion: 1, Secret: sealed2, Fingerprint: p2.Fingerprint, Hint: "abcd", Validated: false})
		if err != nil || rep.Fingerprint != p2.Fingerprint || rep.Hint != "abcd" || rep.RotatedAt == nil || rep.ValidatedAt != nil || rep.ExpiresAt != nil {
			t.Errorf("ReplaceCredentialSecret: %+v %v (rotated_at is set, validated_at and the expiry are cleared)", rep, err)
		}
		if rep, err = q.ReplaceCredentialSecret(c, queries.ReplaceCredentialSecretParams{ID: id1, KeyVersion: 1, Secret: sealed1, Fingerprint: p1.Fingerprint, Hint: "wxyz", ExpiresAt: &expires, Validated: true}); err != nil ||
			rep.ValidatedAt == nil || rep.ExpiresAt == nil || rep.Fingerprint != p1.Fingerprint {
			t.Errorf("ReplaceCredentialSecret (validated, with an expiry): %+v %v", rep, err)
		}
		mark("SetCredentialState")
		st, err := q.SetCredentialState(c, queries.SetCredentialStateParams{ID: id3, State: "disabled", ChangedBy: "platform", Reason: "abuse, ünï"})
		if err != nil || st.State != "disabled" || st.StateChangedBy != "platform" || st.StateReason != "abuse, ünï" {
			t.Errorf("SetCredentialState: %+v %v", st, err)
		}
		if st, err = q.SetCredentialState(c, queries.SetCredentialStateParams{ID: id3, State: "active", ChangedBy: "tenant"}); err != nil || st.State != "active" || st.StateReason != "" {
			t.Errorf("SetCredentialState back: %+v %v", st, err)
		}
		mark("ClearDefaultCredential")
		cleared, err := q.ClearDefaultCredential(c, "openai")
		if err != nil || cleared != id1 {
			t.Errorf("ClearDefaultCredential: %q %v, want %s", cleared, err, id1)
		}
		if _, err := q.ClearDefaultCredential(c, "openai"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("ClearDefaultCredential when there is none: %v", err)
		}
		mark("SetDefaultCredential")
		def, err := q.SetDefaultCredential(c, id2)
		if err != nil || !def.IsDefault || def.ID != id2 {
			t.Errorf("SetDefaultCredential: %+v %v", def, err)
		}
		mark("CountOtherCredentials")
		if n, err := q.CountOtherCredentials(c, queries.CountOtherCredentialsParams{Provider: "openai", ID: id2}); err != nil || n != 1 {
			t.Errorf("CountOtherCredentials: %d %v, want 1", n, err)
		}
		if n, err := q.CountOtherCredentials(c, queries.CountOtherCredentialsParams{Provider: "anthropic", ID: id3}); err != nil || n != 0 {
			t.Errorf("CountOtherCredentials of a provider with one credential: %d %v", n, err)
		}

		mark("CountUsablePins")
		if n, err := q.CountUsablePins(c, id2); err != nil || n != 0 {
			t.Errorf("CountUsablePins with no key: %d %v", n, err)
		}
		pin := id2
		k1, err := q.CreateVirtualKey(c, queries.CreateVirtualKeyParams{TenantID: tenantID, ServiceAccountID: serviceAccountID, KeyID: "tpinnedkeyaaaaaaa", SecretHash: hash, CredentialID: &pin})
		if err != nil {
			return err
		}
		if k1.CredentialID == nil || *k1.CredentialID != id2 {
			t.Errorf("CreateVirtualKey returned the pin %v, want %s", k1.CredentialID, id2)
		}
		if gk, err := q.GetVirtualKey(c, "tpinnedkeyaaaaaaa"); err != nil || gk.CredentialID == nil || *gk.CredentialID != id2 {
			t.Errorf("GetVirtualKey: pin %v %v", gk.CredentialID, err)
		}
		list, err := q.ListVirtualKeys(c, queries.ListVirtualKeysParams{AfterID: zeroID, ServiceAccountID: zeroID, Sort: "-created_at", PageSize: 1})
		if err != nil || len(list) != 1 || list[0].CredentialID == nil || *list[0].CredentialID != id2 {
			t.Errorf("ListVirtualKeys: %+v %v", list, err)
		}
		if n, err := q.CountUsablePins(c, id2); err != nil || n != 1 {
			t.Errorf("CountUsablePins with a usable key: %d %v, want 1", n, err)
		}
		if n, err := q.RevokeVirtualKey(c, "tpinnedkeyaaaaaaa"); err != nil || n != 1 {
			t.Fatalf("revoke: %d %v", n, err)
		}
		if n, err := q.CountUsablePins(c, id2); err != nil || n != 0 {
			t.Errorf("CountUsablePins with a revoked key: %d %v, want 0", n, err)
		}
		return nil
	}))

	// The gateway reads the sealed secret in the tenant's session: the two ForUse statements.
	must("gateway statements", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var id1, id2 string
		if err := tx.QueryRow(c, `SELECT id::text FROM pandokos.provider_credential WHERE label = 'main'`).Scan(&id1); err != nil {
			return err
		}
		if err := tx.QueryRow(c, `SELECT id::text FROM pandokos.provider_credential WHERE label = 'renamed'`).Scan(&id2); err != nil {
			return err
		}
		mark("GetCredentialForUse")
		got, err := q.GetCredentialForUse(c, id1)
		if err != nil || got.ID != id1 || got.Provider != "openai" || got.Label != "main" || got.State != "active" || got.KeyVersion != 1 ||
			!reflect.DeepEqual(got.Secret, sealed1) || got.Fingerprint != "0123456789abcdef0123456789abcdef" {
			t.Errorf("GetCredentialForUse: %+v %v (the sealed bytes come back unchanged)", got, err)
		}
		if _, err := q.GetCredentialForUse(c, "0192f0f0-0000-7000-8000-000000000bad"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetCredentialForUse of a credential that does not exist: %v", err)
		}
		mark("GetDefaultCredentialForUse")
		def, err := q.GetDefaultCredentialForUse(c, "openai")
		if err != nil || def.ID != id2 || def.Label != "renamed" || !reflect.DeepEqual(def.Secret, sealed2) {
			t.Errorf("GetDefaultCredentialForUse: %+v %v, want the credential that was made the default", def, err)
		}
		if _, err := q.GetDefaultCredentialForUse(c, "no-such-provider"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetDefaultCredentialForUse of a provider with no credential: %v", err)
		}
		if dk, err := q.GetDataKey(c, 1); err != nil || !reflect.DeepEqual(dk.WrappedKey, wrapped) {
			t.Errorf("the gateway reads the data key: %v", err)
		}
		return nil
	}))

	// The last statement of the list: the credential goes, and with it its sealed secret.
	must("delete", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var victim string
		if err := tx.QueryRow(c, `SELECT id::text FROM pandokos.provider_credential_metadata WHERE provider = 'anthropic'`).Scan(&victim); err != nil {
			return err
		}
		mark("DeleteCredential")
		if n, err := q.DeleteCredential(c, victim); err != nil || n != 1 {
			t.Errorf("DeleteCredential: %d %v", n, err)
		}
		if n, err := q.DeleteCredential(c, victim); err != nil || n != 0 {
			t.Errorf("a second DeleteCredential: %d %v", n, err)
		}
		return nil
	}))
}
