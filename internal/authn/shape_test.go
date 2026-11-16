package authn

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeidp"
)

// segs returns a string of three segments of n characters of the base64url alphabet.
func segs(n int) string {
	s := strings.Repeat("a", n)
	return s + "." + s + "." + s
}

// TestCredentialShape is T-568: the table of section 5.2. A value that starts with pdk_ never
// reaches the token path; a token is recognized by its shape alone, with the limits of the
// configuration; with tokens disabled nothing but a key is accepted.
func TestCredentialShape(t *testing.T) {
	const max = 64
	idp := fakeidp.Start(t, fakeidp.Options{})
	jwt := idp.Sign("ES256", idp.StandardAt(tokenNow, "workload", "aud", time.Minute))
	if len(jwt) <= max {
		t.Fatalf("the sample token has %d bytes: the limit of the table must be below it", len(jwt))
	}

	t.Run("what looks like a token", func(t *testing.T) {
		pad := func(n int) string { // exactly n bytes: three segments
			rest := n - 2
			a, b := rest/3, rest/3
			return strings.Repeat("a", a) + "." + strings.Repeat("b", b) + "." + strings.Repeat("c", rest-a-b)
		}
		cases := []struct {
			name string
			in   string
			want bool
		}{
			{"three segments", "aaaa.bbbb.cccc", true},
			{"the alphabet", "AZaz09-_.AZaz09-_.AZaz09-_", true},
			{"at the limit", pad(max), true},
			{"one byte over the limit", pad(max + 1), false},
			{"empty", "", false},
			{"two segments", "aaaa.bbbb", false},
			{"four segments", "aaaa.bbbb.cccc.dddd", false},
			{"an empty first segment", ".bbbb.cccc", false},
			{"an empty middle segment", "aaaa..cccc", false},
			{"an empty signature", "aaaa.bbbb.", false},
			{"padding", "aaaa.bbbb.cc==", false},
			{"the standard alphabet: plus", "aa+a.bbbb.cccc", false},
			{"the standard alphabet: slash", "aa/a.bbbb.cccc", false},
			{"a space", "aaaa.bb bb.cccc", false},
			{"a newline", "aaaa.bbbb.cccc\n", false},
			{"a tab", "aaaa.\tbbbb.cccc", false},
			{"a non-ASCII letter", "aaaa.bbbé.cccc", false},
			{"a NUL", "aaaa.bb\x00b.cccc", false},
			{"a bearer prefix", "Bearer aaaa.bbbb.cccc", false},
			{"JWE shape: five segments", "aaaa.bbbb.cccc.dddd.eeee", false},
		}
		for _, c := range cases {
			if got := looksLikeToken(c.in, max); got != c.want {
				t.Errorf("%s: looksLikeToken = %v, want %v", c.name, got, c.want)
			}
		}
		if !looksLikeToken(jwt, 8192) {
			t.Error("a real token does not look like a token")
		}
		if !looksLikeToken(pad(8192), 8192) || looksLikeToken(pad(8193), 8192) {
			t.Error("the default limit of 8192 bytes is not a boundary")
		}
	})

	// Authenticate, without a database: every case is decided before one is needed. A value that
	// reaches the token path with an algorithm that is not allowed answers algorithm_not_allowed
	// (check 2) instead of malformed, which is how the table tells the two paths apart.
	reasonOf := func(a *Authenticator, presented string) string {
		t.Helper()
		_, err := a.Authenticate(context.Background(), presented)
		if err == nil {
			t.Fatalf("%.30q was authenticated", presented)
		}
		f := refusal(t, "Authenticate", err)
		return f.Reason
	}
	enabled := &Authenticator{kinds: map[Kind]bool{KindToken: true}, now: time.Now, tok: TokenOptions{MaxBytes: 4096}}
	disabled := &Authenticator{kinds: map[Kind]bool{KindTenant: true}, now: time.Now}
	// A token whose algorithm is not allowed: the token path refuses it at check 2, before any
	// registration is looked up.
	none := fakeidp.RawJSON([]byte(`{"alg":"none"}`), []byte(`{"iss":"x"}`), nil)
	none = strings.TrimSuffix(none, ".") + ".c2ln" // a non-empty signature: three non-empty segments

	t.Run("a token takes the token path", func(t *testing.T) {
		if got := reasonOf(enabled, none); got != "algorithm_not_allowed" {
			t.Errorf("enabled: reason %q, want algorithm_not_allowed (the token path)", got)
		}
	})
	t.Run("with tokens disabled it is malformed", func(t *testing.T) {
		if got := reasonOf(disabled, none); got != "malformed" {
			t.Errorf("disabled: reason %q, want malformed", got)
		}
		if got := reasonOf(disabled, jwt); got != "malformed" {
			t.Errorf("disabled, a real token: reason %q, want malformed", got)
		}
	})
	t.Run("a value that starts with pdk_ never reaches the token path", func(t *testing.T) {
		for _, in := range []string{
			"pdk_" + none,        // a token behind the prefix
			"pdk_" + jwt,         // three segments, a valid shape for the token path
			"pdk_aaaa.bbbb.cccc", // three segments
			"pdk_",               // nothing after the prefix
			"pdk_nonsense",       // not a key
			"pdk_t" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52), // the shape of a key with a wrong body
		} {
			if got := reasonOf(enabled, in); got != "malformed" && got != "unknown_key" && got != "kind_not_accepted" {
				t.Errorf("%.40q on an authenticator with tokens: reason %q, want a key reason", in, got)
			}
			if got := reasonOf(enabled, in); got == "algorithm_not_allowed" || got == "unknown_issuer" {
				t.Errorf("%.40q reached the token path: %q", in, got)
			}
		}
	})
	t.Run("anything else is malformed", func(t *testing.T) {
		for _, in := range []string{"", " ", "Bearer", "aaaa", "aaaa.bbbb", "not a token at all", "aaaa.bbbb.cccc.dddd", "sk-" + strings.Repeat("a", 40), segs(2000)} {
			if got := reasonOf(enabled, in); got != "malformed" {
				t.Errorf("%.30q: reason %q, want malformed", in, got)
			}
		}
	})
	t.Run("the size limit is the configured one", func(t *testing.T) {
		small := &Authenticator{kinds: map[Kind]bool{KindToken: true}, now: time.Now, tok: TokenOptions{MaxBytes: len(none)}}
		if got := reasonOf(small, none); got != "algorithm_not_allowed" {
			t.Errorf("at the limit: reason %q, want algorithm_not_allowed", got)
		}
		smaller := &Authenticator{kinds: map[Kind]bool{KindToken: true}, now: time.Now, tok: TokenOptions{MaxBytes: len(none) - 1}}
		if got := reasonOf(smaller, none); got != "malformed" {
			t.Errorf("one byte over: reason %q, want malformed", got)
		}
	})
}
