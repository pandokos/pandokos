package admin

import (
	"context"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// restrictionOut is the representation of a tenant's restriction; rows is nil for none.
func restrictionOut(models []string, updated *time.Time) adminapi.ModelRestriction {
	if updated == nil {
		return adminapi.ModelRestriction{Models: []string{}}
	}
	u := updated.UTC()
	return adminapi.ModelRestriction{Restricted: true, Models: nonNil(models), UpdatedAt: &u}
}

// tenantRestriction reads the restriction of the tenant of q: ok is false when there is none.
func tenantRestriction(ctx context.Context, q *queries.Queries) (row queries.GetModelRestrictionRow, ok bool, err error) {
	row, err = q.GetModelRestriction(ctx)
	switch {
	case isNoRowsErr(err):
		return row, false, nil
	case err != nil:
		return row, false, err
	}
	return row, true, nil
}

// ListTenantModels lists the models a tenant may use, each with whether the tenant's own
// restriction allows it and the price in force now. A restriction flags, it does not hide.
func (s *server) ListTenantModels(ctx context.Context, in adminapi.ListTenantModelsInput) (*adminapi.TenantModelList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	after, _, id, err := decodeCursor(in.Cursor, "models")
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	out := &adminapi.TenantModelList{Items: []adminapi.TenantModel{}}
	now := s.now()
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		rows, err := q.ListVisibleModels(ctx, queries.ListVisibleModelsParams{Provider: in.Provider, After: after, AfterID: afterID(after, id), PageSize: ask})
		if err != nil {
			return err
		}
		res, restricted, err := tenantRestriction(ctx, q)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, r := range res.Models {
			allowed[r] = true
		}
		for i, r := range rows {
			if i == limit {
				out.NextCursor = encodeCursor("models", "", rows[limit-1].ID)
				break
			}
			m := modelOut(r)
			item := adminapi.TenantModel{Model: m, Allowed: !restricted || allowed[m.Ref]}
			pv, err := q.PriceAt(ctx, queries.PriceAtParams{ModelID: r.ID, At: &now})
			switch {
			case err == nil:
				v, err := priceOut(priceFromAt(pv))
				if err != nil {
					return err
				}
				item.Price = &v
			case !isNoRowsErr(err):
				return err
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetModelRestriction reads the restriction of a tenant.
func (s *server) GetModelRestriction(ctx context.Context, in adminapi.GetModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var out adminapi.ModelRestriction
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		row, ok, err := tenantRestriction(ctx, queries.New(tx))
		if err != nil {
			return err
		}
		if ok {
			out = restrictionOut(row.Models, &row.UpdatedAt)
		} else {
			out = restrictionOut(nil, nil)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetModelRestriction replaces the restriction of a tenant; every reference must name a model
// the tenant may use.
func (s *server) SetModelRestriction(ctx context.Context, in adminapi.SetModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	models := nonNil(in.Body.Models)
	var out adminapi.ModelRestriction
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if err := checkVisibleRefs(ctx, q, models); err != nil {
			return err
		}
		prev, had, err := tenantRestriction(ctx, q)
		if err != nil {
			return err
		}
		if had && sameStrings(prev.Models, models) {
			out = restrictionOut(prev.Models, &prev.UpdatedAt)
			return nil
		}
		row, err := q.SetModelRestriction(ctx, queries.SetModelRestrictionParams{TenantID: tid.String(), Models: models})
		if err != nil {
			return err
		}
		out = restrictionOut(row.Models, &row.UpdatedAt)
		detail := map[string]any{}
		addRefs(detail, "models", models)
		if had {
			addRefs(detail, "previous", prev.Models)
		}
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: "model_restriction.set", ResourceType: "model_restriction",
			ResourceID: tid.String(), Outcome: "success", RequestID: stateOf(ctx).id, Detail: detail})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearModelRestriction removes the restriction of a tenant; clearing none changes nothing.
func (s *server) ClearModelRestriction(ctx context.Context, in adminapi.ClearModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		prev, had, err := tenantRestriction(ctx, q)
		if err != nil || !had {
			return err
		}
		if _, err := q.ClearModelRestriction(ctx); err != nil {
			return err
		}
		detail := map[string]any{}
		addRefs(detail, "previous", prev.Models)
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: "model_restriction.clear", ResourceType: "model_restriction",
			ResourceID: tid.String(), Outcome: "success", RequestID: stateOf(ctx).id, Detail: detail})
	})
	if err != nil {
		return nil, err
	}
	out := restrictionOut(nil, nil)
	return &out, nil
}
