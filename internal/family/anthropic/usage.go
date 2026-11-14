package anthropic

import "github.com/pandokos/pandokos/internal/family"

// ReadUsageOf reads the usage of a complete answer. The stub reports none.
func ReadUsageOf(_ string, _ []byte) (family.Usage, bool, error) {
	return family.Usage{}, false, nil
}
