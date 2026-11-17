package limits

import "github.com/pandokos/pandokos/internal/tenant"

// Rule is one row of limit_rule.
type Rule struct {
	SubjectKind SubjectKind
	SubjectID   string
	Metric      Metric
	Value       int64
}

// RuleSet is the rules in force for one tenant.
type RuleSet struct {
	Tenant          tenant.ID
	Platform, Own   map[Metric]int64            // the platform ceiling and the tenant's own rules, by metric
	ServiceAccounts map[string]map[Metric]int64 // by service account ID
	Keys            map[string]map[Metric]int64 // by key ID or subject mapping ID
}

// NewRuleSet builds the RuleSet of a tenant from the platform default, the platform row of that
// tenant and the tenant's own rules.
func NewRuleSet(t tenant.ID, _, _ map[Metric]int64, _ []Rule) RuleSet {
	return RuleSet{Tenant: t}
}

// For returns the limits a request of a service account and a key is reserved against, in the
// order of resolution.
func (s RuleSet) For(_, _ string) []Limit { return nil }
