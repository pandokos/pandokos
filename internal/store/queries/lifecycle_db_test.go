package queries_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// lifecycleStatements is the part of T-323 that covers the statements of sprint 6.3: the request,
// the progress and the retention of a tenant deletion, and the activation, re-wrap and retirement of
// key-encryption key versions. Each statement runs as the role and in the scope its header names.
func lifecycleStatements(c context.Context, t *testing.T, d *dbtest.Database, other tenant.ID, called map[string]bool) {
	t.Helper()
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	admin, worker := d.Open(t, store.RoleAdmin), d.Open(t, store.RoleWorker)
	del := d.PopulatedTenant(t)
	keep := d.PopulatedTenant(t)
	old := d.KEKVersion(t)
	const next = "local-1111111111111111"

	// The deletion of a tenant: request, read, list, cancel, request again, run, retention.
	must("suspend", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).SetTenantState(c, queries.SetTenantStateParams{ID: del.String(), State: "suspended"})
		return err
	}))
	request := func() queries.InsertTenantDeletionRow {
		var row queries.InsertTenantDeletionRow
		must("InsertTenantDeletion", admin.Platform(c, func(c context.Context, tx store.Tx) error {
			var err error
			row, err = queries.New(tx).InsertTenantDeletion(c, queries.InsertTenantDeletionParams{
				TenantID: del.String(), Reason: "ünï reason", RequestedByType: "platform", RequestedByID: "pkey0123", RequestedByName: "ops",
				RequestID: "req-6-3", Grace: pgtype.Interval{Valid: true},
			})
			return err
		}))
		return row
	}
	mark("InsertTenantDeletion")
	first := request()
	if first.TenantID != del.String() || first.State != "scheduled" || first.Step != "begin" || first.Reason != "ünï reason" || first.RequestedByName != "ops" ||
		first.RequestID != "req-6-3" || first.StartedAt != nil || first.CompletedAt != nil || first.Passes != 0 || len(first.KekVersions) != 0 ||
		first.Removed != "{}" || first.Audit != "{}" || first.CompletionSeq != nil || first.NotBefore.Before(first.RequestedAt) {
		t.Errorf("InsertTenantDeletion: %+v", first)
	}
	must("a second request is refused by the primary key", func() error {
		err := admin.Platform(c, func(c context.Context, tx store.Tx) error {
			_, err := queries.New(tx).InsertTenantDeletion(c, queries.InsertTenantDeletionParams{TenantID: del.String(), Reason: "x", RequestedByType: "platform", Grace: pgtype.Interval{Valid: true}})
			return err
		})
		if err == nil {
			return context.Canceled
		}
		return nil
	}())

	mark("GetTenantDeletion")
	must("GetTenantDeletion", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		got, err := queries.New(tx).GetTenantDeletion(c, del.String())
		if err != nil || got.State != "scheduled" || got.RequestID != "req-6-3" || len(got.PendingKekVersions) != 0 {
			t.Errorf("GetTenantDeletion: %+v %v", got, err)
		}
		if _, err := queries.New(tx).GetTenantDeletion(c, keep.String()); err == nil {
			t.Error("GetTenantDeletion of a tenant with no request found a row")
		}
		return nil
	}))
	mark("ListTenantDeletions")
	must("ListTenantDeletions", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		rows, err := q.ListTenantDeletions(c, queries.ListTenantDeletionsParams{AfterID: zeroID, PageSize: 10})
		if err != nil || len(rows) != 1 || rows[0].TenantID != del.String() {
			t.Errorf("ListTenantDeletions: %+v %v", rows, err)
		}
		if rows, err = q.ListTenantDeletions(c, queries.ListTenantDeletionsParams{AfterID: zeroID, PageSize: 10, State: "running"}); err != nil || len(rows) != 0 {
			t.Errorf("ListTenantDeletions by another state: %+v %v", rows, err)
		}
		if rows, err = q.ListTenantDeletions(c, queries.ListTenantDeletionsParams{PageSize: 10, After: true, AfterAt: first.RequestedAt.Add(time.Second), AfterID: del.String()}); err != nil || len(rows) != 1 {
			t.Errorf("ListTenantDeletions before a later cursor: %+v %v", rows, err)
		}
		if rows, err = q.ListTenantDeletions(c, queries.ListTenantDeletionsParams{PageSize: 10, After: true, AfterAt: first.RequestedAt, AfterID: del.String()}); err != nil || len(rows) != 0 {
			t.Errorf("ListTenantDeletions after the cursor of the only row: %+v %v", rows, err)
		}
		return nil
	}))
	mark("DeleteScheduledTenantDeletion")
	must("DeleteScheduledTenantDeletion", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if n, err := q.DeleteScheduledTenantDeletion(c, del.String()); err != nil || n != 1 {
			t.Errorf("DeleteScheduledTenantDeletion: %d %v", n, err)
		}
		if n, err := q.DeleteScheduledTenantDeletion(c, del.String()); err != nil || n != 0 {
			t.Errorf("DeleteScheduledTenantDeletion again: %d %v", n, err)
		}
		return nil
	}))
	request()

	mark("ListOpenTenantDeletions")
	mark("CountTenantDeletions")
	must("the open deletions", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		open, err := q.ListOpenTenantDeletions(c)
		if err != nil || len(open) != 1 || open[0].TenantID != del.String() || open[0].RequestID != "req-6-3" || open[0].StartedAt != nil {
			t.Errorf("ListOpenTenantDeletions: %+v %v", open, err)
		}
		counts, err := q.CountTenantDeletions(c)
		if err != nil || !reflect.DeepEqual(counts, []queries.CountTenantDeletionsRow{{State: "scheduled", N: 1}}) {
			t.Errorf("CountTenantDeletions: %+v %v", counts, err)
		}
		return nil
	}))

	mark("TenantErasureStep")
	var steps []string
	for i := 0; i < 40; i++ {
		var step string
		must("TenantErasureStep", worker.WithTenant(c, del, func(c context.Context, tx store.Tx) error {
			var err error
			step, err = queries.New(tx).TenantErasureStep(c, 100)
			return err
		}))
		if len(steps) == 0 || steps[len(steps)-1] != step {
			steps = append(steps, step)
		}
		if step == "settling" {
			d.AgeDeletion(t, del)
		}
		if step == "done" {
			break
		}
	}
	if want := []string{"access", "secrets", "configuration", "usage", "audit", "complete", "settling", "done"}; !reflect.DeepEqual(steps, want) {
		t.Errorf("the answers of TenantErasureStep: %v, want %v", steps, want)
	}

	mark("CountPendingErasures")
	mark("ListDeletedTenantsForWorker")
	mark("DeletedTenantRetention")
	must("after the deletion", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if n, err := q.CountPendingErasures(c); err != nil || n != 1 {
			t.Errorf("CountPendingErasures: %d %v, want 1 (the version that wrapped the key is not retired)", n, err)
		}
		ids, err := q.ListDeletedTenantsForWorker(c)
		if err != nil || !reflect.DeepEqual(ids, []string{del.String()}) {
			t.Errorf("ListDeletedTenantsForWorker: %v %v", ids, err)
		}
		return nil
	}))
	must("DeletedTenantRetention", worker.WithTenant(c, del, func(c context.Context, tx store.Tx) error {
		r, err := queries.New(tx).DeletedTenantRetention(c, queries.DeletedTenantRetentionParams{})
		if err != nil || r.UsageRows == nil || r.AuditRows == nil || *r.UsageRows != 0 || *r.AuditRows != 0 {
			t.Errorf("DeletedTenantRetention with both keys 0: %+v %v, want two zero counts", r, err)
		}
		return nil
	}))
	must("GetTenantDeletion after", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		got, err := queries.New(tx).GetTenantDeletion(c, del.String())
		if err != nil || got.State != "completed" || got.Step != "done" || got.CompletedAt == nil || got.CompletionSeq == nil ||
			!reflect.DeepEqual(got.KekVersions, []string{old}) || !reflect.DeepEqual(got.PendingKekVersions, []string{old}) {
			t.Errorf("GetTenantDeletion after the deletion: %+v %v", got, err)
		}
		return nil
	}))

	// The key versions: activate a new one, re-wrap, retire.
	mark("ActivateKEKVersion")
	must("ActivateKEKVersion", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		for _, want := range []string{"activated", "unchanged"} {
			if got, err := q.ActivateKEKVersion(c, queries.ActivateKEKVersionParams{ID: next, Provider: "local"}); err != nil || got != want {
				t.Errorf("ActivateKEKVersion: %q %v, want %q", got, err, want)
			}
		}
		return nil
	}))
	mark("ListKEKVersionsWithErasures")
	listWith := func() map[string]queries.ListKEKVersionsWithErasuresRow {
		out := map[string]queries.ListKEKVersionsWithErasuresRow{}
		must("ListKEKVersionsWithErasures", admin.Platform(c, func(c context.Context, tx store.Tx) error {
			rows, err := queries.New(tx).ListKEKVersionsWithErasures(c)
			for _, r := range rows {
				out[r.ID] = r
			}
			return err
		}))
		return out
	}
	if v := listWith(); len(v) != 2 || v[old].State != "retiring" || v[old].ErasuresPending != 1 || v[next].State != "active" || v[next].ErasuresPending != 0 {
		t.Errorf("ListKEKVersionsWithErasures after the activation: %+v", v)
	}

	mark("RetireKEKVersions")
	retire := func() int32 {
		var n int32
		must("RetireKEKVersions", worker.Platform(c, func(c context.Context, tx store.Tx) error {
			var err error
			n, err = queries.New(tx).RetireKEKVersions(c)
			return err
		}))
		return n
	}
	if n := retire(); n != 0 {
		t.Errorf("RetireKEKVersions retired %d versions while data keys of other tenants still have the old one", n)
	}
	mark("ListTenantsForRewrap")
	must("ListTenantsForRewrap", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		ids, err := queries.New(tx).ListTenantsForRewrap(c)
		has := func(id tenant.ID) bool {
			for _, x := range ids {
				if x == id.String() {
					return true
				}
			}
			return false
		}
		if err != nil || !has(keep) || has(del) {
			t.Errorf("ListTenantsForRewrap: %v %v, want the tenant that is kept and not the deleted one", ids, err)
		}
		return nil
	}))
	mark("LockDataKeysForRewrap")
	mark("RewrapDataKey")
	for _, tid := range []tenant.ID{keep, other} { // every tenant that has a data key under the old version
		must("re-wrap", worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			rows, err := q.LockDataKeysForRewrap(c, next)
			if err != nil || len(rows) != 1 || rows[0].KekVersion != old || rows[0].Version != 1 || len(rows[0].WrappedKey) == 0 {
				t.Fatalf("LockDataKeysForRewrap: %+v %v", rows, err)
			}
			args := queries.RewrapDataKeyParams{KekVersion: next, WrappedKey: make([]byte, 61), Version: rows[0].Version, OldKekVersion: old}
			if n, err := q.RewrapDataKey(c, args); err != nil || n != 1 {
				t.Errorf("RewrapDataKey: %d %v", n, err)
			}
			if n, err := q.RewrapDataKey(c, args); err != nil || n != 0 {
				t.Errorf("RewrapDataKey of a row that no longer has the old version: %d %v, want 0", n, err)
			}
			if rows, err := q.LockDataKeysForRewrap(c, next); err != nil || len(rows) != 0 {
				t.Errorf("LockDataKeysForRewrap after the re-wrap: %+v %v", rows, err)
			}
			return nil
		}))
	}
	if n := retire(); n != 1 {
		t.Errorf("RetireKEKVersions retired %d versions after the last data key was re-wrapped, want 1", n)
	}
	if v := listWith(); v[old].State != "retired" || v[old].RetiredAt == nil || v[old].ErasuresPending != 0 || v[next].State != "active" {
		t.Errorf("ListKEKVersionsWithErasures after the retirement: %+v", v)
	}
	must("no erasure is pending", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		if n, err := queries.New(tx).CountPendingErasures(c); err != nil || n != 0 {
			t.Errorf("CountPendingErasures after the retirement: %d %v, want 0", n, err)
		}
		return nil
	}))
}
