package admin

import (
	"slices"
	"strconv"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Bounds of a rule (sprint 5.2, section 5.11): a count in requests or tokens, a budget in millionths
// of its currency, at most four thresholds of 1 to 100 percent.
const (
	maxLimitCount    = 1_000_000_000_000
	maxLimitMicros   = 1_000_000_000_000_000
	maxThresholds    = 4
	microsPerUnit    = 6 // fraction digits of an amount
	scaleMessage     = "must be a decimal number greater than 0 and at most 1000000000, with at most six fraction digits"
	noSuchRuleSubj   = "must name a service account, a virtual key or a subject mapping of this tenant"
	noSuchRuleTenant = "must name a tenant"
)

// costMetric and rateMetric classify the nine metrics: the budgets and the three of sprint 5.1.
func costMetric(m adminapi.LimitMetric) bool {
	return m == adminapi.LimitMetricCostPerDay || m == adminapi.LimitMetricCostPerMonth
}

func rateMetric(m adminapi.LimitMetric) bool {
	switch m {
	case adminapi.LimitMetricConcurrentRequests, adminapi.LimitMetricRequestsPerMinute, adminapi.LimitMetricTokensPerMinute:
		return true
	}
	return false
}

// parseAmount turns the decimal of the API into millionths: an integer of 1 to 10^15.
func parseAmount(s string) (int64, bool) {
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		return 0, false
	}
	micros := d.MulInt(1_000_000).String()
	if strings.Contains(micros, ".") {
		return 0, false // more than six fraction digits
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil || n < 1 || n > maxLimitMicros {
		return 0, false
	}
	return n, true
}

// amountText is n millionths as a decimal without trailing zeros.
func amountText(n int64) string {
	return amountDecimal(n).String()
}

// amountDecimal is n millionths as a decimal.
func amountDecimal(n int64) pricing.Decimal { return decimalOf(n).DivPow10(microsPerUnit) }

func decimalOf(n int64) pricing.Decimal {
	d, err := pricing.ParseDecimal(strconv.FormatInt(n, 10))
	if err != nil {
		return pricing.Decimal{}
	}
	return d
}

// ruleFields is what a rule holds besides its subject and scope: the members a request can set and
// an update can change.
type ruleFields struct {
	Metric     adminapi.LimitMetric
	Value      int64 // limit_value: a count, or millionths for a budget
	Currency   string
	Enforce    bool
	Thresholds []int16
}

// checkRule is the validation of the whole rule: the bounds of the value, enforcement and
// thresholds, for a creation and for the result of an update alike.
func checkRule(f ruleFields) []adminapi.FieldError {
	var errs []adminapi.FieldError
	add := func(field, msg string) { errs = append(errs, adminapi.FieldError{Field: field, Message: msg}) }
	cost := costMetric(f.Metric)
	switch {
	case cost && (f.Value < 1 || f.Value > maxLimitMicros):
		add("amount", scaleMessage)
	case !cost && (f.Value < 1 || f.Value > maxLimitCount):
		add("limit", "must be between 1 and 1000000000000")
	}
	if rateMetric(f.Metric) {
		if !f.Enforce {
			add("enforce", "must be true for this metric")
		}
		if len(f.Thresholds) > 0 {
			add("thresholds", "are not accepted for this metric")
		}
	}
	if !f.Enforce && len(f.Thresholds) == 0 && !rateMetric(f.Metric) {
		add("thresholds", "at least one threshold is needed when enforce is false")
	}
	if len(f.Thresholds) > maxThresholds {
		add("thresholds", "must have at most 4 items")
	}
	seen := map[int16]bool{}
	for _, t := range f.Thresholds {
		switch {
		case t < 1 || t > 100:
			add("thresholds", "every item must be between 1 and 100")
		case seen[t]:
			add("thresholds", "must not repeat a value")
		}
		seen[t] = true
	}
	return errs
}

// thresholdsOf returns the percentages ascending, as the column keeps them (never nil).
func thresholdsOf(in []int) []int16 {
	out := make([]int16, 0, len(in))
	for _, t := range in {
		switch {
		case t < -1 || t > 1000:
			out = append(out, 1000) // out of range: checkRule refuses it
		default:
			out = append(out, int16(t)) //nolint:gosec // bounded just above
		}
	}
	slices.Sort(out)
	return out
}

func thresholdsOut(in []int16) []int {
	out := make([]int, len(in))
	for i, t := range in {
		out[i] = int(t)
	}
	slices.Sort(out)
	return out
}

// createFields is the rule a creation asks for, with the shape errors of the members that the
// metric does not take.
func createFields(metric adminapi.LimitMetric, limit int, amount, currency string, enforce bool, thresholds []int) (ruleFields, []adminapi.FieldError) {
	var errs []adminapi.FieldError
	add := func(field, msg string) { errs = append(errs, adminapi.FieldError{Field: field, Message: msg}) }
	f := ruleFields{Metric: metric, Enforce: enforce, Thresholds: thresholdsOf(thresholds)}
	if costMetric(metric) {
		if limit != 0 {
			add("limit", "is not accepted for a cost metric; use amount")
		}
		if currency == "" {
			add("currency", "is required for a cost metric")
		}
		f.Currency = currency
		if amount == "" {
			add("amount", "is required for a cost metric")
		} else if n, ok := parseAmount(amount); ok {
			f.Value = n
		} else {
			add("amount", scaleMessage)
		}
	} else {
		if amount != "" {
			add("amount", "is accepted for a cost metric only")
		}
		if currency != "" {
			add("currency", "is accepted for a cost metric only")
		}
		if limit == 0 {
			add("limit", "is required for this metric")
		}
		f.Value = int64(limit)
	}
	if len(errs) > 0 {
		return f, errs
	}
	return f, checkRule(f)
}

func invalidFields(errs []adminapi.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return &invalidError{fields: errs}
}

// valueOut sets the limit or the amount of a rule's resource.
func valueOut(metric string, v int64) (limit int, amount string) {
	if costMetric(adminapi.LimitMetric(metric)) {
		return 0, amountText(v)
	}
	return int(v), ""
}

// ruleDetail is what the audit row of a creation or deletion holds: every member of the rule.
func ruleDetail(d map[string]any, metric string, value int64, currency string, enforce bool, thresholds []int16) map[string]any {
	d["metric"] = metric
	if costMetric(adminapi.LimitMetric(metric)) {
		d["amount"], d["currency"] = amountText(value), currency
	} else {
		d["limit"] = value
	}
	d["enforce"], d["thresholds"] = enforce, thresholdsOut(thresholds)
	return d
}

// ruleChange is an update of the members that can change.
type ruleChange struct {
	Limit      adminapi.Optional[int]
	Amount     adminapi.Optional[string]
	Enforce    adminapi.Optional[bool]
	Thresholds adminapi.Optional[[]int]
}

func changeOf(b adminapi.LimitRuleUpdate) ruleChange {
	return ruleChange{Limit: b.Limit, Amount: b.Amount, Enforce: b.Enforce, Thresholds: b.Thresholds}
}

func (c ruleChange) empty() bool {
	return !c.Limit.Set && !c.Amount.Set && !c.Enforce.Set && !c.Thresholds.Set
}

// apply merges the change into the rule that exists and validates the result. It returns the new
// fields, the audit detail of what changed (nil when nothing did) or the problem.
func (c ruleChange) apply(cur ruleFields) (ruleFields, map[string]any, error) {
	if c.empty() {
		return cur, nil, invalid("body", "must set at least one of limit, amount, enforce, thresholds")
	}
	cost := costMetric(cur.Metric)
	if cost && c.Limit.Set {
		return cur, nil, invalid("limit", "is not accepted for a cost metric; use amount")
	}
	if !cost && c.Amount.Set {
		return cur, nil, invalid("amount", "is accepted for a cost metric only")
	}
	next := cur
	detail := map[string]any{}
	if c.Limit.Set {
		next.Value = int64(c.Limit.Value)
	}
	if c.Amount.Set {
		n, ok := parseAmount(c.Amount.Value)
		if !ok {
			return cur, nil, invalid("amount", scaleMessage)
		}
		next.Value = n
	}
	if c.Enforce.Set {
		next.Enforce = c.Enforce.Value
	}
	if c.Thresholds.Set {
		next.Thresholds = thresholdsOf(c.Thresholds.Value)
	}
	if err := invalidFields(checkRule(next)); err != nil {
		return cur, nil, err
	}
	if next.Value != cur.Value {
		if cost {
			detail["amount"] = change(amountText(cur.Value), amountText(next.Value))
		} else {
			detail["limit"] = change(cur.Value, next.Value)
		}
	}
	if next.Enforce != cur.Enforce {
		detail["enforce"] = change(cur.Enforce, next.Enforce)
	}
	if !slices.Equal(next.Thresholds, cur.Thresholds) {
		detail["thresholds"] = change(thresholdsOut(cur.Thresholds), thresholdsOut(next.Thresholds))
	}
	if len(detail) == 0 {
		return cur, nil, nil
	}
	return next, detail, nil
}

// mapLimitError turns what the database refuses into the answers of section 5.11: a rule of the
// same subject, scope and metric is a conflict; a platform rule for a tenant that does not exist is
// a 400 on tenant. A check that validation should have caught stays an internal error.
func mapLimitError(err error) error {
	kind, name, ok := store.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch {
	case kind == "unique" && (name == "limit_rule_key" || name == "platform_limit_rule_key"):
		return errConflict
	case kind == "foreign_key" && strings.HasPrefix(name, "platform_limit_rule_tenant"):
		return invalid("tenant", noSuchRuleTenant)
	}
	return err
}

// ruleID reports whether a path names a well-formed rule; anything else is a rule that is not there.
func ruleID(s string) error {
	if _, err := tenant.ParseID(s); err != nil {
		return errNotFound
	}
	return nil
}

// Row conversions: the statements return one row shape each, with the same members.
func limitRuleOut(r queries.GetLimitRuleRow) adminapi.LimitRule {
	limit, amount := valueOut(r.Metric, r.LimitValue)
	return adminapi.LimitRule{ID: r.ID, SubjectKind: adminapi.LimitSubjectKind(r.SubjectKind), SubjectID: r.SubjectID, Provider: r.Provider,
		ProviderID: r.ProviderID, ModelID: r.ModelID, Model: r.Model, Metric: adminapi.LimitMetric(r.Metric), Limit: limit, Amount: amount, Currency: r.Currency, Enforce: r.Enforce,
		Thresholds: thresholdsOut(r.Thresholds), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func platformRuleOut(r queries.GetPlatformLimitRuleRow) adminapi.PlatformLimitRule {
	limit, amount := valueOut(r.Metric, r.LimitValue)
	tid := ""
	if r.TenantID != nil {
		tid = *r.TenantID
	}
	return adminapi.PlatformLimitRule{ID: r.ID, Tenant: tid, Provider: r.Provider, ProviderID: r.ProviderID, Model: r.Model, ModelID: r.ModelID, Metric: adminapi.LimitMetric(r.Metric),
		Limit: limit, Amount: amount, Currency: r.Currency, Enforce: r.Enforce, Thresholds: thresholdsOut(r.Thresholds),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}
