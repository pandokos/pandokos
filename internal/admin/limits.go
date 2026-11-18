package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// maxLimitRules is the number of limit rules a tenant may have (sprint 5.2, section 5.11); the tests
// of the bound lower it through export_test.go.
var maxLimitRules = 5000

// The operations on a tenant's rules run in the session of the tenant named by the path, whose
// permission (limit:read, limit:write, limit:delete) was decided before a handler ran. Which tenant
// is never read from a body.

func ruleFieldsOf(r queries.GetLimitRuleRow) ruleFields {
	return ruleFields{Metric: adminapi.LimitMetric(r.Metric), Value: r.LimitValue, Currency: r.Currency, Enforce: r.Enforce, Thresholds: r.Thresholds}
}

// ListLimits: List the limit rules of a tenant
func (s *server) ListLimits(ctx context.Context, in adminapi.ListLimitsInput) (*adminapi.LimitRuleList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListLimitRulesPageRow
	err = s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		var err error
		rows, err = q.ListLimitRulesPage(ctx, queries.ListLimitRulesPageParams{
			SubjectKind: string(in.SubjectKind), SubjectID: in.SubjectID, Metric: string(in.Metric), Provider: in.Provider, Model: in.Model,
			After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.LimitRuleList{Items: []adminapi.LimitRule{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, limitRuleOut(queries.GetLimitRuleRow(r)))
	}
	return out, nil
}

// CreateLimit: Create a limit rule of a tenant
func (s *server) CreateLimit(ctx context.Context, in adminapi.CreateLimitInput) (*adminapi.LimitRule, error) {
	b := in.Body
	kind := b.SubjectKind
	if kind == "" {
		kind = adminapi.LimitSubjectKindTenant
	}
	var errs []adminapi.FieldError
	switch {
	case kind == adminapi.LimitSubjectKindTenant && b.SubjectID != "":
		errs = append(errs, adminapi.FieldError{Field: "subject_id", Message: "is not accepted for the tenant"})
	case kind != adminapi.LimitSubjectKindTenant && b.SubjectID == "":
		errs = append(errs, adminapi.FieldError{Field: "subject_id", Message: "is required for this subject"})
	}
	if b.Model != "" && b.Provider == "" {
		errs = append(errs, adminapi.FieldError{Field: "model", Message: "needs a provider"})
	}
	f, ferrs := createFields(b.Metric, b.Limit, b.Amount, b.Currency, b.Enforce, b.Thresholds)
	if err := invalidFields(append(errs, ferrs...)); err != nil {
		return nil, err
	}
	var out adminapi.LimitRule
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		// The lock keeps two creations from passing the count together.
		if err := q.LockLimitRules(ctx); err != nil {
			return err
		}
		n, err := q.CountLimitRules(ctx)
		if err != nil {
			return err
		}
		if n >= int64(maxLimitRules) {
			return errInvalidState
		}
		found, err := q.LimitSubjectExists(ctx, queries.LimitSubjectExistsParams{SubjectKind: string(kind), SubjectID: b.SubjectID})
		if err != nil {
			return err
		}
		if !found {
			return invalid("subject_id", noSuchRuleSubj)
		}
		if b.Provider != "" {
			if found, err = q.LimitScopeExists(ctx, queries.LimitScopeExistsParams{Provider: b.Provider}); err != nil {
				return err
			} else if !found {
				return invalid("provider", "must name a provider")
			}
			if b.Model != "" {
				// A model counts when the tenant can use it: the same answer for one that is granted to
				// nobody, one of another tenant and one that does not exist.
				visible, err := visibleRefs(ctx, q)
				if err != nil {
					return err
				}
				if !visible[b.Provider+"/"+b.Model] {
					return invalid("model", "must name a model available to this tenant")
				}
			}
		}
		row, err := q.CreateLimitRule(ctx, queries.CreateLimitRuleParams{
			SubjectKind: string(kind), SubjectID: b.SubjectID, Provider: b.Provider, Model: b.Model, Metric: string(f.Metric),
			LimitValue: f.Value, Currency: f.Currency, Enforce: f.Enforce, Thresholds: f.Thresholds,
		})
		if err != nil {
			return err
		}
		out = limitRuleOut(queries.GetLimitRuleRow(row))
		detail := map[string]any{"subject_kind": row.SubjectKind, "subject_id": row.SubjectID, "provider": row.Provider, "model": row.Model}
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "limit.create", "limit_rule", row.ID,
			ruleDetail(detail, row.Metric, row.LimitValue, row.Currency, row.Enforce, row.Thresholds)))
	})
	if err != nil {
		return nil, mapLimitError(err)
	}
	return &out, nil
}

