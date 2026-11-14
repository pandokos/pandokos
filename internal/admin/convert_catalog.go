package admin

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// errModelsNotVisible is the one refusal of a model reference that a tenant may not use. A
// reference that is unknown, retired, of a disabled provider or granted only to another tenant
// all get it, so a tenant cannot probe for the models of others.
var errModelsNotVisible = invalid("models", "must name models available to the tenant")

// refusal turns a refusal of the catalogue package into an invalid_request on field. The texts
// of those refusals never quote the value they refused.
func refusal(field string, err error) error {
	return invalid(field, strings.TrimPrefix(err.Error(), "catalog: "))
}

// visibleRefs returns the references (provider/name) of every model the tenant of q may use: the
// models of enabled providers that are not retired and are available to all or granted to the
// tenant. q is bound to a transaction of the tenant.
func visibleRefs(ctx context.Context, q *queries.Queries) (map[string]bool, error) {
	out := map[string]bool{}
	after, afterID := false, "00000000-0000-0000-0000-000000000000"
	const page = 500
	for {
		rows, err := q.ListVisibleModels(ctx, queries.ListVisibleModelsParams{After: after, AfterID: afterID, PageSize: page})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.Provider+"/"+r.Name] = true
		}
		if len(rows) < page {
			return out, nil
		}
		after, afterID = true, rows[len(rows)-1].ID
	}
}

// checkVisibleRefs refuses a list of references that names a model the tenant may not use.
func checkVisibleRefs(ctx context.Context, q *queries.Queries, refs []string) error {
	visible, err := visibleRefs(ctx, q)
	if err != nil {
		return err
	}
	for _, r := range refs {
		if !visible[r] {
			return errModelsNotVisible
		}
	}
	return nil
}

// addRefs puts a list of model references in an audit detail; a list too long for the detail is
// replaced by its length.
func addRefs(detail map[string]any, key string, refs []string) {
	size := 0
	for _, r := range refs {
		size += len(r) + 3
	}
	if size > 3000 {
		detail[key+"_count"] = len(refs)
		return
	}
	detail[key] = nonNil(refs)
}

func modelOut[R modelRow](r R) adminapi.Model {
	m := queries.GetModelRow(r)
	m2 := adminapi.Model{
		ID: m.ID, Provider: m.Provider, ApiFamily: adminapi.ApiFamily(m.ApiFamily), Name: m.Name, Ref: m.Provider + "/" + m.Name,
		UpstreamID: m.UpstreamID, DisplayName: m.DisplayName, Description: m.Description, Status: adminapi.ModelStatus(m.Status),
		Availability: adminapi.ModelAvailability(m.Availability), Capabilities: nonNil(m.Capabilities),
		ContextWindow: bound(m.ContextWindow), MaxOutputTokens: bound(m.MaxOutputTokens), PriceOverride: m.PriceOverride,
		CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(),
	}
	return m2
}

// modelRow is every row shape of the statements that return a model with its provider.
type modelRow interface {
	queries.GetModelRow | queries.ListModelsRow | queries.ListVisibleModelsRow
}

// bound is a token bound, 0 for NULL.
func bound(p *int64) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

// boundParam is a token bound as a statement parameter: NULL for 0.
func boundParam(n int) *int64 {
	if n == 0 {
		return nil
	}
	v := int64(n)
	return &v
}

