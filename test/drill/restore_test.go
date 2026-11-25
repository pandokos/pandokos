//go:build drill

package drill

import (
	"strings"
	"testing"
)

// logicalWorld is a world taken through B1, ROT, B2, DEL and the disaster X, with the logical backup
// of B2 returned. The heads and the lists kept outside are those of the guide's section 1.
func logicalWorld(t *testing.T, o Options) (*World, *Backup, *Backup) {
	t.Helper()
	w := NewWorld(t, o)
	b1 := w.BackupLogical(B1)
	w.Rotate()
	b2 := w.BackupLogical(B2)
	w.DeleteTenant(TenantC)
	w.Disaster()
	return w, b1, b2
}

// TestRestoreLogical is T-1137, case L1 and the exit criterion: the guide's steps 4.1 to 4.7 on an
// empty server from the dump of B2 and its key; the seven assertions of 5.3 (the restored credentials
// open); the schema-only dumps equal; G-1 in both forms.
func TestRestoreLogical(t *testing.T) {
	t.Parallel()
	w, _, b2 := logicalWorld(t, Options{})
	r := w.Restore(RestoreSpec{Backup: b2, Keys: K1})
	assertRestored(t, r, b2, "unchanged", worldTenants...)

	if got := r.SchemaDump(); got != b2.Schema {
		t.Errorf("the schema-only dump of the restored database differs from the source's (objects, owners, grants, policies, triggers)")
	}
	if code, out := r.MigrateStatus(); code != 0 {
		t.Errorf("migrate status of the restored database: exit %d\n%s", code, out)
	}
	for _, step := range []Step{Step41, Step42, Step43, Step44, Step45, Step46, Step47} {
		var seen bool
		for _, s := range w.Timer.Steps() {
			seen = seen || s.Step == step
		}
		if !seen {
			t.Errorf("the case did not run the guide's step %s", step)
		}
	}
	w.Finish("L1")
}

// TestRestorePhysical is T-1138, cases P1 and P2: the base backup with WAL to the end of the archive
// (P1, which holds DEL) and to the restore point of B1 (P2); the same assertions; the roles and the
// passwords of the login users come with it (G-3: step 4.1 is for a dump); a server of another major
// refuses the base backup with PostgreSQL's own message.
func TestRestorePhysical(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, Options{})
	b1 := w.BackupPhysical(B1)
	w.Rotate()
	b2 := w.BackupPhysical(B2)
	w.DeleteTenant(TenantC)
	w.Disaster()

	t.Run("P1: to the end of the archive", func(t *testing.T) {
		r := w.Restore(RestoreSpec{Backup: b2, Physical: true, Keys: K1})
		// The archive holds DEL: tenant c is deleted and tenant d's key revoked.
		assertRestored(t, r, b2, "unchanged", TenantA, TenantB)
		if got := r.Roles(); !containsAll(got, "pandokos_owner", "pandokos_gateway", "pandokos_admin", "pandokos_worker") {
			t.Errorf("the roles of the physical restore are %v: a base backup carries the roles and the passwords of the login users (G-3)", got)
		}
		w.Finish("P1")
	})

	t.Run("P2: to the restore point of B1", func(t *testing.T) {
		r := w.Restore(RestoreSpec{Backup: b1, Physical: true, Until: b1.RestorePoint, Keys: K2, Reading: "current"})
		assertRestored(t, r, b1, "", worldTenants...)
		w.Finish("P2")
	})

	t.Run("a server of another major refuses", func(t *testing.T) {
		_, said, err := w.TryRestore(RestoreSpec{Backup: b2, Physical: true, On: TargetNext, Keys: K1})
		if err == nil {
			t.Fatal("a base backup of major 16 was accepted by a server of the later major")
		}
		if !strings.Contains(said, "incompatible") && !strings.Contains(said, "not compatible") {
			t.Errorf("the refusal is not PostgreSQL's own (the guide says so):\n%s", said)
		}
	})
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// TestRestoreOnLaterMajor is T-1144 (the leg of sprint 6.2's R-6): the dump of B2 restored into the
// later major. First check, independent of Pandokos: for each audit table, a digest over seq and the
// text of every jsonb column is equal on source and target (the canonical encoding of the chain
// depends on that text). Second check: the binary runs against the target and both chains verify; if it
// does not start on that major for a reason that is not the restore, the second check is recorded as
// not run with its reason and the first still stands.
func TestRestoreOnLaterMajor(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, Options{})
	w.BackupLogical(B1)
	w.Rotate()
	b2 := w.BackupLogical(B2)
	source := w.Digests()
	w.Disaster()

	r := w.Restore(RestoreSpec{Backup: b2, Keys: K1, On: TargetNext})
	got := r.Digests()
	if len(source) == 0 {
		t.Fatal("no audit table was digested")
	}
	for table, want := range source {
		if got[table] != want {
			t.Errorf("the text of the jsonb columns of %s differs between PostgreSQL 16 and the later major: the canonical encoding of the chain depends on it", table)
		}
	}
	if r.NotRun != "" {
		w.Case.NotRun = r.NotRun
		t.Logf("the second check was not run: %s", r.NotRun)
		w.Finish("N1")
		return
	}
	if code, out := r.AuditVerify("", b2.Heads.Platform); code != 0 {
		t.Errorf("the platform's chain on the later major: exit %d\n%s", code, out)
	}
	for _, name := range worldTenants {
		if code, out := r.AuditVerify(name, b2.Heads.Tenants[name]); code != 0 {
			t.Errorf("the chain of tenant %s on the later major: exit %d\n%s", name, code, out)
		}
	}
	w.Finish("N1")
}
