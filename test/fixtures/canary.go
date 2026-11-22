// Package fixtures holds recorded-shaped provider responses and the canary values that tests
// plant and then search for in every output. Everything here is synthetic.
package fixtures

import (
	"crypto/rand"
	"encoding/hex"
)

const (
	// CanaryProviderKey stands for the provider API key.
	CanaryProviderKey = "pandokos-canary-provider-key-0d6f3a1b9c2e47aa"
	// CanaryCredential stands for a provider credential written through the administration API.
	CanaryCredential = "pandokos-canary-credential-7b1e4c9d20a8f365"
	// CanaryPrompt stands for prompt content.
	CanaryPrompt = "pandokos-canary-prompt-91c7e2"
	// CanaryCompletion stands for completion content.
	CanaryCompletion = "pandokos-canary-completion-5f2a9c"
)

// Canaries returns the four values, for scanning outputs.
func Canaries() []string {
	return []string{CanaryProviderKey, CanaryCredential, CanaryPrompt, CanaryCompletion}
}

// CanaryLabel returns a name for something of a tenant (a tenant, a service account, a key, a
// display name) that no output may show: the prefix, "-canary-" and eight random hexadecimal
// characters, so that two calls differ and a test can search every output for the value.
func CanaryLabel(prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "-canary-" + hex.EncodeToString(b[:])
}
