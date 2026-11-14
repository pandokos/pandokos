package vault_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
)

var reKEKVersion = regexp.MustCompile(`^local-[0-9a-f]{16}$`)

func rawKey(t testing.TB) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func provider(t testing.TB, raw []byte) *vault.LocalProvider {
	t.Helper()
	p, err := vault.NewLocalProvider(secret.New(base64.StdEncoding.EncodeToString(raw)))
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	return p
}

// TestLocalProvider is T-300: the key file, the version identifier, wrap and unwrap, and every
// way an unwrap must fail.
func TestLocalProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("GenerateLocalKey makes a line of 44 characters that decodes to 32 bytes", func(t *testing.T) {
		seen := map[string]bool{}
		for i := 0; i < 20; i++ {
			k, err := vault.GenerateLocalKey()
			if err != nil {
				t.Fatal(err)
			}
			line := k.Reveal()
			if len(line) != 44 || strings.ContainsAny(line, " \t\r\n") {
				t.Fatalf("the key line has %d characters", len(line))
			}
			raw, err := base64.StdEncoding.Strict().DecodeString(line)
			if err != nil || len(raw) != 32 {
				t.Fatalf("the key line does not decode to 32 bytes: %d, %v", len(raw), err)
			}
			if seen[line] {
				t.Fatal("two generated keys are equal")
			}
			seen[line] = true
			if _, err := vault.NewLocalProvider(k); err != nil {
				t.Errorf("a generated key is refused: %v", err)
			}
		}
	})

	t.Run("the content of the key file", func(t *testing.T) {
		good := rawKey(t)
		if _, err := vault.NewLocalProvider(secret.New(base64.StdEncoding.EncodeToString(good))); err != nil {
			t.Fatalf("44 characters of standard base64: %v", err)
		}
		slashes := bytes.Repeat([]byte{0xff}, 32) // standard base64 of these holds only "/" and "="
		plus := bytes.Repeat([]byte{0xfb}, 32)    // and "+"
		for _, raw := range [][]byte{slashes, plus} {
			if _, err := vault.NewLocalProvider(secret.New(base64.StdEncoding.EncodeToString(raw))); err != nil {
				t.Errorf("standard base64 with + or / is refused: %v", err)
			}
		}
		std := base64.StdEncoding.EncodeToString(good)
		refused := map[string]string{
			"31 bytes":               base64.StdEncoding.EncodeToString(good[:31]),
			"33 bytes":               base64.StdEncoding.EncodeToString(append(append([]byte{}, good...), 1)),
			"16 bytes":               base64.StdEncoding.EncodeToString(good[:16]),
			"64 bytes":               base64.StdEncoding.EncodeToString(append(append([]byte{}, good...), good...)),
			"empty":                  "",
			"the URL alphabet":       base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 32)),
			"without padding":        strings.TrimRight(std, "="),
			"hexadecimal":            hex.EncodeToString(good),
			"white space inside":     std[:20] + " " + std[20:],
			"a newline inside":       std[:20] + "\n" + std[20:],
			"not base64":             strings.Repeat("!", 44),
			"text":                   "this is not a key at all, it is a sentence of 44 ch",
			"two keys on one line":   std + std,
			"trailing garbage":       std + "x",
			"a different data shape": `{"key":"` + std + `"}`,
		}
		messages := map[string]bool{}
		for name, in := range refused {
			p, err := vault.NewLocalProvider(secret.New(in))
			if err == nil || p != nil {
				t.Errorf("%s: the key was accepted", name)
				continue
			}
			if !errors.Is(err, vault.ErrInvalidKey) {
				t.Errorf("%s: %v is not ErrInvalidKey", name, err)
			}
			messages[err.Error()] = true
			// The message quotes nothing of the content.
			for _, frag := range []string{in, std[:12], std[len(std)-12:]} {
				if frag != "" && strings.Contains(err.Error(), frag) {
					t.Errorf("%s: the error quotes the content: %q", name, err.Error())
				}
			}
			if strings.Contains(err.Error(), hex.EncodeToString(good)[:16]) {
				t.Errorf("%s: the error quotes the content in hexadecimal", name)
			}
		}
		if len(messages) != 1 {
			t.Errorf("the text of the error is not constant: %d different messages", len(messages))
		}
	})

	t.Run("the version identifier is derived from the key", func(t *testing.T) {
		a, b := rawKey(t), rawKey(t)
		pa, pa2, pb := provider(t, a), provider(t, a), provider(t, b)
		va, _, err := pa.Wrap(ctx, []byte("0123456789abcdef0123456789abcdef"), []byte("aad"))
		if err != nil {
			t.Fatal(err)
		}
		va2, _, _ := pa2.Wrap(ctx, []byte("0123456789abcdef0123456789abcdef"), []byte("aad"))
		vb, _, _ := pb.Wrap(ctx, []byte("0123456789abcdef0123456789abcdef"), []byte("aad"))
		if !reKEKVersion.MatchString(va) || !reKEKVersion.MatchString(vb) {
			t.Errorf("identifiers %q and %q do not look like local-<16 hexadecimal>", va, vb)
		}
		if va != va2 {
			t.Errorf("one key, two identifiers: %q and %q", va, va2)
		}
		if va == vb {
			t.Error("two keys have the same identifier")
		}
		if pa.Name() != "local" {
			t.Errorf("Name() = %q", pa.Name())
		}
		// The identifier of a fixed key is frozen: SHA-256("pandokos kek id v1" 0x00 key), 8 bytes.
		fixed := make([]byte, 32)
		for i := range fixed {
			fixed[i] = byte(0x40 + i)
		}
		v, _, err := provider(t, fixed).Wrap(ctx, make([]byte, 32), nil)
		if err != nil || v != "local-e3b1845a211fbdd5" {
			t.Errorf("the identifier of the synthetic key is %q, %v", v, err)
		}
	})

	t.Run("wrap and unwrap", func(t *testing.T) {
		p := provider(t, rawKey(t))
		dek := rawKey(t)
		aad := []byte("pandokos/dek/v1\x00some associated data")
		version, wrapped, err := p.Wrap(ctx, dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		if len(wrapped) != 61 || wrapped[0] != 0x01 {
			t.Fatalf("wrapped is %d bytes starting 0x%02x, want 61 starting 0x01", len(wrapped), wrapped[0])
		}
		if bytes.Contains(wrapped, dek) {
			t.Error("the wrapped key holds the data key in clear")
		}
		got, err := p.Unwrap(ctx, version, wrapped, aad)
		if err != nil || !bytes.Equal(got, dek) {
			t.Fatalf("unwrap: %v, equal=%v", err, bytes.Equal(got, dek))
		}
		_, wrapped2, _ := p.Wrap(ctx, dek, aad)
		if bytes.Equal(wrapped, wrapped2) {
			t.Error("two wraps of the same key are equal: the nonce is not random")
		}
		// Another provider on another key cannot unwrap, even if it is told the version.
		other := provider(t, rawKey(t))
		if _, err := other.Unwrap(ctx, version, wrapped, aad); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("another key: %v, want ErrKeyVersionUnknown", err)
		}
	})

	t.Run("an unwrap that must fail", func(t *testing.T) {
		p := provider(t, rawKey(t))
		dek := rawKey(t)
		aad := []byte("associated data")
		version, wrapped, err := p.Wrap(ctx, dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		fails := func(name string, v string, w, a []byte, want error) {
			t.Helper()
			got, err := p.Unwrap(ctx, v, w, a)
			if !errors.Is(err, want) {
				t.Errorf("%s: %v, want %v", name, err, want)
			}
			if got != nil {
				t.Errorf("%s: partial data returned (%d bytes)", name, len(got))
			}
			if err != nil {
				for _, frag := range []string{hex.EncodeToString(dek), base64.StdEncoding.EncodeToString(dek), string(dek)} {
					if strings.Contains(err.Error(), frag) {
						t.Errorf("%s: the error holds key material", name)
					}
				}
			}
		}
		fails("other associated data", version, wrapped, []byte("other"), vault.ErrUnwrap)
		fails("no associated data", version, wrapped, nil, vault.ErrUnwrap)
		for _, region := range []struct {
			name string
			at   int
		}{{"nonce", 1}, {"nonce end", 12}, {"ciphertext", 13}, {"ciphertext end", 44}, {"tag", 45}, {"tag end", 60}} {
			for bit := 0; bit < 8; bit++ {
				c := append([]byte(nil), wrapped...)
				c[region.at] ^= 1 << bit
				fails("a flipped bit in the "+region.name, version, c, aad, vault.ErrUnwrap)
			}
		}
		for _, format := range []byte{0x00, 0x02, 0x7f, 0xff} {
			c := append([]byte(nil), wrapped...)
			c[0] = format
			fails("another format byte", version, c, aad, vault.ErrUnwrap)
		}
		for _, n := range []int{0, 1, 12, 13, 28, 44, 60} {
			fails(fmt.Sprintf("truncated to %d bytes", n), version, wrapped[:n], aad, vault.ErrUnwrap)
		}
		fails("one byte too many", version, append(append([]byte(nil), wrapped...), 0), aad, vault.ErrUnwrap)
		fails("nil", version, nil, aad, vault.ErrUnwrap)
		fails("an unknown version", "local-0000000000000000", wrapped, aad, vault.ErrKeyVersionUnknown)
		fails("an empty version", "", wrapped, aad, vault.ErrKeyVersionUnknown)
		fails("the version of a similar key", "local-"+strings.Repeat("f", 16), wrapped, aad, vault.ErrKeyVersionUnknown)
		// The failures do not damage the provider.
		if got, err := p.Unwrap(ctx, version, wrapped, aad); err != nil || !bytes.Equal(got, dek) {
			t.Errorf("unwrap after the failures: %v", err)
		}
	})

	t.Run("Available", func(t *testing.T) {
		p := provider(t, rawKey(t))
		version, _, err := p.Wrap(ctx, rawKey(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Available(ctx, version); err != nil {
			t.Errorf("the version of the key: %v", err)
		}
		for _, v := range []string{"", "local-0000000000000000", "kms-1"} {
			if err := p.Available(ctx, v); !errors.Is(err, vault.ErrKeyVersionUnknown) {
				t.Errorf("Available(%q) = %v, want ErrKeyVersionUnknown", v, err)
			}
		}
	})
}

// TestProviders checks the list of providers that a credential may name until the catalogue
// of sprint 2.2 exists (D-9).
func TestProviders(t *testing.T) {
	got := vault.Providers()
	if len(got) != 2 || got[0] != "anthropic" || got[1] != "openai" {
		t.Errorf("Providers() = %v, want [anthropic openai]", got)
	}
	got[0] = "changed"
	if vault.Providers()[0] != "anthropic" {
		t.Error("changing the returned slice changed the list")
	}
	for _, n := range []string{"openai", "anthropic"} {
		if !vault.KnownProvider(n) {
			t.Errorf("KnownProvider(%q) is false", n)
		}
	}
	for _, n := range []string{"", "OpenAI", "gemini", "openai ", "open-ai"} {
		if vault.KnownProvider(n) {
			t.Errorf("KnownProvider(%q) is true", n)
		}
	}
}
