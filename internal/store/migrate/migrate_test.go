package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func fsOf(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

// TestLoad is T-104.
func TestLoad(t *testing.T) {
	t.Run("valid set", func(t *testing.T) {
		fsys := fsOf(map[string]string{
			"sql/0002_more.sql": "SELECT 2;\n",
			"sql/0001_init.sql": "SELECT 1;\n",
			"sql/README.md":     "ignored",
		})
		ms, err := Load(fsys, "sql")
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) != 2 {
			t.Fatalf("got %d migrations", len(ms))
		}
		if ms[0].Version != 1 || ms[0].Name != "init" || ms[0].ID() != "0001_init" || ms[0].SQL != "SELECT 1;\n" {
			t.Errorf("first migration: %+v", ms[0])
		}
		if ms[1].Version != 2 || ms[1].Name != "more" || ms[1].ID() != "0002_more" {
			t.Errorf("second migration: %+v", ms[1])
		}
		sum := sha256.Sum256([]byte("SELECT 1;\n"))
		if ms[0].Checksum != hex.EncodeToString(sum[:]) {
			t.Errorf("checksum %q", ms[0].Checksum)
		}
	})
	t.Run("a sub-directory is ignored", func(t *testing.T) {
		fsys := fsOf(map[string]string{"sql/0001_init.sql": "SELECT 1;", "sql/old/0007_x.sql": "SELECT 1;"})
		ms, err := Load(fsys, "sql")
		if err != nil || len(ms) != 1 {
			t.Fatalf("%v %v", ms, err)
		}
	})
	bad := []struct {
		name  string
		files map[string]string
		file  string
	}{
		{"short number", map[string]string{"sql/1_init.sql": "SELECT 1;"}, "1_init.sql"},
		{"hyphen", map[string]string{"sql/0001-init.sql": "SELECT 1;"}, "0001-init.sql"},
		{"upper case", map[string]string{"sql/0001_Init.sql": "SELECT 1;"}, "0001_Init.sql"},
		{"duplicate version", map[string]string{"sql/0001_a.sql": "SELECT 1;", "sql/0002_b.sql": "SELECT 1;", "sql/0002_c.sql": "SELECT 1;"}, "0002_"},
		{"gap", map[string]string{"sql/0001_a.sql": "SELECT 1;", "sql/0003_b.sql": "SELECT 1;"}, "0003_b.sql"},
		{"not starting at one", map[string]string{"sql/0002_a.sql": "SELECT 1;"}, "0002_a.sql"},
		{"empty file", map[string]string{"sql/0001_a.sql": ""}, "0001_a.sql"},
		{"invalid UTF-8", map[string]string{"sql/0001_a.sql": "SELECT '\xff\xfe';"}, "0001_a.sql"},
	}
	for _, b := range bad {
		t.Run(b.name, func(t *testing.T) {
			_, err := Load(fsOf(b.files), "sql")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), b.file) {
				t.Errorf("the error does not name the file %q: %v", b.file, err)
			}
		})
	}
}

// TestLoadRejectsForbiddenStatements is T-105.
func TestLoadRejectsForbiddenStatements(t *testing.T) {
	refused := map[string]string{
		"commit":              "SELECT 1;\nCOMMIT;\n",
		"commit lower":        "select 1;\ncommit;\n",
		"commit indented":     "SELECT 1;\n  COMMIT;\n",
		"rollback":            "SELECT 1;\nROLLBACK;\n",
		"rollback lower":      "select 1;\nrollback;\n",
		"concurrently":        "CREATE INDEX CONCURRENTLY i ON t (c);\n",
		"concurrently lower":  "create index concurrently i on t (c);\n",
		"set role":            "SET ROLE pandokos_owner;\n",
		"set role lower":      "set role pandokos_owner;\n",
		"set local role":      "SET LOCAL ROLE pandokos_owner;\n",
		"set session role":    "SET SESSION ROLE pandokos_owner;\n",
		"set authorization":   "SET SESSION AUTHORIZATION postgres;\n",
		"set authorization l": "set session authorization postgres;\n",
	}
	for name, content := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := Load(fsOf(map[string]string{"sql/0001_x.sql": content}), "sql")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "0001_x.sql") {
				t.Errorf("the error does not name the file: %v", err)
			}
		})
	}
	t.Run("accepts plpgsql BEGIN and commit_count", func(t *testing.T) {
		content := "CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$\nBEGIN\n    PERFORM 1;\nEND\n$$;\nCREATE TABLE t (\n    commit_count integer,\n    rollback_count integer\n);\n"
		if _, err := Load(fsOf(map[string]string{"sql/0001_x.sql": content}), "sql"); err != nil {
			t.Fatal(err)
		}
	})
}