func providerOut(r queries.PandokosProvider) adminapi.Provider {
	return adminapi.Provider{
		ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, ApiFamily: adminapi.ApiFamily(r.ApiFamily),
		Enabled: r.Enabled, DatasetProvider: r.DatasetProvider, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func endpointOut(provider string, r queries.PandokosProviderEndpoint) adminapi.Endpoint {
	return adminapi.Endpoint{
		ID: r.ID, Provider: provider, Name: r.Name, BaseURL: r.BaseUrl, Region: r.Region, Private: r.Private, Enabled: r.Enabled,
		IsDefault: r.IsDefault, Description: r.Description, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// priceRow is every row shape of the statements that return a price version with its rates.
type priceRow struct {
	ID, ModelID, Currency, Source, Note string
	ValidFrom, CreatedAt                time.Time
	ImportID                            *string
	Categories                          []string
	Thresholds                          []int64
	Rates                               []string
}

func priceOut(r priceRow) (adminapi.PriceVersion, error) {
	rates := make([]adminapi.PriceRate, len(r.Categories))
	for i := range r.Categories {
		d, err := pricing.ParseDecimal(r.Rates[i])
		if err != nil {
			return adminapi.PriceVersion{}, err
		}
		rates[i] = adminapi.PriceRate{Category: adminapi.PriceCategory(r.Categories[i]), Threshold: int(r.Thresholds[i]), Rate: d.String()}
	}
	v := adminapi.PriceVersion{
		ID: r.ID, ModelID: r.ModelID, ValidFrom: r.ValidFrom.UTC(), Currency: r.Currency, Source: adminapi.PriceSource(r.Source),
		Note: r.Note, Rates: rates, CreatedAt: r.CreatedAt.UTC(),
	}
	if r.ImportID != nil {
		v.ImportID = *r.ImportID
	}
	return v, nil
}

func priceFromGet(r queries.GetPriceVersionRow) priceRow {
	return priceRow{r.ID, r.ModelID, r.Currency, r.Source, r.Note, r.ValidFrom, r.CreatedAt, r.ImportID, r.Categories, r.Thresholds, r.Rates}
}

func priceFromList(r queries.ListPriceVersionsRow) priceRow {
	return priceRow{r.ID, r.ModelID, r.Currency, r.Source, r.Note, r.ValidFrom, r.CreatedAt, r.ImportID, r.Categories, r.Thresholds, r.Rates}
}

func priceFromAt(r queries.PriceAtRow) priceRow {
	return priceRow{r.ID, r.ModelID, r.Currency, r.Source, r.Note, r.ValidFrom, r.CreatedAt, r.ImportID, r.Categories, r.Thresholds, r.Rates}
}

// rateDetail is the rates of a version as an audit detail: category, threshold, canonical rate.
func rateDetail(rates []adminapi.PriceRate) []any {
	out := make([]any, len(rates))
	for i, r := range rates {
		out[i] = map[string]any{"category": string(r.Category), "threshold": r.Threshold, "rate": r.Rate}
	}
	return out
}

// sortRates orders rates by category, then threshold, as a version keeps them.
func sortRates(rates []pricing.Rate) {
	sort.Slice(rates, func(i, j int) bool {
		if rates[i].Category != rates[j].Category {
			return rates[i].Category < rates[j].Category
		}
		return rates[i].Threshold < rates[j].Threshold
	})
}

// platformEvent is the audit event of an operation of the platform administrator.
func (s *server) platformEvent(ctx context.Context, action, resourceType, resourceID string, detail map[string]any) audit.Event {
	return audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: action, ResourceType: resourceType, ResourceID: resourceID,
		Outcome: "success", RequestID: stateOf(ctx).id, Detail: detail}
}

// inPlatform runs fn in a platform transaction with the statements bound to it.
func (s *server) inPlatform(ctx context.Context, fn func(ctx context.Context, tx store.Tx, q *queries.Queries) error) error {
	return s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error { return fn(ctx, tx, queries.New(tx)) })
}

// mapCatalogError turns the database errors every write of the catalogue can meet into the
// answers of section 5.8: a name in use is a conflict; a row that something still references is
// an invalid state.
func mapCatalogError(err error) error {
	kind, name, ok := store.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch kind {
	case "unique":
		switch name {
		case "provider_name_key", "provider_endpoint_name_key", "model_name_key", "model_upstream_id_key", "price_version_valid_from_key":
			return errConflict
		}
	case "foreign_key", "restrict":
		return errInvalidState
	case "check":
		if name == "provider_name_reserved" {
			return invalid("name", "is reserved")
		}
	}
	return err
}
