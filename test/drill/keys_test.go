//go:build drill

package drill

import (
	"strings"
	"testing"
	"time"
)

// recoverAndServe is steps 6.3 and 6.4 for the tenants the loss touched: the recovery script for the
// version, a new key, every touched tenant's credentials revoked with the script's reason; each tenant
// enters a credential again and is served. Every tenant of a world is touched: a backup older than the
// rotation holds no data key under a version that the case keeps. That the tenants whose data keys are
// under a version that is kept are not touched is proved by TestLostKeyRecovery (internal/vault).
func recoverAndServe(t *testing.T, r *Restored, lost []string, affected []string) {
	t.Helper()
	for _, version := range lost {
		out := r.RecoverLostKey(version)
		if !strings.Contains(out, "revoked") {
			t.Errorf("the recovery script for %s printed no count of the credentials it revoked:\n%s", version, out)
		}
	}
	for _, name := range affected {
		if state := r.CredentialState(name); state != "revoked" {
			t.Errorf("tenant %s: the credential is %q after the recovery, want revoked (the script's reason)", name, state)
		}
		if status, _ := r.Call(name); status == 200 {
			t.Errorf("tenant %s is served with a credential that the lost key sealed", name)
		}
		fresh := r.EnterCredential(name)
		if status, seen := r.Call(name); status != 200 || seen != Fingerprint(fresh) {
			t.Errorf("tenant %s after entering a credential again: %d (credential %q), want 200 and the new one", name, status, seen)
		}
	}
}

// TestKeyCasesFile is T-1140: the four cases of the key of sprint.md 5.5 with a key file, one
// subtest each; row 2 is executed in both readings of the guide (G-5); in K3 and K4 the tenants
// whose data keys were under the lost version get revoked credentials with the script's reason, enter
// a credential again and are served, and the other tenants (K3) never stopped.
func TestKeyCasesFile(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*World, *Backup, *Backup) {
		t.Helper()
		return logicalWorld(t, Options{})
	}

	t.Run("K1: the backup and its key", func(t *testing.T) {
		t.Parallel()
		w, _, b2 := setup(t)
		r := w.Restore(RestoreSpec{Backup: b2, Keys: K1})
		w.Step(Step61, func() { assertRestored(t, r, b2, "unchanged", worldTenants...) })
		w.Finish("K1")
	})

	t.Run("K2: a backup older than a rotation, the old key kept", func(t *testing.T) {
		t.Parallel()
		w, b1, b2 := setup(t)
		w.Step(Step62, func() {
			// The reading in which the current key is key_file and the old one is in unwrap_key_files:
			// serve activates the version of the key that wraps (the current one), the worker re-wraps
			// again, every credential opens (G-5).
			current := w.Restore(RestoreSpec{Backup: b1, Keys: K2, Reading: "current"})
			assertRestored(t, current, b1, "activated", worldTenants...)
			// The other reading: the old key as key_file with the backup it belongs to starts as it was.
			old := w.Restore(RestoreSpec{Backup: b1, Keys: K2, Reading: "old"})
			assertRestored(t, old, b1, "unchanged", worldTenants...)
			// The refusal the guide quotes: the old key as key_file with a backup newer than the rotation
			// names both versions.
			stale := w.Restore(RestoreSpec{Backup: b2, Keys: K2, Reading: "stale"})
			if stale.Proc != nil {
				t.Error("serve started with the old key as key_file on a backup that is newer than the rotation")
			} else if !strings.Contains(stale.ServeLog, b1.KEKVersion) || !strings.Contains(stale.ServeLog, b2.KEKVersion) || !strings.Contains(stale.ServeLog, "was replaced by") {
				t.Errorf("serve refused the old key as key_file without the message of the guide (the version %s was replaced by %s):\n%s", b1.KEKVersion, b2.KEKVersion, stale.ServeLog)
			}
		})
		w.Finish("K2")
	})

	t.Run("K3: the same backup, without the old key", func(t *testing.T) {
		t.Parallel()
		w, b1, _ := setup(t)
		affected := worldTenants
		r := w.Restore(RestoreSpec{Backup: b1, Keys: K3})
		w.Step(Step63, func() { recoverAndServe(t, r, []string{b1.KEKVersion}, affected) })
		w.Finish("K3")
	})

	t.Run("K4: the key lost entirely", func(t *testing.T) {
		t.Parallel()
		w, b1, b2 := setup(t)
		r := w.Restore(RestoreSpec{Backup: b2, Keys: K4})
		w.Step(Step64, func() {
			// The same for every version: the recovery for each, a new key, every tenant enters its
			// credentials again; nothing else is lost.
			recoverAndServe(t, r, []string{b1.KEKVersion, b2.KEKVersion}, worldTenants)
		})
		w.Finish("K4")
	})
}

