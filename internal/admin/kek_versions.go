package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListKEKVersions lists the versions of the key-encryption key.
func (s *server) ListKEKVersions(context.Context, adminapi.ListKEKVersionsInput) (*adminapi.KEKVersionList, error) {
	return nil, errNotImplemented
}
