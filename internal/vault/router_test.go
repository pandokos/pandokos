package vault_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/pandokos/pandokos/internal/vault"
)

// fakeProvider is a key provider of a given name that counts its calls and returns what a test
// tells it to.
type fakeProvider struct {
	name    string
	version string // what Wrap answers
	mu      sync.Mutex
	wraps   int
	unwraps int
	avails  int
	err     error
	returns [][]byte // every slice Unwrap returned, in order
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Wrap(_ context.Context, plaintext, _ []byte) (string, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wraps++
	if f.err != nil {
		return "", nil, f.err
	}
	return f.version, append([]byte{0x7f}, plaintext...), nil
}

func (f *fakeProvider) Unwrap(_ context.Context, _ string, wrapped, _ []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unwraps++
	if f.err != nil {
		return nil, f.err
	}
	out := append([]byte(nil), wrapped[1:]...)
	f.returns = append(f.returns, out)
	return out, nil
}

func (f *fakeProvider) Available(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.avails++
	return f.err
}

func (f *fakeProvider) calls() (wraps, unwraps, avails int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wraps, f.unwraps, f.avails
}

// TestRouter is T-1069.
func TestRouter(t *testing.T) {
	t.Parallel()
	local := &fakeProvider{name: "local", version: "local-0123456789abcdef"}
	transit := &fakeProvider{name: "transit", version: "transit-0123456789abcdef-v1"}
	r, err := vault.NewRouter(transit, local)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	aad := []byte("aad")

	t.Run("Name and Wrap are the active provider's", func(t *testing.T) {
		if r.Name() != "transit" {
			t.Errorf("Name = %q, want transit", r.Name())
		}
		v, w, err := r.Wrap(ctx(t), []byte("data key"), aad)
		if err != nil || v != transit.version || len(w) == 0 {
			t.Fatalf("Wrap = %q, %x, %v", v, w, err)
		}
		if wr, _, _ := local.calls(); wr != 0 {
			t.Errorf("Wrap reached the other provider")
		}
	})

	t.Run("Unwrap and Available go to the provider the identifier names", func(t *testing.T) {
		_, w, _ := local.Wrap(ctx(t), []byte("k"), aad)
		if got, err := r.Unwrap(ctx(t), local.version, w, aad); err != nil || string(got) != "k" {
			t.Errorf("Unwrap of a local version: %q, %v", got, err)
		}
		if _, u, _ := local.calls(); u != 1 {
			t.Errorf("the local provider got %d unwraps, want 1", u)
		}
		if _, u, _ := transit.calls(); u != 0 {
			t.Errorf("the transit provider got an unwrap for a local version")
		}
		if err := r.Available(ctx(t), transit.version); err != nil {
			t.Errorf("Available: %v", err)
		}
		if _, _, a := transit.calls(); a != 1 {
			t.Errorf("the transit provider got %d Available calls, want 1", a)
		}
		if _, _, a := local.calls(); a != 0 {
			t.Errorf("Available of a transit version reached the local provider")
		}
	})

	t.Run("an identifier of no provider is unknown", func(t *testing.T) {
		before := func() int { a, b, c := local.calls(); d, e, f := transit.calls(); return a + b + c + d + e + f }()
		for _, id := range []string{"", "vault-0123", "localx-1", "-local", "kms-0123456789abcdef", "LOCAL-0123456789abcdef"} {
			if err := r.Available(ctx(t), id); !errors.Is(err, vault.ErrKeyVersionUnknown) {
				t.Errorf("Available(%q) = %v, want ErrKeyVersionUnknown", id, err)
			}
			if _, err := r.Unwrap(ctx(t), id, []byte{1, 2}, aad); !errors.Is(err, vault.ErrKeyVersionUnknown) {
				t.Errorf("Unwrap(%q) = %v, want ErrKeyVersionUnknown", id, err)
			}
		}
		if after := func() int { a, b, c := local.calls(); d, e, f := transit.calls(); return a + b + c + d + e + f }(); after != before {
			t.Errorf("an identifier of no provider reached a provider")
		}
	})

	t.Run("ProviderOf", func(t *testing.T) {
		for id, want := range map[string]string{
			"local-0123456789abcdef": "local", "transit-0123456789abcdef-v3": "transit", "": "", "other-1": "", "localx": "",
		} {
			if got := r.ProviderOf(id); got != want {
				t.Errorf("ProviderOf(%q) = %q, want %q", id, got, want)
			}
		}
	})

	t.Run("two providers of one name, or none, are refused", func(t *testing.T) {
		if _, err := vault.NewRouter(local, &fakeProvider{name: "local"}); err == nil {
			t.Errorf("two providers named local")
		}
		if _, err := vault.NewRouter(local, transit, &fakeProvider{name: "transit"}); err == nil {
			t.Errorf("two providers named transit")
		}
		if _, err := vault.NewRouter(nil); err == nil {
			t.Errorf("no active provider")
		}
		if _, err := vault.NewRouter(local, nil); err == nil {
			t.Errorf("a nil provider among the others")
		}
	})

	t.Run("a version with a foreign prefix is refused", func(t *testing.T) {
		bad := &fakeProvider{name: "transit", version: "local-0123456789abcdef"}
		rr, err := vault.NewRouter(bad, local)
		if err != nil {
			t.Fatal(err)
		}
		if v, w, err := rr.Wrap(ctx(t), []byte("k"), aad); err == nil || v != "" || w != nil {
			t.Errorf("Wrap with a foreign prefix: %q %x %v", v, w, err)
		}
	})

	t.Run("errors of a provider pass through", func(t *testing.T) {
		down := &fakeProvider{name: "transit", version: "transit-0123456789abcdef-v1", err: vault.ErrProviderUnavailable}
		rr, err := vault.NewRouter(down, local)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := rr.Wrap(ctx(t), []byte("k"), aad); !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("Wrap: %v", err)
		}
		if err := rr.Available(ctx(t), down.version); !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("Available: %v", err)
		}
	})
}
