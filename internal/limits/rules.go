package limits

import (
	"slices"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

// Rule is one row of limit_rule or platform_limit_rule.
type Rule struct {
	ID                  string
	SubjectKind         SubjectKind
	SubjectID           string
	ProviderID, ModelID string // scope by identifier; "" means every
	Provider, Model     string // scope by name; "" means every (names matter until C1)
	Metric              Metric
	Value               int64 // requests or tokens; millionths of the currency for a cost metric
	Currency            string
	Enforce             bool
	Thresholds          []int // percentages of Value
}

// Query is what a request is, for the resolution of its limits.
type Query struct {
	ServiceAccountID, KeyID string
	ProviderID, ModelID     string    // identifiers of the target (filled from C1 on)
	Provider, Model         string    // names of the target
	Start                   time.Time // start of the request
}

type scopeKey struct{ provider, model string }

type subjectRef struct {
	kind SubjectKind
	id   string
}

// scopeLimits holds, per metric (in the order of Metrics), the limits of one scope of one
// subject: one, or two when a platform rule and a tenant rule count in different currencies. The
// Period is not set; For sets it.
type scopeLimits [metricCount][]Limit

// RuleSet is the rules in force for one tenant, resolved into the limits of each subject and
// scope, so that For does a fixed number of map lookups and never scans the rules.
type RuleSet struct {
	Tenant tenant.ID
	// Calendar is the calendar of the tenant (sprint 5.3, C2): the loader sets it; For uses it from
	// step C2 on.
	Calendar TenantCalendar
	subjects map[subjectRef]map[scopeKey]*scopeLimits
}

const metricCount = 9

func metricIndex9(m Metric) int {
	switch m {
	case ConcurrentRequests:
		return 0
	case RequestsPerMinute:
		return 1
	case TokensPerMinute:
		return 2
	case RequestsPerDay:
		return 3
	case RequestsPerMonth:
		return 4
	case TokensPerDay:
		return 5
	case TokensPerMonth:
		return 6
	case CostPerDay:
		return 7
	case CostPerMonth:
		return 8
	}
	return -1
}

// knownMetric reports whether this version knows the metric.
func knownMetric(m Metric) bool { return metricIndex9(m) >= 0 }

// rateMetric reports whether the metric is one of the three of sprint 5.1.
func rateMetric(m Metric) bool {
	switch m {
	case ConcurrentRequests, RequestsPerMinute, TokensPerMinute:
		return true
	}
	return false
}

func knownSubject(k SubjectKind) bool {
	return k == SubjectTenant || k == SubjectServiceAccount || k == SubjectKey
}

// validRule reports whether a rule can be put to work: a known metric, a value of at least 1, a
// currency for a cost metric, thresholds from 1 to 100, a provider for a model, and for the
// metrics of 5.1 enforcement and no thresholds.
func validRule(r Rule) bool {
	if !knownMetric(r.Metric) || r.Value < 1 {
		return false
	}
	if r.Metric.Cost() && r.Currency == "" {
		return false
	}
	if r.Model != "" && r.Provider == "" {
		return false
	}
	for _, p := range r.Thresholds {
		if p < 1 || p > 100 {
			return false
		}
	}
	if rateMetric(r.Metric) {
		return r.Enforce
	}
	// A rule that neither refuses nor marks does nothing.
	return r.Enforce || len(r.Thresholds) > 0
}

// NewRuleSet builds the RuleSet of a tenant from the platform default, the platform row of that
// tenant and the tenant's own rules, for the three metrics of sprint 5.1 (every rule enforces,
// none has thresholds). The platform ceiling of a metric is the row of that tenant when there is
// one, else the default. A rule with a metric this version does not know, or a value below 1, is
// ignored.
func NewRuleSet(t tenant.ID, platformDefault, platformTenant map[Metric]int64, rules []Rule) RuleSet {
	conv := func(m map[Metric]int64) []Rule {
		var out []Rule
		for _, k := range Metrics() { // a fixed order
			if v, ok := m[k]; ok && rateMetric(k) {
				out = append(out, Rule{SubjectKind: SubjectTenant, Metric: k, Value: v, Enforce: true})
			}
		}
		return out
	}
	var own []Rule
	for _, r := range rules {
		if !rateMetric(r.Metric) {
			continue
		}
		r.Enforce, r.Thresholds = true, nil
		own = append(own, r)
	}
	return NewScopedRuleSet(t, conv(platformDefault), conv(platformTenant), own)
}

// ruleSlot is what applies to one (subject, scope, metric): at most one platform rule and one
// tenant rule.
type ruleSlot struct{ plat, own *Rule }

type slotKey struct {
	subject subjectRef
	scope   scopeKey
	metric  int
}

// NewScopedRuleSet builds the RuleSet of a tenant from rules that carry a scope, a currency, an
// enforcement and thresholds (sprint 5.2): the platform default rows, the platform rows of this
// tenant (the subject of a platform rule is ignored; a row of the tenant replaces the default of
// its scope and metric) and the tenant's own rules. Invalid rules are ignored.
func NewScopedRuleSet(t tenant.ID, platformDefault, platformTenant, rules []Rule) RuleSet {
	slots := map[slotKey]*ruleSlot{}
	slot := func(k slotKey) *ruleSlot {
		s := slots[k]
		if s == nil {
			s = &ruleSlot{}
			slots[k] = s
		}
		return s
	}
	platRef := subjectRef{kind: SubjectTenant}
	for _, group := range [][]Rule{platformDefault, platformTenant} { // the tenant's row replaces the default
		for _, r := range group {
			if !validRule(r) {
				continue
			}
			r := r
			if !r.Metric.Cost() {
				r.Currency = ""
			}
			slot(slotKey{platRef, scopeKey{r.Provider, r.Model}, metricIndex9(r.Metric)}).plat = &r
		}
	}
	for _, r := range rules {
		if !validRule(r) || !knownSubject(r.SubjectKind) {
			continue
		}
		if r.SubjectKind == SubjectTenant {
			r.SubjectID = ""
		} else if r.SubjectID == "" {
			continue
		}
		r := r
		if !r.Metric.Cost() {
			r.Currency = ""
		}
		ref := subjectRef{r.SubjectKind, r.SubjectID}
		slot(slotKey{ref, scopeKey{r.Provider, r.Model}, metricIndex9(r.Metric)}).own = &r
	}
	s := RuleSet{Tenant: t}
	for k, sl := range slots {
		if s.subjects == nil {
			s.subjects = map[subjectRef]map[scopeKey]*scopeLimits{}
		}
		by := s.subjects[k.subject]
		if by == nil {
			by = map[scopeKey]*scopeLimits{}
			s.subjects[k.subject] = by
		}
		sc := by[k.scope]
		if sc == nil {
			sc = &scopeLimits{}
			by[k.scope] = sc
		}
		sc[k.metric] = s.counters(k.subject, k.scope, Metrics()[k.metric], sl)
	}
	return s
}

// leveled is a rule and the level that set it.
type leveled struct {
	r     *Rule
	level string
}

// counters returns the limits of one (subject, scope, metric): the rules that share a currency
// share a counter.
func (s RuleSet) counters(sub subjectRef, sc scopeKey, m Metric, sl *ruleSlot) []Limit {
	var rules []leveled // platform first
	if sl.plat != nil {
		rules = append(rules, leveled{sl.plat, LevelPlatform})
	}
	if sl.own != nil {
		rules = append(rules, leveled{sl.own, LevelTenant})
	}
	var out []Limit
	var done []string
	for _, r := range rules {
		if slices.Contains(done, r.r.Currency) {
			continue
		}
		done = append(done, r.r.Currency)
		var group []leveled
		for _, o := range rules {
			if o.r.Currency == r.r.Currency {
				group = append(group, o)
			}
		}
		out = append(out, s.counter(sub, sc, m, r.r.Currency, group))
	}
	return out
}

// counter merges the rules of one counter: Max is the smallest enforced value (the platform's on
// a tie), the marks are those of every rule.
func (s RuleSet) counter(sub subjectRef, sc scopeKey, m Metric, currency string, rules []leveled) Limit {
	l := Limit{Tenant: s.Tenant, Subject: sub.kind, ID: sub.id, Provider: sc.provider, Model: sc.model,
		ProviderID: rules[0].r.ProviderID, ModelID: rules[0].r.ModelID,
		Metric: m, Currency: currency, Level: rules[0].level, RuleID: rules[0].r.ID}
	_, periodic := m.Period()
	for _, lr := range rules {
		r := lr.r
		if r.Enforce && (l.Max == 0 || r.Value < l.Max) {
			l.Max, l.Level, l.RuleID = r.Value, lr.level, r.ID
		}
		if !periodic {
			continue
		}
		for _, p := range r.Thresholds {
			l.Marks = append(l.Marks, Mark{At: (r.Value*int64(p) + 99) / 100, Percent: p, RuleID: r.ID, Level: lr.level, Value: r.Value})
		}
	}
	slices.SortStableFunc(l.Marks, func(a, b Mark) int {
		switch {
		case a.At != b.At:
			return cmpInt(a.At, b.At)
		case a.Level != b.Level:
			if a.Level == LevelPlatform {
				return -1
			}
			return 1
		}
		return cmpInt(int64(a.Percent), int64(b.Percent))
	})
	return l
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// For returns the limits a request described by q is reserved against, in the order of
// resolution: the subject (the tenant, then the service account, then the key), within a subject
// the scope (all, then the provider, then the model), within a scope the metrics in the order of
// Metrics. An empty ID yields no limit for that subject. A subject without a rule yields no limit.
// Period limits are returned only with a calendar; their period is the one that contains q.Start.
func (s RuleSet) For(q Query, cal *Calendar) []Limit {
	if len(s.subjects) == 0 {
		return nil
	}
	var scopes [3]scopeKey
	ns := 1
	if q.Provider != "" {
		scopes[ns] = scopeKey{provider: q.Provider}
		ns++
		if q.Model != "" {
			scopes[ns] = scopeKey{q.Provider, q.Model}
			ns++
		}
	}
	var out []Limit
	var days, months Period
	var haveDay, haveMonth bool
	emit := func(ref subjectRef) {
		by := s.subjects[ref]
		if by == nil {
			return
		}
		for _, sc := range scopes[:ns] {
			set := by[sc]
			if set == nil {
				continue
			}
			for i := range set {
				for _, l := range set[i] {
					if kind, ok := l.Metric.Period(); ok {
						if cal == nil {
							continue
						}
						if kind == PerDay {
							if !haveDay {
								days, haveDay = cal.Of(PerDay, q.Start), true
							}
							l.Period = days
						} else {
							if !haveMonth {
								months, haveMonth = cal.Of(PerMonth, q.Start), true
							}
							l.Period = months
						}
					}
					out = append(out, l)
				}
			}
		}
	}
	emit(subjectRef{kind: SubjectTenant})
	if q.ServiceAccountID != "" {
		emit(subjectRef{SubjectServiceAccount, q.ServiceAccountID})
	}
	if q.KeyID != "" {
		emit(subjectRef{SubjectKey, q.KeyID})
	}
	return out
}
