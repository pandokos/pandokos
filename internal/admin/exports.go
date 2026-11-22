package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ExportUsageRecords is a stub until sprint 6.2 part C5.
func (s *server) ExportUsageRecords(context.Context, adminapi.ExportUsageRecordsInput) (*adminapi.Stream, error) {
	return nil, errNotImplemented
}

// ExportAuditEvents is a stub until sprint 6.2 part C5.
func (s *server) ExportAuditEvents(context.Context, adminapi.ExportAuditEventsInput) (*adminapi.Stream, error) {
	return nil, errNotImplemented
}

// ExportPlatformAuditEvents is a stub until sprint 6.2 part C5.
func (s *server) ExportPlatformAuditEvents(context.Context, adminapi.ExportPlatformAuditEventsInput) (*adminapi.Stream, error) {
	return nil, errNotImplemented
}
