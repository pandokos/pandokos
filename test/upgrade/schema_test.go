//go:build upgrade

package upgrade

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// crossing is the schema step of the upgrade under test: from the schema the previous release needs to
// the schema this tree needs, and the migrations in between. Nothing here is a constant of one
// release pair: with a patch release, or a minor without a migration, Crossed is empty.
type crossing struct {
	Previous int      // the schema version the previous binary needs
	This     int      // the schema version this tree needs
	Crossed  []string // the ids ("0017_events") of the migrations after Previous up to This, in order
}

// None is true when the previous release and this tree have the same schema.
func (c crossing) None() bool { return len(c.Crossed) == 0 }

// Crosses is true when the migration with this id is one of those the upgrade applies.
func (c crossing) Crosses(id string) bool { return slices.Contains(c.Crossed, id) }

// String names the step for messages.
func (c crossing) String() string {
	if c.None() {
		return "schema " + strconv.Itoa(c.Previous) + " (no migration crossed)"
	}
	return "schema " + strconv.Itoa(c.Previous) + " to " + strconv.Itoa(c.This) + " (" + strings.Join(c.Crossed, ", ") + ")"
}

var reNeeds = regexp.MustCompile(`this binary needs version (\d+)`)

// treeMigrations is the ids of the migrations of this tree, in order, read from the migration files
// (0017_events.sql is the migration 0017_events, version 17).
func treeMigrations(tb testing.TB) []string {
	tb.Helper()
	entries, err := os.ReadDir(filepath.Join(testutil.RepoRoot(tb), "internal", "store", "migrations", "sql"))
	if err != nil {
		tb.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".sql"); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// migrationVersion is the version of a migration id: its leading number.
func migrationVersion(tb testing.TB, id string) int {
	tb.Helper()
	n, _, _ := strings.Cut(id, "_")
	v, err := strconv.Atoi(n)
	if err != nil {
		tb.Fatalf("the migration %q has no version number", id)
	}
	return v
}

// neededVersion is the schema version a binary says it needs: migrate status on an empty database
// exits 1 and says "this binary needs version N".
func neededVersion(tb testing.TB, bin instance.Binary) int {
	tb.Helper()
	d := dbtest.New(tb) // empty
	code, out, errs := bin.Run(tb, d.Env(tb), "migrate", "status")
	m := reNeeds.FindStringSubmatch(out + errs)
	if code == 0 || m == nil {
		tb.Fatalf("migrate status on an empty database exits %d and does not say which version the binary needs:\n%s%s", code, out, errs)
	}
	v, _ := strconv.Atoi(m[1])
	return v
}

// schemaCrossing derives the crossing of the upgrade from the previous binary to this tree: the
// previous version from the previous binary, this version from the migration files of the tree, and the
// migrations crossed from the names of the files.
func schemaCrossing(tb testing.TB) crossing {
	tb.Helper()
	ids := treeMigrations(tb)
	if len(ids) == 0 {
		tb.Fatal("this tree has no migrations")
	}
	c := crossing{Previous: neededVersion(tb, instance.Previous(tb)), This: migrationVersion(tb, ids[len(ids)-1])}
	if c.This != len(ids) {
		tb.Fatalf("this tree has %d migrations and the newest is version %d: versions are not 1..N", len(ids), c.This)
	}
	if c.Previous > c.This {
		tb.Fatalf("the previous release needs schema %d, newer than this tree's %d", c.Previous, c.This)
	}
	for _, id := range ids {
		if migrationVersion(tb, id) > c.Previous {
			c.Crossed = append(c.Crossed, id)
		}
	}
	return c
}
