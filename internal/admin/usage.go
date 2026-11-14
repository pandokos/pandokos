package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListUsageRecords lists the usage records of a tenant, newest first. Not implemented yet.
func (s *server) ListUsageRecords(_ context.Context, _ adminapi.ListUsageRecordsInput) (*adminapi.UsageRecordList, error) {
	return nil, errors.New("admin: not implemented")
}
