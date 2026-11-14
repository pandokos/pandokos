package genaiprices_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
)

// TestParse is T-374: shapes.json parses into the expected structure, numbers stay text, and
// every refusal is an error that quotes no content of the document.
func TestParse(t *testing.T) {
	d := parseFixture(t, "shapes.json")

	t.Run("the structure", func(t *testing.T) {
		m, ok := d.Find("synthetic", "synthetic-all-categories")
		if !ok {
			t.Fatal("synthetic-all-categories not found")
		}
		if m.ID != "synthetic-all-categories" || !m.Deprecated {
			t.Errorf("model %q, deprecated %v", m.ID, m.Deprecated)
		}
		if m.ContextWindow == nil || m.ContextWindow.String() != "32000" {
			t.Errorf("context window %v", m.ContextWindow)
		}
		plain, ok := d.Find("synthetic", "synthetic-plain")
		if !ok {
			t.Fatal("synthetic-plain not found")
		}
		if plain.ContextWindow != nil || plain.Deprecated {
			t.Errorf("synthetic-plain: window %v, deprecated %v", plain.ContextWindow, plain.Deprecated)
		}
		if _, ok := d.Find("openai", "synthetic-plain"); ok {
			t.Error("a model of another dataset provider matched")
		}
		if _, ok := d.Find("nobody", "synthetic-plain"); ok {
			t.Error("an unknown dataset provider matched")
		}
	})

	t.Run("numbers are kept as text", func(t *testing.T) {
		// 0.195 would not survive a float64 round trip as a short text; read from its own
		// characters it is exactly 0.195, and 0.0195 exactly that.
		v, _, err := extract(t, d, "synthetic", "synthetic-all-categories", today)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{"input@0=0.195": true, "cache_read@0=0.0195": true}
		for _, r := range ratesOf(v) {
			delete(want, r)
		}
		if len(want) != 0 {
			t.Errorf("rates %v: missing %v", ratesOf(v), want)
		}
	})

	t.Run("unknown members are ignored", func(t *testing.T) {
		doc := `[{"id":"p","name":"n","unknown_provider_member":{"a":[1,2]},"models":[{"id":"m","match":{"equals":"m"},
			"prices":{"input_mtok":1,"output_mtok":2},"unknown_model_member":"x","description":"y","extra":{"deep":[true,null]}}],"api_pattern":"zz"}]`
		d, err := genaiprices.Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := d.Find("p", "m"); !ok {
			t.Error("the model was not found")
		}
	})

	t.Run("an empty list of providers", func(t *testing.T) {
		d, err := genaiprices.Parse([]byte(" [ ] \n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := d.Find("p", "m"); ok {
			t.Error("found a model in nothing")
		}
	})

	t.Run("Issues names a pattern that RE2 refuses by model, never by pattern", func(t *testing.T) {
		is := d.Issues()
		if got := details(is, "regex_unsupported"); len(got) != 1 || !strings.Contains(got[0], "synthetic-match-lookbehind") {
			t.Errorf("issues of the dataset: %+v", is)
		}
		for _, i := range is {
			if strings.Contains(i.Detail, "(?<=") {
				t.Errorf("an issue quotes the pattern: %+v", i)
			}
		}
	})

	t.Run("refusals quote nothing", func(t *testing.T) {
		const marker = "synthetic-marker-quoted-content"
		pad := bytes.Repeat([]byte(" "), 16<<20-2)
		big := append(append([]byte("["), pad...), ']')
		cases := map[string][]byte{
			"not json":                     []byte(marker),
			"an object at the root":        []byte(`{"not":"` + marker + `"}`),
			"a string at the root":         []byte(`"` + marker + `"`),
			"a number at the root":         []byte(`12`),
			"empty":                        nil,
			"truncated":                    []byte(`[{"id":"` + marker),
			"trailing garbage":             []byte(`[] ` + marker),
			"models of the wrong type":     []byte(`[{"id":"p","models":"` + marker + `"}]`),
			"a model id of the wrong type": []byte(`[{"id":"p","models":[{"id":["` + marker + `"],"match":{"equals":"a"},"prices":{}}]}]`),
			"deprecated of the wrong type": []byte(`[{"id":"p","models":[{"id":"m","match":{"equals":"a"},"deprecated":"` + marker + `","prices":{}}]}]`),
			"match of the wrong type":      []byte(`[{"id":"p","models":[{"id":"m","match":"` + marker + `","prices":{}}]}]`),
			"a clause of the wrong type":   []byte(`[{"id":"p","models":[{"id":"m","match":{"or":"` + marker + `"},"prices":{}}]}]`),
			"a provider of the wrong type": []byte(`["` + marker + `"]`),
			"one byte too many":            append(append([]byte(nil), big...), ' '),
		}
		for name, doc := range cases {
			_, err := genaiprices.Parse(doc)
			if err == nil {
				t.Errorf("%s: accepted", name)
				continue
			}
			if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "quoted-content") {
				t.Errorf("%s: the error quotes the document: %v", name, err)
			}
		}
		if n := len(cases["one byte too many"]); n != 16<<20+1 {
			t.Fatalf("test data is %d bytes", n)
		}
		// Exactly 16 MiB is accepted.
		if len(big) != 16<<20 {
			t.Fatalf("test data is %d bytes", len(big))
		}
		if _, err := genaiprices.Parse(big); err != nil {
			t.Errorf("16 MiB: %v", err)
		}
		if _, err := genaiprices.Parse(fixture(t, "invalid.json")); err == nil {
			t.Error("invalid.json accepted")
		}
	})
}
