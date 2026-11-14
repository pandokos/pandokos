package admin

import (
	"context"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ListPriceVersions lists the price versions of a model, newest first; with at, the one in force
// at that instant (at most one item).
func (s *server) ListPriceVersions(ctx context.Context, in adminapi.ListPriceVersionsInput) (*adminapi.PriceVersionList, error) {
	var at *time.Time
	if in.At != "" {
		t, err := time.Parse(time.RFC3339Nano, in.At)
		if err != nil {
			return nil, invalid("at", "must be a date and time in RFC 3339")
		}
		at = &t
	}
	after, cursorTime, _, err := decodeCursor(in.Cursor, "valid_from")
	if err != nil {
		return nil, err
	}
	var afterValid time.Time
	if after {
		if afterValid, err = time.Parse(time.RFC3339Nano, cursorTime); err != nil {
			return nil, invalid("cursor", "is not a cursor of this collection and sort")
		}
	}
	limit, ask := limitOf(in.Limit)
	var rows []priceRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		if _, err := q.GetModel(ctx, in.Model); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		if at != nil {
			r, err := q.PriceAt(ctx, queries.PriceAtParams{ModelID: in.Model, At: at})
			switch {
			case isNoRowsErr(err):
				return nil
			case err != nil:
				return err
			}
			rows = []priceRow{priceFromAt(r)}
			return nil
		}
		list, err := q.ListPriceVersions(ctx, queries.ListPriceVersionsParams{ModelID: in.Model, After: after, AfterValidFrom: afterValid, PageSize: ask})
		for _, r := range list {
			rows = append(rows, priceFromList(r))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.PriceVersionList{Items: []adminapi.PriceVersion{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeCursor("valid_from", last.ValidFrom.UTC().Format(time.RFC3339Nano), last.ID)
			break
		}
		v, err := priceOut(r)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// CreatePriceOverride adds an operator price to a model and flags the model so that imports leave
// it alone.
func (s *server) CreatePriceOverride(ctx context.Context, in adminapi.CreatePriceOverrideInput) (*adminapi.PriceVersion, error) {
	b := in.Body
	v := pricing.Version{Currency: b.Currency, Source: "override"}
	for _, r := range b.Rates {
		d, err := pricing.ParseDecimal(r.Rate)
		if err != nil {
			return nil, invalid("rates", "a rate must be a decimal number")
		}
		v.Rates = append(v.Rates, pricing.Rate{Category: pricing.Category(r.Category), Threshold: int64(r.Threshold), PerMillion: d})
	}
	if err := v.Validate(); err != nil {
		return nil, invalid("rates", strings.TrimPrefix(err.Error(), "pricing: "))
	}
	sortRates(v.Rates)
	var validFrom *time.Time
	if b.ValidFrom != nil {
		if b.ValidFrom.Before(s.now()) {
			return nil, invalid("valid_from", "must not be in the past")
		}
		u := b.ValidFrom.UTC()
		validFrom = &u
	}
	var out adminapi.PriceVersion
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		lock, err := q.LockModel(ctx, in.Model)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		m, err := q.GetModel(ctx, lock.ID)
		if err != nil {
			return err
		}
		// The version that is in force when the new one starts, for the audit row.
		prev, err := q.PriceAt(ctx, queries.PriceAtParams{ModelID: m.ID, At: validFrom})
		if err != nil && !isNoRowsErr(err) {
			return err
		}
		created, err := q.CreatePriceVersion(ctx, queries.CreatePriceVersionParams{
			ModelID: m.ID, ValidFrom: validFrom, Currency: v.Currency, Source: "override", Note: b.Note,
		})
		if err != nil {
			return err
		}
		cats, ths, rates := make([]string, len(v.Rates)), make([]int64, len(v.Rates)), make([]string, len(v.Rates))
		for i, r := range v.Rates {
			cats[i], ths[i], rates[i] = string(r.Category), r.Threshold, r.PerMillion.String()
		}
		if _, err := q.CreatePriceRates(ctx, queries.CreatePriceRatesParams{PriceVersionID: created.ID, Categories: cats, Thresholds: ths, Rates: rates}); err != nil {
			return err
		}
		if _, err := q.SetModelPriceOverride(ctx, queries.SetModelPriceOverrideParams{ID: m.ID, PriceOverride: true}); err != nil {
			return err
		}
		row, err := q.GetPriceVersion(ctx, queries.GetPriceVersionParams{ID: created.ID, ModelID: m.ID})
		if err != nil {
			return err
		}
		out, err = priceOut(priceFromGet(row))
		if err != nil {
			return err
		}
		detail := map[string]any{"provider": m.Provider, "model": m.Name, "source": "override", "currency": out.Currency,
			"valid_from": out.ValidFrom.Format(time.RFC3339Nano), "rates": rateDetail(out.Rates)}
		if prev.ID != "" {
			detail["previous_version"] = prev.ID
		}
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "price.create", "price_version", created.ID, detail))
	})
	if kind, name, ok := store.ConstraintViolation(err); ok && kind == "check" && name == "" {
		// The guard of the table: a version cannot start before the database's now().
		return nil, invalid("valid_from", "must not be in the past")
	}
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// DeletePriceVersion removes a version that has not started.
func (s *server) DeletePriceVersion(ctx context.Context, in adminapi.DeletePriceVersionInput) (*adminapi.PriceVersion, error) {
	var out adminapi.PriceVersion
	err := s.inPlatform(ctx, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		m, err := q.GetModel(ctx, in.Model)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		row, err := q.GetPriceVersion(ctx, queries.GetPriceVersionParams{ID: in.PriceVersion, ModelID: m.ID})
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		out, err = priceOut(priceFromGet(row))
		if err != nil {
			return err
		}
		// The trigger of the table refuses a version that has started (invalid_state below).
		if _, err := q.DeletePriceVersion(ctx, queries.DeletePriceVersionParams{ID: row.ID, ModelID: m.ID}); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, s.platformEvent(ctx, "price.delete", "price_version", row.ID, map[string]any{
			"provider": m.Provider, "model": m.Name, "valid_from": row.ValidFrom.UTC().Format(time.RFC3339Nano)}))
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}

// ClearPriceOverride lets imports price the model again. The override stays in force until an
// import creates a version.
func (s *server) ClearPriceOverride(ctx context.Context, in adminapi.ClearPriceOverrideInput) (*adminapi.Model, error) {
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
		if !cur.PriceOverride {
			out = modelOut(cur)
			return nil
		}
		future, err := q.HasFuturePriceVersion(ctx, cur.ID)
		if err != nil {
			return err
		}
		if future {
			return errInvalidState
		}
		if _, err := q.SetModelPriceOverride(ctx, queries.SetModelPriceOverrideParams{ID: cur.ID, PriceOverride: false}); err != nil {
			return err
		}
		row, err := q.GetModel(ctx, cur.ID)
		if err != nil {
			return err
		}
		out = modelOut(row)
		e := s.platformEvent(ctx, "price.clear_override", "model", cur.ID, map[string]any{"provider": cur.Provider, "model": cur.Name})
		e.Reason = in.Body.Reason
		return audit.RecordPlatform(ctx, tx, e)
	})
	if err != nil {
		return nil, mapCatalogError(err)
	}
	return &out, nil
}
