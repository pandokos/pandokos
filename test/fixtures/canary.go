// Package fixtures holds recorded-shaped provider responses and the canary values that tests
// plant and then search for in every output. Everything here is synthetic.
package fixtures

const (
	// CanaryProviderKey stands for the provider API key.
	CanaryProviderKey = "pandokos-canary-provider-key-0d6f3a1b9c2e47aa"
	// CanaryPrompt stands for prompt content.
	CanaryPrompt = "pandokos-canary-prompt-91c7e2"
	// CanaryCompletion stands for completion content.
	CanaryCompletion = "pandokos-canary-completion-5f2a9c"
)

// Canaries returns the three values, for scanning outputs.
func Canaries() []string {
	return []string{CanaryProviderKey, CanaryPrompt, CanaryCompletion}
}
