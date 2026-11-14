package genaiprices_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
)

// TestMatch is T-375: the match clauses of the dataset, exact and case-sensitive.
func TestMatch(t *testing.T) {
	d := parseFixture(t, "shapes.json")

	for _, c := range []struct {
		name, upstream, want string // want: the id of the model that matches, "" for none
	}{
		{"equals", "synthetic-plain", "synthetic-plain"},
		{"equals is exact (prefix)", "synthetic-plain-2", ""},
		{"equals is exact (suffix)", "x-synthetic-plain", ""},
		{"equals is case-sensitive", "Synthetic-Plain", ""},
		{"starts_with", "synthetic-sw-anything", "synthetic-match-starts-with"},
		{"starts_with, the prefix alone", "synthetic-sw-", "synthetic-match-starts-with"},
		{"starts_with, not at the start", "x-synthetic-sw-1", ""},
		{"starts_with is case-sensitive", "SYNTHETIC-SW-1", ""},
		{"ends_with", "model-synthetic-ew", "synthetic-match-ends-with"},
		{"ends_with, not at the end", "model-synthetic-ew-1", ""},
		{"contains", "a-synthetic-mid-b", "synthetic-match-contains"},
		{"contains, absent", "a-synthetic-mid", ""},
		{"contains is case-sensitive", "a-Synthetic-Mid-b", ""},
		{"regex", "synthetic-rx-20250101", "synthetic-match-regex"},
		{"regex is anchored by the pattern", "synthetic-rx-2025", ""},
		{"regex, a suffix the pattern refuses", "synthetic-rx-20250101x", ""},
		{"or, first member", "synthetic-or-a", "synthetic-match-or"},
		{"or, second member", "synthetic-or-b", "synthetic-match-or"},
		{"or, none", "synthetic-or-c", ""},
		{"or is case-sensitive", "SYNTHETIC-OR-A", ""},
		{"and, both", "synthetic-and-1-end", "synthetic-match-and"},
		{"and, only the first", "synthetic-and-1", ""},
		{"and, only the second", "x-end", ""},
		{"nothing matches an empty identifier", "", ""},
		{"nothing matches an unknown identifier", "no-such-model", ""},
	} {
		m, ok := d.Find("synthetic", c.upstream)
		switch {
		case c.want == "" && ok:
			t.Errorf("%s: %q matched %s", c.name, c.upstream, m.ID)
		case c.want != "" && !ok:
			t.Errorf("%s: %q matched nothing, want %s", c.name, c.upstream, c.want)
		case c.want != "" && m.ID != c.want:
			t.Errorf("%s: %q matched %s, want %s", c.name, c.upstream, m.ID, c.want)
		}
	}

	t.Run("a Python-only construct matches nothing and is reported", func(t *testing.T) {
		if m, ok := d.Find("synthetic", "xsynthetic-lb"); ok {
			t.Errorf("a pattern RE2 refuses matched %s", m.ID)
		}
		if m, ok := d.Find("synthetic", "synthetic-lb"); ok {
			t.Errorf("a pattern RE2 refuses matched %s", m.ID)
		}
		got := details(d.Issues(), "regex_unsupported")
		if len(got) != 1 || !strings.Contains(got[0], "synthetic-match-lookbehind") {
			t.Errorf("issues: %v", got)
		}
	})

	t.Run("the first match wins, in the order of the document", func(t *testing.T) {
		doc := `[{"id":"p","models":[
			{"id":"specific","match":{"equals":"gpt-x-2025"},"prices":{"input_mtok":1,"output_mtok":2}},
			{"id":"family","match":{"starts_with":"gpt-x"},"prices":{"input_mtok":3,"output_mtok":4}},
			{"id":"catch-all","match":{"contains":"gpt"},"prices":{"input_mtok":5,"output_mtok":6}}]}]`
		d, err := genaiprices.Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		for in, want := range map[string]string{"gpt-x-2025": "specific", "gpt-x-2026": "family", "gpt-y": "catch-all"} {
			if m, ok := d.Find("p", in); !ok || m.ID != want {
				t.Errorf("%s: %v %v, want %s", in, m, ok, want)
			}
		}
		if _, ok := d.Find("p", "claude"); ok {
			t.Error("a model matched nothing it should")
		}
	})

	t.Run("nesting", func(t *testing.T) {
		doc := `[{"id":"p","models":[{"id":"nested","match":{"and":[
			{"or":[{"starts_with":"a-"},{"starts_with":"b-"}]},
			{"or":[{"ends_with":"-1"},{"and":[{"contains":"mid"},{"ends_with":"-2"}]}]}]},
			"prices":{"input_mtok":1,"output_mtok":2}}]}]`
		d, err := genaiprices.Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		for in, want := range map[string]bool{
			"a-x-1": true, "b-x-1": true, "a-mid-2": true, "b-mid-2": true,
			"a-x-2": false, "c-x-1": false, "c-mid-2": false, "a-mid-3": false, "": false,
		} {
			if _, ok := d.Find("p", in); ok != want {
				t.Errorf("%q: matched %v, want %v", in, ok, want)
			}
		}
	})

	t.Run("regular expressions use RE2 and search the identifier", func(t *testing.T) {
		doc := `[{"id":"p","models":[
			{"id":"search","match":{"regex":"claude-(opus|sonnet)-4"},"prices":{"input_mtok":1,"output_mtok":2}},
			{"id":"anchored","match":{"regex":"^o[0-9]+$"},"prices":{"input_mtok":1,"output_mtok":2}}]}]`
		d, err := genaiprices.Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		for in, want := range map[string]string{
			"claude-opus-4": "search", "anthropic.claude-sonnet-4-5-v1": "search", "o3": "anchored", "o3-mini": "", "claude-haiku": "",
		} {
			m, ok := d.Find("p", in)
			if (want == "") == ok || (ok && m.ID != want) {
				t.Errorf("%q: %v %v, want %q", in, m, ok, want)
			}
		}
		if len(d.Issues()) != 0 {
			t.Errorf("issues for valid patterns: %+v", d.Issues())
		}
	})

	t.Run("many models", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString(`[{"id":"p","models":[`)
		for i := 0; i < 500; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"id":"m%d","match":{"equals":"model-%d"},"prices":{"input_mtok":1,"output_mtok":2}}`, i, i)
		}
		sb.WriteString(`]}]`)
		d, err := genaiprices.Parse([]byte(sb.String()))
		if err != nil {
			t.Fatal(err)
		}
		if m, ok := d.Find("p", "model-499"); !ok || m.ID != "m499" {
			t.Errorf("%v %v", m, ok)
		}
	})
}
