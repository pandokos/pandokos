package migrations_test

import (
	"testing"

	"github.com/pandokos/pandokos/internal/store/migrate"
	"github.com/pandokos/pandokos/internal/store/migrations"
)

func embeddedMigrations(t *testing.T) []migrate.Migration {
	t.Helper()
	ms, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// migrationSQL returns the SQL of the migration with the version.
func migrationSQL(t *testing.T, version int) string {
	t.Helper()
	for _, m := range embeddedMigrations(t) {
		if m.Version == version {
			return m.SQL
		}
	}
	t.Fatalf("no migration %d", version)
	return ""
}
