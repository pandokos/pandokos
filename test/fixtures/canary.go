// Package fixtures holds recorded-shaped provider responses and the canary values that tests
// plant and then search for in every output. Everything here is synthetic.
package fixtures

const (
	// CanaryProviderKey stands for the provider API key.
	CanaryProviderKey = "pandokos-canary-provider-key-0d6f3a1b9c2e47aa"
	// CanaryAccessToken stands for the static access token callers present.
	CanaryAccessToken = "pandokos-canary-access-token-4e8b2d7c61f0a935"
	// CanaryPrompt stands for prompt content.
	CanaryPrompt = "pandokos-canary-prompt-91c7e2"
	// CanaryCompletion stands for completion content.
	CanaryCompletion = "pandokos-canary-completion-5f2a9c"
)

// Canaries returns the four values, for scanning outputs.
func Canaries() []string {
	return []string{CanaryProviderKey, CanaryAccessToken, CanaryPrompt, CanaryCompletion}
}
