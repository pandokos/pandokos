// Package vaulttest gives tests a key provider of the vault on a key made for the test. It is
// apart from test/testutil, which every test imports, so that the vault is imported only by the
// tests that use it.
package vaulttest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/dbtest"
)

// NewKeyProvider returns a vault.LocalProvider on a key generated (vault.GenerateLocalKey) for
// this test.
func NewKeyProvider(tb testing.TB) vault.KeyProvider {
	tb.Helper()
	key, err := vault.GenerateLocalKey()
	if err != nil {
		tb.Fatal(err)
	}
	p, err := vault.NewLocalProvider(key)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

// Key is a key-encryption key made for a test: the content of its key file, the file, and the
// version the vault derives from it.
type Key struct {
	Secret  secret.Secret
	File    string
	Version string
}

// Keys returns n new keys, each written as a key file of mode 0600 in a temporary directory of the
// test, with the versions the vault derives.
func Keys(tb testing.TB, n int) []Key {
	tb.Helper()
	dir := tb.TempDir()
	out := make([]Key, 0, n)
	for i := 0; i < n; i++ {
		s, err := vault.GenerateLocalKey()
		if err != nil {
			tb.Fatal(err)
		}
		f := filepath.Join(dir, fmt.Sprintf("kek-%d", i))
		if err := os.WriteFile(f, []byte(s.Reveal()+"\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
		p, err := vault.NewLocalProvider(s)
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, Key{Secret: s, File: f, Version: p.Versions()[0]})
	}
	return out
}

// Provider returns a local provider that wraps with active and unwraps with the others too.
func Provider(tb testing.TB, active Key, unwrap ...Key) *vault.LocalProvider {
	tb.Helper()
	var u []secret.Secret
	for _, k := range unwrap {
		u = append(u, k.Secret)
	}
	p, err := vault.NewLocalProvider(active.Secret, u...)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

// ProviderFor returns the local provider on the key of the database (dbtest.Database.KEKFile): the
// version that Migrate activated, so that a Sealer on it can create data keys. It fails the test if
// the version the vault derives differs from the one dbtest computed.
func ProviderFor(tb testing.TB, d *dbtest.Database) vault.KeyProvider {
	tb.Helper()
	b, err := os.ReadFile(d.KEKFile(tb))
	if err != nil {
		tb.Fatal(err)
	}
	p, err := vault.NewLocalProvider(secret.New(strings.TrimSpace(string(b))))
	if err != nil {
		tb.Fatal(err)
	}
	if got, want := p.Versions()[0], d.KEKVersion(tb); got != want {
		tb.Fatalf("the vault derives the version %s from the test key, dbtest %s", got, want)
	}
	return p
}
