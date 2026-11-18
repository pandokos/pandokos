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

func knownMetric(m Metric) bool {
	switch m {
	case ConcurrentRequests, RequestsPerMinute, TokensPerMinute:
		return true
	}
	return false
}

// NewRuleSet builds the RuleSet of a tenant from the platform default, the platform row of that
// tenant and the tenant's own rules. The platform ceiling of a metric is the row of that tenant
// when there is one, else the default. A rule with a metric or subject kind this version does not
// know, or a value below 1, is ignored.
func NewRuleSet(t tenant.ID, platformDefault, platformTenant map[Metric]int64, rules []Rule) RuleSet {
	s := RuleSet{Tenant: t}
	for m, v := range platformDefault {
		if knownMetric(m) && v >= 1 {
			if s.Platform == nil {
				s.Platform = map[Metric]int64{}
			}
			s.Platform[m] = v
		}
	}
	for m, v := range platformTenant {
		if knownMetric(m) && v >= 1 {
			if s.Platform == nil {
				s.Platform = map[Metric]int64{}
			}
			s.Platform[m] = v
		}
	}
	put := func(dst map[string]map[Metric]int64, id string, m Metric, v int64) map[string]map[Metric]int64 {
		if dst == nil {
			dst = map[string]map[Metric]int64{}
		}
		if dst[id] == nil {
			dst[id] = map[Metric]int64{}
		}
		dst[id][m] = v
		return dst
	}
	for _, r := range rules {
		if !knownMetric(r.Metric) || r.Value < 1 {
			continue
		}
		switch r.SubjectKind {
		case SubjectTenant:
			if s.Own == nil {
				s.Own = map[Metric]int64{}
			}
			s.Own[r.Metric] = r.Value
		case SubjectServiceAccount:
			s.ServiceAccounts = put(s.ServiceAccounts, r.SubjectID, r.Metric, r.Value)
		case SubjectKey:
			s.Keys = put(s.Keys, r.SubjectID, r.Metric, r.Value)
		}
	}
	return s
}

// For returns the limits a request of a service account and a key is reserved against, in the
// order of resolution: the tenant, then the service account, then the key; within a subject
// concurrent requests, requests per minute, tokens per minute. An empty ID yields no limit for
// that subject. A subject without a rule yields no limit, and so does a tenant without a platform
// ceiling or rule.
func (s RuleSet) For(serviceAccountID, keyID string) []Limit {
	out := make([]Limit, 0, 9)
	for _, m := range Metrics() {
		ceil, hasCeil := s.Platform[m]
		own, hasOwn := s.Own[m]
		switch {
		case hasCeil && hasOwn && own < ceil:
			out = append(out, Limit{Tenant: s.Tenant, Subject: SubjectTenant, Metric: m, Max: own, Level: LevelTenant})
		case hasCeil:
			out = append(out, Limit{Tenant: s.Tenant, Subject: SubjectTenant, Metric: m, Max: ceil, Level: LevelPlatform})
		case hasOwn:
			out = append(out, Limit{Tenant: s.Tenant, Subject: SubjectTenant, Metric: m, Max: own, Level: LevelTenant})
		}
	}
	add := func(kind SubjectKind, by map[string]map[Metric]int64, id string) {
		if id == "" {
			return
		}
		for _, m := range Metrics() {
			if v, ok := by[id][m]; ok {
				out = append(out, Limit{Tenant: s.Tenant, Subject: kind, ID: id, Metric: m, Max: v, Level: LevelTenant})
			}
		}
	}
	add(SubjectServiceAccount, s.ServiceAccounts, serviceAccountID)
	add(SubjectKey, s.Keys, keyID)
	return out
}
