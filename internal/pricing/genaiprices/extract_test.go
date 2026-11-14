package genaiprices_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
)

// TestExtract is T-376: every row of the extraction table of section 5.6 with its issue.
// A model that is skipped returns genaiprices.ErrSkipped, a zero version and the issue that
// says why; an imported model returns a version that validates, in USD.
func TestExtract(t *testing.T) {
	d := parseFixture(t, "shapes.json")

	ok := []struct {
		name, upstream string
		rates          []string // sorted category@threshold=rate
		issues         []string // sorted reasons
	}{
		{"a plain price", "synthetic-plain", []string{"input@0=1", "output@0=2"}, nil},
		{"every supported category", "synthetic-all-categories", []string{
			"cache_read@0=0.0195", "cache_write@0=0.24", "cache_write_1h@0=0.39", "input@0=0.195",
			"input_audio@0=3.5", "output@0=0.78", "output_audio@0=7"}, nil},
		{"tiers become thresholds", "synthetic-tiers", []string{
			"input@0=1", "input@100000=2", "input@500000=4", "output@0=3", "output@100000=6"}, nil},
		{"a conditional price whose date has passed", "synthetic-conditional", []string{"input@0=1.5", "output@0=2.5"}, nil},
		{"a price that starts in the future is not imported", "synthetic-scheduled", []string{"input@0=1", "output@0=2"},
			[]string{"scheduled_price_not_imported"}},
		{"a time-of-day price is ignored", "synthetic-time-of-day", []string{"input@0=1", "output@0=2"},
			[]string{"time_of_day_price_ignored"}},
		{"variants are ignored", "synthetic-variants", []string{"input@0=1", "output@0=2"}, []string{"price_variants_ignored"}},
		{"other categories are ignored", "synthetic-other-category", []string{"input@0=1", "output@0=2"},
			[]string{"category_ignored"}},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			v, issues, err := extract(t, d, "synthetic", c.upstream, today)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got := ratesOf(v); !reflect.DeepEqual(got, c.rates) {
				t.Errorf("rates %v, want %v", got, c.rates)
			}
			if v.Currency != "USD" {
				t.Errorf("currency %q", v.Currency)
			}
			if verr := v.Validate(); verr != nil {
				t.Errorf("the extracted version does not validate: %v", verr)
			}
			if v.ID != "" || v.ModelID != "" || !v.ValidFrom.IsZero() {
				t.Errorf("Extract set an identity: %+v", v)
			}
			if got := unique(reasons(issues)); !reflect.DeepEqual(got, unique(c.issues)) {
				t.Errorf("issues %v, want %v", got, unique(c.issues))
			}
			for _, i := range issues {
				if i.Reason == "" {
					t.Errorf("an issue without a reason: %+v", i)
				}
			}
		})
	}

	t.Run("issue details are identifiers only", func(t *testing.T) {
		_, issues, err := extract(t, d, "synthetic", "synthetic-other-category", today)
		if err != nil {
			t.Fatal(err)
		}
		got := details(issues, "category_ignored")
		want := []string{"cache_audio_read_mtok", "input_image_mtok", "requests_kcount", "web_searches_kcount"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("details %v, want %v", got, want)
		}
		_, issues, err = extract(t, d, "synthetic", "synthetic-variants", today)
		if err != nil {
			t.Fatal(err)
		}
		all := strings.Join(details(issues, "price_variants_ignored"), " ")
		for _, k := range []string{"service_tier", "processing"} {
			if !strings.Contains(all, k) {
				t.Errorf("the variant keys %q do not name %s", all, k)
			}
		}
		for _, bad := range []string{"flex", "batch", "0.5"} {
			if strings.Contains(all, bad) {
				t.Errorf("the variant details %q quote a value (%s)", all, bad)
			}
		}
	})

	t.Run("a model with nothing usable is skipped", func(t *testing.T) {
		for upstream, reason := range map[string]string{
			"synthetic-no-supported-price":   "no_supported_price",
			"synthetic-negative":             "value_not_representable",
			"synthetic-too-many-digits":      "value_not_representable",
			"synthetic-above-limit":          "value_not_representable",
			"synthetic-tier-start-too-large": "value_not_representable",
		} {
			v, issues, err := extract(t, d, "synthetic", upstream, today)
			if !errors.Is(err, genaiprices.ErrSkipped) {
				t.Errorf("%s: error %v, want ErrSkipped", upstream, err)
			}
			if len(v.Rates) != 0 {
				t.Errorf("%s: rates returned for a skipped model: %v", upstream, ratesOf(v))
			}
			found := false
			for _, i := range issues {
				found = found || i.Reason == reason
			}
			if !found {
				t.Errorf("%s: issues %v, want %s", upstream, reasons(issues), reason)
			}
		}
	})

	t.Run("the conditional rule on the days around a start date", func(t *testing.T) {
		for _, c := range []struct {
			on   time.Time
			want []string
		}{
			{time.Date(2019, 12, 31, 0, 0, 0, 0, time.UTC), []string{"input@0=1", "output@0=2"}},
			{time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), []string{"input@0=1.5", "output@0=2.5"}},
			{time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), []string{"input@0=1.5", "output@0=2.5"}},
			{time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC), []string{"input@0=1.5", "output@0=2.5"}},
		} {
			v, issues, err := extract(t, d, "synthetic", "synthetic-conditional", c.on)
			if err != nil {
				t.Fatalf("%s: %v", c.on.Format(time.DateOnly), err)
			}
			if got := ratesOf(v); !reflect.DeepEqual(got, c.want) {
				t.Errorf("on %s: rates %v, want %v", c.on.Format(time.DateOnly), got, c.want)
			}
			// Before the start date the later entry is reported, as the scheduled one is.
			wantIssue := c.on.Before(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
			if has := contains(reasons(issues), "scheduled_price_not_imported"); has != wantIssue {
				t.Errorf("on %s: scheduled issue %v, want %v", c.on.Format(time.DateOnly), has, wantIssue)
			}
		}
		// The scheduled fixture: the price of 2999 is picked up by an extraction on that day.
		v, _, err := extract(t, d, "synthetic", "synthetic-scheduled", time.Date(2999, 12, 31, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if got := ratesOf(v); !reflect.DeepEqual(got, []string{"input@0=9", "output@0=9"}) {
			t.Errorf("on the start date: %v", got)
		}
	})

	t.Run("bounds of a representable value", func(t *testing.T) {
		for name, c := range map[string]struct {
			prices string
			ok     bool
		}{
			"zero rate":                                {`{"input_mtok":0,"output_mtok":0}`, true},
			"one million":                              {`{"input_mtok":1000000,"output_mtok":1}`, true},
			"twelve fraction digits":                   {`{"input_mtok":0.123456789012,"output_mtok":1}`, true},
			"above one million":                        {`{"input_mtok":1000000.000001,"output_mtok":1}`, false},
			"thirteen fraction digits":                 {`{"input_mtok":0.1234567890123,"output_mtok":1}`, false},
			"negative":                                 {`{"input_mtok":-0.000001,"output_mtok":1}`, false},
			"tier start at the limit":                  {`{"input_mtok":{"base":1,"tiers":[{"start":100000000,"price":2}]},"output_mtok":1}`, true},
			"tier start above the limit":               {`{"input_mtok":{"base":1,"tiers":[{"start":100000001,"price":2}]},"output_mtok":1}`, false},
			"a tier price above the limit":             {`{"input_mtok":{"base":1,"tiers":[{"start":10,"price":1000001}]},"output_mtok":1}`, false},
			"an ignored category cannot spoil a model": {`{"input_mtok":1,"output_mtok":1,"web_searches_kcount":-5}`, true},
		} {
			doc := fmt.Sprintf(`[{"id":"p","models":[{"id":"m","match":{"equals":"m"},"prices":%s}]}]`, c.prices)
			ds, err := genaiprices.Parse([]byte(doc))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			m, found := ds.Find("p", "m")
			if !found {
				t.Fatalf("%s: model not found", name)
			}
			v, _, err := genaiprices.Extract(m, today)
			if c.ok && (err != nil || v.Validate() != nil) {
				t.Errorf("%s: refused: %v %v", name, err, v.Validate())
			}
			if !c.ok && !errors.Is(err, genaiprices.ErrSkipped) {
				t.Errorf("%s: error %v, want ErrSkipped", name, err)
			}
		}
	})

	t.Run("a wrong type in the prices is an error that quotes nothing", func(t *testing.T) {
		const marker = "synthetic-marker-in-prices"
		for name, prices := range map[string]string{
			"a string rate":   `{"input_mtok":"` + marker + `","output_mtok":1}`,
			"a list of rates": `{"input_mtok":[1,2],"output_mtok":1}`,
			"a string":        `"` + marker + `"`,
			"a number":        `12`,
		} {
			doc := fmt.Sprintf(`[{"id":"p","models":[{"id":"m","match":{"equals":"m"},"prices":%s}]}]`, prices)
			ds, err := genaiprices.Parse([]byte(doc))
			if err != nil {
				continue // refused earlier is as good
			}
			m, _ := ds.Find("p", "m")
			if _, _, err = genaiprices.Extract(m, today); err == nil {
				t.Errorf("%s: accepted", name)
			} else if strings.Contains(err.Error(), marker) {
				t.Errorf("%s: the error quotes the document: %v", name, err)
			}
		}
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// unique returns a sorted list without repeats; reasons is already sorted.
func unique(list []string) []string {
	var out []string
	for _, s := range list {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}
