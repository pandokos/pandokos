package declare

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// drain reads every page of a list.
func drain[T any](fetch func(cursor string) ([]T, string, error)) ([]T, error) {
	var all []T
	cursor := ""
	for {
		items, next, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next == "" || len(items) == 0 {
			return all, nil
		}
		cursor = next
	}
}

// soft turns the 404 of a setting that has no value yet into the zero value.
func soft[T any](v *T, err error) (*T, error) {
	var pe *adminapi.ProblemError
	if errors.As(err, &pe) && pe.Problem.Status == http.StatusNotFound {
		return nil, nil
	}
	return v, err
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func sameSet(a, b []string) bool { return slices.Equal(sorted(a), sorted(b)) }

func sameInts(a, b []int) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// instant is a time as the document writes it.
func instant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseInstant is the time of a value the document validation accepted.
func parseInstant(v string) time.Time {
	t, _ := time.Parse(time.RFC3339, v)
	return t.UTC()
}

// sameDecimal compares two rates as numbers, so that 2.5 and 2.50 are the same rate.
func sameDecimal(a, b string) bool {
	x, ok1 := new(big.Rat).SetString(a)
	y, ok2 := new(big.Rat).SetString(b)
	if !ok1 || !ok2 {
		return a == b
	}
	return x.Cmp(y) == 0
}

func sameRates(a []Rate, b []Rate) bool {
	if len(a) != len(b) {
		return false
	}
	less := func(x, y Rate) int {
		return cmp.Or(cmp.Compare(x.Category, y.Category), cmp.Compare(x.Threshold, y.Threshold))
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.SortFunc(a, less)
	slices.SortFunc(b, less)
	for i := range a {
		if a[i].Category != b[i].Category || a[i].Threshold != b[i].Threshold || !sameDecimal(a[i].Rate, b[i].Rate) {
			return false
		}
	}
	return true
}

func ratesOut(in []adminapi.PriceRate) []Rate {
	out := make([]Rate, len(in))
	for i, r := range in {
		out[i] = Rate{Category: string(r.Category), Threshold: r.Threshold, Rate: r.Rate}
	}
	slices.SortFunc(out, func(x, y Rate) int {
		return cmp.Or(cmp.Compare(x.Category, y.Category), cmp.Compare(x.Threshold, y.Threshold))
	})
	return out
}

func ratesIn(in []Rate) []adminapi.PriceRate {
	out := make([]adminapi.PriceRate, len(in))
	for i, r := range in {
		out[i] = adminapi.PriceRate{Category: adminapi.PriceCategory(r.Category), Threshold: r.Threshold, Rate: r.Rate}
	}
	return out
}

// reKeyID is the public ID of a virtual key; a rule whose key subject is not one is a rule on a subject mapping.
var reKeyID = regexp.MustCompile(`^t[a-z2-7]{16}$`)

// ---- the state of a tenant

type tenantState struct {
	zone, currency  string
	metadataKeys    []string
	restricted      bool
	restriction     []string
	roles           []adminapi.Role
	serviceAccounts []adminapi.ServiceAccount
	limits          []adminapi.LimitRule
	prices          []adminapi.TenantPriceVersion
}

func readTenant(ctx context.Context, c *adminapi.Client, id string) (*tenantState, error) {
	s := &tenantState{}
	var err error
	if s.roles, err = drain(func(cur string) ([]adminapi.Role, string, error) {
		l, err := c.ListRoles(ctx, adminapi.ListRolesInput{Tenant: id, Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	if s.serviceAccounts, err = drain(func(cur string) ([]adminapi.ServiceAccount, string, error) {
		l, err := c.ListServiceAccounts(ctx, adminapi.ListServiceAccountsInput{Tenant: id, Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	if s.limits, err = drain(func(cur string) ([]adminapi.LimitRule, string, error) {
		l, err := c.ListLimits(ctx, adminapi.ListLimitsInput{Tenant: id, Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	if s.prices, err = drain(func(cur string) ([]adminapi.TenantPriceVersion, string, error) {
		l, err := c.ListPriceListVersions(ctx, adminapi.ListPriceListVersionsInput{Tenant: id, Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	tz, err := soft(c.GetTenantTimeZone(ctx, adminapi.GetTenantTimeZoneInput{Tenant: id}))
	if err != nil {
		return nil, err
	}
	if tz != nil {
		s.zone = tz.TimeZone
	}
	ac, err := soft(c.GetActiveCurrency(ctx, adminapi.GetActiveCurrencyInput{Tenant: id}))
	if err != nil {
		return nil, err
	}
	if ac != nil {
		s.currency = ac.Currency
	}
	mk, err := soft(c.GetMetadataKeys(ctx, adminapi.GetMetadataKeysInput{Tenant: id}))
	if err != nil {
		return nil, err
	}
	if mk != nil && mk.Restricted {
		s.metadataKeys = sorted(mk.Keys)
	}
	mr, err := soft(c.GetModelRestriction(ctx, adminapi.GetModelRestrictionInput{Tenant: id}))
	if err != nil {
		return nil, err
	}
	if mr != nil && mr.Restricted {
		s.restricted, s.restriction = true, sorted(mr.Models)
	}
	return s, nil
}

// limitSubject is the subject of a rule as a document writes it; ok is false for a rule that a
// document cannot express (a rule on a subject mapping).
func (s *tenantState) limitSubject(r adminapi.LimitRule) (subject string, ok bool) {
	switch r.SubjectKind {
	case adminapi.LimitSubjectKindTenant:
		return "tenant", true
	case adminapi.LimitSubjectKindServiceAccount:
		for _, a := range s.serviceAccounts {
			if a.ID == r.SubjectID {
				return "service_account: " + a.Name, true
			}
		}
	case adminapi.LimitSubjectKindKey:
		if reKeyID.MatchString(r.SubjectID) {
			return "key: " + r.SubjectID, true
		}
	}
	return "", false
}

func limitOf(r adminapi.LimitRule, subject string) Limit {
	return Limit{Subject: subject, Provider: r.Provider, Model: r.Model, Metric: string(r.Metric), Limit: r.Limit, Amount: r.Amount, Currency: r.Currency,
		Enforce: r.Enforce, Thresholds: nonEmptyInts(r.Thresholds)}
}

func nonEmptyInts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	return slices.Clone(in)
}

func sortLimits(in []Limit, key func(Limit) string) {
	slices.SortFunc(in, func(a, b Limit) int { return cmp.Compare(key(a), key(b)) })
}

func subjectKey(l Limit) string { return limitKey(l.Subject, l) }

// document builds the document of a tenant, sorted by natural key. Left-out notes are returned with it.
func (s *tenantState) document() *Document {
	d := &Document{Pandokos: Version, Kind: KindTenant}
	if s.zone != "" || s.currency != "" || len(s.metadataKeys) > 0 {
		d.Settings = &Settings{TimeZone: s.zone, ActiveCurrency: s.currency, MetadataKeys: s.metadataKeys}
	}
	if s.restricted {
		d.ModelRestriction = &Restriction{Models: nonNilStrings(s.restriction)}
	}
	for _, r := range s.roles {
		d.Roles = append(d.Roles, Role{Name: r.Name, Description: r.Description, Permissions: sorted(r.Permissions)})
	}
	slices.SortFunc(d.Roles, func(a, b Role) int { return cmp.Compare(a.Name, b.Name) })
	for _, a := range s.serviceAccounts {
		sa := ServiceAccount{Name: a.Name, Description: a.Description, Roles: nonEmptyStrings(sorted(a.Roles))}
		if a.ValidFrom != nil {
			sa.ValidFrom = instant(*a.ValidFrom)
		}
		if a.ValidUntil != nil {
			sa.ValidUntil = instant(*a.ValidUntil)
		}
		d.ServiceAccounts = append(d.ServiceAccounts, sa)
	}
	slices.SortFunc(d.ServiceAccounts, func(a, b ServiceAccount) int { return cmp.Compare(a.Name, b.Name) })
	for _, r := range s.limits {
		subject, ok := s.limitSubject(r)
		if !ok {
			d.LeftOut = append(d.LeftOut, fmt.Sprintf("a limit rule on a subject mapping (%s) is not in the document: a mapping has no portable name", r.Metric))
			continue
		}
		d.Limits = append(d.Limits, limitOf(r, subject))
	}
	sortLimits(d.Limits, subjectKey)
	byCurrency := map[string][]PriceVersion{}
	for _, v := range s.prices {
		if v.Kind != adminapi.TenantPriceKindPrice {
			d.LeftOut = append(d.LeftOut, fmt.Sprintf("a price withdrawal of %s/%s (%s) from %s is not in the document", v.Provider, v.Model, v.Currency, instant(v.ValidFrom)))
			continue
		}
		byCurrency[v.Currency] = append(byCurrency[v.Currency], PriceVersion{Provider: v.Provider, Model: v.Model, ValidFrom: instant(v.ValidFrom), Note: v.Note, Rates: ratesOut(v.Rates)})
	}
	for cur, vs := range byCurrency {
		slices.SortFunc(vs, func(a, b PriceVersion) int {
			return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model), parseInstant(a.ValidFrom).Compare(parseInstant(b.ValidFrom)))
		})
		d.PriceLists = append(d.PriceLists, PriceList{Currency: cur, Versions: vs})
	}
	slices.SortFunc(d.PriceLists, func(a, b PriceList) int { return cmp.Compare(a.Currency, b.Currency) })
	slices.Sort(d.LeftOut)
	return d
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonEmptyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in
}

// ---- the state of the platform

type platformState struct {
	providers []adminapi.Provider
	endpoints map[string][]adminapi.Endpoint // by provider name
	models    []adminapi.Model
	grants    map[string][]adminapi.ModelGrant // by model ID
	overrides map[string][]adminapi.PriceVersion
	limits    []adminapi.PlatformLimitRule
	tenants   []adminapi.Tenant
}

// readPlatform reads the platform. Grants and overrides are read for the models that can have
// them (selected availability, an override in force) and for those in want, by reference.
func readPlatform(ctx context.Context, c *adminapi.Client, want map[string]bool) (*platformState, error) {
	s := &platformState{endpoints: map[string][]adminapi.Endpoint{}, grants: map[string][]adminapi.ModelGrant{}, overrides: map[string][]adminapi.PriceVersion{}}
	var err error
	if s.tenants, err = drain(func(cur string) ([]adminapi.Tenant, string, error) {
		l, err := c.ListTenants(ctx, adminapi.ListTenantsInput{Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	if s.providers, err = drain(func(cur string) ([]adminapi.Provider, string, error) {
		l, err := c.ListProviders(ctx, adminapi.ListProvidersInput{Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	for _, p := range s.providers {
		name := p.Name
		eps, err := drain(func(cur string) ([]adminapi.Endpoint, string, error) {
			l, err := c.ListEndpoints(ctx, adminapi.ListEndpointsInput{Provider: name, Cursor: cur})
			if err != nil {
				return nil, "", err
			}
			return l.Items, l.NextCursor, nil
		})
		if err != nil {
			return nil, err
		}
		s.endpoints[name] = eps
	}
	if s.models, err = drain(func(cur string) ([]adminapi.Model, string, error) {
		l, err := c.ListModels(ctx, adminapi.ListModelsInput{Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	for _, m := range s.models {
		id, ref := m.ID, m.Provider+"/"+m.Name
		if m.Availability == adminapi.ModelAvailabilitySelected || want[ref] {
			g, err := drain(func(cur string) ([]adminapi.ModelGrant, string, error) {
				l, err := c.ListModelGrants(ctx, adminapi.ListModelGrantsInput{Model: id, Cursor: cur})
				if err != nil {
					return nil, "", err
				}
				return l.Items, l.NextCursor, nil
			})
			if err != nil {
				return nil, err
			}
			s.grants[id] = g
		}
		if m.PriceOverride || want[ref] {
			vs, err := drain(func(cur string) ([]adminapi.PriceVersion, string, error) {
				l, err := c.ListPriceVersions(ctx, adminapi.ListPriceVersionsInput{Model: id, Cursor: cur})
				if err != nil {
					return nil, "", err
				}
				return l.Items, l.NextCursor, nil
			})
			if err != nil {
				return nil, err
			}
			for _, v := range vs {
				if v.Source == adminapi.PriceSourceOverride {
					s.overrides[id] = append(s.overrides[id], v)
				}
			}
		}
	}
	if s.limits, err = drain(func(cur string) ([]adminapi.PlatformLimitRule, string, error) {
		l, err := c.ListPlatformLimits(ctx, adminapi.ListPlatformLimitsInput{Cursor: cur})
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *platformState) tenantName(id string) (string, bool) {
	if id == "" {
		return "default", true
	}
	for _, t := range s.tenants {
		if t.ID == id {
			return t.Name, true
		}
	}
	return "", false
}

func (s *platformState) document() *Document {
	d := &Document{Pandokos: Version, Kind: KindPlatform}
	for _, p := range s.providers {
		pr := Provider{Name: p.Name, APIFamily: string(p.ApiFamily), DisplayName: p.DisplayName, Description: p.Description, DatasetProvider: p.DatasetProvider,
			StreamUsageOption: string(p.StreamUsageOption)}
		for _, e := range s.endpoints[p.Name] {
			pr.Endpoints = append(pr.Endpoints, Endpoint{Name: e.Name, BaseURL: e.BaseURL, Region: e.Region, Private: e.Private, IsDefault: e.IsDefault, Description: e.Description})
		}
		slices.SortFunc(pr.Endpoints, func(a, b Endpoint) int { return cmp.Compare(a.Name, b.Name) })
		d.Providers = append(d.Providers, pr)
	}
	slices.SortFunc(d.Providers, func(a, b Provider) int { return cmp.Compare(a.Name, b.Name) })
	for _, m := range s.models {
		d.Models = append(d.Models, Model{Provider: m.Provider, Name: m.Name, UpstreamID: m.UpstreamID, DisplayName: m.DisplayName, Description: m.Description,
			Status: string(m.Status), Availability: string(m.Availability), Capabilities: nonEmptyStrings(sorted(m.Capabilities)), ContextWindow: m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens, DefaultOutputTokens: m.DefaultOutputTokens})
		for _, g := range s.grants[m.ID] {
			d.ModelGrants = append(d.ModelGrants, ModelGrant{Provider: m.Provider, Model: m.Name, Tenant: g.TenantName})
		}
		for _, v := range s.overrides[m.ID] {
			d.PriceOverrides = append(d.PriceOverrides, PriceVersion{Provider: m.Provider, Model: m.Name, ValidFrom: instant(v.ValidFrom), Note: v.Note, Currency: v.Currency, Rates: ratesOut(v.Rates)})
		}
	}
	slices.SortFunc(d.Models, func(a, b Model) int { return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Name, b.Name)) })
	slices.SortFunc(d.ModelGrants, func(a, b ModelGrant) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model), cmp.Compare(a.Tenant, b.Tenant))
	})
	slices.SortFunc(d.PriceOverrides, func(a, b PriceVersion) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model), parseInstant(a.ValidFrom).Compare(parseInstant(b.ValidFrom)))
	})
	for _, r := range s.limits {
		name, ok := s.tenantName(r.Tenant)
		if !ok {
			d.LeftOut = append(d.LeftOut, fmt.Sprintf("a ceiling (%s) of a tenant that is not listed is not in the document", r.Metric))
			continue
		}
		d.PlatformLimits = append(d.PlatformLimits, Limit{Tenant: name, Provider: r.Provider, Model: r.Model, Metric: string(r.Metric), Limit: r.Limit, Amount: r.Amount,
			Currency: r.Currency, Enforce: r.Enforce, Thresholds: nonEmptyInts(r.Thresholds)})
	}
	sortLimits(d.PlatformLimits, func(l Limit) string { return limitKey(l.tenantRef(), l) })
	slices.Sort(d.LeftOut)
	return d
}

// Export reads the configuration of target into a document, sorted by natural key. It makes no
// call that changes anything.
func Export(ctx context.Context, c *adminapi.Client, target Target) (*Document, error) {
	if target.Platform {
		s, err := readPlatform(ctx, c, nil)
		if err != nil {
			return nil, err
		}
		return s.document(), nil
	}
	s, err := readTenant(ctx, c, target.Tenant)
	if err != nil {
		return nil, err
	}
	return s.document(), nil
}

// YAML writes a document as the command does: two spaces of indentation, and the notes of what
// the export left out as comment lines at the end.
func (d *Document) YAML() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	for _, n := range d.LeftOut {
		b.WriteString("# " + strings.ReplaceAll(n, "\n", " ") + "\n")
	}
	return b.Bytes(), nil
}
