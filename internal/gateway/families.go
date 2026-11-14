package gateway

import (
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
)

// Families returns a new map of the adapters this build serves, by family name.
func Families() map[string]family.Adapter {
	return map[string]family.Adapter{openai.Name: openai.Adapter()}
}
