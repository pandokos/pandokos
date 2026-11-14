package vault_test

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
)

var reFingerprint = regexp.MustCompile(`^[0-9a-f]{32}$`)

func tenantID(t testing.TB, last byte) tenant.ID {
	t.Helper()
	id, err := tenant.ParseID(fmt.Sprintf("0192f0f0-0000-7000-8000-0000000000%02x", last))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const (
	credA = "0192f0f0-1111-7000-8000-00000000000a"
	credB = "0192f0f0-1111-7000-8000-00000000000b"
)

func dataKey(t testing.TB, raw []byte, tid tenant.ID, version int32) *vault.DataKey {
	t.Helper()
	dk, err := vault.NewDataKey(raw, tid, version)
	if err != nil {
		t.Fatalf("NewDataKey: %v", err)
	}
	return dk
}

// TestSealOpen is T-302: the round trip, the freshness of every seal, and the binding of a
// sealed value to its tenant, its credential and its data key version.
func TestSealOpen(t *testing.T) {
	raw := rawKey(t)
	tid := tenantID(t, 1)
	dk := dataKey(t, raw, tid, 1)

	t.Run("a data key is 32 bytes", func(t *testing.T) {
		for _, n := range []int{0, 1, 16, 31, 33, 64} {
			if k, err := vault.NewDataKey(make([]byte, n), tid, 1); err == nil || k != nil {
				t.Errorf("a data key of %d bytes was accepted", n)
			}
		}
	})

	t.Run("round trip", func(t *testing.T) {
		for _, n := range []int{1, 8, 19, 255, 8192} {
			plain := bytes.Repeat([]byte("k"), n)
			sealed, err := dk.Seal(credA, plain)
			if err != nil {
				t.Fatalf("%d bytes: %v", n, err)
			}
			if len(sealed) != n+29 || sealed[0] != 0x01 {
				t.Errorf("%d bytes sealed to %d bytes starting 0x%02x, want %d starting 0x01", n, len(sealed), sealed[0], n+29)
			}
			if bytes.Contains(sealed, plain[:min(n, 8)]) && n >= 8 {
				t.Errorf("%d bytes: the plaintext is in the sealed value", n)
			}
			got, err := dk.Open(credA, sealed)
			if err != nil || !bytes.Equal(got, plain) {
				t.Errorf("%d bytes: open: %v", n, err)
			}
			// Open returns a slice of its own: the caller may clear it.
			clear(got)
			again, err := dk.Open(credA, sealed)
			if err != nil || !bytes.Equal(again, plain) {
				t.Errorf("%d bytes: clearing the result of one Open changed the next: %v", n, err)
			}
		}
	})

	t.Run("a thousand seals of one plaintext are a thousand values", func(t *testing.T) {
		seen := map[string]bool{}
		for i := 0; i < 1000; i++ {
			s, err := dk.Seal(credA, []byte("one plaintext"))
			if err != nil {
				t.Fatal(err)
			}
			if seen[string(s)] {
				t.Fatal("two seals are equal")
			}
			seen[string(s)] = true
		}
	})

	t.Run("under the same data key, the tenant, the credential and the version are bound", func(t *testing.T) {
		sealed, err := dk.Seal(credA, []byte("bound plaintext"))
		if err != nil {
			t.Fatal(err)
		}
		for name, other := range map[string]*vault.DataKey{
			"another tenant":            dataKey(t, raw, tenantID(t, 2), 1),
			"another data key version":  dataKey(t, raw, tid, 2),
			"another key of the tenant": dataKey(t, rawKey(t), tid, 1),
		} {
			if got, err := other.Open(credA, sealed); !errors.Is(err, vault.ErrUndecryptable) || got != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		if got, err := dk.Open(credB, sealed); !errors.Is(err, vault.ErrUndecryptable) || got != nil {
			t.Errorf("another credential: %v", err)
		}
		// Sealed for the other credential, it opens there and not here.
		sealedB, err := dk.Seal(credB, []byte("bound plaintext"))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := dk.Open(credB, sealedB); err != nil || string(got) != "bound plaintext" {
			t.Errorf("credential B: %v", err)
		}
		if _, err := dk.Open(credA, sealedB); !errors.Is(err, vault.ErrUndecryptable) {
			t.Errorf("B's sealed value opened for A: %v", err)
		}
	})

	t.Run("every damaged value is the same error", func(t *testing.T) {
		sealed, err := dk.Seal(credA, []byte("synthetic-plaintext"))
		if err != nil {
			t.Fatal(err)
		}
		fails := func(name string, in []byte) {
			t.Helper()
			got, err := dk.Open(credA, in)
			if !errors.Is(err, vault.ErrUndecryptable) || got != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			if err.Error() != vault.ErrUndecryptable.Error() {
				t.Errorf("%s: the error has a detail: %q", name, err.Error())
			}
		}
		for i := range sealed {
			for bit := 0; bit < 8; bit++ {
				c := append([]byte(nil), sealed...)
				c[i] ^= 1 << bit
				fails(fmt.Sprintf("bit %d of byte %d", bit, i), c)
			}
		}
		for n := 0; n < len(sealed); n++ {
			fails(fmt.Sprintf("truncated to %d bytes", n), sealed[:n])
		}
		for _, f := range []byte{0x00, 0x02, 0x80, 0xff} {
			c := append([]byte(nil), sealed...)
			c[0] = f
			fails(fmt.Sprintf("format byte 0x%02x", f), c)
		}
		fails("one byte too many", append(append([]byte(nil), sealed...), 0))
		fails("nil", nil)
		fails("only a format byte", []byte{0x01})
	})

	t.Run("an empty plaintext is refused", func(t *testing.T) {
		for _, p := range [][]byte{nil, {}} {
			if s, err := dk.Seal(credA, p); err == nil || s != nil {
				t.Errorf("an empty plaintext was sealed: %v", err)
			}
		}
	})

	t.Run("a credential ID is a canonical UUID", func(t *testing.T) {
		for _, bad := range []string{"", "x", strings.ToUpper(credA), strings.ReplaceAll(credA, "-", "")} {
			if _, err := dk.Seal(bad, []byte("plaintext")); err == nil {
				t.Errorf("Seal accepted the credential ID %q", bad)
			}
			if _, err := dk.Open(bad, []byte{1, 2, 3}); err == nil {
				t.Errorf("Open accepted the credential ID %q", bad)
			}
		}
	})
}

// TestFingerprintAndHint is T-303.
func TestFingerprintAndHint(t *testing.T) {
	raw := rawKey(t)
	tid := tenantID(t, 1)
	dk := dataKey(t, raw, tid, 1)
	secretText := []byte("pandokos-synthetic-provider-key-0001")

	t.Run("fingerprint", func(t *testing.T) {
		fp := dk.Fingerprint(secretText)
		if !reFingerprint.MatchString(fp) {
			t.Fatalf("fingerprint %q is not 32 hexadecimal characters", fp)
		}
		if again := dk.Fingerprint(secretText); again != fp {
			t.Error("the fingerprint is not deterministic")
		}
		if same := dataKey(t, raw, tenantID(t, 2), 3).Fingerprint(secretText); same != fp {
			t.Error("the fingerprint depends on the tenant or the version of the data key, not on its key material")
		}
		if other := dataKey(t, rawKey(t), tid, 1).Fingerprint(secretText); other == fp {
			t.Error("two data keys give one secret the same fingerprint: tenants could be correlated")
		}
		if other := dk.Fingerprint([]byte("pandokos-synthetic-provider-key-0002")); other == fp {
			t.Error("two secrets have one fingerprint")
		}
		// It is none of the things an attacker with the database could compute.
		enc, err := hkdf.Key(sha256.New, raw, nil, "pandokos/vault/v1/credential-encryption", 32)
		if err != nil {
			t.Fatal(err)
		}
		m := hmac.New(sha256.New, enc)
		m.Write(secretText)
		sum := sha256.Sum256(secretText)
		m2 := hmac.New(sha256.New, raw)
		m2.Write(secretText)
		salted := sha256.Sum256(append(append([]byte(nil), raw...), secretText...))
		for name, v := range map[string][]byte{
			"an HMAC under the encryption subkey": m.Sum(nil),
			"a plain SHA-256":                     sum[:],
			"an HMAC under the data key itself":   m2.Sum(nil),
			"SHA-256 of key and secret":           salted[:],
		} {
			if hex.EncodeToString(v[:16]) == fp || hex.EncodeToString(v) == fp {
				t.Errorf("the fingerprint is %s", name)
			}
		}
		// It is the HMAC under the fingerprint subkey, first 16 bytes.
		fpk, err := hkdf.Key(sha256.New, raw, nil, "pandokos/vault/v1/credential-fingerprint", 32)
		if err != nil {
			t.Fatal(err)
		}
		m3 := hmac.New(sha256.New, fpk)
		m3.Write(secretText)
		if want := hex.EncodeToString(m3.Sum(nil)[:16]); fp != want {
			t.Errorf("fingerprint %s, want %s", fp, want)
		}
	})

	t.Run("hint", func(t *testing.T) {
		cases := []struct{ name, in, want string }{
			{"empty", "", ""},
			{"one character", "k", ""},
			{"fifteen characters", "123456789012345", ""},
			{"sixteen characters", "1234567890123456", "3456"},
			{"a long key", "pandokos-synthetic-provider-key-0001", "0001"},
			{"fifteen characters of two bytes each", strings.Repeat("é", 15), ""},
			{"sixteen characters of three bytes each", strings.Repeat("日", 16), "日日日日"},
			{"the last four characters, not bytes", strings.Repeat("a", 12) + "éüöß", "éüöß"},
			{"a short key of many bytes", strings.Repeat("日", 15), ""},
		}
		for _, c := range cases {
			if got := vault.Hint(c.in); got != c.want {
				t.Errorf("%s: Hint = %q, want %q", c.name, got, c.want)
			}
		}
	})
}
