package resolver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/gateway"
)

// Catalogue implements gateway.Catalogue over the catalogue cache.
type Catalogue struct {
	cache *catalog.Cache
	log   *slog.Logger
}

// NewCatalogue returns the gateway's view of c.
func NewCatalogue(c *catalog.Cache, log *slog.Logger) *Catalogue {
	return &Catalogue{cache: c, log: log}
}

// Provider returns the API family of an enabled provider.
func (c *Catalogue) Provider(ctx context.Context, name string) (string, bool, error) {
	_, _ = ctx, name
	return "", false, errors.New("resolver: not implemented")
}

// Resolve returns the target for a model.
func (c *Catalogue) Resolve(ctx context.Context, p authz.Principal, provider, model string) (gateway.Target, error) {
	_, _, _, _ = ctx, p, provider, model
	return gateway.Target{}, errors.New("resolver: not implemented")
}
