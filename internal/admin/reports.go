package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errNotImplemented is what the stubs of sprint 6.2 answer (a 500) until their parts are built.
var errNotImplemented = errors.New("admin: not implemented")

// GetUsageReport is a stub until sprint 6.2 part C4.
func (s *server) GetUsageReport(context.Context, adminapi.GetUsageReportInput) (*adminapi.UsageReport, error) {
	return nil, errNotImplemented
}
