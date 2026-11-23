package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errEventsNotBuilt is what the operations of sprint 6.4 answer until their parts are implemented
// (the destinations and the exports, plan parts C6 and C7). It is a plain error: the router
// answers it as an internal error and adds no error code.
var errEventsNotBuilt = errors.New("admin: event operations are not implemented yet")

func (s *server) ListEventDestinations(context.Context, adminapi.ListEventDestinationsInput) (*adminapi.EventDestinationList, error) {
	return nil, errEventsNotBuilt
}

func (s *server) CreateEventDestination(context.Context, adminapi.CreateEventDestinationInput) (*adminapi.EventDestinationCreated, error) {
	return nil, errEventsNotBuilt
}

func (s *server) GetEventDestination(context.Context, adminapi.GetEventDestinationInput) (*adminapi.EventDestination, error) {
	return nil, errEventsNotBuilt
}

func (s *server) UpdateEventDestination(context.Context, adminapi.UpdateEventDestinationInput) (*adminapi.EventDestination, error) {
	return nil, errEventsNotBuilt
}

func (s *server) DeleteEventDestination(context.Context, adminapi.DeleteEventDestinationInput) (*adminapi.NoContent, error) {
	return nil, errEventsNotBuilt
}

func (s *server) ListEventDeadLetters(context.Context, adminapi.ListEventDeadLettersInput) (*adminapi.EventDeadLetterList, error) {
	return nil, errEventsNotBuilt
}

func (s *server) ReplayEventDeadLetters(context.Context, adminapi.ReplayEventDeadLettersInput) (*adminapi.EventDeadLetterReplayed, error) {
	return nil, errEventsNotBuilt
}

func (s *server) RewindEventDestination(context.Context, adminapi.RewindEventDestinationInput) (*adminapi.EventDestination, error) {
	return nil, errEventsNotBuilt
}

func (s *server) RotateEventDestinationSecret(context.Context, adminapi.RotateEventDestinationSecretInput) (*adminapi.EventDestinationCreated, error) {
	return nil, errEventsNotBuilt
}

func (s *server) TestEventDestination(context.Context, adminapi.TestEventDestinationInput) (*adminapi.EventDestinationTest, error) {
	return nil, errEventsNotBuilt
}

func (s *server) ExportEvents(context.Context, adminapi.ExportEventsInput) (*adminapi.Stream, error) {
	return nil, errEventsNotBuilt
}

func (s *server) ExportPlatformEvents(context.Context, adminapi.ExportPlatformEventsInput) (*adminapi.Stream, error) {
	return nil, errEventsNotBuilt
}
