package openai

import "github.com/pandokos/pandokos/internal/family"

// Estimate estimates the usage of a request whose provider reported none, from what the
// gateway measured.
func Estimate(operation string, e family.Evidence) family.Usage {
	_, _ = operation, e
	return family.Usage{}
}
