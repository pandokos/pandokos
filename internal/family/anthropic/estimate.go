package anthropic

import "github.com/pandokos/pandokos/internal/family"

// Estimate estimates the usage of a request whose provider reported none. The stub estimates
// nothing.
func Estimate(_ string, _ family.Evidence) family.Usage { return family.Usage{} }
