// Package vaulttest gives tests a key provider of the vault on a key made for the test. It is
// apart from test/testutil, which every test imports, so that the vault is imported only by the
// tests that use it.
package vaulttest

import (
	"testing"

	"github.com/pandokos/pandokos/internal/vault"
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
