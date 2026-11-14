package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ListModels lists the models of the catalogue.
func (s *server) ListModels(ctx context.Context, in adminapi.ListModelsInput) (*adminapi.ModelList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListModelsRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		rows, err = q.ListModels(ctx, queries.ListModelsParams{
			Provider: in.Provider, Name: in.Name, Status: string(in.Status), Availability: string(in.Availability),
			After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.ModelList{Items: []adminapi.Model{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, modelOut(r))
	}
	return out, nil
}

// GetModel reads a model.
func (s *server) GetModel(ctx context.Context, in adminapi.GetModelInput) (*adminapi.Model, error) {
	var row queries.GetModelRow
	err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		row, err = q.GetModel(ctx, in.Model)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	o := modelOut(row)
	return &o, nil
}

// CreateModel adds a model to the catalogue.
func (s *server) CreateModel(ctx context.Context, in adminapi.CreateModelInput) (*adminapi.Model, error) {
	b := in.Body
	if err := catalog.ValidateModelName(b.Name); err != nil {
		return nil, refusal("name", err)
	}
	upstream := b.UpstreamID
	if upstream == "" {
		upstream = b.Name
	}
	if err := catalog.ValidateUpstreamID(upstream); err != nil {
		return nil, refusal("upstream_id", err)
	}
	status, availability := string(b.Status), string(b.Availability)
	if status == "" {
		status = string(adminapi.ModelStatusActive)
	}
	if availability == "" {
		availability = string(adminapi.ModelAvailabilitySelected)
	}
	var out adminapi.Model
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		p, err := q.GetProvider(ctx, b.Provider)
		if isNoRowsErr(err) {
			return invalid("provider", "must name a provider of the catalogue")
		}
		if err != nil {
			return err
		}
		id, err := q.CreateModel(ctx, queries.CreateModelParams{
			ProviderID: p.ID, Name: b.Name, UpstreamID: upstream, DisplayName: b.DisplayName, Description: b.Description, Status: status,
			Availability: availability, Capabilities: nonNil(b.Capabilities), ContextWindow: boundParam(b.ContextWindow), MaxOutputTokens: boundParam(b.MaxOutputTokens),
		})
		if err != nil {
			return err
		}
		row, err := q.GetModel(ctx, id)
		if err != nil {
			return err
		}
		out = modelOut(row)
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "model.create", "model", id, map[string]any{
			"provider": p.Name, "name": row.Name, "upstream_id": row.UpstreamID, "status": row.Status, "availability": row.Availability}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// UpdateModel changes the members that are present.
func (s *server) UpdateModel(ctx context.Context, in adminapi.UpdateModelInput) (*adminapi.Model, error) {
	b := in.Body
	if b.UpstreamID.Set {
		if err := catalog.ValidateUpstreamID(b.UpstreamID.Value); err != nil {
			return nil, refusal("upstream_id", err)
		}
	}
	var out adminapi.Model
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		if _, err := q.LockModel(ctx, in.Model); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		cur, err := q.GetModel(ctx, in.Model)
		if err != nil {
			return err
		}
		detail := map[string]any{}
		par := queries.UpdateModelParams{ID: cur.ID}
		if b.UpstreamID.Set && b.UpstreamID.Value != cur.UpstreamID {
			par.SetUpstreamID, par.UpstreamID = true, b.UpstreamID.Value
			detail["upstream_id"] = change(cur.UpstreamID, b.UpstreamID.Value)
		}
		if b.DisplayName.Set && b.DisplayName.Value != cur.DisplayName {
			par.SetDisplayName, par.DisplayName = true, b.DisplayName.Value
			detail["display_name"] = change(cur.DisplayName, b.DisplayName.Value)
		}
		if b.Description.Set && b.Description.Value != cur.Description {
			par.SetDescription, par.Description = true, b.Description.Value
			detail["description"] = change(cur.Description, b.Description.Value)
		}
		if b.Status.Set && string(b.Status.Value) != cur.Status {
			par.SetStatus, par.Status = true, string(b.Status.Value)
			detail["status"] = change(cur.Status, string(b.Status.Value))
		}
		if b.Availability.Set && string(b.Availability.Value) != cur.Availability {
			par.SetAvailability, par.Availability = true, string(b.Availability.Value)
			detail["availability"] = change(cur.Availability, string(b.Availability.Value))
		}
		if b.Capabilities.Set && !sameStrings(nonNil(b.Capabilities.Value), nonNil(cur.Capabilities)) {
			par.SetCapabilities, par.Capabilities = true, nonNil(b.Capabilities.Value)
			detail["capabilities"] = change(nonNil(cur.Capabilities), nonNil(b.Capabilities.Value))
		}
		if b.ContextWindow.Set && b.ContextWindow.Value != bound(cur.ContextWindow) {
			par.SetContextWindow, par.ContextWindow = true, boundParam(b.ContextWindow.Value)
			detail["context_window"] = change(bound(cur.ContextWindow), b.ContextWindow.Value)
		}
		if b.MaxOutputTokens.Set && b.MaxOutputTokens.Value != bound(cur.MaxOutputTokens) {
			par.SetMaxOutputTokens, par.MaxOutputTokens = true, boundParam(b.MaxOutputTokens.Value)
			detail["max_output_tokens"] = change(bound(cur.MaxOutputTokens), b.MaxOutputTokens.Value)
		}
		if len(detail) == 0 {
			out = modelOut(cur)
			return nil
		}
		if _, err := q.UpdateModel(ctx, par); err != nil {
			return err
		}
		row, err := q.GetModel(ctx, cur.ID)
		if err != nil {
			return err
		}
		out = modelOut(row)
		detail["provider"], detail["name"] = cur.Provider, cur.Name
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "model.update", "model", cur.ID, detail))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// DeleteModel removes a model that has no price version; a priced model is retired instead.
func (s *server) DeleteModel(ctx context.Context, in adminapi.DeleteModelInput) (*adminapi.Model, error) {
	var out adminapi.Model
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		if _, err := q.LockModel(ctx, in.Model); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		cur, err := q.GetModel(ctx, in.Model)
		if err != nil {
			return err
		}
		out = modelOut(cur)
		// The audit row goes first: when it cannot be written nothing is deleted, and when the
		// delete is refused (a price version references the model) the transaction rolls both back.
		if err := audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "model.delete", "model", cur.ID, map[string]any{
			"provider": cur.Provider, "name": cur.Name})); err != nil {
			return err
		}
		_, err = q.DeleteModel(ctx, cur.ID)
		return err
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}
