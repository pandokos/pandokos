//go:build drill

package drill

import (
	"slices"
	"testing"
	"time"
)

// TestAfterRestore is T-1139: the rows 5.a to 5.e of sprint.md 5.4, each sentence of the guide's
// section 5 as an assertion.
func TestAfterRestore(t *testing.T) {
	t.Parallel()
	w, _, b2 := logicalWorld(t, Options{GracePeriod: "0s"})
	r := w.Restore(RestoreSpec{Backup: b2, Keys: K1})
	c, d := w.Tenants[TenantC], w.Tenants[TenantD]

	t.Run("5.a: request again the deletions completed after the backup", func(t *testing.T) {
		defer w.Use(t)()
		w.Step(Step5a, func() {
			// The restored backup brings tenant c back, and its credential opens.
			if status, seen := r.Call(TenantC); status != 200 || seen != Fingerprint(c.Credential) {
				t.Errorf("tenant c after the restore: %d, credential %q: a restored backup brings a deleted tenant back", status, seen)
			}
			// The list of completed deletions kept at X, against the restored one.
			if !slices.Equal(w.Out.CompletedDeletion, []string{c.ID}) {
				t.Fatalf("the list kept outside at X is %v, want only tenant c", w.Out.CompletedDeletion)
			}
			if got := r.ReplayDeletion(); !slices.Equal(got, []string{c.ID}) {
				t.Fatalf("the difference between the list kept outside and the restored one is %v, want tenant c", got)
			}
			if !r.WaitDeleted(TenantC, 2*time.Minute) {
				t.Fatal("the deletion of tenant c did not complete again")
			}
			if n := r.DataKeyRows(TenantC); n != 0 {
				t.Errorf("tenant c has %d data keys after its deletion completed again", n)
			}
			if !r.DeletionCompleted(TenantC) {
				t.Error("no completion record exists again for tenant c")
			}
		})
	})

	t.Run("5.b: the platform's events list the deletions too", func(t *testing.T) {
		defer w.Use(t)()
		w.Step(Step5b, func() {
			pulled := w.Out.PulledDeletions
			if !slices.Equal(pulled, w.Out.CompletedDeletion) {
				t.Errorf("the tenant.deleted events pulled before X name %v, the list of completed deletions %v: they must be equal", pulled, w.Out.CompletedDeletion)
			}
			if got := r.PullDeletionEvents(); !slices.Equal(got, pulled) {
				t.Errorf("after the replay the platform's events name %v, want the same tenants as before X (%v)", got, pulled)
			}
		})
	})

	t.Run("5.c: the usage and the audit rows after the backup are missing", func(t *testing.T) {
		defer w.Use(t)()
		w.Step(Step5c, func() {
			for _, name := range worldTenants {
				if got, want := r.LedgerRows(name), b2.Ledger[name]; name != TenantC && got != want {
					t.Errorf("tenant %s: the restored ledger has %d rows, the backup %d", name, got, want)
				}
			}
			// The head kept at X is reported as a mismatch; the head kept at B2 verifies.
			atX := w.Out.Heads[X]
			if code, out := r.AuditVerify(TenantA, atX.Tenants[TenantA]); code == 0 {
				t.Errorf("the head kept at X verifies against the restored log: the rows after the backup were not lost?\n%s", out)
			}
			if code, out := r.AuditVerify(TenantA, b2.Heads.Tenants[TenantA]); code != 0 {
				t.Errorf("the head kept at B2 does not verify: exit %d\n%s", code, out)
			}
			// The budget consumed after B2 is available again.
			a := w.Tenants[TenantA]
			if admitted := r.AdmittedBeforeRefusal(TenantA); admitted != a.Quota-int(r.LedgerRows(TenantA)) {
				t.Errorf("tenant a: the quota admits %d more calls, the restored ledger says %d", admitted, a.Quota-int(r.LedgerRows(TenantA)))
			}
		})
	})

	t.Run("5.d: the keys revoked since the backup are valid again", func(t *testing.T) {
		defer w.Use(t)()
		w.Step(Step5d, func() {
			if status, seen := r.Call(TenantD); status != 200 || seen != Fingerprint(d.Credential) {
				t.Errorf("tenant d's key, revoked at DEL, answers %d (credential %q) after the restore, want 200: the record lists it under what the operator must redo", status, seen)
			}
		})
	})

	t.Run("5.e: a deletion that was running continues", func(t *testing.T) {
		// A case whose backup is taken during the grace period: after the restore the deletion completes
		// without a new request.
		v := NewWorld(t, Options{GracePeriod: "1m"})
		v.RequestDeletion(TenantC)
		during := v.BackupLogical(B2)
		v.Disaster()
		rv := v.Restore(RestoreSpec{Backup: during, Keys: K1})
		v.Step(Step5e, func() {
			if !rv.WaitDeleted(TenantC, 4*time.Minute) {
				t.Error("the deletion that was running at the backup did not complete after the restore without a new request")
			}
		})
		v.Finish("AR5e")
	})
	w.Finish("AR")
}

// TestEventsAfterRestore is T-1143, row 5.f (G-7), written to observe first: what a destination and
// a puller see after a restore. The expected behaviours below are the ones the corrected guide will
// state; what the first run showed is kept for the record, and O-3 decides whether anything is built:
// a destination's cursor is the backup's, so the events between the cursor and B2 are delivered again
// with identifiers the receiver has already seen (at-least-once); new events take sequence numbers
// that were already issued before X; and a puller that kept a cursor past the restored head is
// answered, not failed.
func TestEventsAfterRestore(t *testing.T) {
	t.Parallel()
	w, _, b2 := logicalWorld(t, Options{})
	a := w.Tenants[TenantA]
	seenBefore := append([]string(nil), w.Receiver.IDs()...)
	w.Receiver.SetSecrets(a.Secret)

	r := w.Restore(RestoreSpec{Backup: b2, Keys: K1})
	w.Step(Step5f, func() {
		restoredHead := r.EventHead(TenantA)
		r.EmitEvent(TenantA)

		// Redelivery from the backup's cursor, with identifiers already seen.
		if len(seenBefore) == 0 {
			t.Fatal("the receiver saw no event before X: the case has nothing to redeliver")
		}
		deadline := time.Now().Add(60 * time.Second)
		for w.Receiver.Duplicates() == 0 && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if w.Receiver.Duplicates() == 0 {
			t.Error("no event was delivered again after the restore: the cursor of a destination is the backup's, so events between it and B2 are redelivered")
		}
		// New events take sequence numbers that were already issued before X.
		if issuedBeforeX := w.Out.EventHead(TenantA); r.EventHead(TenantA) > issuedBeforeX {
			t.Errorf("tenant a's log is at %d after the restore and %d was issued before X", r.EventHead(TenantA), issuedBeforeX)
		}
		if restoredHead > w.Out.EventHead(TenantA) {
			t.Errorf("the restored head %d is past what was issued before X (%d)", restoredHead, w.Out.EventHead(TenantA))
		}
		// A puller that kept a cursor past the restored head is answered (not failed with a 5xx).
		status, body := r.EventExport(TenantA, w.Out.EventHead(TenantA)+10)
		if status >= 500 || status == 0 {
			t.Errorf("event export from a cursor past the restored head answers %d: %s", status, body)
		}
		t.Logf("event export from a cursor past the head answers %d (kept for the record)", status)
	})
	w.Finish("EV")
}
