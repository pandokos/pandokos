package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// ListSubjectMappings: List the subject mappings of a tenant
func (s *server) ListSubjectMappings(ctx context.Context, in adminapi.ListSubjectMappingsInput) (*adminapi.SubjectMappingList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListSubjectMappingsRow
	err = s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		var err error
		rows, err = q.ListSubjectMappings(ctx, queries.ListSubjectMappingsParams{
			IssuerID: in.Issuer, ServiceAccountID: in.ServiceAccount, Subject: in.Subject, After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.SubjectMappingList{Items: []adminapi.SubjectMapping{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, mappingRow(r).out())
	}
	return out, nil
}

// errMappingIssuer and errMappingServiceAccount are what a mapping that names something the
// tenant cannot see is answered with. A registration of another tenant, a platform-level one that
// does not list the tenant, a service account of another tenant and an ID that exists nowhere give
// the same answer, so the request cannot be used to find out what exists elsewhere.
var (
	errMappingIssuer         = invalid("issuer", "must name a registration of this tenant, or a platform-level one that lists it")
	errMappingServiceAccount = invalid("service_account_id", "must name a service account of this tenant")
)

// CreateSubjectMapping: Let a token subject act as a service account
func (s *server) CreateSubjectMapping(ctx context.Context, in adminapi.CreateSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	b := in.Body
	if hasControl(b.Subject) {
		return nil, invalid("subject", "must not contain control characters")
	}
	if hasControl(b.Description) {
		return nil, invalid("description", "must not contain control characters")
	}
	var out adminapi.SubjectMapping
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		// The registration must be one the session sees: GetTrustedIssuer reads the tenant's own
		// rows, and GetTenantPlatformIssuer returns a row only where the tenant is listed.
		par := queries.CreateSubjectMappingParams{TenantID: tid.String(), Subject: b.Subject, ServiceAccountID: b.ServiceAccountID, Description: b.Description}
		var err error
		switch b.Issuer.Scope {
		case adminapi.IssuerScopeTenant:
			_, err = q.GetTrustedIssuer(ctx, b.Issuer.ID)
			par.TrustedIssuerID = &b.Issuer.ID
		case adminapi.IssuerScopePlatform:
			_, err = q.GetTenantPlatformIssuer(ctx, b.Issuer.ID)
			par.PlatformIssuerID = &b.Issuer.ID
		default:
			return errMappingIssuer
		}
		if isNoRowsErr(err) {
			return errMappingIssuer
		}
		if err != nil {
			return err
		}
		if _, err := q.GetServiceAccount(ctx, b.ServiceAccountID); err != nil {
			if isNoRowsErr(err) {
				return errMappingServiceAccount
			}
			return err
		}
		row, err := q.CreateSubjectMapping(ctx, par)
		if err != nil {
			return err
		}
		m := mappingRow(row)
		out = m.out()
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "subject_mapping.create", "subject_mapping", row.ID, m.detail()))
	})
	if err != nil {
		return nil, mapIssuerError(err)
	}
	return &out, nil
}

// GetSubjectMapping: Read a subject mapping
func (s *server) GetSubjectMapping(ctx context.Context, in adminapi.GetSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	var out adminapi.SubjectMapping
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		row, err := q.GetSubjectMapping(ctx, in.Mapping)
		if err != nil {
			return err
		}
		out = mappingRow(row).out()
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

// DeleteSubjectMapping: Delete a subject mapping
func (s *server) DeleteSubjectMapping(ctx context.Context, in adminapi.DeleteSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	var out adminapi.SubjectMapping
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		row, err := q.DeleteSubjectMapping(ctx, in.Mapping)
		if err != nil {
			return err
		}
		m := mappingRow(row)
		out = m.out()
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "subject_mapping.delete", "subject_mapping", row.ID, m.detail()))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
