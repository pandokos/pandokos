package genaiprices_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
)

// TestSnapshot is T-377. It asserts no price of a real model (prices change with the snapshot);
// it asserts that the embedded file is the one SOURCE describes, and that every model the
// migration seeds is matched and priced.
func TestSnapshot(t *testing.T) {
	data := genaiprices.Snapshot()
	d, err := genaiprices.Parse(data)
	if err != nil {
		t.Fatalf("the embedded snapshot does not parse: %v", err)
	}

	t.Run("SOURCE describes the file", func(t *testing.T) {
		src, err := os.ReadFile("snapshot/SOURCE")
		if err != nil {
			t.Fatal(err)
		}
		field := func(name string) string {
			m := regexp.MustCompile(`(?m)^` + name + `: (.+)$`).FindStringSubmatch(string(src))
			if m == nil {
				t.Fatalf("SOURCE has no %s line", name)
			}
			return strings.TrimSpace(m[1])
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != field("sha256") {
			t.Errorf("sha256 of the embedded file is %s, SOURCE says %s", got, field("sha256"))
		}
		file, err := os.ReadFile("snapshot/data.json")
		if err != nil {
			t.Fatal(err)
		}
		if string(file) != string(data) {
			t.Error("Snapshot() is not the file snapshot/data.json byte for byte")
		}
		commit, retrieved, digest := genaiprices.SnapshotInfo()
		if commit != field("commit") || digest != field("sha256") {
			t.Errorf("SnapshotInfo() = %q, %q; SOURCE says %q, %q", commit, digest, field("commit"), field("sha256"))
		}
		if want, err := time.Parse(time.DateOnly, field("retrieved")); err != nil || !retrieved.Equal(want) {
			t.Errorf("retrieved %v, SOURCE says %s (%v)", retrieved, field("retrieved"), err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
			t.Errorf("commit %q is not a full commit hash", commit)
		}
	})

	t.Run("LICENSE is the MIT text", func(t *testing.T) {
		b, err := os.ReadFile("snapshot/LICENSE")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"MIT License", "Pydantic", "Permission is hereby granted, free of charge",
			`THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("LICENSE lacks %q", want)
			}
		}
	})

	t.Run("every model of the migration seed is matched and priced", func(t *testing.T) {
		sql, err := os.ReadFile("../../store/migrations/sql/0004_catalogue.sql")
		if err != nil {
			t.Fatal(err)
		}
		// ('openai', 'gpt-4o', '{text}', 128000),
		re := regexp.MustCompile(`\('(openai|anthropic)', '([^']+)', '\{[a-z_,]+\}', ([0-9]+)\)`)
		seed := re.FindAllStringSubmatch(string(sql), -1)
		if len(seed) != 17 {
			t.Fatalf("found %d seeded models in the migration, the design lists 17", len(seed))
		}
		for _, s := range seed {
			provider, name, window := s[1], s[2], s[3]
			m, ok := d.Find(provider, name)
			if !ok {
				t.Errorf("%s/%s: the snapshot has no matching model", provider, name)
				continue
			}
			v, _, err := genaiprices.Extract(m, time.Now().UTC().Truncate(24*time.Hour))
			if err != nil {
				t.Errorf("%s/%s: extraction: %v", provider, name, err)
				continue
			}
			if err := v.Validate(); err != nil {
				t.Errorf("%s/%s: the version is not valid: %v", provider, name, err)
			}
			hasInput := false
			for _, r := range v.Rates {
				hasInput = hasInput || (r.Category == "input" && r.Threshold == 0)
			}
			if !hasInput {
				t.Errorf("%s/%s: no input rate", provider, name)
			}
			if m.ContextWindow == nil || m.ContextWindow.String() != window {
				got := "none"
				if m.ContextWindow != nil {
					got = m.ContextWindow.String()
				}
				t.Errorf("%s/%s: the migration seeds a context window of %s, the snapshot says %s. "+
					"A mismatch after a snapshot update is a reminder, not a bug: update neither file silently, raise it", provider, name, window, got)
			}
		}
	})

	t.Run("the dataset has providers", func(t *testing.T) {
		if _, ok := d.Find("openai", "gpt-4o"); !ok {
			t.Error("gpt-4o is not in the snapshot")
		}
		if len(data) < 500_000 || len(data) > 16<<20 {
			t.Errorf("the snapshot is %d bytes", len(data))
		}
	})
}
