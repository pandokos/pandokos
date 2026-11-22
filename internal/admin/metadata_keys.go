package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// GetMetadataKeys is a stub until sprint 6.2 part C7.
func (s *server) GetMetadataKeys(context.Context, adminapi.GetMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	return nil, errNotImplemented
}

// SetMetadataKeys is a stub until sprint 6.2 part C7.
func (s *server) SetMetadataKeys(context.Context, adminapi.SetMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	return nil, errNotImplemented
}

// ClearMetadataKeys is a stub until sprint 6.2 part C7.
func (s *server) ClearMetadataKeys(context.Context, adminapi.ClearMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	return nil, errNotImplemented
}
