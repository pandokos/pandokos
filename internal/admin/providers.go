package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ListProviders lists the providers of the catalogue, by name.
func (s *server) ListProviders(ctx context.Context, in adminapi.ListProvidersInput) (*adminapi.ProviderList, error) {
	after, name, _, err := decodeCursor(in.Cursor, "name")
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.PandokosProvider
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		rows, err = q.ListProviders(ctx, queries.ListProvidersParams{After: after, AfterName: name, PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.ProviderList{Items: []adminapi.Provider{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("name", rows[limit-1].Name, rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, providerOut(r))
	}
	return out, nil
}

// GetProvider reads a provider.
func (s *server) GetProvider(ctx context.Context, in adminapi.GetProviderInput) (*adminapi.Provider, error) {
	var row queries.PandokosProvider
	err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		row, err = q.GetProvider(ctx, in.Provider)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	o := providerOut(row)
	return &o, nil
}

// CreateProvider adds a provider to the catalogue.
func (s *server) CreateProvider(ctx context.Context, in adminapi.CreateProviderInput) (*adminapi.Provider, error) {
	b := in.Body
	if err := catalog.ValidateProviderName(b.Name); err != nil {
		return nil, refusal("name", err)
	}
	option := b.StreamUsageOption
	if option == "" {
		option = adminapi.StreamUsageOptionAdd
	}
	var out adminapi.Provider
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		row, err := q.CreateProvider(ctx, queries.CreateProviderParams{
			Name: b.Name, DisplayName: b.DisplayName, Description: b.Description, ApiFamily: string(b.ApiFamily), Enabled: true, DatasetProvider: b.DatasetProvider,
			StreamUsageOption: string(option),
		})
		if err != nil {
			return err
		}
		out = providerOut(row)
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "provider.create", "provider", row.Name, map[string]any{
			"name": row.Name, "api_family": row.ApiFamily, "display_name": row.DisplayName, "description": row.Description,
			"dataset_provider": row.DatasetProvider, "enabled": row.Enabled, "stream_usage_option": row.StreamUsageOption}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// UpdateProvider changes the members that are present.
func (s *server) UpdateProvider(ctx context.Context, in adminapi.UpdateProviderInput) (*adminapi.Provider, error) {
	b := in.Body
	var out adminapi.Provider
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		cur, err := q.GetProvider(ctx, in.Provider)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		detail := map[string]any{}
		par := queries.UpdateProviderParams{Name: cur.Name}
		if b.DisplayName.Set && b.DisplayName.Value != cur.DisplayName {
			par.SetDisplayName, par.DisplayName = true, b.DisplayName.Value
			detail["display_name"] = change(cur.DisplayName, b.DisplayName.Value)
		}
		if b.Description.Set && b.Description.Value != cur.Description {
			par.SetDescription, par.Description = true, b.Description.Value
			detail["description"] = change(cur.Description, b.Description.Value)
		}
		if b.Enabled.Set && b.Enabled.Value != cur.Enabled {
			par.SetEnabled, par.Enabled = true, b.Enabled.Value
			detail["enabled"] = change(cur.Enabled, b.Enabled.Value)
		}
		if b.DatasetProvider.Set && b.DatasetProvider.Value != cur.DatasetProvider {
			par.SetDatasetProvider, par.DatasetProvider = true, b.DatasetProvider.Value
			detail["dataset_provider"] = change(cur.DatasetProvider, b.DatasetProvider.Value)
		}
		if b.StreamUsageOption.Set && string(b.StreamUsageOption.Value) != cur.StreamUsageOption {
			par.SetStreamUsageOption, par.StreamUsageOption = true, string(b.StreamUsageOption.Value)
			detail["stream_usage_option"] = change(cur.StreamUsageOption, string(b.StreamUsageOption.Value))
		}
		if len(detail) == 0 {
			out = providerOut(cur)
			return nil
		}
		row, err := q.UpdateProvider(ctx, par)
		if err != nil {
			return err
		}
		out = providerOut(row)
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "provider.update", "provider", row.Name, detail))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// DeleteProvider removes a provider that nothing references.
func (s *server) DeleteProvider(ctx context.Context, in adminapi.DeleteProviderInput) (*adminapi.Provider, error) {
	var out adminapi.Provider
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		row, err := q.DeleteProvider(ctx, in.Provider)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		out = providerOut(row)
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "provider.delete", "provider", row.Name, map[string]any{
			"name": row.Name, "api_family": row.ApiFamily}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}
