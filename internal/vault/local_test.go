package vault_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// TestLocalProviderSeveralKeys is T-987: a provider with an active key and unwrap keys wraps with
// the active one and unwraps each by its version, refuses what a rotation must never allow, and
// prints no key on any path.
func TestLocalProviderSeveralKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	keys := vaulttest.Keys(t, 3)
	p, err := vault.NewLocalProvider(keys[0].Secret, keys[1].Secret, keys[2].Secret)
	if err != nil {
		t.Fatalf("NewLocalProvider with two unwrap keys: %v", err)
	}

	t.Run("it wraps with the active key", func(t *testing.T) {
		version, wrapped, err := p.Wrap(ctx, []byte("0123456789abcdef0123456789abcdef"), []byte("aad"))
		if err != nil || version != keys[0].Version {
			t.Fatalf("Wrap: version %q, %v; want %q", version, err, keys[0].Version)
		}
		// Only the active key opens it.
		for i, k := range keys {
			alone := vaulttest.Provider(t, k)
			_, err := alone.Unwrap(ctx, keys[0].Version, wrapped, []byte("aad"))
			if (i == 0) != (err == nil) {
				t.Errorf("a provider that holds only key %d unwraps what the active key wrapped: %v", i, err)
			}
		}
		if got := p.Versions(); fmt.Sprint(got) != fmt.Sprint([]string{keys[0].Version, keys[1].Version, keys[2].Version}) {
			t.Errorf("Versions() = %v, want the active version first, then the unwrap versions in the order given", got)
		}
	})

	t.Run("it unwraps each key by its version", func(t *testing.T) {
		for i, k := range keys {
			plain := []byte(fmt.Sprintf("a data key of key number %d, 32 bytes!", i))[:32]
			version, wrapped, err := vaulttest.Provider(t, k).Wrap(ctx, plain, []byte("tenant:1"))
			if err != nil || version != k.Version {
				t.Fatal(version, err)
			}
			got, err := p.Unwrap(ctx, version, wrapped, []byte("tenant:1"))
			if err != nil || !bytes.Equal(got, plain) {
				t.Errorf("Unwrap under version %d: %q, %v", i, got, err)
			}
			if _, err := p.Unwrap(ctx, version, wrapped, []byte("tenant:2")); !errors.Is(err, vault.ErrUnwrap) {
				t.Errorf("Unwrap with other associated data: %v, want ErrUnwrap", err)
			}
			if err := p.Available(ctx, k.Version); err != nil {
				t.Errorf("Available(%s): %v", k.Version, err)
			}
			// A value wrapped under one key is not opened as another's.
			other := keys[(i+1)%3].Version
			if _, err := p.Unwrap(ctx, other, wrapped, []byte("tenant:1")); !errors.Is(err, vault.ErrUnwrap) {
				t.Errorf("Unwrap under the version of another key: %v, want ErrUnwrap", err)
			}
		}
		if _, err := p.Unwrap(ctx, "local-0000000000000000", []byte{1, 2}, nil); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("a version the provider does not hold: %v, want ErrKeyVersionUnknown", err)
		}
		if err := p.Available(ctx, "local-0000000000000000"); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("Available of an unknown version: %v", err)
		}
	})

	t.Run("what is refused quotes no content", func(t *testing.T) {
		garbage := secret.New("not-a-key-but-recognisable-text-0123")
		nine := make([]secret.Secret, 0, 9)
		for _, k := range vaulttest.Keys(t, 9) {
			nine = append(nine, k.Secret)
		}
		for name, build := range map[string]func() (*vault.LocalProvider, error){
			"the active key twice": func() (*vault.LocalProvider, error) { return vault.NewLocalProvider(keys[0].Secret, keys[0].Secret) },
			"an unwrap key twice": func() (*vault.LocalProvider, error) {
				return vault.NewLocalProvider(keys[0].Secret, keys[1].Secret, keys[1].Secret)
			},
			"the active key as an unwrap key": func() (*vault.LocalProvider, error) {
				return vault.NewLocalProvider(keys[0].Secret, keys[1].Secret, keys[0].Secret)
			},
			"nine unwrap keys":      func() (*vault.LocalProvider, error) { return vault.NewLocalProvider(keys[0].Secret, nine...) },
			"an invalid unwrap key": func() (*vault.LocalProvider, error) { return vault.NewLocalProvider(keys[0].Secret, garbage) },
			"an invalid active key": func() (*vault.LocalProvider, error) { return vault.NewLocalProvider(garbage, keys[1].Secret) },
			"an empty unwrap key":   func() (*vault.LocalProvider, error) { return vault.NewLocalProvider(keys[0].Secret, secret.New("")) },
		} {
			got, err := build()
			if err == nil || got != nil {
				t.Errorf("%s: %v, %v; want a refusal", name, got, err)
				continue
			}
			if !errors.Is(err, vault.ErrInvalidKey) {
				t.Errorf("%s: %v, want ErrInvalidKey", name, err)
			}
			for _, k := range keys {
				if strings.Contains(err.Error(), k.Secret.Reveal()) || strings.Contains(err.Error(), k.Version) {
					t.Errorf("%s: the error quotes a key or its version: %v", name, err)
				}
			}
			if strings.Contains(err.Error(), garbage.Reveal()) || strings.Contains(err.Error(), "recognisable") {
				t.Errorf("%s: the error quotes the content of a file: %v", name, err)
			}
		}
	})

	t.Run("every formatting path prints the redaction marker", func(t *testing.T) {
		var forms []string
		for _, k := range keys {
			raw, err := base64.StdEncoding.DecodeString(k.Secret.Reveal())
			if err != nil {
				t.Fatal(err)
			}
			forms = append(forms, k.Secret.Reveal())
			forms = append(forms, byteForms(raw)...)
		}
		outputs := map[string]string{}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			outputs["fmt "+verb] = fmt.Sprintf(verb, p) + fmt.Sprintf(verb, *p)
		}
		outputs["wrapped in a struct"] = fmt.Sprintf("%+v", struct{ P any }{p})
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("provider", "p", p, slog.Any("q", *p))
		slog.New(slog.NewTextHandler(&buf, nil)).Info("provider", "p", p)
		outputs["slog"] = buf.String()
		if j, err := json.Marshal(map[string]any{"p": p}); err == nil {
			outputs["json"] = string(j)
		}
		for where, out := range outputs {
			for _, f := range forms {
				if len(f) >= 6 && strings.Contains(out, f) {
					t.Errorf("through %s: a key is printed", where)
				}
			}
		}
		if got := fmt.Sprintf("%v", p); got != secret.Redacted {
			t.Errorf("the provider prints %q, want %q", got, secret.Redacted)
		}
		for where, out := range outputs {
			if !strings.Contains(out, secret.Redacted) {
				t.Errorf("through %s: no redaction marker in %q", where, out)
			}
		}
	})
}
