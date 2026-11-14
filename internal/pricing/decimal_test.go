package pricing_test

import (
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/pricing"
)

func mustDec(t testing.TB, s string) pricing.Decimal {
	t.Helper()
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", s, err)
	}
	return d
}

// TestDecimal is T-370: parsing, canonical text and exact arithmetic of pricing.Decimal.
func TestDecimal(t *testing.T) {
	t.Run("parse and canonical text", func(t *testing.T) {
		for in, want := range map[string]string{
			"0":                                "0",
			"-0":                               "0",
			"0.0":                              "0",
			"0.000":                            "0",
			"-0.00":                            "0",
			"1":                                "1",
			"1.0":                              "1",
			"1.50":                             "1.5",
			"1.500000":                         "1.5",
			"-1.50":                            "-1.5",
			"0.5":                              "0.5",
			"0.000000000001":                   "0.000000000001",
			"2.45":                             "2.45",
			"100":                              "100",
			"100.0":                            "100",
			"1000000":                          "1000000",
			"123456789012345678901234567890":   "123456789012345678901234567890",
			"0.123456789012345678901234567890": "0.12345678901234567890123456789",
			"10.10":                            "10.1",
		} {
			d, err := pricing.ParseDecimal(in)
			if err != nil {
				t.Errorf("ParseDecimal(%q): %v", in, err)
				continue
			}
			if got := d.String(); got != want {
				t.Errorf("ParseDecimal(%q).String() = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("refusals", func(t *testing.T) {
		digits60 := strings.Repeat("9", 60)
		for name, in := range map[string]string{
			"empty":                 "",
			"exponent":              "1e3",
			"upper exponent":        "1E-3",
			"exponent after point":  "1.5e2",
			"plus sign":             "+1",
			"NaN":                   "NaN",
			"nan lower":             "nan",
			"Inf":                   "Inf",
			"minus Inf":             "-Infinity",
			"leading space":         " 1",
			"trailing space":        "1 ",
			"inner space":           "1 000",
			"newline":               "1\n",
			"no integer part":       ".5",
			"no fraction digits":    "1.",
			"only a point":          ".",
			"only a sign":           "-",
			"two points":            "1.2.3",
			"comma":                 "1,5",
			"leading zero":          "01",
			"leading zeros":         "007.5",
			"hexadecimal":           "0x10",
			"letters":               "abc",
			"unicode digits":        "١٢",
			"double sign":           "--1",
			"31 fraction digits":    "0." + strings.Repeat("1", 31),
			"61 digits":             "1" + digits60,
			"61 digits with point":  "1" + digits60[:30] + "." + digits60[:30],
			"sign only after digit": "1-",
		} {
			if d, err := pricing.ParseDecimal(in); err == nil {
				t.Errorf("%s: ParseDecimal(%q) = %s, want an error", name, in, d)
			} else if in != "" && len(in) > 3 && strings.Contains(err.Error(), in) {
				t.Errorf("%s: the error quotes the input: %v", name, err)
			}
		}
		// The bounds themselves are accepted.
		if _, err := pricing.ParseDecimal("0." + strings.Repeat("1", 30)); err != nil {
			t.Errorf("30 fraction digits: %v", err)
		}
		if _, err := pricing.ParseDecimal(digits60); err != nil {
			t.Errorf("60 digits: %v", err)
		}
	})

	t.Run("Add", func(t *testing.T) {
		for _, c := range []struct{ a, b, want string }{
			{"0", "0", "0"},
			{"1", "2", "3"},
			{"0.1", "0.2", "0.3"},
			{"0.000001", "0.000009", "0.00001"},
			{"1.5", "-1.5", "0"},
			{"-1", "-2.25", "-3.25"},
			{"999999999999999999", "1", "1000000000000000000"},
			{"0.000000000000000001", "0.000000000000000002", "0.000000000000000003"},
			{"2.5", "0.125", "2.625"},
		} {
			if got := mustDec(t, c.a).Add(mustDec(t, c.b)).String(); got != c.want {
				t.Errorf("%s + %s = %s, want %s", c.a, c.b, got, c.want)
			}
		}
		var zero pricing.Decimal
		if got := zero.Add(mustDec(t, "1.25")).String(); got != "1.25" {
			t.Errorf("zero value + 1.25 = %s", got)
		}
		if zero.String() != "0" || zero.Sign() != 0 {
			t.Errorf("the zero value is %q, sign %d", zero.String(), zero.Sign())
		}
	})

	t.Run("values are immutable", func(t *testing.T) {
		a, b := mustDec(t, "1.5"), mustDec(t, "2.5")
		_ = a.Add(b)
		_ = a.MulInt(7)
		_ = a.DivPow10(3)
		_ = a.Round(0)
		if a.String() != "1.5" || b.String() != "2.5" {
			t.Errorf("a method changed its receiver or argument: %s %s", a, b)
		}
	})

	t.Run("MulInt", func(t *testing.T) {
		for _, c := range []struct {
			a    string
			n    int64
			want string
		}{
			{"0.5", 3, "1.5"},
			{"2.45", 1000000, "2450000"},
			{"2.45", 0, "0"},
			{"-2.5", 4, "-10"},
			{"2.5", -4, "-10"},
			{"0.000000000001", 1, "0.000000000001"},
			{"1000000", 9223372036854775807, "9223372036854775807000000"},
			{"0.1", 3, "0.3"},
		} {
			if got := mustDec(t, c.a).MulInt(c.n).String(); got != c.want {
				t.Errorf("%s * %d = %s, want %s", c.a, c.n, got, c.want)
			}
		}
	})

	t.Run("DivPow10 is exact", func(t *testing.T) {
		for _, c := range []struct {
			a    string
			n    int
			want string
		}{
			{"1", 6, "0.000001"},
			{"2450000", 6, "2.45"},
			{"1", 0, "1"},
			{"0", 6, "0"},
			{"-5", 1, "-0.5"},
			{"0.000000000001", 6, "0.000000000000000001"},
			{"1", 18, "0.000000000000000001"},
			{"123456789", 9, "0.123456789"},
			{"1500", 3, "1.5"},
		} {
			if got := mustDec(t, c.a).DivPow10(c.n).String(); got != c.want {
				t.Errorf("%s / 10^%d = %s, want %s", c.a, c.n, got, c.want)
			}
		}
	})

	t.Run("Round is half away from zero at every place", func(t *testing.T) {
		for _, c := range []struct {
			a      string
			places int
			want   string
		}{
			{"2.5", 0, "3"},
			{"-2.5", 0, "-3"},
			{"2.4", 0, "2"},
			{"-2.4", 0, "-2"},
			{"2.6", 0, "3"},
			{"-2.6", 0, "-3"},
			{"0.5", 0, "1"},
			{"-0.5", 0, "-1"},
			{"0.49", 0, "0"},
			{"-0.49", 0, "0"},
			{"1.005", 2, "1.01"},
			{"-1.005", 2, "-1.01"},
			{"1.004", 2, "1"},
			{"1.0049999", 2, "1"},
			{"0.0000005", 6, "0.000001"},
			{"0.0000004", 6, "0"},
			{"0.000000000000000001", 18, "0.000000000000000001"},
			{"0.0000000000000000005", 18, "0.000000000000000001"},
			{"0.0000000000000000004", 18, "0"},
			{"123.456", 5, "123.456"},
			{"123.456", 3, "123.456"},
			{"123.456", 2, "123.46"},
			{"123.456", 1, "123.5"},
			{"123.456", 0, "123"},
			{"9.995", 2, "10"},
			{"99.5", 0, "100"},
			{"0", 3, "0"},
		} {
			if got := mustDec(t, c.a).Round(c.places).String(); got != c.want {
				t.Errorf("Round(%s, %d) = %s, want %s", c.a, c.places, got, c.want)
			}
		}
	})

	t.Run("Cmp and Sign", func(t *testing.T) {
		for _, c := range []struct {
			a, b string
			want int
		}{
			{"1", "1.0", 0},
			{"1", "2", -1},
			{"2", "1", 1},
			{"-1", "1", -1},
			{"0.1", "0.01", 1},
			{"0", "-0", 0},
			{"-0.5", "-0.25", -1},
			{"1000000", "999999.999999999999", 1},
		} {
			if got := mustDec(t, c.a).Cmp(mustDec(t, c.b)); got != c.want {
				t.Errorf("Cmp(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
			}
		}
		for in, want := range map[string]int{"0": 0, "-0.0": 0, "0.000001": 1, "-0.000001": -1, "5": 1} {
			if got := mustDec(t, in).Sign(); got != want {
				t.Errorf("Sign(%s) = %d, want %d", in, got, want)
			}
		}
	})

	t.Run("text round trip", func(t *testing.T) {
		for _, in := range []string{"0", "1", "-1", "2.45", "0.000000000001", "123456789.123456789", "-0.5"} {
			d := mustDec(t, in)
			b, err := d.MarshalText()
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != in {
				t.Errorf("MarshalText(%s) = %q", in, b)
			}
			var back pricing.Decimal
			if err := back.UnmarshalText(b); err != nil {
				t.Errorf("UnmarshalText(%q): %v", b, err)
			}
			if back.Cmp(d) != 0 || back.String() != in {
				t.Errorf("round trip of %s gave %s", in, back)
			}
		}
		var d pricing.Decimal
		for _, bad := range []string{"", "1e3", "abc", " 1", "1."} {
			if err := d.UnmarshalText([]byte(bad)); err == nil {
				t.Errorf("UnmarshalText(%q) accepted", bad)
			}
		}
	})
}

// FuzzParseDecimal is the fuzz part of T-370: whatever parses, prints a text that parses to the
// same value and prints the same text.
func FuzzParseDecimal(f *testing.F) {
	for _, s := range []string{"0", "-0", "1", "1.50", "0.000000000001", "-12.5", "", "1e3", "+1", ".5", "1.", "01", "999999999999999999999.999999999999",
		strings.Repeat("9", 60), "0." + strings.Repeat("1", 30), "0." + strings.Repeat("1", 31), "١٢", "1 ", "NaN"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := pricing.ParseDecimal(s)
		if err != nil {
			return
		}
		text := d.String()
		e, err := pricing.ParseDecimal(text)
		if err != nil {
			t.Fatalf("%q parsed, but its text %q does not: %v", s, text, err)
		}
		if e.String() != text || e.Cmp(d) != 0 {
			t.Fatalf("%q -> %q -> %q", s, text, e.String())
		}
		if strings.ContainsAny(text, "eE+ ") || strings.HasSuffix(text, ".") || (strings.Contains(text, ".") && strings.HasSuffix(text, "0")) {
			t.Fatalf("%q printed as %q, which is not canonical", s, text)
		}
		if text == "-0" {
			t.Fatalf("%q printed as -0", s)
		}
	})
}
