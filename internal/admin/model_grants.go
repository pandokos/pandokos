package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

func grantOut(r queries.GetModelGrantRow) adminapi.ModelGrant {
	return adminapi.ModelGrant{ModelID: r.ModelID, TenantID: r.TenantID, TenantName: r.TenantName, CreatedAt: r.CreatedAt.UTC()}
}

// ListModelGrants lists the tenants a model is granted to, by tenant ID.
func (s *server) ListModelGrants(ctx context.Context, in adminapi.ListModelGrantsInput) (*adminapi.ModelGrantList, error) {
	after, _, id, err := decodeCursor(in.Cursor, "tenant")
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListModelGrantsRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		if _, err := q.GetModel(ctx, in.Model); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		var err error
		rows, err = q.ListModelGrants(ctx, queries.ListModelGrantsParams{ModelID: in.Model, After: after, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.ModelGrantList{Items: []adminapi.ModelGrant{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("tenant", "", rows[limit-1].TenantID)
			break
		}
		out.Items = append(out.Items, adminapi.ModelGrant{ModelID: r.ModelID, TenantID: r.TenantID, TenantName: r.TenantName, CreatedAt: r.CreatedAt.UTC()})
	}
	return out, nil
}

// GrantModel grants a model to a tenant; granting twice changes nothing.
func (s *server) GrantModel(ctx context.Context, in adminapi.GrantModelInput) (*adminapi.ModelGrant, error) {
	var out adminapi.ModelGrant
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		m, err := q.GetModel(ctx, in.Model)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.GetTenant(ctx, in.Tenant); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		n, err := q.GrantModel(ctx, queries.GrantModelParams{ModelID: m.ID, TenantID: in.Tenant})
		if err != nil {
			return err
		}
		g, err := q.GetModelGrant(ctx, queries.GetModelGrantParams{ModelID: m.ID, TenantID: in.Tenant})
		if err != nil {
			return err
		}
		out = grantOut(g)
		if n == 0 {
			return nil
		}
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "model_grant.create", "model", m.ID, map[string]any{
			"provider": m.Provider, "name": m.Name, "tenant_id": g.TenantID, "tenant_name": g.TenantName}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// RevokeModelGrant removes a grant and returns it as it was.
func (s *server) RevokeModelGrant(ctx context.Context, in adminapi.RevokeModelGrantInput) (*adminapi.ModelGrant, error) {
	var out adminapi.ModelGrant
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		m, err := q.GetModel(ctx, in.Model)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		g, err := q.GetModelGrant(ctx, queries.GetModelGrantParams{ModelID: m.ID, TenantID: in.Tenant})
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.RevokeModelGrant(ctx, queries.RevokeModelGrantParams{ModelID: m.ID, TenantID: in.Tenant}); err != nil {
			return err
		}
		out = grantOut(g)
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "model_grant.delete", "model", m.ID, map[string]any{
			"provider": m.Provider, "name": m.Name, "tenant_id": g.TenantID, "tenant_name": g.TenantName}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}