// TestKeyCasesService is T-1141: the cases S1 to S4 of sprint.md 5.5 with OpenBao: S1 restores the
// service from its snapshot into an empty server; S3 trims; S4 in its three forms; kek check reports
// no weak setting and allow_exportable_key is never set; the recovery script takes a transit-…
// identifier.
func TestKeyCasesService(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*World, *Backup, *Backup) {
		t.Helper()
		w := NewWorld(t, Options{Service: true})
		w.ServiceSnapshot("before-rotation")
		b1 := w.BackupLogical(B1)
		w.Rotate()
		w.ServiceSnapshot("after-rotation") // after every rotation and before Pandokos is restarted (D-7)
		b2 := w.BackupLogical(B2)
		w.DeleteTenant(TenantC)
		w.Disaster()
		return w, b1, b2
	}
	notWeak := func(t *testing.T, r *Restored) {
		t.Helper()
		code, out := r.KekCheck()
		if code != 0 || strings.Contains(out, "exportable") {
			t.Errorf("kek check reports a weak setting or fails (exit %d): the key of the drill is not exportable and allow_exportable_key is never set\n%s", code, out)
		}
	}

	t.Run("S1: the service restored from its own snapshot", func(t *testing.T) {
		t.Parallel()
		w, _, b2 := setup(t)
		r := w.Restore(RestoreSpec{Backup: b2, Keys: S1})
		w.Step(Step61s, func() { assertRestored(t, r, b2, "unchanged", worldTenants...) })
		notWeak(t, r)
		w.Finish("S1")
	})

	t.Run("S2: the old version still in range", func(t *testing.T) {
		t.Parallel()
		w, b1, _ := setup(t)
		r := w.Restore(RestoreSpec{Backup: b1, Keys: S2})
		w.Step(Step62s, func() { assertRestored(t, r, b1, "activated", worldTenants...) })
		w.Finish("S2")
	})

	t.Run("S3: the old version trimmed", func(t *testing.T) {
		t.Parallel()
		w, b1, _ := setup(t)
		if status := w.ServiceTrimAsPandokos(2); status != 403 {
			t.Errorf("the policy of Pandokos trims a version: %d, want 403 (it cannot)", status)
		}
		w.ServiceTrim(2)
		affected := worldTenants
		r := w.Restore(RestoreSpec{Backup: b1, Keys: S3})
		w.Step(Step63s, func() {
			lost := b1.KEKVersion
			if !strings.HasPrefix(lost, "transit-") {
				t.Errorf("the identifier of a key service's version is %q, want transit-…", lost)
			}
			recoverAndServe(t, r, []string{lost}, affected)
		})
		notWeak(t, r)
		w.Finish("S3")
	})

	t.Run("S4: the key lost, in its three forms", func(t *testing.T) {
		t.Parallel()
		for _, form := range []struct {
			id, name string
			do       func(w *World)
			keys     KeyCase
		}{
			{"a", "the key deleted in the service", func(w *World) { w.ServiceDeleteKey() }, S4a},
			{"b", "the service lost with no snapshot", func(w *World) { w.ServiceLose() }, S4b},
			{"c", "a snapshot older than a rotation with a database backup taken after the re-wrap", func(*World) {}, S4c},
		} {
			t.Run(form.name, func(t *testing.T) {
				t.Parallel()
				w, b1, b2 := setup(t)
				form.do(w)
				r := w.Restore(RestoreSpec{Backup: b2, Keys: form.keys})
				w.Step(Step64s, func() { recoverAndServe(t, r, []string{b1.KEKVersion, b2.KEKVersion}, worldTenants) })
				w.Finish("S4" + form.id)
			})
		}
	})
}

// TestRecoveryAndDestinations is T-1142, F-1, written to observe first: without the change, what the
// relay does with a destination after the recovery script is recorded; with it, the destination whose
// signing secret was sealed under the lost version is disabled with its cursor kept (a destination under a
// version that is kept is untouched: TestLostKeyRecovery of internal/vault proves that, no destination of a
// drill world has one), and after rotate-secret and enabling it the events kept by the retention
// arrive signed with the new secret; the script run twice changes nothing more; a version that does
// not exist changes nothing.
func TestRecoveryAndDestinations(t *testing.T) {
	t.Parallel()
	w, b1, _ := logicalWorld(t, Options{})
	affected := worldTenants
	r := w.Restore(RestoreSpec{Backup: b1, Keys: K3})

	out := r.RecoverLostKey(b1.KEKVersion)
	if !strings.Contains(out, "destinations") {
		t.Errorf("the script prints no count of the destinations it disabled (F-1):\n%s", out)
	}
	for _, name := range affected {
		tn := w.Tenants[name]
		code, text, _ := r.CLI(name, "event-destination", "get", "--tenant", tn.ID, tn.Destination, "-o", "json")
		if code != 0 || !strings.Contains(text, `"enabled": false`) {
			t.Fatalf("the destination of tenant %s is not disabled after the script (F-1):\n%s", name, text)
		}
		before := text
		// The script run twice changes nothing more; a version that does not exist changes nothing.
		again := r.RecoverLostKey(b1.KEKVersion)
		unknown := r.RecoverLostKey("local-0000000000000000")
		if _, after, _ := r.CLI(name, "event-destination", "get", "--tenant", tn.ID, tn.Destination, "-o", "json"); after != before {
			t.Errorf("a second run of the script, or a run for an unknown version, changed the destination:\n%s\n%s\n%s%s", before, after, again, unknown)
		}
		// The owner enters a credential again, rotates the signing secret, gives it to the receiver and
		// enables the destination: the events kept arrive signed with the new secret.
		r.EnterCredential(name)
		secret := r.RotateDestinationSecret(name)
		w.Receiver.SetSecrets(secret)
		// The count is taken before the destination is enabled: the relay passes every 100 ms and may deliver
		// the events kept before the command has returned.
		got := len(w.Receiver.IDs())
		if code, text, errs := r.CLI(name, "event-destination", "update", "--tenant", tn.ID, "--enabled", tn.Destination); code != 0 {
			t.Fatalf("enabling the destination: exit %d\n%s%s", code, text, errs)
		}
		deadline := time.Now().Add(90 * time.Second)
		for len(w.Receiver.IDs()) == got && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if len(w.Receiver.IDs()) == got {
			t.Errorf("tenant %s: no event arrived after rotate-secret and enabling the destination", name)
		}
	}
	w.Finish("FD")
}
