package admin

import (
	"context"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// The operations on platform-level registrations run in platform scope and their audit rows go to
// the platform log only. Which principals may call them is decided before a handler runs, by the
// permissions platform_issuer:* of the document, which only platform roles hold.

// ListPlatformIssuers: List the platform-level token issuers
func (s *server) ListPlatformIssuers(ctx context.Context, in adminapi.ListPlatformIssuersInput) (*adminapi.PlatformIssuerList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, name, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListPlatformIssuersRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		rows, err = q.ListPlatformIssuers(ctx, queries.ListPlatformIssuersParams{
			Name: in.Name, Enabled: string(in.Enabled), After: after, Sort: sortBy, AfterID: afterID(after, id), AfterName: name, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.PlatformIssuerList{Items: []adminapi.PlatformIssuer{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, rows[limit-1].Name, rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, piRow(r).out())
	}
	return out, nil
}

// CreatePlatformIssuer: Register a platform-level token issuer
func (s *server) CreatePlatformIssuer(ctx context.Context, in adminapi.CreatePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	v := platformCreateValues(in.Body)
	if err := patchOfCreate(v).validate(); err != nil {
		return nil, err
	}
	var out adminapi.PlatformIssuer
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		row, err := q.CreatePlatformIssuer(ctx, queries.CreatePlatformIssuerParams{
			Name: v.Name, Description: v.Description, Issuer: v.Issuer, Audience: v.Audience, JwksUri: v.JwksURI, Algorithms: v.Algorithms,
			SubjectClaim: v.SubjectClaim, TenantClaim: v.TenantClaim, RequiredClaims: claimsText(v.RequiredClaims), Enabled: v.Enabled,
		})
		if err != nil {
			return err
		}
		out = piRow(row).out()
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_issuer.create", "platform_issuer", row.ID, v.createDetail()))
	})
	if err != nil {
		return nil, mapIssuerError(err)
	}
	return &out, nil
}

// GetPlatformIssuer: Read a platform-level token issuer
func (s *server) GetPlatformIssuer(ctx context.Context, in adminapi.GetPlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	var out adminapi.PlatformIssuer
	err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		row, err := q.GetPlatformIssuer(ctx, in.Issuer)
		if err != nil {
			return err
		}
		out = piRow(row).out()
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

// UpdatePlatformIssuer: Change, disable or enable a platform-level token issuer
func (s *server) UpdatePlatformIssuer(ctx context.Context, in adminapi.UpdatePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	patch := patchOfPlatform(in.Body)
	if err := patch.validate(); err != nil {
		return nil, err
	}
	var out adminapi.PlatformIssuer
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		if _, err := q.LockPlatformIssuer(ctx, in.Issuer); err != nil {
			return err
		}
		cur, err := q.GetPlatformIssuer(ctx, in.Issuer)
		if err != nil {
			return err
		}
		change, detail := patch.diff(piRow(cur).values())
		if detail == nil {
			out = piRow(cur).out()
			return nil
		}
		row, err := q.UpdatePlatformIssuer(ctx, queries.UpdatePlatformIssuerParams{
			ID: cur.ID, Name: change.Name, Description: change.Description, Issuer: change.Issuer, Audience: change.Audience, JwksUri: change.JwksURI,
			Algorithms: derefStrings(change.Algorithms), SubjectClaim: change.SubjectClaim, TenantClaim: change.TenantClaim,
			RequiredClaims: claimsPtr(change.RequiredClaims), Enabled: change.Enabled,
		})
		if err != nil {
			return err
		}
		out = piRow(row).out()
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_issuer.update", "platform_issuer", cur.ID, detail))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, mapIssuerError(err)
	}
	return &out, nil
}

// DeletePlatformIssuer: Delete a platform-level token issuer with its tenant list and subject mappings
func (s *server) DeletePlatformIssuer(ctx context.Context, in adminapi.DeletePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	var out adminapi.PlatformIssuer
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		if _, err := q.LockPlatformIssuer(ctx, in.Issuer); err != nil {
			return err
		}
		// The mappings are the tenants' and the platform session cannot read them: a definer
		// function of the database counts them, and the cascade of the delete removes them.
		n, err := q.CountPlatformIssuerMappings(ctx, in.Issuer)
		if err != nil {
			return err
		}
		row, err := q.DeletePlatformIssuer(ctx, in.Issuer)
		if err != nil {
			return err
		}
		out = piRow(row).out()
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_issuer.delete", "platform_issuer", row.ID,
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

// ListPlatformIssuerTenants: List the tenants of a platform-level token issuer
func (s *server) ListPlatformIssuerTenants(ctx context.Context, in adminapi.ListPlatformIssuerTenantsInput) (*adminapi.PlatformIssuerTenantList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, at, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	afterAt := time.Time{}
	if after {
		if afterAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, invalid("cursor", "is not a cursor of this collection and sort")
		}
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListPlatformIssuerTenantsRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		if _, err := q.GetPlatformIssuer(ctx, in.Issuer); err != nil {
			return err
		}
		var err error
		rows, err = q.ListPlatformIssuerTenants(ctx, queries.ListPlatformIssuerTenantsParams{
			IssuerID: in.Issuer, After: after, Sort: sortBy, AfterCreatedAt: afterAt, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &adminapi.PlatformIssuerTenantList{Items: []adminapi.PlatformIssuerTenant{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeCursor(sortBy, last.CreatedAt.UTC().Format(time.RFC3339Nano), last.TenantID)
			break
		}
		out.Items = append(out.Items, issuerTenantOut(r))
	}
	return out, nil
}

// PutPlatformIssuerTenant: Add a tenant to a platform-level token issuer, or change its claim value
func (s *server) PutPlatformIssuerTenant(ctx context.Context, in adminapi.PutPlatformIssuerTenantInput) (*adminapi.PlatformIssuerTenant, error) {
	if hasControl(in.Body.ClaimValue) {
		return nil, invalid("claim_value", "must not contain control characters")
	}
	var out adminapi.PlatformIssuerTenant
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		if _, err := q.LockPlatformIssuer(ctx, in.Issuer); err != nil {
			return err
		}
		t, err := q.GetTenant(ctx, in.Tenant)
		if err != nil {
			return err
		}
		if t.State == "deleting" || t.State == "deleted" {
			return errNotFound
		}
		value := in.Body.ClaimValue
		if value == "" {
			value = t.ID
		}
		row, err := q.PutPlatformIssuerTenant(ctx, queries.PutPlatformIssuerTenantParams{IssuerID: in.Issuer, TenantID: t.ID, ClaimValue: value})
		if err != nil {
			return err
		}
		out = adminapi.PlatformIssuerTenant{IssuerID: row.IssuerID, TenantID: row.TenantID, TenantName: row.TenantName, ClaimValue: row.ClaimValue, CreatedAt: row.CreatedAt.UTC()}
		if row.PreviousClaimValue == row.ClaimValue {
			return nil // already so: nothing changed, nothing to audit
		}
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_issuer.tenant_add", "platform_issuer", in.Issuer,
			map[string]any{"tenant_id": row.TenantID, "claim_value": row.ClaimValue}))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, mapIssuerError(err)
	}
	return &out, nil
}

// DeletePlatformIssuerTenant: Remove a tenant from a platform-level token issuer
func (s *server) DeletePlatformIssuerTenant(ctx context.Context, in adminapi.DeletePlatformIssuerTenantInput) (*adminapi.PlatformIssuerTenant, error) {
	var out adminapi.PlatformIssuerTenant
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		if _, err := q.LockPlatformIssuer(ctx, in.Issuer); err != nil {
			return err
		}
		row, err := q.DeletePlatformIssuerTenant(ctx, queries.DeletePlatformIssuerTenantParams{IssuerID: in.Issuer, TenantID: in.Tenant})
		if err != nil {
			return err
		}
		out = adminapi.PlatformIssuerTenant{IssuerID: row.IssuerID, TenantID: row.TenantID, TenantName: row.TenantName, ClaimValue: row.ClaimValue, CreatedAt: row.CreatedAt.UTC()}
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "platform_issuer.tenant_remove", "platform_issuer", in.Issuer,
			map[string]any{"tenant_id": row.TenantID, "claim_value": row.ClaimValue}))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
