package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

const (
	// maxReportWindow and maxReportRows bound a report (section 5.3).
	maxReportWindow = 400 * 24 * time.Hour
	maxReportRows   = 10000
)

// tenantCalendar loads the calendar of a tenant from its time zone columns, in the session that
// holds them.
func (s *server) tenantCalendar(ctx context.Context, q *queries.Queries) (limits.TenantCalendar, error) {
	zones := s.limits.Zones
	if zones == nil {
		return limits.TenantCalendar{}, errors.New("admin: reports need the zones of the installation")
	}
	tz, err := q.GetTenantTimeZone(ctx)
	if err != nil {
		return limits.TenantCalendar{}, err
	}
	cur, err := zones.Calendar(tz.TimeZone)
	if err != nil {
		return limits.TenantCalendar{}, err
	}
	c := limits.TenantCalendar{Current: cur}
	if tz.TimeZoneEffectiveFrom != nil {
		if c.Previous, err = zones.Calendar(tz.TimeZonePrevious); err != nil {
			return limits.TenantCalendar{}, err
		}
		c.From = *tz.TimeZoneEffectiveFrom
	}
	return c, nil
}

// reportInstant reads a civil date (the start of that day in the tenant's calendar) or an RFC 3339
// instant.
func reportInstant(cal limits.TenantCalendar, text string) (time.Time, bool) {
	if d, err := time.Parse("2006-01-02", text); err == nil {
		return cal.CivilStart(d.Year(), d.Month(), d.Day()), true
	}
	t, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// reportGroups reads group_by: a comma-separated subset of provider, model, caller and key.
func reportGroups(text string) (groups []string, ok bool) {
	if text == "" {
		return []string{"model"}, true
	}
	seen := map[string]bool{}
	for _, g := range strings.Split(text, ",") {
		switch g {
		case "provider", "model", "caller", "key":
		default:
			return nil, false
		}
		if seen[g] {
			return nil, false
		}
		seen[g] = true
		groups = append(groups, g)
	}
	return groups, true
}

// GetUsageReport reports the usage of a tenant by period and by provider, model, caller and key,
// always split by currency and price source (section 5.3).
func (s *server) GetUsageReport(ctx context.Context, in adminapi.GetUsageReportInput) (*adminapi.UsageReport, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	period := in.Period
	if period == "" {
		period = adminapi.ReportPeriodMonth
	}
	var errs []adminapi.FieldError
	if !period.Valid() {
		errs = append(errs, adminapi.FieldError{Field: "period", Message: "must be one of day, month, total"})
	}
	groups, gok := reportGroups(in.GroupBy)
	if !gok {
		errs = append(errs, adminapi.FieldError{Field: "group_by", Message: "must be a comma-separated list of provider, model, caller and key, each once"})
	}
	if in.Provider != "" && catalog.ValidateProviderName(in.Provider) != nil {
		errs = append(errs, adminapi.FieldError{Field: "provider", Message: "is not a provider name"})
	}
	if in.Model != "" && catalog.ValidateModelName(in.Model) != nil {
		errs = append(errs, adminapi.FieldError{Field: "model", Message: "is not a model name"})
	}
	now := s.now().UTC()
	var cal limits.TenantCalendar
	var rows []queries.UsageReportRow
	var bounds []time.Time
	var from, to time.Time
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var err error
		if cal, err = s.tenantCalendar(ctx, q); err != nil {
			return err
		}
		from, to = cal.Of(limits.PerMonth, now).Start, now
		var ok bool
		if in.From != "" {
			if from, ok = reportInstant(cal, in.From); !ok {
				errs = append(errs, adminapi.FieldError{Field: "from", Message: "must be a date (YYYY-MM-DD) or an RFC 3339 instant with a zone"})
			}
		}
		if in.To != "" {
			if to, ok = reportInstant(cal, in.To); !ok {
				errs = append(errs, adminapi.FieldError{Field: "to", Message: "must be a date (YYYY-MM-DD) or an RFC 3339 instant with a zone"})
			}
		}
		if len(errs) > 0 {
			return invalidFields(errs)
		}
		switch {
		case !from.Before(to):
			return invalid("from", "must be before to")
		case to.Sub(from) > maxReportWindow:
			return invalid("from", "the window is at most 400 days")
		}
		bounds = cal.Bounds(periodKind(period), from, to)
		rows, err = q.UsageReport(ctx, queries.UsageReportParams{
			ByProvider: has(groups, "provider"), ByModel: has(groups, "model"), ByCaller: has(groups, "caller"), ByKey: has(groups, "key"),
			Provider: in.Provider, Model: in.Model, MaxRows: maxReportRows, Bounds: bounds,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(rows) > maxReportRows {
		return nil, invalid("group_by", "the report has more than 10000 rows: narrow the window, the period or the grouping")
	}
	out := &adminapi.UsageReport{From: from.UTC(), To: to.UTC(), Period: period, TimeZone: cal.At(to).Zone(),
		GroupBy: groups, Rows: make([]adminapi.UsageReportRow, 0, len(rows))}
	for _, r := range rows {
		i := int(r.Period)
		if i < 1 || i >= len(bounds) {
			continue
		}
		row := adminapi.UsageReportRow{
			PeriodStart: bounds[i-1].UTC(), PeriodEnd: bounds[i].UTC(), Provider: r.Provider, Model: r.Model, ModelID: r.ModelID,
			PrincipalKind: r.PrincipalKind, PrincipalID: r.PrincipalID, KeyID: r.KeyID, Currency: r.Currency, PriceSource: r.PriceSource,
			Requests: int(r.Requests), RequestsOk: int(r.RequestsOk), RejectedLimit: int(r.RejectedLimit),
			InputTokens: int(r.InputTokens), OutputTokens: int(r.OutputTokens), CacheReadTokens: int(r.CacheReadTokens),
			CacheWriteTokens: int(r.CacheWriteTokens), ReasoningTokens: int(r.ReasoningTokens),
			EstimatedInputTokens: int(r.EstimatedInputTokens), EstimatedOutputTokens: int(r.EstimatedOutputTokens),
			Cost: adminapi.Null[string](), CostUnknown: int(r.CostUnknown),
		}
		if r.CostKnown > 0 {
			row.Cost = adminapi.Value(reportCost(r.Cost))
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// reportCost writes a sum of costs as the canonical decimal text.
func reportCost(sum string) string {
	if d, err := pricing.ParseDecimal(sum); err == nil {
		return d.String()
	}
	return sum
}

func periodKind(p adminapi.ReportPeriod) limits.PeriodKind {
	switch p {
	case adminapi.ReportPeriodDay:
		return limits.PerDay
	case adminapi.ReportPeriodMonth:
		return limits.PerMonth
	}
	return limits.PeriodTotal
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