func ms(n int) []Migration {
	var out []Migration
	for i := 1; i <= n; i++ {
		out = append(out, Migration{Version: i, Name: "m" + string(rune('a'+i-1)), Checksum: strings.Repeat(string(rune('a'+i-1)), 64)})
	}
	return out
}

func ledger(of []Migration, versions ...int) []Applied {
	var out []Applied
	for _, v := range versions {
		m := of[v-1]
		out = append(out, Applied{Version: v, Name: m.Name, Checksum: m.Checksum})
	}
	return out
}

// TestCompare is T-112: every row of the comparison table of sprint.md section 5.4.
func TestCompare(t *testing.T) {
	bin := ms(3)

	t.Run("equal", func(t *testing.T) {
		pending, err := compare(ledger(bin, 1, 2, 3), bin)
		if err != nil || len(pending) != 0 {
			t.Fatalf("pending %v, error %v", pending, err)
		}
	})
	t.Run("no ledger", func(t *testing.T) {
		pending, err := compare(nil, bin)
		if err != nil || len(pending) != 3 {
			t.Fatalf("pending %v, error %v", pending, err)
		}
	})
	t.Run("older", func(t *testing.T) {
		pending, err := compare(ledger(bin, 1), bin)
		if err != nil || len(pending) != 2 || pending[0].Version != 2 || pending[1].Version != 3 {
			t.Fatalf("pending %v, error %v", pending, err)
		}
	})
	t.Run("newer", func(t *testing.T) {
		long := ms(4)
		_, err := compare(ledger(long, 1, 2, 3, 4), bin)
		if !errors.Is(err, ErrSchemaNewer) {
			t.Fatalf("error %v", err)
		}
		want := "the database schema is newer than this binary: schema version 4, this binary knows version 3; upgrade Pandokos"
		if err.Error() != want {
			t.Errorf("message %q, want %q", err.Error(), want)
		}
	})
	t.Run("checksum differs", func(t *testing.T) {
		l := ledger(bin, 1, 2)
		l[1].Checksum = strings.Repeat("0", 64)
		_, err := compare(l, bin)
		if !errors.Is(err, ErrChecksum) {
			t.Fatalf("error %v", err)
		}
		want := "a migration recorded in the database differs from this binary: 0002_mb"
		if err.Error() != want {
			t.Errorf("message %q, want %q", err.Error(), want)
		}
	})
	t.Run("name differs", func(t *testing.T) {
		l := ledger(bin, 1)
		l[0].Name = "other"
		_, err := compare(l, bin)
		if !errors.Is(err, ErrChecksum) || !strings.HasSuffix(err.Error(), ": 0001_ma") {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("gap", func(t *testing.T) {
		l := ledger(bin, 1, 3)
		_, err := compare(l, bin)
		if !errors.Is(err, ErrLedger) {
			t.Fatalf("error %v", err)
		}
		want := "the migration ledger is inconsistent: version 3 follows version 1"
		if err.Error() != want {
			t.Errorf("message %q, want %q", err.Error(), want)
		}
	})
	t.Run("ledger not starting at one", func(t *testing.T) {
		_, err := compare(ledger(bin, 2, 3), bin)
		if !errors.Is(err, ErrLedger) {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("gaps are reported before a newer schema", func(t *testing.T) {
		l := []Applied{{Version: 1, Name: "ma", Checksum: bin[0].Checksum}, {Version: 99, Name: "x", Checksum: strings.Repeat("0", 64)}}
		if _, err := compare(l, bin); !errors.Is(err, ErrLedger) {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("newer is reported before a checksum difference", func(t *testing.T) {
		long := ms(4)
		l := ledger(long, 1, 2, 3, 4)
		l[0].Checksum = strings.Repeat("0", 64)
		if _, err := compare(l, bin); !errors.Is(err, ErrSchemaNewer) {
			t.Fatalf("error %v", err)
		}
	})
}
