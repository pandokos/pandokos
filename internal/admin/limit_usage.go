package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// GetLimitUsage says what the ledger holds for a day or month rule in the period at now, through
// the reader and the sum that the counters use (section 5.4). Requests in flight are not counted.
func (s *server) GetLimitUsage(ctx context.Context, in adminapi.GetLimitUsageInput) (*adminapi.LimitUsage, error) {
	if err := ruleID(in.Rule); err != nil {
		return nil, err
	}
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var rule queries.GetLimitRuleRow
	var cal limits.TenantCalendar
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var err error
		if rule, err = q.GetLimitRule(ctx, in.Rule); err != nil {
			return err
		}
		cal, err = s.tenantCalendar(ctx, q)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	metric := limits.Metric(rule.Metric)
	kind, periodic := metric.Period()
	if !periodic {
		return nil, invalidState("metric", "only a rule counted per day or per month has a usage")
	}
	if s.limits.Ledger == nil {
		return nil, errors.New("admin: limit usage needs the ledger reader")
	}
	now := s.now().UTC()
	p := cal.Of(kind, now)
	rows, err := s.limits.Ledger.Usage(ctx, tid, p.Start, now, now)
	if err != nil {
		return nil, err
	}
	l := limits.Limit{Tenant: tid, Subject: limits.SubjectKind(rule.SubjectKind), ID: rule.SubjectID, ProviderID: rule.ProviderID,
		ModelID: rule.ModelID, Metric: metric, Currency: rule.Currency}
	used := limits.Sum(l, rows)
	out := &adminapi.LimitUsage{RuleID: rule.ID, Metric: rule.Metric, PeriodStart: p.Start.UTC(), PeriodEnd: p.End.UTC(), AsOf: now}
	if metric.Cost() {
		out.Currency, out.UsedAmount = rule.Currency, used.Cost.String()
	} else {
		out.Used = int(used.N)
	}
	return out, nil
}
