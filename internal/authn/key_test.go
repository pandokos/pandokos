package authn

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
)

var (
	reKey    = regexp.MustCompile(`^` + Pattern + `$`)
	alphabet = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
)

// TestGenerateAndParse is T-205.
func TestGenerateAndParse(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		kind := KindTenant
		if i%2 == 1 {
			kind = KindPlatform
		}
		key, keyID, hash, err := Generate(kind)
		if err != nil {
			t.Fatal(err)
		}
		s := key.Reveal()
		if len(s) != 81 || !reKey.MatchString(s) {
			t.Fatalf("key %d has length %d or does not match the pattern", i, len(s))
		}
		if s[4] != byte(kind) || keyID != s[4:21] || len(keyID) != 17 {
			t.Fatalf("key ID %q, kind letter %q", keyID, s[4])
		}
		if s[:4] != "pdk_" || s[21] != '_' {
			t.Fatalf("prefix or separator wrong in key %d", i)
		}
		if seen[s] {
			t.Fatal("a key was generated twice")
		}
		seen[s] = true

		raw, err := alphabet.DecodeString(s[22:74])
		if err != nil || len(raw) != 32 {
			t.Fatalf("the secret does not decode to 32 bytes: %v", err)
		}
		if want := sha256.Sum256(raw); hash != want {
			t.Fatal("the returned hash is not the SHA-256 of the 32 secret bytes")
		}
		gotID, gotKind, gotHash, err := Parse(s)
		if err != nil || gotID != keyID || gotKind != kind || gotHash != hash {
			t.Fatalf("Parse = %q %c %x %v", gotID, gotKind, gotHash, err)
		}
	}
	key, _, _, err := Generate(KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.TypeOf(key) != reflect.TypeOf(secret.Secret{}) {
		t.Errorf("the key is a %T, want secret.Secret", key)
	}
	if got := fmt.Sprintf("%v %+v %s", key, key, key); strings.Contains(got, "pdk_") {
		t.Errorf("formatting a key prints it: %s", got)
	}
}

// TestParseRejects is T-206.
func TestParseRejects(t *testing.T) {
	key, _, _, err := Generate(KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	good := key.Reveal()
	if _, _, _, err := Parse(good); err != nil {
		t.Fatal(err)
	}
	mutate := func(i int, c byte) string { b := []byte(good); b[i] = c; return string(b) }
	cases := map[string]string{
		"empty":             "",
		"too short":         good[:80],
		"too long":          good + "a",
		"wrong prefix":      "pdx_" + good[4:],
		"upper-case prefix": "PDK_" + good[4:],
		"wrong kind letter": mutate(4, 'x'),
		"upper-case kind":   mutate(4, 'T'),
		"missing separator": mutate(21, 'a'),
		"wrong separator":   mutate(21, '-'),
		"first separator":   mutate(3, '-'),
		"digit 0":           mutate(30, '0'),
		"digit 1":           mutate(30, '1'),
		"digit 8":           mutate(30, '8'),
		"upper-case":        strings.ToUpper(good[:30]) + good[30:],
		"space":             mutate(40, ' '),
		"padding":           mutate(40, '='),
		"non-ascii":         good[:40] + "é" + good[42:],
		"bearer prefix":     "Bearer " + good,
		"newline":           good + "\n",
	}
	for name, in := range cases {
		if _, _, _, err := Parse(in); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
	// One changed character anywhere after the prefix is caught by the checksum, the checksum's
	// own characters included.
	for i := 4; i < len(good); i++ {
		if i == 21 {
			continue
		}
		next := byte('a')
		if good[i] == 'a' {
			next = 'b'
		}
		for _, c := range []byte{next, '2', 'z'} {
			if c == good[i] {
				continue
			}
			if _, _, _, err := Parse(mutate(i, c)); !errors.Is(err, ErrMalformed) {
				t.Errorf("changing character %d to %c: err = %v, want ErrMalformed", i, c, err)
			}
		}
	}
	// The error never contains what was presented.
	_, _, _, err = Parse(mutate(40, '!'))
	if err == nil || strings.Contains(err.Error(), good[22:50]) {
		t.Errorf("the error repeats the input: %v", err)
	}
}

// FuzzParse is T-207: Parse never panics and accepts only what matches the pattern.
func FuzzParse(f *testing.F) {
	key, _, _, err := Generate(KindPlatform)
	if err != nil {
		f.Fatal(err)
	}
	good := key.Reveal()
	for _, seed := range []string{"", good, good[:80], good + "x", "pdk_", strings.Repeat("a", 81), "pdk_t" + strings.Repeat("a", 76), strings.ToUpper(good)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, kind, _, err := Parse(s)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("an error that is not ErrMalformed: %v", err)
			}
			return
		}
		if !reKey.MatchString(s) {
			t.Fatalf("Parse accepted a string that does not match the pattern")
		}
		if kind != KindTenant && kind != KindPlatform {
			t.Fatalf("kind %c", kind)
		}
	})
}
