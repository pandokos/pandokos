package resolver

import (
	"context"
	"errors"
	"log/slog"
	"net/url"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/gateway"
)

// source is what the adapter reads: the two methods of *catalog.Cache that it uses.
type source interface {
	Snapshot(ctx context.Context) (*catalog.Snapshot, error)
	Resolve(ctx context.Context, p authz.Principal, provider, name string) (catalog.Model, catalog.Endpoint, error)
}

// Catalogue implements gateway.Catalogue over the catalogue cache.
type Catalogue struct {
	src source
	log *slog.Logger
}

// NewCatalogue returns the gateway's view of c.
func NewCatalogue(c *catalog.Cache, log *slog.Logger) *Catalogue { return newCatalogue(c, log) }

func newCatalogue(src source, log *slog.Logger) *Catalogue { return &Catalogue{src: src, log: log} }

// Provider returns the API family of an enabled provider; ok is false for a name that is
// unknown or disabled.
func (c *Catalogue) Provider(ctx context.Context, name string) (string, bool, error) {
	snap, err := c.src.Snapshot(ctx)
	if err != nil {
		return "", false, err
	}
	p, ok := snap.Provider(name)
	if !ok || !p.Enabled {
		return "", false, nil
	}
	return p.Family, true, nil
}

// Resolve returns the target for a model. The base URL of the endpoint is validated again: a
// stored URL that fails is logged (names only, never the URL) and is ErrProviderUnavailable.
func (c *Catalogue) Resolve(ctx context.Context, p authz.Principal, provider, model string) (gateway.Target, error) {
	m, ep, err := c.src.Resolve(ctx, p, provider, model)
	switch {
	case errors.Is(err, catalog.ErrModelNotAllowed):
		return gateway.Target{}, gateway.ErrModelNotAllowed
	case errors.Is(err, catalog.ErrProviderUnavailable):
		return gateway.Target{}, gateway.ErrProviderUnavailable
	case err != nil:
		return gateway.Target{}, err
	}
	snap, err := c.src.Snapshot(ctx)
	if err != nil {
		return gateway.Target{}, err
	}
	var base *url.URL
	norm, err := catalog.ValidateEndpointURL(ep.BaseURL, ep.Private)
	if err == nil {
		base, err = url.Parse(norm)
	}
	if err != nil {
		c.log.ErrorContext(ctx, "endpoint unusable", "provider", provider, "endpoint", ep.Name)
		return gateway.Target{}, gateway.ErrProviderUnavailable
	}
	return gateway.Target{
		Provider: provider, Endpoint: ep.Name, ModelID: m.ID, Model: m.Name, UpstreamModel: m.UpstreamID,
		BaseURL: base, Private: ep.Private, Prices: snap.Versions(m.ID),
	}, nil
}
