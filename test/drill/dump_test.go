//go:build drill

package drill

import (
	"strings"
	"testing"
)

// TestDump is T-1136: a dump made by a role that row-level security binds fails and names it (pg_dump
// runs with row_security off and refuses rather than writing a partial dump); a superuser's
// succeeds; and the dump, the base backup and the WAL archive contain no canary credential, no
// signing secret and no byte sequence of the key file.
func TestDump(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, Options{})

	out, err := w.DumpAs("pandokos_owner")
	if err == nil {
		t.Fatal("a dump by the owner role, which row-level security binds, succeeded: it would be a partial dump")
	}
	if !strings.Contains(out, "row-level security") && !strings.Contains(out, "row_security") {
		t.Errorf("the refusal does not name row-level security:\n%s", out)
	}

	w.Step(Step1a, func() { w.BackupLogical(B1) })
	w.Step(Step1b, func() { w.BackupPhysical(B1) })
	w.Rotate()
	w.BackupLogical(B2) // a second dump, after a rotation: the key file changed
	files, size := scanBackups(t, w)
	if files < 3 || size == 0 {
		t.Errorf("%d files and %d bytes were searched: the dump, the base backup and the archive were expected", files, size)
	}
	w.Finish("D1")
}
