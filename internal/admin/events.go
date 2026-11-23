package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errEventsNotBuilt is what the operations of sprint 6.4 answer until their parts are implemented
// (the exports, plan part C7). It is a plain error: the router
// answers it as an internal error and adds no error code.
var errEventsNotBuilt = errors.New("admin: event operations are not implemented yet")

func (s *server) ExportEvents(context.Context, adminapi.ExportEventsInput) (*adminapi.Stream, error) {
	return nil, errEventsNotBuilt
}

func (s *server) ExportPlatformEvents(context.Context, adminapi.ExportPlatformEventsInput) (*adminapi.Stream, error) {
	return nil, errEventsNotBuilt
}
