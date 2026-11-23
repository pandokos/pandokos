package admin_test

import (
	"context"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// TestKEKVersions is T-999: the list of the versions of the key-encryption key during and after a
// rotation, with what each still holds up: the backups of deleted tenants.
func TestKEKVersions(t *testing.T) {
	api := apitest.NewPeople(t, apitest.PeopleConfig{})
	plat := api.PlatformCaller()
	auditor, _ := api.PlatformAs(t, authz.RolePlatformAuditor)
	worker := api.DB.Open(t, store.RoleWorker)
	v0 := api.DB.KEKVersion(t)
	list := func(who *apitest.Caller) map[string]adminapi.KEKVersion {
		t.Helper()
		l := decode[adminapi.KEKVersionList](t, who.Do(t, "GET", "/v1/kek-versions", nil, nil), 200)
		out := map[string]adminapi.KEKVersion{}
		for _, v := range l.Items {
			out[v.ID] = v
		}
		return out
	}

	// Before: the one version the installation started with.
	got := list(plat)
	if len(got) != 1 || got[v0].State != adminapi.KEKVersionStateActive || got[v0].Provider != "local" || got[v0].ActivatedAt.IsZero() || got[v0].CreatedAt.IsZero() ||
		got[v0].RetiredAt != nil || got[v0].ErasuresPending != 0 {
		t.Fatalf("the versions at the start: %+v", got)
	}

	// A tenant is deleted under it; another keeps a data key under it.
	gone, _ := api.Tenant(t, apitest.UniqueName("kek-gone"))
	kept, _ := api.Tenant(t, apitest.UniqueName("kek-kept"))
	empty, _ := api.Tenant(t, apitest.UniqueName("kek-empty")) // made now: after the rotation this instance's key is a replaced one
	if _, err := api.DB.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, empty.ID); err != nil {
		t.Fatal(err)
	}
	api.DB.CompleteDeletion(t, parseTenant(t, gone.ID))
	d := decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", "/v1/tenants/"+gone.ID+"/deletion", nil, nil), 200)
	if len(d.KekVersions) != 1 || d.KekVersions[0] != v0 || d.BackupErasure.State != adminapi.BackupErasureStatePending ||
		len(d.BackupErasure.PendingKekVersions) != 1 || d.BackupErasure.PendingKekVersions[0] != v0 {
		t.Errorf("the deletion before the rotation: %+v, want the version %s pending", d, v0)
	}
	if got := list(plat); got[v0].ErasuresPending != 1 {
		t.Errorf("erasures_pending of the version is %d, want 1", got[v0].ErasuresPending)
	}

	// The rotation: a new version is active, the old one is retiring while a data key still has it.
	v1 := vaulttest.Keys(t, 1)[0].Version
	api.DB.ActivateKEK(t, v1)
	got = list(auditor) // the auditor reads the list too (kek:read)
	if len(got) != 2 || got[v0].State != adminapi.KEKVersionStateRetiring || got[v1].State != adminapi.KEKVersionStateActive || got[v0].RetiredAt != nil ||
		got[v1].ActivatedAt.Before(got[v0].ActivatedAt) {
		t.Errorf("the versions during the rotation: %+v", got)
	}
	if got[v0].ErasuresPending != 1 || got[v1].ErasuresPending != 0 {
		t.Errorf("erasures_pending during the rotation: %d for the old version, %d for the new, want 1 and 0", got[v0].ErasuresPending, got[v1].ErasuresPending)
	}
	if retire := retireVersions(t, worker); retire != 0 {
		t.Errorf("the worker retired %d versions while the tenant that is kept has a data key under the old one", retire)
	}

	// The re-wrap of the data key that is left, then the retirement.
	if _, err := api.DB.Super(t).Exec(ctx(t), `UPDATE pandokos.tenant_data_key SET kek_version = $1 WHERE tenant_id = $2::uuid`, v1, kept.ID); err != nil {
		t.Fatal(err)
	}
	if retire := retireVersions(t, worker); retire != 1 {
		t.Fatalf("the worker retired %d versions, want 1", retire)
	}
	got = list(plat)
	if got[v0].State != adminapi.KEKVersionStateRetired || got[v0].RetiredAt == nil || got[v0].ErasuresPending != 0 || got[v1].State != adminapi.KEKVersionStateActive {
		t.Errorf("the versions after the retirement: %+v", got)
	}
	d = decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", "/v1/tenants/"+gone.ID+"/deletion", nil, nil), 200)
	if d.BackupErasure.State != adminapi.BackupErasureStateComplete || len(d.BackupErasure.PendingKekVersions) != 0 {
		t.Errorf("the deletion after the retirement: %+v, want backup_erasure complete", d.BackupErasure)
	}
	l := decode[adminapi.TenantDeletionList](t, plat.Do(t, "GET", "/v1/tenant-deletions?state=completed", nil, nil), 200)
	found := false
	for _, it := range l.Items {
		found = found || (it.TenantID == gone.ID && it.BackupErasure.State == adminapi.BackupErasureStateComplete)
	}
	if !found {
		t.Error("the list of deletions does not show backup_erasure complete")
	}
	// A deletion with no key (no data key was ever made) has nothing to wait for.
	api.DB.CompleteDeletion(t, parseTenant(t, empty.ID))
	d = decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", "/v1/tenants/"+empty.ID+"/deletion", nil, nil), 200)
	if len(d.KekVersions) != 0 || d.BackupErasure.State == adminapi.BackupErasureStatePending {
		t.Errorf("the deletion of a tenant with no data key: %+v, want no version and nothing pending", d)
	}
}

// retireVersions calls kek_retire as the worker role in platform scope, as the re-wrap job does.
func retireVersions(t *testing.T, worker *store.DB) int {
	t.Helper()
	var n int32
	err := worker.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
		var err error
		n, err = queries.New(tx).RetireKEKVersions(c)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}
