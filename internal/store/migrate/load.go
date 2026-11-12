package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var fileName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// forbidden are the statements that cannot run inside the transaction the migrator opens, or
// that would change who owns what the migration creates.
var forbidden = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?m)^\s*(COMMIT|ROLLBACK)\b`),
	regexp.MustCompile(`(?i)\bCONCURRENTLY\b`),
	regexp.MustCompile(`(?i)\bSET\s+(SESSION\s+|LOCAL\s+)?ROLE\b`),
	regexp.MustCompile(`(?i)\bSET\s+SESSION\s+AUTHORIZATION\b`),
}

// Load reads every *.sql file of dir in fsys, sorted by version. Every error names the file.
func Load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	var out []Migration
	files := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := e.Name()
		m := fileName.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("migration file %s: the name must be NNNN_name.sql with four digits and a name of [a-z0-9_]", name)
		}
		version, _ := strconv.Atoi(m[1])
		if other, dup := files[version]; dup {
			return nil, fmt.Errorf("migration file %s: version %d is also used by %s", name, version, other)
		}
		files[version] = name
		b, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("migration file %s: %w", name, err)
		}
		switch {
		case len(b) == 0:
			return nil, fmt.Errorf("migration file %s: the file is empty", name)
		case !utf8.Valid(b):
			return nil, fmt.Errorf("migration file %s: the file is not valid UTF-8", name)
		}
		for _, re := range forbidden {
			if re.Match(b) {
				return nil, fmt.Errorf("migration file %s: matches %s, which cannot run in the migration transaction", name, re.String())
			}
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{Version: version, Name: m[2], SQL: string(b), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migration file %s: %w", m.ID()+".sql", errors.New("versions must be 1..n without gaps"))
		}
	}
	return out, nil
}
