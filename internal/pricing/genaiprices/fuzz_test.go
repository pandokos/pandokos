package genaiprices

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// seedDatasets returns the datasets FuzzParse starts from: the embedded snapshot cut to its first
// provider and a handful of its models, a small dataset with every construct the importer
// follows or reports, and malformed variants of both.
func seedDatasets(f *testing.F) [][]byte {
	f.Helper()
	var providers []json.RawMessage
	if err := json.Unmarshal(Snapshot(), &providers); err != nil || len(providers) == 0 {
		f.Fatalf("the embedded snapshot does not decode: %v", err)
	}
	var first map[string]json.RawMessage
	if err := json.Unmarshal(providers[0], &first); err != nil {
		f.Fatal(err)
	}
	var models []json.RawMessage
	if err := json.Unmarshal(first["models"], &models); err != nil {
		f.Fatal(err)
	}
	if len(models) > 8 {
		models = models[:8]
	}
	first["models"], _ = json.Marshal(models)
	cut, err := json.Marshal([]any{first})
	if err != nil {
		f.Fatal(err)
	}
	small := `[{"id":"synthetic","models":[
		{"id":"synthetic-a","match":{"starts_with":"synthetic-a"},"prices":{"input_mtok":"1.25","output_mtok":10,"cache_read_mtok":0.125,"unknown_mtok":1}},
		{"id":"synthetic-tiered","match":{"or":[{"equals":"t1"},{"regex":"^t[0-9]+$"}]},"prices":{"input_mtok":{"base":"3","tiers":[{"start":200000,"price":"6"}]},"output_mtok":15}},
		{"id":"synthetic-scheduled","match":{"contains":"sched"},"prices":[{"constraint":{"start_date":"2026-01-01"},"prices":{"input_mtok":1,"output_mtok":2}},{"constraint":{"start_date":"2999-01-01"},"prices":{"input_mtok":9,"output_mtok":9}},{"constraint":{"start_time":"09:00:00","end_time":"17:00:00"},"prices":{"input_mtok":5,"output_mtok":5}}]},
		{"id":"synthetic-variants","match":{"ends_with":"-v"},"prices":{"input_mtok":1,"output_mtok":1},"price_variants":[{"when":{"service_tier":"flex"}}],"context_window":128000,"deprecated":true},
		{"id":"synthetic-bad-regex","match":{"regex":"(?<name>x)"},"prices":{"input_mtok":1}},
		{"id":"synthetic-huge","match":{"regex":"a{1001}"},"prices":{"input_mtok":1e400,"output_mtok":"1000001"}},
		{"id":"synthetic-negative","match":{"and":[{"contains":"n"},{"or":[]}]},"prices":{"input_mtok":-1}},
		{"id":"synthetic-digits","match":{"equals":"d"},"prices":{"input_mtok":"0.1234567890123456","output_mtok":"123456789012345678901234567890123456789012345678901234567890"}}
	]}]`
	return [][]byte{
		cut, []byte(small),
		[]byte(small[:len(small)/2]), []byte(`[]`), []byte(`[{}]`), []byte(`{}`), []byte(` [{"id":1}]`),
		[]byte(`[{"id":"p","models":null}]`), []byte(`[{"id":"p","models":[{"id":"m","prices":null}]}]`),
		[]byte(`[{"id":"p","models":[{"id":"m","match":{"equals":"m"},"prices":[]}]}]`),
		[]byte(`[{"id":"p","models":[{"id":"m","match":{"equals":"m"},"prices":[{"constraint":{"start_date":"not-a-date"},"prices":{}}]}]}]`),
		[]byte(`[]` + `[]`), []byte(``),
	}
}

// FuzzParse is T-1109: Parse never panics and refuses with its two fixed errors; for every model of
// what it accepts, Extract returns a version that validates, whose rates are non-negative decimals
// of at most 12 fraction digits, or one of its fixed errors; the list of issues is bounded by the
// size of the prices. Regular expressions are RE2 by construction (regexp/syntax), and what that
// guarantees is asserted by no input taking longer than the deadline of the test.
func FuzzParse(f *testing.F) {
	for _, s := range seedDatasets(f) {
		f.Add(s)
	}
	today := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		d, err := Parse(data)
		if err != nil {
			if d != nil || (!errors.Is(err, ErrInvalid) && !errors.Is(err, ErrTooLarge)) {
				t.Fatalf("Parse: %v, %v", d, err)
			}
			return
		}
		if n := len(d.Issues()); n > 0 {
			models := 0
			for _, p := range d.providers {
				models += len(p.Models)
			}
			if n > models {
				t.Fatalf("%d dataset issues for %d models", n, models)
			}
		}
		for _, p := range d.providers {
			for i := range p.Models {
				m := &p.Models[i]
				_ = m.Match.matches(m.ID)
				_ = m.Match.matches(p.ID)
				v, issues, err := Extract(m, today)
				if len(issues) > len(m.Prices)/2+2 {
					t.Fatalf("%d issues for prices of %d bytes", len(issues), len(m.Prices))
				}
				for _, is := range issues {
					if is.Reason == "" {
						t.Fatal("an issue without a reason")
					}
				}
				if err != nil {
					if !errors.Is(err, ErrSkipped) && !errors.Is(err, ErrMalformedPrices) {
						t.Fatalf("Extract: %v", err)
					}
					continue
				}
				if err := v.Validate(); err != nil {
					t.Fatalf("a version that does not validate: %v", err)
				}
				for _, r := range v.Rates {
					if r.PerMillion.Sign() < 0 {
						t.Fatalf("a negative rate %s", r.PerMillion)
					}
					if _, frac, ok := strings.Cut(r.PerMillion.String(), "."); ok && len(frac) > 12 {
						t.Fatalf("a rate of %d fraction digits: %s", len(frac), r.PerMillion)
					}
				}
			}
		}
	})
}
