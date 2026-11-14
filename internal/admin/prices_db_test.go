package admin_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/test/testutil"
)

func fixtureBytes(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "prices", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestPriceOverrides is T-403: operator prices, versions and imports that leave an overridden
// model alone.
func TestPriceOverrides(t *testing.T) {
	c := newCat(t)
	p := c.createProvider(t, nil)
	c.createEndpoint(t, p.Name, nil)
	m := c.createModel(t, p.Name, map[string]any{"name": "synthetic-priced", "availability": "all"})
	other := c.createModel(t, p.Name, map[string]any{"name": "synthetic-other", "availability": "all"})
	prices := "/v1/models/" + m.ID + "/prices"
	rates := func(pairs ...string) []map[string]any {
		var out []map[string]any
		for i := 0; i+2 < len(pairs)+1 && i < len(pairs); i += 3 {
			th := 0
			if pairs[i+1] != "0" {
				th = atoi(pairs[i+1])
			}
			out = append(out, map[string]any{"category": pairs[i], "threshold": th, "rate": pairs[i+2]})
		}
		return out
	}

	t.Run("an override now: decimal strings in, decimal strings out", func(t *testing.T) {
		before := time.Now().Add(-time.Minute)
		r := c.platform(t, "POST", prices, map[string]any{"currency": "USD", "note": "first, ünï", "rates": rates("output", "0", "10.000", "input", "0", "2.50", "input", "100000", "5", "cache_read", "0", "0.10")})
		v := decode[adminapi.PriceVersion](t, r, 201)
		if v.ModelID != m.ID || v.Currency != "USD" || v.Source != adminapi.PriceSourceOverride || v.Note != "first, ünï" || v.ImportID != "" || len(v.ID) != 36 || v.CreatedAt.IsZero() {
			t.Errorf("created: %+v", v)
		}
		if v.ValidFrom.Before(before) || v.ValidFrom.After(time.Now().Add(time.Minute)) {
			t.Errorf("valid_from %v is not now", v.ValidFrom)
		}
		// The rates come back canonical, ordered by category and threshold.
		want := []adminapi.PriceRate{
			{Category: adminapi.PriceCategoryCacheRead, Threshold: 0, Rate: "0.1"},
			{Category: adminapi.PriceCategoryInput, Threshold: 0, Rate: "2.5"},
			{Category: adminapi.PriceCategoryInput, Threshold: 100000, Rate: "5"},
			{Category: adminapi.PriceCategoryOutput, Threshold: 0, Rate: "10"},
		}
		if len(v.Rates) != len(want) {
			t.Fatalf("rates: %+v", v.Rates)
		}
		for i := range want {
			if v.Rates[i] != want[i] {
				t.Errorf("rate %d: %+v, want %+v", i, v.Rates[i], want[i])
			}
		}
		// Never a JSON number, on the way out.
		raw := string(r.Body)
		if !regexp.MustCompile(`"rate":"2\.5"`).MatchString(raw) || regexp.MustCompile(`"rate":[0-9]`).MatchString(raw) {
			t.Errorf("a rate is not a JSON string: %s", raw)
		}
		if got := decode[adminapi.Model](t, c.platform(t, "GET", "/v1/models/"+m.ID, nil), 200); !got.PriceOverride {
			t.Error("the model is not flagged as overridden")
		}
		if got := decode[adminapi.Model](t, c.platform(t, "GET", "/v1/models/"+other.ID, nil), 200); got.PriceOverride {
			t.Error("another model is flagged")
		}
		list := c.platform(t, "GET", prices, nil)
		if regexp.MustCompile(`"rate":[0-9]`).MatchString(string(list.Body)) {
			t.Errorf("a rate of the list is a JSON number: %s", list.Body)
		}
	})

	t.Run("what an override may not be", func(t *testing.T) {
		before := c.api.Snapshot(t, "audit_event", "platform_audit_event")
		good := rates("input", "0", "1")
		for name, body := range map[string]map[string]any{
			"a number instead of a decimal string": {"currency": "USD", "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": 2.5}}},
			"a negative rate":                      {"currency": "USD", "rates": rates("input", "0", "-1")},
			"an exponent":                          {"currency": "USD", "rates": rates("input", "0", "1e3")},
			"above a million":                      {"currency": "USD", "rates": rates("input", "0", "1000001")},
			"thirteen fraction digits":             {"currency": "USD", "rates": rates("input", "0", "0.1234567890123")},
			"a leading zero":                       {"currency": "USD", "rates": rates("input", "0", "01")},
			"a tier without a base rate":           {"currency": "USD", "rates": rates("input", "0", "1", "output", "1000", "2")},
			"the same threshold twice":             {"currency": "USD", "rates": rates("input", "0", "1", "input", "0", "2")},
			"a category nobody prices":             {"currency": "USD", "rates": rates("web_search", "0", "1")},
			"a threshold above 1e8":                {"currency": "USD", "rates": rates("input", "0", "1", "input", "100000001", "2")},
			"a lower-case currency":                {"currency": "usd", "rates": good},
			"a currency of two letters":            {"currency": "US", "rates": good},
			"no rate":                              {"currency": "USD", "rates": []map[string]any{}},
			"no rates member":                      {"currency": "USD"},
			"no currency":                          {"rates": good},
			"a null":                               {"currency": "USD", "rates": nil},
			"65 rates":                             {"currency": "USD", "rates": manyRates(65)},
			"a note over 500 characters":           {"currency": "USD", "rates": good, "note": strings.Repeat("n", 501)},
			"a valid_from in the past":             {"currency": "USD", "rates": good, "valid_from": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
			"a valid_from that is no time":         {"currency": "USD", "rates": good, "valid_from": "tomorrow"},
			"an unknown member":                    {"currency": "USD", "rates": good, "source": "import"},
		} {
			r := c.platform(t, "POST", prices, body)
			if r.Status != 400 {
				t.Errorf("%s: status %d\n%s", name, r.Status, r.Body)
				continue
			}
			p := problem(t, r)
			if p.Code != adminapi.ProblemCodeInvalidRequest || len(p.Errors) == 0 {
				t.Errorf("%s: %+v", name, p)
			}
		}
		if after := c.api.Snapshot(t, "audit_event", "platform_audit_event"); after != before {
			t.Error("a refused override changed a table")
		}
		expect(t, c.platform(t, "POST", "/v1/models/0192f0f0-0000-7000-8000-00000000beef/prices", map[string]any{"currency": "USD", "rates": good}), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "GET", "/v1/models/0192f0f0-0000-7000-8000-00000000beef/prices", nil), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("a version in the future, and the version in force at an instant", func(t *testing.T) {
		future := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
		v2 := decode[adminapi.PriceVersion](t, c.platform(t, "POST", prices, map[string]any{"currency": "EUR", "valid_from": future.Format(time.RFC3339), "rates": rates("input", "0", "3", "output", "0", "12")}), 201)
		if !v2.ValidFrom.Equal(future) || v2.Currency != "EUR" {
			t.Errorf("future: %+v", v2)
		}
		// Two versions never share an instant.
		expect(t, c.platform(t, "POST", prices, map[string]any{"currency": "EUR", "valid_from": future.Format(time.RFC3339), "rates": rates("input", "0", "4")}), 409, adminapi.ProblemCodeConflict)

		list := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", prices, nil), 200)
		if len(list.Items) != 2 || list.Items[0].ID != v2.ID || list.Items[1].Currency != "USD" {
			t.Fatalf("the versions, newest first: %+v", list.Items)
		}
		at := func(when time.Time) []adminapi.PriceVersion {
			t.Helper()
			return decode[adminapi.PriceVersionList](t, c.platform(t, "GET", prices+"?at="+url.QueryEscape(when.UTC().Format(time.RFC3339Nano)), nil), 200).Items
		}
		if got := at(time.Now().Add(time.Minute)); len(got) != 1 || got[0].ID != list.Items[1].ID {
			t.Errorf("at a minute from now: %+v", got)
		}
		if got := at(future); len(got) != 1 || got[0].ID != v2.ID {
			t.Errorf("at the instant the second starts: %+v", got)
		}
		if got := at(future.Add(-time.Nanosecond)); len(got) != 1 || got[0].ID != list.Items[1].ID {
			t.Errorf("a nanosecond before: %+v", got)
		}
		if got := at(future.Add(24 * time.Hour)); len(got) != 1 || got[0].ID != v2.ID {
			t.Errorf("a day after: %+v", got)
		}
		if got := at(time.Now().Add(-24 * time.Hour)); len(got) != 0 {
			t.Errorf("before the first version: %+v", got)
		}
		for _, bad := range []string{"tomorrow", "2026-11-14", "1699999999"} {
			invalidOn(t, "at="+bad, c.platform(t, "GET", prices+"?at="+bad, nil), "at")
		}
		// Pages.
		page := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", prices+"?limit=1", nil), 200)
		if len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].ID != v2.ID {
			t.Fatalf("the first page: %+v", page)
		}
		rest := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", prices+"?limit=1&cursor="+url.QueryEscape(page.NextCursor), nil), 200)
		if len(rest.Items) != 1 || rest.Items[0].ID != list.Items[1].ID || rest.NextCursor != "" {
			t.Errorf("the second page: %+v", rest)
		}
	})

	t.Run("a version that has started cannot be deleted, one that has not can", func(t *testing.T) {
		list := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", prices, nil), 200)
		future, started := list.Items[0], list.Items[1]
		expect(t, c.platform(t, "DELETE", prices+"/"+started.ID, nil), 409, adminapi.ProblemCodeInvalidState)
		expect(t, c.platform(t, "DELETE", prices+"/0192f0f0-0000-7000-8000-00000000beef", nil), 404, adminapi.ProblemCodeNotFound)
		// A version of another model is not found under this one.
		o := decode[adminapi.PriceVersion](t, c.platform(t, "POST", "/v1/models/"+other.ID+"/prices", map[string]any{"currency": "USD", "rates": rates("input", "0", "1")}), 201)
		expect(t, c.platform(t, "DELETE", prices+"/"+o.ID, nil), 404, adminapi.ProblemCodeNotFound)
		// clear-price-override is refused while a version has not started.
		expect(t, c.platform(t, "POST", "/v1/models/"+m.ID+"/clear-price-override", map[string]any{}), 409, adminapi.ProblemCodeInvalidState)
		if got := decode[adminapi.Model](t, c.platform(t, "GET", "/v1/models/"+m.ID, nil), 200); !got.PriceOverride {
			t.Error("a refused clearing cleared the flag")
		}
		d := decode[adminapi.PriceVersion](t, c.platform(t, "DELETE", prices+"/"+future.ID, nil), 200)
		if d.ID != future.ID || d.Currency != "EUR" || len(d.Rates) != 2 {
			t.Errorf("the deleted version: %+v", d)
		}
		if got := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", prices, nil), 200); len(got.Items) != 1 || got.Items[0].ID != started.ID {
			t.Errorf("after the deletion: %+v", got.Items)
		}
		// Deleting the last future version does not clear the flag.
		if got := decode[adminapi.Model](t, c.platform(t, "GET", "/v1/models/"+m.ID, nil), 200); !got.PriceOverride {
			t.Error("deleting the future version cleared the flag")
		}
	})

	t.Run("clearing the flag keeps the price in force", func(t *testing.T) {
		cleared := decode[adminapi.Model](t, c.platform(t, "POST", "/v1/models/"+m.ID+"/clear-price-override", map[string]any{"reason": "back to the dataset"}), 200)
		if cleared.PriceOverride || cleared.ID != m.ID {
			t.Errorf("cleared: %+v", cleared)
		}
		if got := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", prices, nil), 200); len(got.Items) != 1 || got.Items[0].Source != adminapi.PriceSourceOverride {
			t.Errorf("the override went out of force with the flag: %+v", got.Items)
		}
		// Clearing a model that is not overridden changes nothing and is not an error.
		if again := decode[adminapi.Model](t, c.platform(t, "POST", "/v1/models/"+m.ID+"/clear-price-override", map[string]any{}), 200); again.PriceOverride {
			t.Errorf("cleared twice: %+v", again)
		}
		expect(t, c.platform(t, "POST", "/v1/models/0192f0f0-0000-7000-8000-00000000beef/clear-price-override", map[string]any{}), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("imports never change an override", func(t *testing.T) {
		gpt := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider=openai&name=gpt-4o", nil), 200).Items[0]
		mini := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider=openai&name=gpt-4o-mini", nil), 200).Items[0]
		decode[adminapi.PriceVersion](t, c.platform(t, "POST", "/v1/models/"+gpt.ID+"/prices", map[string]any{"currency": "USD", "note": "negotiated", "rates": rates("input", "0", "0.5", "output", "0", "2")}), 201)
		imp := decode[adminapi.PriceImport](t, c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded"}), 201)
		outcome := func(model string) string {
			for _, it := range imp.Items {
				if it.Provider == "openai" && it.Model == model {
					return it.Outcome
				}
			}
			return "(absent)"
		}
		if outcome("gpt-4o") != "overridden" || outcome("gpt-4o-mini") != "created" || imp.Overridden < 1 {
			t.Errorf("gpt-4o %s, gpt-4o-mini %s, overridden %d", outcome("gpt-4o"), outcome("gpt-4o-mini"), imp.Overridden)
		}
		if got := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", "/v1/models/"+gpt.ID+"/prices", nil), 200); len(got.Items) != 1 || got.Items[0].Source != adminapi.PriceSourceOverride || got.Items[0].Rates[0].Rate != "0.5" {
			t.Errorf("an import touched an overridden model: %+v", got.Items)
		}
		if got := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", "/v1/models/"+mini.ID+"/prices", nil), 200); len(got.Items) != 1 || got.Items[0].Source != adminapi.PriceSourceImport || got.Items[0].ImportID != imp.ID {
			t.Errorf("an import did not price a model that is not overridden: %+v", got.Items)
		}
		// Cleared, the next import prices it: a new version, and the override stays in the history.
		decode[adminapi.Model](t, c.platform(t, "POST", "/v1/models/"+gpt.ID+"/clear-price-override", map[string]any{}), 200)
		time.Sleep(5 * time.Millisecond)
		again := decode[adminapi.PriceImport](t, c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded"}), 201)
		if again.VersionsCreated != 1 {
			t.Errorf("the second import created %d versions, want 1 (gpt-4o, after the clearing)", again.VersionsCreated)
		}
		got := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", "/v1/models/"+gpt.ID+"/prices", nil), 200)
		if len(got.Items) != 2 || got.Items[0].Source != adminapi.PriceSourceImport || got.Items[1].Source != adminapi.PriceSourceOverride {
			t.Errorf("after the clearing and an import: %+v", got.Items)
		}
	})
}

func manyRates(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"category": "input", "threshold": i, "rate": "1"}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// TestPriceImportAPI is T-404: runPriceImport with the embedded snapshot and with a download, its
// report, and its refusals.
func TestPriceImportAPI(t *testing.T) {
	t.Run("embedded", func(t *testing.T) {
		c := newCat(t)
		models := func() int {
			var n int
			if err := c.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.model`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		versions := func() int {
			var n int
			if err := c.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.price_version`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := models()

		// A dry run reports and writes nothing.
		dry := decode[adminapi.PriceImport](t, c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded", "dry_run": true}), 201)
		if !dry.DryRun || dry.Source != adminapi.PriceImportSourceEmbedded || dry.Outcome != "success" || dry.DatasetFormat != "genai-prices-v2" || dry.ModelsSeen != 17 || dry.VersionsCreated < 15 || len(dry.Items) != 17 {
			t.Errorf("dry run: %+v", dry)
		}
		if dry.ID != "" {
			t.Errorf("a dry run has an import ID: %s", dry.ID)
		}
		if versions() != 0 || len(decode[adminapi.PriceImportList](t, c.platform(t, "GET", "/v1/price-imports", nil), 200).Items) != 0 {
			t.Error("a dry run wrote something")
		}

		imp := decode[adminapi.PriceImport](t, c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded"}), 201)
		if imp.DryRun || imp.ID == "" || imp.Source != adminapi.PriceImportSourceEmbedded || imp.Outcome != "success" || imp.ModelsSeen != 17 || imp.VersionsCreated != dry.VersionsCreated || imp.Digest == "" || len(imp.Digest) != 64 ||
			imp.ActorType != "platform" || imp.StartedAt.IsZero() || imp.CreatedAt == nil {
			t.Errorf("import: %+v", imp)
		}
		if sum := imp.VersionsCreated + imp.Unchanged + imp.Overridden + imp.Unmatched + imp.Skipped; sum != imp.ModelsSeen {
			t.Errorf("the counts add up to %d, models seen %d", sum, imp.ModelsSeen)
		}
		created := 0
		for _, it := range imp.Items {
			if it.Provider == "" || it.Model == "" || it.Outcome == "" {
				t.Errorf("an incomplete item: %+v", it)
			}
			if it.Outcome == "created" {
				created++
			}
		}
		if created != imp.VersionsCreated {
			t.Errorf("%d items created, %d versions", created, imp.VersionsCreated)
		}
		if models() != before {
			t.Errorf("an import created models: %d, was %d", models(), before)
		}
		if versions() != imp.VersionsCreated {
			t.Errorf("%d versions in the database, the import says %d", versions(), imp.VersionsCreated)
		}
		gpt := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider=openai&name=gpt-4o", nil), 200).Items[0]
		pv := decode[adminapi.PriceVersionList](t, c.platform(t, "GET", "/v1/models/"+gpt.ID+"/prices", nil), 200).Items
		if len(pv) != 1 || pv[0].Source != adminapi.PriceSourceImport || pv[0].ImportID != imp.ID || pv[0].Currency != "USD" {
			t.Fatalf("gpt-4o: %+v", pv)
		}
		hasInput := false
		for _, r := range pv[0].Rates {
			hasInput = hasInput || (r.Category == adminapi.PriceCategoryInput && r.Threshold == 0 && r.Rate != "")
		}
		if !hasInput {
			t.Errorf("no input rate: %+v", pv[0].Rates)
		}

		// The same snapshot again: nothing new.
		again := decode[adminapi.PriceImport](t, c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded"}), 201)
		if again.VersionsCreated != 0 || again.Unchanged != imp.VersionsCreated || again.ID == imp.ID {
			t.Errorf("the second import: %+v", again)
		}
		if versions() != imp.VersionsCreated {
			t.Error("the second import created versions")
		}

		// get has the report, the lists do not; the list is newest first.
		got := decode[adminapi.PriceImport](t, c.platform(t, "GET", "/v1/price-imports/"+imp.ID, nil), 200)
		if got.ID != imp.ID || len(got.Items) != len(imp.Items) || got.VersionsCreated != imp.VersionsCreated || got.Digest != imp.Digest {
			t.Errorf("get: %d items, %+v", len(got.Items), got)
		}
		list := decode[adminapi.PriceImportList](t, c.platform(t, "GET", "/v1/price-imports", nil), 200)
		if len(list.Items) != 2 || list.Items[0].ID != again.ID || list.Items[1].ID != imp.ID {
			t.Fatalf("the list: %+v", list.Items)
		}
		for _, it := range list.Items {
			if len(it.Items) != 0 {
				t.Errorf("a list item carries the report: %+v", it)
			}
		}
		page := decode[adminapi.PriceImportList](t, c.platform(t, "GET", "/v1/price-imports?limit=1", nil), 200)
		if len(page.Items) != 1 || page.NextCursor == "" {
			t.Errorf("page: %+v", page)
		} else if rest := decode[adminapi.PriceImportList](t, c.platform(t, "GET", "/v1/price-imports?limit=1&cursor="+url.QueryEscape(page.NextCursor), nil), 200); len(rest.Items) != 1 || rest.Items[0].ID != imp.ID {
			t.Errorf("second page: %+v", rest)
		}
		expect(t, c.platform(t, "GET", "/v1/price-imports/0192f0f0-0000-7000-8000-00000000beef", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "GET", "/v1/price-imports/not-an-id", nil), 404, adminapi.ProblemCodeNotFound)

		// The body.
		for name, body := range map[string]any{
			"the file source":            map[string]any{"source": "file"},
			"no source":                  map[string]any{"dry_run": true},
			"an unknown source":          map[string]any{"source": "ftp"},
			"a url":                      map[string]any{"source": "embedded", "url": "https://example.com/data.json"},
			"a dry_run that is a string": map[string]any{"source": "embedded", "dry_run": "yes"},
			"a null source":              map[string]any{"source": nil},
		} {
			if r := c.platform(t, "POST", "/v1/price-imports", body); r.Status != 400 {
				t.Errorf("%s: %d\n%s", name, r.Status, r.Body)
			}
		}
		// A remote import while the download is off is refused on the member.
		invalidOn(t, "remote while refresh is disabled", c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "remote"}), "source")
	})

	t.Run("remote", func(t *testing.T) {
		var mu sync.Mutex
		var asked []string
		body := fixtureBytes(t, "changed.json")
		fail := error(nil)
		c := newCat(t, func(o *admin.Options) {
			o.Refresh.Enabled = true
			o.Refresh.URL = "https://prices.example.com/dir/data.json"
			o.Refresh.Fetch = func(_ context.Context, u string) ([]byte, error) {
				mu.Lock()
				defer mu.Unlock()
				asked = append(asked, u)
				if fail != nil {
					return nil, fail
				}
				return body, nil
			}
		})
		versions := func() int {
			var n int
			if err := c.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.price_version`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		imp := decode[adminapi.PriceImport](t, c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "remote"}), 201)
		mu.Lock()
		got := append([]string(nil), asked...)
		mu.Unlock()
		if len(got) != 1 || got[0] != "https://prices.example.com/dir/data.json" {
			t.Errorf("the download was asked for %v: only the URL of the configuration may be fetched", got)
		}
		if imp.Source != adminapi.PriceImportSourceRemote || imp.Outcome != "success" || !strings.Contains(imp.SourceRef, "prices.example.com") || imp.VersionsCreated != 2 || imp.Unmatched != 15 {
			t.Errorf("remote import of changed.json: %+v (gpt-4o and o4-mini are in the document, the other 15 seeded models are not)", imp)
		}
		if versions() != 2 {
			t.Errorf("%d versions", versions())
		}
		// A body that names a URL is not accepted: the destination is configuration only.
		if r := c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "remote", "url": "https://evil.example.com/data.json"}); r.Status != 400 {
			t.Errorf("a URL in the body: %d", r.Status)
		}
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if n != 1 {
			t.Errorf("a refused request downloaded something: %d downloads", n)
		}

		// A dry run downloads and reports, and writes nothing.
		dry := decode[adminapi.PriceImport](t, c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "remote", "dry_run": true}), 201)
		if !dry.DryRun || dry.VersionsCreated != 0 || dry.Unchanged != 2 || versions() != 2 {
			t.Errorf("remote dry run: %+v", dry)
		}

		// Each failure class is 502, with the fixed text and nothing of the cause; nothing is imported.
		before := c.api.Snapshot(t, "audit_event", "platform_audit_event", "price_import")
		for _, class := range []string{"unreachable", "timeout", "status", "redirect", "too_large"} {
			mu.Lock()
			fail = &catalog.FetchError{Class: class}
			mu.Unlock()
			r := c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "remote"})
			p := expect(t, r, 502, adminapi.ProblemCodePriceSourceUnavailable)
			if p.Title != "Price source unavailable" || p.Detail != "The price dataset could not be downloaded. Nothing was imported." || len(p.Errors) != 0 {
				t.Errorf("%s: %+v", class, p)
			}
			if strings.Contains(string(r.Body), class) && class != "timeout" && class != "status" && class != "redirect" {
				// the class belongs to the log and to the import row, not to the response
				t.Errorf("%s: the response names the class: %s", class, r.Body)
			}
		}
		if after := c.api.Snapshot(t, "audit_event", "platform_audit_event", "price_import"); after != before {
			t.Error("a failed download changed a table other than the import log and the audit log")
		}
		if versions() != 2 {
			t.Errorf("a failed download created versions: %d", versions())
		}
		mu.Lock()
		fail = nil
		mu.Unlock()

		// A document that cannot be read is 422, and no byte of it is quoted anywhere.
		const marker = "synthetic-marker-in-the-dataset"
		for name, doc := range map[string][]byte{
			"an object":        []byte(`{"not": "` + marker + `"}`),
			"not JSON":         []byte(marker),
			"a wrong type":     []byte(`[{"id": "openai", "models": "` + marker + `"}]`),
			"the invalid file": fixtureBytes(t, "invalid.json"),
		} {
			mu.Lock()
			body = doc
			mu.Unlock()
			r := c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "remote"})
			p := expect(t, r, 422, adminapi.ProblemCodePriceDatasetInvalid)
			if p.Title != "Price dataset invalid" || p.Detail != "The price dataset could not be read. Nothing was imported." {
				t.Errorf("%s: %+v", name, p)
			}
			testutil.AssertAbsent(t, name+" response", string(r.Body), marker)
			testutil.AssertAbsent(t, name+" log", c.api.Logs.String(), marker)
		}
		if versions() != 2 {
			t.Errorf("an invalid document created versions: %d", versions())
		}
		// And the report of an import never holds dataset text beyond identifiers.
		list := decode[adminapi.PriceImportList](t, c.platform(t, "GET", "/v1/price-imports", nil), 200)
		raw, _ := json.Marshal(list)
		testutil.AssertAbsent(t, "the import list", string(raw), marker)
	})
}
