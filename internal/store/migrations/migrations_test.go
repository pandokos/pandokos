package migrations

import (
	"os"
	"strings"
	"testing"
)

// TestEmbeddedMatchesSumFile is T-106.
func TestEmbeddedMatchesSumFile(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].Name != "init" || ms[0].Version != 1 {
		t.Fatalf("the first migration must be 0001_init: %+v", ms)
	}
	b, err := os.ReadFile("migrations.sum")
	if err != nil {
		t.Fatal(err)
	}
	type line struct{ name, sum string }
	var lines []line
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Fields(l)
		if len(f) != 2 || len(f[1]) != 64 {
			t.Fatalf("malformed line in migrations.sum: %q", l)
		}
		lines = append(lines, line{f[0], f[1]})
	}
	if len(lines) != len(ms) {
		t.Fatalf("migrations.sum has %d lines, there are %d migrations", len(lines), len(ms))
	}
	for i, m := range ms {
		if lines[i].name != m.ID()+".sql" {
			t.Errorf("line %d names %s, want %s", i+1, lines[i].name, m.ID()+".sql")
		}
		if lines[i].sum != m.Checksum {
			t.Errorf("%s: checksum in migrations.sum is %s, the file has %s", lines[i].name, lines[i].sum, m.Checksum)
		}
	}
}
