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
	"github.com/pandokos/pandokos/internal/tenant"
)

// Test seams (T-751): when set, createPriceListVersion calls afterTenantPriceInsert right after the
// insert of the version and its rates and fails with its error, so that a test can check that the
// version and its audit row stay together. Nil in production.
var afterTenantPriceInsert func() error

// maxTenantPriceVersions is the bound on the versions of one tenant (sprint 5.4, section 5.5); a
// variable so that T-749 can lower it.
var maxTenantPriceVersions = 50_000

// The price lists of a tenant are the tenant's alone: every handler runs in the session of the tenant
// named by the path (whose permission was decided before the handler ran), and the platform has no
// operation on them. No statement names the tenant in a WHERE clause: the policies of the tables do.

// ListPriceLists lists the price lists of a tenant, one per currency.
func (s *server) ListPriceLists(ctx context.Context, in adminapi.ListPriceListsInput) (*adminapi.TenantPriceListList, error) {
	out := &adminapi.TenantPriceListList{Items: []adminapi.TenantPriceList{}}
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		inForce, err := s.inForceCurrency(ctx, q)
		if err != nil {
			return err
		}
		rows, err := q.ListTenantPriceLists(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out.Items = append(out.Items, adminapi.TenantPriceList{Currency: r.Currency, Versions: int(r.Versions), ModelsPriced: int(r.ModelsPriced), Active: r.Currency == inForce})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListPriceListVersions lists the versions of a tenant's lists, newest first.
func (s *server) ListPriceListVersions(ctx context.Context, in adminapi.ListPriceListVersionsInput) (*adminapi.TenantPriceVersionList, error) {
	after, _, id, err := decodeCursor(in.Cursor, "tenant_prices")
	if err != nil {
		return nil, err
	}
	if in.Model != "" && in.Provider == "" {
		return nil, invalid("provider", "is required with model")
	}
	var at *time.Time
	if in.At != "" {
		t, perr := time.Parse(time.RFC3339, in.At)
		if perr != nil {
			return nil, invalid("at", "must be a time in RFC 3339 format")
		}
		at = &t
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListTenantPriceVersionsRow
	err = s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		var err error
		rows, err = q.ListTenantPriceVersions(ctx, queries.ListTenantPriceVersionsParams{
			Currency: in.Currency, Provider: in.Provider, Model: in.Model, At: at, After: after, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.TenantPriceVersionList{Items: []adminapi.TenantPriceVersion{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("tenant_prices", "", rows[limit-1].ID)
			break
		}
		v, err := tenantPriceOut(tenantPriceFromList(r))
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// priceRates validates the rates of a price like createPriceOverride does and returns them sorted.
func priceRates(currency string, in []adminapi.PriceRate) ([]pricing.Rate, error) {
	v := pricing.Version{Currency: currency, Source: "tenant"}
	for _, r := range in {
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
	return v.Rates, nil
}

// CreatePriceListVersion adds a price, or the end of a price, to a tenant's list (section 5.5). The
// lock comes first, so that the start of a version is read after the versions another writer committed
// and the bound is counted under it.
func (s *server) CreatePriceListVersion(ctx context.Context, in adminapi.CreatePriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	b := in.Body
	kind := b.Kind
	if kind == "" {
		kind = adminapi.TenantPriceKindPrice
	}
	var out adminapi.TenantPriceVersion
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		if err := q.LockTenantPrices(ctx); err != nil {
			return err
		}
		// The model: the same answer for one that does not exist, one that is granted elsewhere or nowhere,
		// one of another provider and one that is retired.
		m, err := q.ResolveTenantPriceModel(ctx, queries.ResolveTenantPriceModelParams{Provider: b.Provider, Model: b.Model})
		if isNoRowsErr(err) {
			return invalid("model", "must name a model available to this tenant")
		}
		if err != nil {
			return err
		}
		var rates []pricing.Rate
		if kind == adminapi.TenantPriceKindEnd {
			if len(b.Rates) > 0 {
				return invalid("rates", "must be absent for an end")
			}
		} else if rates, err = priceRates(b.Currency, b.Rates); err != nil {
			return err
		}
		var start time.Time
		if b.ValidFrom != nil {
			if b.ValidFrom.Before(s.now()) {
				return invalid("valid_from", "must not be in the past")
			}
			start = b.ValidFrom.UTC()
		} else if start, err = q.ClockTimestamp(ctx); err != nil {
			return err
		}
		// The version of the list in force when the new one starts.
		prev, err := q.TenantPriceAt(ctx, queries.TenantPriceAtParams{Currency: b.Currency, ModelID: m.ID, At: start})
		if err != nil && !isNoRowsErr(err) {
			return err
		}
		havePrev := err == nil
		if kind == adminapi.TenantPriceKindEnd && (!havePrev || prev.Kind != string(adminapi.TenantPriceKindPrice)) {
			return invalidState("kind", "the list has no price in force for this model at that time")
		}
		n, err := q.CountTenantPriceVersions(ctx)
		if err != nil {
			return err
		}
		if n >= int64(maxTenantPriceVersions) {
			return invalidState("tenant_prices", "the tenant has the largest number of price versions")
		}
		created, err := q.CreateTenantPriceVersion(ctx, queries.CreateTenantPriceVersionParams{ModelID: m.ID, ValidFrom: start, Kind: string(kind), Currency: b.Currency, Note: b.Note})
		if err != nil {
			return err
		}
		if kind == adminapi.TenantPriceKindPrice {
			cats, ths, rs := make([]string, len(rates)), make([]int64, len(rates)), make([]string, len(rates))
			for i, r := range rates {
				cats[i], ths[i], rs[i] = string(r.Category), r.Threshold, r.PerMillion.String()
			}
			if _, err := q.CreateTenantPriceRates(ctx, queries.CreateTenantPriceRatesParams{PriceVersionID: created.ID, Categories: cats, Thresholds: ths, Rates: rs}); err != nil {
				return err
			}
		}
		if afterTenantPriceInsert != nil {
			if err := afterTenantPriceInsert(); err != nil {
				return err
			}
		}
		row, err := q.GetTenantPriceVersion(ctx, created.ID)
		if err != nil {
			return err
		}
		if out, err = tenantPriceOut(tenantPriceFromGet(row)); err != nil {
			return err
		}
		// in_force of the statement reads the start of the transaction, which is before a start taken from
		// the clock after the lock: the version is in force when it is the one of its list at the clock now.
		if !out.InForce {
			clock, err := q.ClockTimestamp(ctx)
			if err != nil {
				return err
			}
			if !start.After(clock) {
				if cur, err := q.TenantPriceAt(ctx, queries.TenantPriceAtParams{Currency: b.Currency, ModelID: m.ID, At: clock}); err == nil && cur.ID == created.ID {
					out.InForce = true
				} else if err != nil && !isNoRowsErr(err) {
					return err
				}
			}
		}
		detail := map[string]any{"provider": m.Provider, "model": m.Name, "model_id": m.ID, "currency": out.Currency, "kind": string(out.Kind),
			"valid_from": out.ValidFrom.Format(time.RFC3339Nano)}
		if kind == adminapi.TenantPriceKindPrice {
			detail["rates"] = rateDetail(out.Rates)
		}
		if havePrev {
			detail["previous_version"] = prev.ID
		}
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "tenant_price.create", "tenant_price_version", created.ID, detail))
	})
	if err != nil {
		return nil, mapTenantPriceError(err)
	}
	return &out, nil
}

// GetPriceListVersion reads a version of a tenant's lists, started or not, replaced or not.
func (s *server) GetPriceListVersion(ctx context.Context, in adminapi.GetPriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	if err := ruleID(in.Price); err != nil {
		return nil, err
	}
	var out adminapi.TenantPriceVersion
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		row, err := q.GetTenantPriceVersion(ctx, in.Price)
		if err != nil {
			return err
		}
		out, err = tenantPriceOut(tenantPriceFromGet(row))
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePriceListVersion removes a version that has not started; the trigger of the table refuses one that has.
func (s *server) DeletePriceListVersion(ctx context.Context, in adminapi.DeletePriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	if err := ruleID(in.Price); err != nil {
		return nil, err
	}
	var out adminapi.TenantPriceVersion
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		row, err := q.GetTenantPriceVersion(ctx, in.Price)
		if err != nil {
			return err
		}
		if out, err = tenantPriceOut(tenantPriceFromGet(row)); err != nil {
			return err
		}
		if _, err := q.DeleteTenantPriceVersion(ctx, row.ID); err != nil {
			return err
		}
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "tenant_price.delete", "tenant_price_version", row.ID, map[string]any{
			"provider": row.Provider, "model": row.Model, "model_id": row.ModelID, "currency": row.Currency, "kind": row.Kind,
			"valid_from": row.ValidFrom.UTC().Format(time.RFC3339Nano)}))
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, mapTenantPriceError(err)
	}
	return &out, nil
}

// mapTenantPriceError turns the errors of the price tables into the answers of section 5.5: the same list,
// model and start twice is a conflict; a start in the past (the guard of the table) is a 400 on valid_from;
// a version that has started cannot be removed. Nothing else is mapped: a 23000 or 42501 is a defect.
func mapTenantPriceError(err error) error {
	kind, name, ok := store.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch {
	case kind == "unique" && name == "tenant_price_version_valid_from_key":
		return errConflict
	case kind == "check" && name == "":
		return invalid("valid_from", "must not be in the past")
	case kind == "restrict":
		return errInvalidState
	}
	return err
}
