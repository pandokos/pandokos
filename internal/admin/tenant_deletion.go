package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errNotImplemented marks the operations of sprint 6.3 whose behaviour a later step adds.
var errNotImplemented = errors.New("admin: not implemented")

// RequestTenantDeletion suspends a tenant and schedules its deletion.
func (s *server) RequestTenantDeletion(context.Context, adminapi.RequestTenantDeletionInput) (*adminapi.TenantDeletion, error) {
	return nil, errNotImplemented
}

// CancelTenantDeletion removes a deletion that has not started.
func (s *server) CancelTenantDeletion(context.Context, adminapi.CancelTenantDeletionInput) (*adminapi.Tenant, error) {
	return nil, errNotImplemented
}

// GetTenantDeletion reads the deletion of a tenant.
func (s *server) GetTenantDeletion(context.Context, adminapi.GetTenantDeletionInput) (*adminapi.TenantDeletion, error) {
	return nil, errNotImplemented
}

// ListTenantDeletions lists the deletions, newest request first.
func (s *server) ListTenantDeletions(context.Context, adminapi.ListTenantDeletionsInput) (*adminapi.TenantDeletionList, error) {
	return nil, errNotImplemented
}

// GetDeletedTenantAuditHead reads the head of the audit chain kept for a deleted tenant.
func (s *server) GetDeletedTenantAuditHead(context.Context, adminapi.GetDeletedTenantAuditHeadInput) (*adminapi.AuditHead, error) {
	return nil, errNotImplemented
}

// ExportDeletedTenantAuditEvents exports the audit chain kept for a deleted tenant.
func (s *server) ExportDeletedTenantAuditEvents(context.Context, adminapi.ExportDeletedTenantAuditEventsInput) (*adminapi.Stream, error) {
	return nil, errNotImplemented
}