// GetLimit: Read a limit rule
func (s *server) GetLimit(ctx context.Context, in adminapi.GetLimitInput) (*adminapi.LimitRule, error) {
	if err := ruleID(in.Rule); err != nil {
		return nil, err
	}
	var out adminapi.LimitRule
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		row, err := q.GetLimitRule(ctx, in.Rule)
		if err != nil {
			return err
		}
		out = limitRuleOut(row)
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

// UpdateLimit: Change the value, the enforcement or the thresholds of a limit rule
func (s *server) UpdateLimit(ctx context.Context, in adminapi.UpdateLimitInput) (*adminapi.LimitRule, error) {
	ch := changeOf(in.Body)
	if ch.empty() {
		return nil, invalid("body", "must set at least one of limit, amount, enforce, thresholds")
	}
	if err := ruleID(in.Rule); err != nil {
		return nil, err
	}
	var out adminapi.LimitRule
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		cur, err := q.LockLimitRule(ctx, in.Rule)
		if err != nil {
			return err
		}
		next, detail, err := ch.apply(ruleFieldsOf(queries.GetLimitRuleRow(cur)))
		if err != nil {
			return err
		}
		if detail == nil {
			out = limitRuleOut(queries.GetLimitRuleRow(cur))
			return nil
		}
		row, err := q.UpdateLimitRule(ctx, queries.UpdateLimitRuleParams{ID: cur.ID, LimitValue: next.Value, Enforce: next.Enforce, Thresholds: next.Thresholds})
		if err != nil {
			return err
		}
		out = limitRuleOut(queries.GetLimitRuleRow(row))
		detail["subject_kind"], detail["subject_id"], detail["provider"], detail["model"], detail["metric"] =
			cur.SubjectKind, cur.SubjectID, cur.Provider, cur.Model, cur.Metric
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "limit.update", "limit_rule", cur.ID, detail))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, mapLimitError(err)
	}
	return &out, nil
}

// DeleteLimit: Delete a limit rule
func (s *server) DeleteLimit(ctx context.Context, in adminapi.DeleteLimitInput) (*adminapi.LimitRule, error) {
	if err := ruleID(in.Rule); err != nil {
		return nil, err
	}
	var out adminapi.LimitRule
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		row, err := q.DeleteLimitRule(ctx, in.Rule)
		if err != nil {
			return err
		}
		out = limitRuleOut(queries.GetLimitRuleRow(row))
		detail := map[string]any{"subject_kind": row.SubjectKind, "subject_id": row.SubjectID, "provider": row.Provider, "model": row.Model}
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "limit.delete", "limit_rule", row.ID,
			ruleDetail(detail, row.Metric, row.LimitValue, row.Currency, row.Enforce, row.Thresholds)))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTenantPlatformLimits: List the ceilings the platform sets for a tenant. The session is the
// tenant's: the policy of the table leaves the defaults and the tenant's own rows, so nothing of
// another tenant can be read.
func (s *server) ListTenantPlatformLimits(ctx context.Context, in adminapi.ListTenantPlatformLimitsInput) (*adminapi.PlatformLimitRuleList, error) {
	after, _, id, err := decodeCursor(in.Cursor, "id")
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListTenantPlatformLimitRulesRow
	err = s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		var err error
		rows, err = q.ListTenantPlatformLimitRules(ctx, queries.ListTenantPlatformLimitRulesParams{After: after, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.PlatformLimitRuleList{Items: []adminapi.PlatformLimitRule{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("id", "", rows[limit-1].ID)
			break
		}
		l, amount := valueOut(r.Metric, r.LimitValue)
		item := adminapi.PlatformLimitRule{ID: r.ID, Provider: r.Provider, Model: r.Model, Metric: adminapi.LimitMetric(r.Metric), Limit: l, Amount: amount,
			Currency: r.Currency, Enforce: r.Enforce, Thresholds: thresholdsOut(r.Thresholds), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
		if !r.IsDefault {
			item.Tenant = in.Tenant
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ListLimitEvents: List what the tenant's quotas and budgets recorded, newest first
func (s *server) ListLimitEvents(ctx context.Context, in adminapi.ListLimitEventsInput) (*adminapi.LimitEventList, error) {
	after, _, id, err := decodeCursor(in.Cursor, "-id")
	if err != nil {
		return nil, err
	}
	if in.Rule != "" {
		if _, err := tenant.ParseID(in.Rule); err != nil {
			return nil, invalid("rule", "must be an identifier")
		}
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListLimitEventsRow
	err = s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		var err error
		rows, err = q.ListLimitEvents(ctx, queries.ListLimitEventsParams{Kind: string(in.Kind), RuleID: in.Rule, After: after, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.LimitEventList{Items: []adminapi.LimitEvent{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("-id", "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, limitEventOut(r))
	}
	return out, nil
}

func limitEventOut(r queries.ListLimitEventsRow) adminapi.LimitEvent {
	l, amount := valueOut(r.Metric, r.LimitValue)
	return adminapi.LimitEvent{ID: r.ID, Kind: adminapi.LimitEventKind(r.Kind), RuleID: r.RuleID, RuleLevel: adminapi.LimitRuleLevel(r.RuleLevel),
		SubjectKind: adminapi.LimitSubjectKind(r.SubjectKind), SubjectID: r.SubjectID, Provider: r.Provider, Model: r.Model,
		Metric: adminapi.LimitMetric(r.Metric), Currency: r.Currency, PeriodStart: r.PeriodStart.UTC(), PeriodEnd: r.PeriodEnd.UTC(),
		Percent: int(r.Percent), Limit: l, Amount: amount, Observed: observedText(r.Observed), OccurredAt: r.OccurredAt.UTC()}
}
