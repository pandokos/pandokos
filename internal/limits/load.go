package limits

import (
	"context"
	"errors"
	"fmt"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// MaxRulesPerTenant is the number of limit rules one tenant may have; more is a load error.
const MaxRulesPerTenant = 5000

// LoadRules reads the rules of one tenant in one tenant transaction on the gateway pool: the
// platform rules (the defaults and the tenant's own row), then the tenant's rules. A metric or
// subject kind this version does not know (a newer schema during an upgrade) is skipped; the
// rule cache logs how many in one debug line.
//
// The set carries the calendar of the tenant, read in the same transaction: its zone, and while a
// change is pending the zone in force until then. A zone this binary cannot load is an error.
//
// defaultCurrency is pricing.default_currency, the active currency of a tenant that chose none;
// the set carries the currency in force (and the one before it while a change is pending), read in
// the same transaction after the time zone.
func LoadRules(ctx context.Context, db *store.DB, id tenant.ID, zones *Zones, defaultCurrency string) (RuleSet, error) {
	set, _, err := loadRules(ctx, db, id, zones, defaultCurrency)
	return set, err
}

// loadRules is LoadRules that also returns the number of rules skipped.
func loadRules(ctx context.Context, db *store.DB, id tenant.ID, zones *Zones, defaultCurrency string) (RuleSet, int, error) {
	var set RuleSet
	skipped := 0
	err := db.WithTenant(ctx, id, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		plat, err := q.ListPlatformLimitRules(ctx)
		if err != nil {
			return err
		}
		rows, err := q.ListLimitRules(ctx)
		if err != nil {
			return err
		}
		if len(rows) > MaxRulesPerTenant {
			return fmt.Errorf("a tenant has more than %d limit rules (%d read)", MaxRulesPerTenant, len(rows))
		}
		toRule := func(r Rule) (Rule, bool) {
			if !knownMetric(r.Metric) || (r.SubjectKind != "" && !knownSubject(r.SubjectKind)) || !validRule(r) {
				return r, false
			}
			return r, true
		}
		var def, own []Rule
		for _, r := range plat {
			rule, ok := toRule(Rule{ID: r.ID, SubjectKind: SubjectTenant, ProviderID: r.ProviderID, ModelID: r.ModelID, Provider: r.Provider, Model: r.Model,
				Metric: Metric(r.Metric), Value: r.LimitValue, Currency: r.Currency, Enforce: r.Enforce, Thresholds: percents(r.Thresholds)})
			switch {
			case !ok:
				skipped++
			case r.IsDefault:
				def = append(def, rule)
			default:
				own = append(own, rule)
			}
		}
		rules := make([]Rule, 0, len(rows))
		for _, r := range rows {
			rule, ok := toRule(Rule{ID: r.ID, SubjectKind: SubjectKind(r.SubjectKind), SubjectID: r.SubjectID, ProviderID: r.ProviderID,
				ModelID: r.ModelID, Provider: r.Provider, Model: r.Model, Metric: Metric(r.Metric), Value: r.LimitValue, Currency: r.Currency, Enforce: r.Enforce, Thresholds: percents(r.Thresholds)})
			if !ok || !knownSubject(rule.SubjectKind) {
				skipped++
				continue
			}
			rules = append(rules, rule)
		}
		tz, err := q.GetTenantTimeZone(ctx)
		if err != nil {
			return err
		}
		cal, err := tenantCalendar(zones, tz)
		if err != nil {
			return err
		}
		ac, err := q.GetTenantActiveCurrency(ctx)
		if err != nil {
			return err
		}
		set = NewScopedRuleSet(id, def, own, rules)
		set.Calendar = cal
		set.Currency = pricing.ResolveActive(ac.ActiveCurrency, ac.ActiveCurrencyPrevious, ac.ActiveCurrencyEffectiveFrom, defaultCurrency)
		return nil
	})
	if err != nil {
		return RuleSet{}, 0, fmt.Errorf("limits: load rules: %w", err)
	}
	return set, skipped, nil
}

// tenantCalendar builds the calendar of a tenant from the three columns: Current from time_zone;
// while a change is pending, Previous from time_zone_previous and From.
func tenantCalendar(zones *Zones, tz queries.GetTenantTimeZoneRow) (TenantCalendar, error) {
	if zones == nil {
		return TenantCalendar{}, errors.New("no zones")
	}
	cur, err := zones.Calendar(tz.TimeZone)
	if err != nil {
		return TenantCalendar{}, err
	}
	c := TenantCalendar{Current: cur}
	if tz.TimeZoneEffectiveFrom != nil {
		if c.Previous, err = zones.Calendar(tz.TimeZonePrevious); err != nil {
			return TenantCalendar{}, err
		}
		c.From = *tz.TimeZoneEffectiveFrom
	}
	return c, nil
}

func percents(in []int16) []int {
	if len(in) == 0 {
		return nil
	}
	out := make([]int, len(in))
	for i, p := range in {
		out[i] = int(p)
	}
	return out
}
