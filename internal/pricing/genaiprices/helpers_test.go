package genaiprices_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
)

// today is the UTC day the extraction tests run on, far from every constraint of the fixtures.
var today = time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "fixtures", "prices", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseFixture(t testing.TB, name string) *genaiprices.Dataset {
	t.Helper()
	d, err := genaiprices.Parse(fixture(t, name))
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return d
}

// ratesOf renders the rates of a version as sorted "category@threshold=rate" strings.
func ratesOf(v pricing.Version) []string {
	var out []string
	for _, r := range v.Rates {
		out = append(out, string(r.Category)+"@"+itoa(r.Threshold)+"="+r.PerMillion.String())
	}
	sort.Strings(out)
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// extract finds the model of a fixture and extracts its version.
func extract(t testing.TB, d *genaiprices.Dataset, provider, upstream string, on time.Time) (pricing.Version, []genaiprices.Issue, error) {
	t.Helper()
	m, ok := d.Find(provider, upstream)
	if !ok {
		t.Fatalf("no model matches %s/%s", provider, upstream)
	}
	return genaiprices.Extract(m, on)
}

func reasons(is []genaiprices.Issue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Reason)
	}
	sort.Strings(out)
	return out
}

func details(is []genaiprices.Issue, reason string) []string {
	var out []string
	for _, i := range is {
		if i.Reason == reason {
			out = append(out, i.Detail)
		}
	}
	sort.Strings(out)
	return out
}
