package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// tenantEvent is the audit event of a successful change by the principal of the request in a
// tenant's log.
func tenantEvent(ctx context.Context, action, resourceType, resourceID string, detail map[string]any) audit.Event {
	return audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: action, ResourceType: resourceType, ResourceID: resourceID,
		Outcome: "success", RequestID: stateOf(ctx).id, Detail: detail}
}

// inTenantPath runs fn in the session of the tenant named by a path, with the statements bound to it.
func (s *server) inTenantPath(ctx context.Context, tenantText string, fn func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error) error {
	tid, err := tenantID(tenantText)
	if err != nil {
		return err
	}
	return s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error { return fn(ctx, tx, queries.New(tx), tid) })
}

// ListTenantPlatformIssuers: List the platform-level token issuers that list the tenant. The
// session is the tenant's: the policy of the tenant list leaves only its own entries, so nothing of
// the other tenants of a registration can be read.
func (s *server) ListTenantPlatformIssuers(ctx context.Context, in adminapi.ListTenantPlatformIssuersInput) (*adminapi.TenantPlatformIssuerList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListTenantPlatformIssuersRow
	err = s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		var err error
		rows, err = q.ListTenantPlatformIssuers(ctx, queries.ListTenantPlatformIssuersParams{After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.TenantPlatformIssuerList{Items: []adminapi.TenantPlatformIssuer{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, tenantPlatformIssuerOut(queries.GetTenantPlatformIssuerRow(r)))
	}
	return out, nil
}

// ListTokenIssuers: List the trusted token issuers of a tenant
func (s *server) ListTokenIssuers(ctx context.Context, in adminapi.ListTokenIssuersInput) (*adminapi.TokenIssuerList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, name, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListTrustedIssuersRow
	err = s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		var err error
		rows, err = q.ListTrustedIssuers(ctx, queries.ListTrustedIssuersParams{
			Name: in.Name, Enabled: string(in.Enabled), After: after, Sort: sortBy, AfterID: afterID(after, id), AfterName: name, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.TokenIssuerList{Items: []adminapi.TokenIssuer{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, rows[limit-1].Name, rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, tiRow(r).out())
	}
	return out, nil
}

// CreateTokenIssuer: Register a trusted token issuer
func (s *server) CreateTokenIssuer(ctx context.Context, in adminapi.CreateTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	v := tokenCreateValues(in.Body)
	if err := patchOfCreate(v).validate(); err != nil {
		return nil, err
	}
	var out adminapi.TokenIssuer
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		row, err := q.CreateTrustedIssuer(ctx, queries.CreateTrustedIssuerParams{
			TenantID: tid.String(), Name: v.Name, Description: v.Description, Issuer: v.Issuer, Audience: v.Audience, JwksUri: v.JwksURI,
			Algorithms: v.Algorithms, SubjectClaim: v.SubjectClaim, RequiredClaims: claimsText(v.RequiredClaims), Enabled: v.Enabled,
		})
		if err != nil {
			return err
		}
		out = tiRow(row).out()
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "token_issuer.create", "token_issuer", row.ID, v.createDetail()))
	})
	if err != nil {
		return nil, mapIssuerError(err)
	}
	return &out, nil
}

// GetTokenIssuer: Read a trusted token issuer
func (s *server) GetTokenIssuer(ctx context.Context, in adminapi.GetTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	var out adminapi.TokenIssuer
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		row, err := q.GetTrustedIssuer(ctx, in.Issuer)
		if err != nil {
			return err
		}
		out = tiRow(row).out()
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

// UpdateTokenIssuer: Change, disable or enable a trusted token issuer
func (s *server) UpdateTokenIssuer(ctx context.Context, in adminapi.UpdateTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	patch := patchOfToken(in.Body)
	if err := patch.validate(); err != nil {
		return nil, err
	}
	var out adminapi.TokenIssuer
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		if _, err := q.LockTrustedIssuer(ctx, in.Issuer); err != nil {
			return err
		}
		cur, err := q.GetTrustedIssuer(ctx, in.Issuer)
		if err != nil {
			return err
		}
		change, detail := patch.diff(tiRow(cur).values())
		if detail == nil {
			out = tiRow(cur).out()
			return nil
		}
		row, err := q.UpdateTrustedIssuer(ctx, queries.UpdateTrustedIssuerParams{
			ID: cur.ID, Name: change.Name, Description: change.Description, Issuer: change.Issuer, Audience: change.Audience, JwksUri: change.JwksURI,
			Algorithms: derefStrings(change.Algorithms), SubjectClaim: change.SubjectClaim, RequiredClaims: claimsPtr(change.RequiredClaims), Enabled: change.Enabled,
		})
		if err != nil {
			return err
		}
		out = tiRow(row).out()
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "token_issuer.update", "token_issuer", cur.ID, detail))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, mapIssuerError(err)
	}
	return &out, nil
}

func derefStrings(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// DeleteTokenIssuer: Delete a trusted token issuer with its subject mappings
func (s *server) DeleteTokenIssuer(ctx context.Context, in adminapi.DeleteTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	var out adminapi.TokenIssuer
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		// The lock keeps a mapping from being added between the count and the deletion.
		if _, err := q.LockTrustedIssuer(ctx, in.Issuer); err != nil {
			return err
		}
		n, err := q.CountSubjectMappingsOfIssuer(ctx, in.Issuer)
		if err != nil {
			return err
		}
		row, err := q.DeleteTrustedIssuer(ctx, in.Issuer)
		if err != nil {
			return err
		}
		out = tiRow(row).out()
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "token_issuer.delete", "token_issuer", row.ID,
			map[string]any{"name": row.Name, "mappings_deleted": n}))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
