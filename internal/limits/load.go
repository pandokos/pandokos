package limits

import (
	"context"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

// MaxRulesPerTenant is the number of limit rules one tenant may have; more is a load error.
const MaxRulesPerTenant = 5000

// LoadRules reads the rules of one tenant in one tenant transaction on the gateway pool.
func LoadRules(_ context.Context, _ *store.DB, _ tenant.ID) (RuleSet, error) {
	return RuleSet{}, errNotImplemented
}
