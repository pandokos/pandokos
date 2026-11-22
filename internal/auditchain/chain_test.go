package auditchain_test

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/auditchain"
)

var update = flag.Bool("update", false, "rewrite testdata/canonical.golden from the SQL function pandokos.audit_canonical (needs the test database)")

// canonicalCases are the details of T-936: nested objects and arrays, keys that sort differently by
// bytes and by code point, escapes, an empty object and array, numbers as jsonb stores them,
// booleans and null. The input is the text a client writes; the golden file holds the text jsonb
// stores and the encoding the SQL function gives.
var canonicalCases = []struct{ name, in string }{
	{"empty-object", `{}`},
	{"empty-array", `[]`},
	{"null", `null`},
	{"true", `true`},
	{"false", `false`},
	{"integer", `1`},
	{"decimal-keeps-zeros", `1.50`},
	{"negative-decimal", `-0.5`},
	{"exponent-as-stored", `1e3`},
	{"string", `"pandokos"`},
	{"escapes", `"a\"b\\c\nd\te\u0001f"`},
	{"unicode", `"héllo 世界"`},
	{"key-order", `{"B": 1, "a": 2, "é": 3, "ab": 4, "": 5}`},
	{"nested", `{"z": [1, {"y": null, "x": [true, false, []]}, "s"], "a": {"b": {"c": {}}}}`},
	{"rates", `{"rates": [{"category": "input", "rate": "3.00", "threshold": 0}], "currency": "EUR"}`},
}

func readGolden(t *testing.T) map[string][2]string {
	t.Helper()
	f, err := os.Open("testdata/canonical.golden")
	if err != nil {
		t.Fatalf("the golden file is missing (run with -update and the test database): %v", err)
	}
	defer f.Close()
	out := map[string][2]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("golden line %q: want name, stored JSON and hex", line)
		}
		out[parts[0]] = [2]string{parts[1], parts[2]}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCanonicalEncoding is T-936, the Go half: C(v) of the details above equals the golden bytes
// (written from the SQL function), and a handful of encodings written here by hand from the
// definition of section 5.6.
func TestCanonicalEncoding(t *testing.T) {
	t.Run("by hand", func(t *testing.T) {
		for _, c := range []struct{ name, in, want string }{
			{"null", `null`, "7a"},
			{"true", `true`, "74"},
			{"false", `false`, "66"},
			{"empty object", `{}`, "6f00000000"},
			{"empty array", `[]`, "6100000000"},
			{"integer", `1`, "6e" + "00000001" + "31"},
			{"decimal", `1.50`, "6e" + "00000004" + hex.EncodeToString([]byte("1.50"))},
			{"string", `"ab"`, "73" + "00000002" + "6162"},
			{"object", `{"b": 1, "a": null}`, "6f00000002" + "00000001" + "61" + "7a" + "00000001" + "62" + "6e0000000131"},
			{"array", `[true, "x"]`, "6100000002" + "74" + "7300000001" + "78"},
			{"keys by bytes", `{"B": 1, "a": 2, "é": 3}`, "6f00000003" +
				"00000001" + "42" + "6e0000000131" +
				"00000001" + "61" + "6e0000000132" +
				"00000002" + "c3a9" + "6e0000000133"},
		} {
			got, err := auditchain.Canonical([]byte(c.in))
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if hex.EncodeToString(got) != c.want {
				t.Errorf("%s: C = %x, want %s", c.name, got, c.want)
			}
		}
	})

	t.Run("golden", func(t *testing.T) {
		golden := readGolden(t)
		for _, c := range canonicalCases {
			g, ok := golden[c.name]
			if !ok {
				t.Errorf("%s: no golden line", c.name)
				continue
			}
			got, err := auditchain.Canonical([]byte(g[0]))
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if hex.EncodeToString(got) != g[1] {
				t.Errorf("%s: C(%s) = %x, want %s", c.name, g[0], got, g[1])
			}
		}
	})

	t.Run("one definition for the digest", func(t *testing.T) {
		c, err := auditchain.Canonical([]byte(`{"a": [1, 2]}`))
		if err != nil {
			t.Fatal(err)
		}
		d, err := auditchain.DetailDigest([]byte(`{"a": [1, 2]}`))
		if err != nil {
			t.Fatal(err)
		}
		if want := sha256Sum(c); d != want {
			t.Errorf("DetailDigest = %x, want SHA-256 of C: %x", d, want)
		}
	})

	t.Run("a float never enters", func(t *testing.T) {
		// 0.1 + 0.2 style text and a 40-digit integer keep their text.
		for _, in := range []string{`0.30000000000000004`, `12345678901234567890123456789012345678901234567890`, `1.000000000000000001`} {
			got, err := auditchain.Canonical([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			want := append([]byte{'n', 0, 0, 0, byte(len(in))}, in...)
			if !bytes.Equal(got, want) {
				t.Errorf("C(%s) = %x, want %x", in, got, want)
			}
		}
	})

	t.Run("invalid JSON is an error", func(t *testing.T) {
		for _, in := range []string{``, `{`, `{"a":}`, `[1,`, `nul`} {
			if _, err := auditchain.Canonical([]byte(in)); err == nil {
				t.Errorf("C(%q) succeeded", in)
			}
		}
	})
}
