package admin

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// afterActiveCurrencySet is a test seam (T-751): when set, setActiveCurrency calls it right after the
// write of the tenant row and fails with its error. Nil in production.
var afterActiveCurrencySet func() error

// maxInactiveLimits bounds the list of inactive limits an answer carries.
const maxInactiveLimits = 100

// GetActiveCurrency reads the active currency of a tenant.
func (s *server) GetActiveCurrency(ctx context.Context, in adminapi.GetActiveCurrencyInput) (*adminapi.ActiveCurrency, error) {
	var out adminapi.ActiveCurrency
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		row, err := q.GetTenantActiveCurrency(ctx)
		if err != nil {
			return err
		}
		out = s.activeCurrencyOut(row.ActiveCurrency, row.ActiveCurrencyPrevious, row.ActiveCurrencyEffectiveFrom, s.now())
		return nil
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetActiveCurrency chooses the active currency of a tenant: the change rule of section 5.3, in the
// session of the tenant. A currency other than the default needs a list; the change takes effect at
// the next change of month of the zone in force, the instant a change of zone takes effect too.
func (s *server) SetActiveCurrency(ctx context.Context, in adminapi.SetActiveCurrencyInput) (*adminapi.ActiveCurrency, error) {
	to := in.Body.Currency
	var out adminapi.ActiveCurrency
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		zones := s.limits.Zones
		if zones == nil {
			return errors.New("admin: the active currency needs the zones of the installation")
		}
		cur, err := q.LockTenantActiveCurrency(ctx, tid.String())
		if err != nil {
			return err
		}
		pending := cur.ActiveCurrencyEffectiveFrom != nil && cur.DbNow.Before(*cur.ActiveCurrencyEffectiveFrom)
		inForce := cur.ActiveCurrency
		if pending {
			inForce = cur.ActiveCurrencyPrevious
		}
		var (
			prev, next string
			from       *time.Time
			write      bool
		)
		switch {
		case to == cur.ActiveCurrency:
			// Nothing is written; the answer shows a pending instant.
			prev, next, from = cur.ActiveCurrencyPrevious, to, cur.ActiveCurrencyEffectiveFrom
		case to == inForce:
			prev, next, from, write = "", to, nil, true
		case pending:
			prev, next, from, write = cur.ActiveCurrencyPrevious, to, cur.ActiveCurrencyEffectiveFrom, true
		default:
			// The instant is that of the calendar of the zone in force (and of a change of zone pending).
			zone := zoneInForce(cur.TimeZone, cur.TimeZonePrevious, cur.TimeZoneEffectiveFrom, cur.DbNow)
			cal, err := zones.Calendar(zone)
			if err != nil {
				return err
			}
			at := limits.NextChange(cal, cur.DbNow)
			prev, next, from, write = inForce, to, &at, true
		}
		if write && to != "" {
			has, err := q.CurrencyHasList(ctx, to)
			if err != nil {
				return err
			}
			if !has {
				return invalid("currency", "no price list in this currency: add prices in it first")
			}
		}
		if write {
			if _, err := q.SetTenantActiveCurrency(ctx, queries.SetTenantActiveCurrencyParams{ID: tid.String(), ActiveCurrency: next, ActiveCurrencyPrevious: prev, ActiveCurrencyEffectiveFrom: from}); err != nil {
				return err
			}
			if afterActiveCurrencySet != nil {
				if err := afterActiveCurrencySet(); err != nil {
					return err
				}
			}
		}
		out = s.activeCurrencyOut(next, prev, from, cur.DbNow)
		inactive, err := s.inactiveLimits(ctx, q, s.currencyName(next))
		if err != nil {
			return err
		}
		out.InactiveLimits = inactive
		if !write {
			return nil
		}
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "tenant.active_currency_update", "tenant", tid.String(), map[string]any{
			"from": cur.ActiveCurrency, "to": next, "in_force": out.InForce, "effective_from": fmtTime(out.EffectiveFrom), "inactive_limits": len(inactive)}))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// inactiveLimits lists, for a new currency, the cost rules of the tenant and the ceilings of the platform
// that apply to it whose currency is another: from the change they count and refuse nothing. A default
// ceiling the tenant's own ceiling of the same scope, metric and currency replaces is left out. Ordered
// by level, then rule ID; at most the first 100. Both lists are read in the session of the tenant.
func (s *server) inactiveLimits(ctx context.Context, q *queries.Queries, currency string) ([]adminapi.InactiveLimit, error) {
	rules, err := q.ListLimitRules(ctx)
	if err != nil {
		return nil, err
	}
	ceilings, err := q.ListPlatformLimitRules(ctx)
	if err != nil {
		return nil, err
	}
	var out []adminapi.InactiveLimit
	for _, r := range rules {
		if m := adminapi.LimitMetric(r.Metric); costMetric(m) && r.Currency != currency {
			out = append(out, adminapi.InactiveLimit{Level: "tenant", RuleID: r.ID, Metric: m, Currency: r.Currency})
		}
	}
	type slot struct{ provider, model, metric, currency string }
	own := map[slot]bool{}
	for _, r := range ceilings {
		if !r.IsDefault {
			own[slot{r.ProviderID, r.ModelID, r.Metric, r.Currency}] = true
		}
	}
	for _, r := range ceilings {
		m := adminapi.LimitMetric(r.Metric)
		if !costMetric(m) || r.Currency == currency || (r.IsDefault && own[slot{r.ProviderID, r.ModelID, r.Metric, r.Currency}]) {
			continue
		}
		out = append(out, adminapi.InactiveLimit{Level: "platform", RuleID: r.ID, Metric: m, Currency: r.Currency})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level < out[j].Level
		}
		return out[i].RuleID < out[j].RuleID
	})
	if len(out) > maxInactiveLimits {
		out = out[:maxInactiveLimits]
	}
	return out, nil
}
