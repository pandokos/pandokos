//go:build upgrade

package upgrade

import (
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/instance"
)

// TestPreviousBinary is T-1129: the binary built from test/upgrade/PREVIOUS reports that version and
// that commit, and its migrate status on an empty database needs the schema version it is derived to
// need; this tree's binary needs the number of migrations of this tree. The migrations an upgrade
// crosses are the ones after the previous version up to this tree's (none for a patch release).
func TestPreviousBinary(t *testing.T) {
	t.Parallel()
	pin := ReadPin(t)
	if pin.Version == "" || pin.Commit == "" || pin.Image == "" {
		t.Fatalf("test/upgrade/PREVIOUS is not three lines: %+v", pin)
	}
	old := instance.Previous(t)
	code, out, errs := old.Run(t, nil, "version")
	if code != 0 {
		t.Fatalf("version: exit %d\n%s%s", code, out, errs)
	}
	if want := "pandokos " + strings.TrimPrefix(pin.Version, "v") + " (commit " + pin.Commit + ","; !strings.HasPrefix(out, want) {
		t.Errorf("the previous binary says %q, want it to start with %q", strings.TrimSpace(out), want)
	}

	c := schemaCrossing(t) // the previous version is what the previous binary says; this tree's, the migration files
	t.Logf("the upgrade crosses %s", c)
	if got := neededVersion(t, instance.Build(t)); got != c.This {
		t.Errorf("this tree's binary needs schema version %d, its migration files make %d", got, c.This)
	}
	if want := c.This - c.Previous; len(c.Crossed) != want {
		t.Errorf("%d migrations crossed (%v), want %d", len(c.Crossed), c.Crossed, want)
	}
	if !c.None() && c.Previous != migrationVersion(t, c.Crossed[0])-1 {
		t.Errorf("the first migration crossed is %s but the previous release needs schema %d", c.Crossed[0], c.Previous)
	}
}
