package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// errDefaultNeeded is the refusal of is_default false on the default endpoint.
var errDefaultNeeded = invalid("is_default", "the default endpoint cannot be unset; make another endpoint the default instead")

// ListEndpoints lists the endpoints of a provider, by name.
func (s *server) ListEndpoints(ctx context.Context, in adminapi.ListEndpointsInput) (*adminapi.EndpointList, error) {
	after, name, _, err := decodeCursor(in.Cursor, "name")
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.PandokosProviderEndpoint
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		p, err := q.GetProvider(ctx, in.Provider)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		rows, err = q.ListEndpoints(ctx, queries.ListEndpointsParams{ProviderID: p.ID, After: after, AfterName: name, PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.EndpointList{Items: []adminapi.Endpoint{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("name", rows[limit-1].Name, rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, endpointOut(in.Provider, r))
	}
	return out, nil
}

// getEndpoint finds a provider and one of its endpoints.
func getEndpoint(ctx context.Context, q *queries.Queries, provider, name string) (queries.PandokosProvider, queries.PandokosProviderEndpoint, error) {
	p, err := q.GetProvider(ctx, provider)
	if isNoRowsErr(err) {
		return p, queries.PandokosProviderEndpoint{}, errNotFound
	}
	if err != nil {
		return p, queries.PandokosProviderEndpoint{}, err
	}
	e, err := q.GetEndpoint(ctx, queries.GetEndpointParams{ProviderID: p.ID, Name: name})
	if isNoRowsErr(err) {
		return p, e, errNotFound
	}
	return p, e, err
}

// GetEndpoint reads an endpoint.
func (s *server) GetEndpoint(ctx context.Context, in adminapi.GetEndpointInput) (*adminapi.Endpoint, error) {
	var out adminapi.Endpoint
	err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		_, e, err := getEndpoint(ctx, q, in.Provider, in.Endpoint)
		out = endpointOut(in.Provider, e)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateEndpoint adds an endpoint; the first endpoint of a provider is its default.
func (s *server) CreateEndpoint(ctx context.Context, in adminapi.CreateEndpointInput) (*adminapi.Endpoint, error) {
	b := in.Body
	baseURL, err := catalog.ValidateEndpointURL(b.BaseURL, b.Private)
	if err != nil {
		return nil, refusal("base_url", err)
	}
	var out adminapi.Endpoint
	err = s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		p, err := q.GetProvider(ctx, in.Provider)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if b.IsDefault {
			if _, err := q.ClearDefaultEndpoint(ctx, queries.ClearDefaultEndpointParams{ProviderID: p.ID, Keep: b.Name}); err != nil {
				return err
			}
		}
		row, err := q.CreateEndpoint(ctx, queries.CreateEndpointParams{
			ProviderID: p.ID, Name: b.Name, BaseUrl: baseURL, Region: b.Region, Private: b.Private, Enabled: true, IsDefault: b.IsDefault, Description: b.Description,
		})
		if err != nil {
			return err
		}
		out = endpointOut(p.Name, row)
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "endpoint.create", "endpoint", p.Name+"/"+row.Name, map[string]any{
			"provider": p.Name, "name": row.Name, "base_url": row.BaseUrl, "region": row.Region, "private": row.Private,
			"is_default": row.IsDefault, "description": row.Description}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// UpdateEndpoint changes the members that are present; is_default true moves the default.
func (s *server) UpdateEndpoint(ctx context.Context, in adminapi.UpdateEndpointInput) (*adminapi.Endpoint, error) {
	b := in.Body
	var out adminapi.Endpoint
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		p, cur, err := getEndpoint(ctx, q, in.Provider, in.Endpoint)
		if err != nil {
			return err
		}
		detail := map[string]any{}
		par := queries.UpdateEndpointParams{ProviderID: p.ID, Name: cur.Name}
		// The address is checked against the privacy it will have.
		private := cur.Private
		if b.Private.Set {
			private = b.Private.Value
		}
		baseURL := cur.BaseUrl
		if b.BaseURL.Set {
			baseURL = b.BaseURL.Value
		}
		if b.BaseURL.Set || b.Private.Set {
			norm, err := catalog.ValidateEndpointURL(baseURL, private)
			if err != nil {
				field := "base_url"
				if !b.BaseURL.Set {
					field = "private"
				}
				return refusal(field, err)
			}
			baseURL = norm
		}
		if baseURL != cur.BaseUrl {
			par.SetBaseUrl, par.BaseUrl = true, baseURL
			detail["base_url"] = change(cur.BaseUrl, baseURL)
		}
		if b.Region.Set && b.Region.Value != cur.Region {
			par.SetRegion, par.Region = true, b.Region.Value
			detail["region"] = change(cur.Region, b.Region.Value)
		}
		if private != cur.Private {
			par.SetPrivate, par.Private = true, private
			detail["private"] = change(cur.Private, private)
		}
		if b.Enabled.Set && b.Enabled.Value != cur.Enabled {
			par.SetEnabled, par.Enabled = true, b.Enabled.Value
			detail["enabled"] = change(cur.Enabled, b.Enabled.Value)
		}
		if b.Description.Set && b.Description.Value != cur.Description {
			par.SetDescription, par.Description = true, b.Description.Value
			detail["description"] = change(cur.Description, b.Description.Value)
		}
		if b.IsDefault.Set && b.IsDefault.Value != cur.IsDefault {
			if !b.IsDefault.Value {
				return errDefaultNeeded
			}
			// The default moves in this transaction: the old one stops being the default first.
			if _, err := q.ClearDefaultEndpoint(ctx, queries.ClearDefaultEndpointParams{ProviderID: p.ID, Keep: cur.Name}); err != nil {
				return err
			}
			par.SetIsDefault, par.IsDefault = true, true
			detail["is_default"] = change(false, true)
		}
		if len(detail) == 0 {
			out = endpointOut(p.Name, cur)
			return nil
		}
		row, err := q.UpdateEndpoint(ctx, par)
		if err != nil {
			return err
		}
		out = endpointOut(p.Name, row)
		detail["provider"], detail["name"] = p.Name, row.Name
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "endpoint.update", "endpoint", p.Name+"/"+row.Name, detail))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// DeleteEndpoint removes an endpoint; the default goes only when it is the provider's last one.
func (s *server) DeleteEndpoint(ctx context.Context, in adminapi.DeleteEndpointInput) (*adminapi.Endpoint, error) {
	var out adminapi.Endpoint
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		p, cur, err := getEndpoint(ctx, q, in.Provider, in.Endpoint)
		if err != nil {
			return err
		}
		if cur.IsDefault {
			others, err := q.ListEndpoints(ctx, queries.ListEndpointsParams{ProviderID: p.ID, PageSize: 2})
			if err != nil {
				return err
			}
			if len(others) > 1 {
				return errInvalidState
			}
		}
		row, err := q.DeleteEndpoint(ctx, queries.DeleteEndpointParams{ProviderID: p.ID, Name: cur.Name})
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		out = endpointOut(p.Name, row)
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "endpoint.delete", "endpoint", p.Name+"/"+row.Name, map[string]any{
			"provider": p.Name, "name": row.Name, "base_url": row.BaseUrl, "is_default": row.IsDefault}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}
