package limits

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// MaxRulesPerTenant is the number of limit rules one tenant may have; more is a load error.
const MaxRulesPerTenant = 5000

// LoadRules reads the rules of one tenant in one tenant transaction on the gateway pool: the
// platform rules (the defaults and the tenant's own row), then the tenant's rules. A metric or
// subject kind this version does not know (a newer schema during an upgrade) is skipped and
// counted in one debug line.
func LoadRules(ctx context.Context, db *store.DB, id tenant.ID) (RuleSet, error) {
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
		def, own := map[Metric]int64{}, map[Metric]int64{}
		for _, r := range plat {
			m := Metric(r.Metric)
			if !knownMetric(m) {
				skipped++
				continue
			}
			if r.IsDefault {
				def[m] = r.LimitValue
			} else {
				own[m] = r.LimitValue
			}
		}
		rules := make([]Rule, 0, len(rows))
		for _, r := range rows {
			k := SubjectKind(r.SubjectKind)
			if !knownMetric(Metric(r.Metric)) || (k != SubjectTenant && k != SubjectServiceAccount && k != SubjectKey) {
				skipped++
				continue
			}
			rules = append(rules, Rule{SubjectKind: k, SubjectID: r.SubjectID, Metric: Metric(r.Metric), Value: r.LimitValue})
		}
		set = NewRuleSet(id, def, own, rules)
		return nil
	})
	if err != nil {
		return RuleSet{}, fmt.Errorf("limits: load rules: %w", err)
	}
	if skipped > 0 {
		slog.Debug("limit rules of an unknown metric or subject skipped", "tenant_id", id.String(), "skipped", skipped)
	}
	return set, nil
}
