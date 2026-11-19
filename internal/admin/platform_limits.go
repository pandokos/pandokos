package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The operations on the ceilings of the platform run in platform scope and their audit rows go to
// the platform log only. Which principals may call them is decided before a handler runs, by the
// permissions platform_limit:* of the document, which only platform roles hold.

// observedText is the decimal of the column without trailing zeros.
func observedText(s string) string {
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		return s
	}
	return d.String()
}

func platformFieldsOf(r queries.GetPlatformLimitRuleRow) ruleFields {
	return ruleFields{Metric: adminapi.LimitMetric(r.Metric), Value: r.LimitValue, Currency: r.Currency, Enforce: r.Enforce, Thresholds: r.Thresholds}
}

func tenantOfRule(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ListPlatformLimits: List the ceilings the platform sets for tenants
func (s *server) ListPlatformLimits(ctx context.Context, in adminapi.ListPlatformLimitsInput) (*adminapi.PlatformLimitRuleList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	if in.AppliesTo != "" && in.AppliesTo != "default" {
		if _, err := tenant.ParseID(in.AppliesTo); err != nil {
			return nil, invalid("applies_to", `must be "default" or the identifier of a tenant`)
		}
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListPlatformLimitRulesPageRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		rows, err = q.ListPlatformLimitRulesPage(ctx, queries.ListPlatformLimitRulesPageParams{
			AppliesTo: in.AppliesTo, Metric: string(in.Metric), After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.PlatformLimitRuleList{Items: []adminapi.PlatformLimitRule{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, platformRuleOut(queries.GetPlatformLimitRuleRow(r)))
	}
	return out, nil
}

// CreatePlatformLimit: Create a ceiling for every tenant or for one
func (s *server) CreatePlatformLimit(ctx context.Context, in adminapi.CreatePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	b := in.Body
	var errs []adminapi.FieldError
	var forTenant *string
	if b.Tenant != "" {
		if _, err := tenant.ParseID(b.Tenant); err != nil {
			errs = append(errs, adminapi.FieldError{Field: "tenant", Message: noSuchRuleTenant})
		}
		forTenant = &b.Tenant
	}
	if b.Model != "" && b.Provider == "" {
		errs = append(errs, adminapi.FieldError{Field: "model", Message: "needs a provider"})
	}
	f, ferrs := createFields(b.Metric, b.Limit, b.Amount, b.Currency, b.Enforce, b.Thresholds)
	if err := invalidFields(append(errs, ferrs...)); err != nil {
		return nil, err
	}
	var out adminapi.PlatformLimitRule
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		var scope queries.ResolveLimitScopeRow
		if b.Provider != "" {
			var err error
			scope, err = q.ResolveLimitScope(ctx, queries.ResolveLimitScopeParams{Provider: b.Provider, Model: b.Model})
			if isNoRowsErr(err) {
				return invalid("provider", "must name a provider")
			}
			if err != nil {
				return err
			}
			if b.Model != "" && scope.ModelID == "" {
				return invalid("model", "must name a model of the provider")
			}
		}
		bs, err := s.platformBudgetScope(ctx, q, nilIfEmpty(scope.ProviderID), nilIfEmpty(scope.ModelID), forTenant)
		if err != nil {
			return err
		}
		if err := checkBudgetCurrency(ctx, q, string(f.Metric), f.Currency, bs); err != nil {
			return err
		}
		row, err := q.CreatePlatformLimitRule(ctx, queries.CreatePlatformLimitRuleParams{
			TenantID: forTenant, ProviderID: nilIfEmpty(scope.ProviderID), ModelID: nilIfEmpty(scope.ModelID), Metric: string(f.Metric), LimitValue: f.Value, Currency: f.Currency,
			Enforce: f.Enforce, Thresholds: f.Thresholds,
		})
		if err != nil {
			return err
		}
		out = platformRuleOut(queries.GetPlatformLimitRuleRow(row))
		detail := map[string]any{"tenant": tenantOfRule(row.TenantID), "provider": row.Provider, "model": row.Model}
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_limit.create", "platform_limit_rule", row.ID,
			ruleDetail(detail, row.ProviderID, row.ModelID, row.Metric, row.LimitValue, row.Currency, row.Enforce, row.Thresholds)))
	})
	if err != nil {
		return nil, mapLimitError(err)
	}
	return &out, nil
}

// GetPlatformLimit: Read a platform ceiling
func (s *server) GetPlatformLimit(ctx context.Context, in adminapi.GetPlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	if err := ruleID(in.Rule); err != nil {
		return nil, err
	}
	var out adminapi.PlatformLimitRule
	err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		row, err := q.GetPlatformLimitRule(ctx, in.Rule)
		if err != nil {
			return err
		}
		out = platformRuleOut(row)
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

// UpdatePlatformLimit: Change the value, the enforcement or the thresholds of a platform ceiling
func (s *server) UpdatePlatformLimit(ctx context.Context, in adminapi.UpdatePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	ch := changeOf(in.Body)
	if ch.empty() {
		return nil, invalid("body", "must set at least one of limit, amount, enforce, thresholds")
	}
	if err := ruleID(in.Rule); err != nil {
		return nil, err
	}
	var out adminapi.PlatformLimitRule
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		cur, err := q.LockPlatformLimitRule(ctx, in.Rule)
		if err != nil {
			return err
		}
		next, detail, err := ch.apply(platformFieldsOf(queries.GetPlatformLimitRuleRow(cur)))
		if err != nil {
			return err
		}
		if detail == nil {
			out = platformRuleOut(queries.GetPlatformLimitRuleRow(cur))
			return nil
		}
		bs, err := s.platformBudgetScope(ctx, q, nilIfEmpty(cur.ProviderID), nilIfEmpty(cur.ModelID), cur.TenantID)
		if err != nil {
			return err
		}
		if err := checkBudgetCurrency(ctx, q, cur.Metric, cur.Currency, bs); err != nil {
			return err
		}
		row, err := q.UpdatePlatformLimitRule(ctx, queries.UpdatePlatformLimitRuleParams{ID: cur.ID, LimitValue: next.Value, Enforce: next.Enforce, Thresholds: next.Thresholds})
		if err != nil {
			return err
		}
		out = platformRuleOut(queries.GetPlatformLimitRuleRow(row))
		detail["tenant"], detail["provider"], detail["model"], detail["metric"] = tenantOfRule(cur.TenantID), cur.Provider, cur.Model, cur.Metric
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_limit.update", "platform_limit_rule", cur.ID, detail))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, mapLimitError(err)
	}
	return &out, nil
}

// DeletePlatformLimit: Delete a platform ceiling
func (s *server) DeletePlatformLimit(ctx context.Context, in adminapi.DeletePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	if err := ruleID(in.Rule); err != nil {
		return nil, err
	}
	var out adminapi.PlatformLimitRule
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		row, err := q.DeletePlatformLimitRule(ctx, in.Rule)
		if err != nil {
			return err
		}
		out = platformRuleOut(queries.GetPlatformLimitRuleRow(row))
		detail := map[string]any{"tenant": tenantOfRule(row.TenantID), "provider": row.Provider, "model": row.Model}
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_limit.delete", "platform_limit_rule", row.ID,
			ruleDetail(detail, row.ProviderID, row.ModelID, row.Metric, row.LimitValue, row.Currency, row.Enforce, row.Thresholds)))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
