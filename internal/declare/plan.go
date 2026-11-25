package declare

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// step is the call that carries one change out.
type step func(ctx context.Context, c *adminapi.Client) error

// refs hold the IDs a later step needs from an earlier one: what exists, and what a create made.
type refs struct {
	serviceAccount map[string]string // by name
	model          map[string]string // by reference provider/name
	tenant         map[string]string // by name (platform)
}

type planner struct {
	now     time.Time
	refs    *refs
	changes []Change
}

func (p *planner) add(action, resource, detail string, run step) {
	p.changes = append(p.changes, Change{Action: action, Resource: resource, Detail: detail, run: run})
}

func (p *planner) create(resource string, run step) { p.add("create", resource, "", run) }
func (p *planner) update(resource, detail string, run step) {
	p.add("update", resource, detail, run)
}
func (p *planner) conflict(resource, detail string) { p.add("conflict", resource, detail, nil) }
func (p *planner) notInDocument(resource string)    { p.add("not in document", resource, "", nil) }

// Diff compares doc with the current state of target by natural key and returns the plan: what is
// absent is created, a mutable field that differs is updated, an immutable one is a conflict,
// price versions are only added, and what exists but is not in the document is reported. It makes
// no call that changes anything.
func Diff(ctx context.Context, c *adminapi.Client, target Target, doc *Document, opts ...Options) (Plan, error) {
	if err := doc.Validate(target); err != nil {
		return Plan{}, err
	}
	p := &planner{now: time.Now().UTC(), refs: &refs{serviceAccount: map[string]string{}, model: map[string]string{}, tenant: map[string]string{}}}
	var err error
	if target.Platform {
		err = p.platform(ctx, c, doc)
	} else {
		err = p.tenant(ctx, c, target.Tenant, doc)
	}
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Target: target, Changes: p.changes, refs: p.refs}
	if len(opts) > 0 {
		plan.opts = opts[0]
	}
	return plan, nil
}

// Preview is the result of the plan as it stands, with no call made: what a dry run prints.
func (p Plan) Preview() Result {
	var r Result
	for _, ch := range p.Changes {
		r.count(ch, ch.run != nil)
	}
	return r
}

func (r *Result) count(ch Change, done bool) {
	switch {
	case ch.Action == "conflict":
		r.Conflicts++
	case ch.Action == "not in document":
		r.LeftAlone++
	case done:
		r.Changes++
	}
	r.Lines = append(r.Lines, ch.Line())
}

// Apply carries the plan out through the client in its order and stops at the first failed call;
// what was done before it is in the result.
func (p Plan) Apply(ctx context.Context, c *adminapi.Client) (Result, error) {
	var r Result
	for _, ch := range p.Changes {
		if ch.run != nil {
			if err := ch.run(ctx, c); err != nil {
				return r, fmt.Errorf("%s %s: %w", ch.Action, ch.Resource, err)
			}
		}
		r.count(ch, true)
	}
	return r, nil
}

// ---- the platform

func (p *planner) platform(ctx context.Context, c *adminapi.Client, doc *Document) error {
	want := map[string]bool{}
	for _, m := range doc.Models {
		want[m.Provider+"/"+m.Name] = true
	}
	for _, g := range doc.ModelGrants {
		want[g.Provider+"/"+g.Model] = true
	}
	for _, o := range doc.PriceOverrides {
		want[o.Provider+"/"+o.Model] = true
	}
	st, err := readPlatform(ctx, c, want)
	if err != nil {
		return err
	}
	for _, t := range st.tenants {
		p.refs.tenant[t.Name] = t.ID
	}
	modelByRef := map[string]adminapi.Model{}
	for _, m := range st.models {
		modelByRef[m.Provider+"/"+m.Name] = m
		p.refs.model[m.Provider+"/"+m.Name] = m.ID
	}

	// Providers.
	provByName := map[string]adminapi.Provider{}
	for _, pr := range st.providers {
		provByName[pr.Name] = pr
	}
	docProviders := slices.Clone(doc.Providers)
	slices.SortFunc(docProviders, func(a, b Provider) int { return cmp.Compare(a.Name, b.Name) })
	inDoc := map[string]bool{}
	for _, d := range docProviders {
		inDoc[d.Name] = true
		cur, ok := provByName[d.Name]
		res := "provider " + d.Name
		switch {
		case !ok:
			p.create(res, func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.CreateProvider(ctx, adminapi.CreateProviderInput{Body: adminapi.ProviderCreate{Name: d.Name, ApiFamily: adminapi.ApiFamily(d.APIFamily),
					DisplayName: d.DisplayName, Description: d.Description, DatasetProvider: d.DatasetProvider, StreamUsageOption: adminapi.StreamUsageOption(d.StreamUsageOption)}})
				return err
			})
		case d.APIFamily != string(cur.ApiFamily):
			p.conflict(res, "api_family differs and cannot be changed")
		default:
			var body adminapi.ProviderUpdate
			var changed []string
			if d.DisplayName != "" && d.DisplayName != cur.DisplayName {
				body.DisplayName = adminapi.Some(d.DisplayName)
				changed = append(changed, "display_name")
			}
			if d.Description != "" && d.Description != cur.Description {
				body.Description = adminapi.Some(d.Description)
				changed = append(changed, "description")
			}
			if d.DatasetProvider != "" && d.DatasetProvider != cur.DatasetProvider {
				body.DatasetProvider = adminapi.Some(d.DatasetProvider)
				changed = append(changed, "dataset_provider")
			}
			if d.StreamUsageOption != "" && d.StreamUsageOption != string(cur.StreamUsageOption) {
				body.StreamUsageOption = adminapi.Some(adminapi.StreamUsageOption(d.StreamUsageOption))
				changed = append(changed, "stream_usage_option")
			}
			if len(changed) > 0 {
				name := d.Name
				p.update(res, join(changed), func(ctx context.Context, c *adminapi.Client) error {
					_, err := c.UpdateProvider(ctx, adminapi.UpdateProviderInput{Provider: name, Body: body})
					return err
				})
			}
		}
	}
	for _, pr := range sortedKeys(provByName) {
		if !inDoc[pr] {
			p.notInDocument("provider " + pr)
		}
	}

	// Endpoints of the providers of the document.
	for _, d := range docProviders {
		have := map[string]adminapi.Endpoint{}
		for _, e := range st.endpoints[d.Name] {
			have[e.Name] = e
		}
		eps := slices.Clone(d.Endpoints)
		slices.SortFunc(eps, func(a, b Endpoint) int { return cmp.Compare(a.Name, b.Name) })
		seen := map[string]bool{}
		for _, e := range eps {
			seen[e.Name] = true
			res := "endpoint " + d.Name + "/" + e.Name
			cur, ok := have[e.Name]
			provider := d.Name
			if !ok {
				p.create(res, func(ctx context.Context, c *adminapi.Client) error {
					_, err := c.CreateEndpoint(ctx, adminapi.CreateEndpointInput{Provider: provider, Body: adminapi.EndpointCreate{Name: e.Name, BaseURL: e.BaseURL,
						Region: e.Region, Private: e.Private, IsDefault: e.IsDefault, Description: e.Description}})
					return err
				})
				continue
			}
			var body adminapi.EndpointUpdate
			var changed []string
			if e.BaseURL != "" && e.BaseURL != cur.BaseURL {
				body.BaseURL = adminapi.Some(e.BaseURL)
				changed = append(changed, "base_url")
			}
			if e.Region != "" && e.Region != cur.Region {
				body.Region = adminapi.Some(e.Region)
				changed = append(changed, "region")
			}
			if e.Private != cur.Private {
				body.Private = adminapi.Some(e.Private)
				changed = append(changed, "private")
			}
			if e.IsDefault && !cur.IsDefault {
				body.IsDefault = adminapi.Some(true)
				changed = append(changed, "is_default")
			}
			if e.Description != "" && e.Description != cur.Description {
				body.Description = adminapi.Some(e.Description)
				changed = append(changed, "description")
			}
			if len(changed) > 0 {
				name := e.Name
				p.update(res, join(changed), func(ctx context.Context, c *adminapi.Client) error {
					_, err := c.UpdateEndpoint(ctx, adminapi.UpdateEndpointInput{Provider: provider, Endpoint: name, Body: body})
					return err
				})
			}
		}
		for _, n := range sortedKeys(have) {
			if !seen[n] {
				p.notInDocument("endpoint " + d.Name + "/" + n)
			}
		}
	}

	// Models.
	docModels := slices.Clone(doc.Models)
	slices.SortFunc(docModels, func(a, b Model) int { return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Name, b.Name)) })
	modelInDoc := map[string]bool{}
	for _, d := range docModels {
		ref := d.Provider + "/" + d.Name
		modelInDoc[ref] = true
		cur, ok := modelByRef[ref]
		res := "model " + ref
		if !ok {
			p.create(res, func(ctx context.Context, c *adminapi.Client) error {
				m, err := c.CreateModel(ctx, adminapi.CreateModelInput{Body: adminapi.ModelCreate{Provider: d.Provider, Name: d.Name, UpstreamID: d.UpstreamID, DisplayName: d.DisplayName,
					Description: d.Description, Status: adminapi.ModelStatus(d.Status), Availability: adminapi.ModelAvailability(d.Availability), Capabilities: d.Capabilities,
					ContextWindow: d.ContextWindow, MaxOutputTokens: d.MaxOutputTokens, DefaultOutputTokens: d.DefaultOutputTokens}})
				if err != nil {
					return err
				}
				p.refs.model[ref] = m.ID
				return nil
			})
			continue
		}
		var body adminapi.ModelUpdate
		var changed []string
		if d.UpstreamID != "" && d.UpstreamID != cur.UpstreamID {
			body.UpstreamID = adminapi.Some(d.UpstreamID)
			changed = append(changed, "upstream_id")
		}
		if d.DisplayName != "" && d.DisplayName != cur.DisplayName {
			body.DisplayName = adminapi.Some(d.DisplayName)
			changed = append(changed, "display_name")
		}
		if d.Description != "" && d.Description != cur.Description {
			body.Description = adminapi.Some(d.Description)
			changed = append(changed, "description")
		}
		if d.Status != "" && d.Status != string(cur.Status) {
			body.Status = adminapi.Some(adminapi.ModelStatus(d.Status))
			changed = append(changed, "status")
		}
		if d.Availability != "" && d.Availability != string(cur.Availability) {
			body.Availability = adminapi.Some(adminapi.ModelAvailability(d.Availability))
			changed = append(changed, "availability")
		}
		if len(d.Capabilities) > 0 && !sameSet(d.Capabilities, cur.Capabilities) {
			body.Capabilities = adminapi.Some(d.Capabilities)
			changed = append(changed, "capabilities")
		}
		if d.ContextWindow != 0 && d.ContextWindow != cur.ContextWindow {
			body.ContextWindow = adminapi.Some(d.ContextWindow)
			changed = append(changed, "context_window")
		}
		if d.MaxOutputTokens != 0 && d.MaxOutputTokens != cur.MaxOutputTokens {
			body.MaxOutputTokens = adminapi.Some(d.MaxOutputTokens)
			changed = append(changed, "max_output_tokens")
		}
		if d.DefaultOutputTokens != 0 && d.DefaultOutputTokens != cur.DefaultOutputTokens {
			body.DefaultOutputTokens = adminapi.Some(d.DefaultOutputTokens)
			changed = append(changed, "default_output_tokens")
		}
		if len(changed) > 0 {
			id := cur.ID
			p.update(res, join(changed), func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.UpdateModel(ctx, adminapi.UpdateModelInput{Model: id, Body: body})
				return err
			})
		}
	}
	for _, ref := range sortedKeys(modelByRef) {
		if !modelInDoc[ref] {
			p.notInDocument("model " + ref)
		}
	}

	// Grants.
	type grantKey struct{ ref, tenant string }
	haveGrant := map[grantKey]bool{}
	for _, m := range st.models {
		for _, g := range st.grants[m.ID] {
			haveGrant[grantKey{m.Provider + "/" + m.Name, g.TenantName}] = true
		}
	}
	docGrants := slices.Clone(doc.ModelGrants)
	slices.SortFunc(docGrants, func(a, b ModelGrant) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model), cmp.Compare(a.Tenant, b.Tenant))
	})
	inDocGrant := map[grantKey]bool{}
	for _, g := range docGrants {
		ref := g.Provider + "/" + g.Model
		k := grantKey{ref, g.Tenant}
		inDocGrant[k] = true
		if haveGrant[k] {
			continue
		}
		res := "model_grant " + ref + " " + g.Tenant
		tid, ok := p.refs.tenant[g.Tenant]
		if !ok {
			p.conflict(res, "the tenant does not exist")
			continue
		}
		p.create(res, func(ctx context.Context, c *adminapi.Client) error {
			id := p.refs.model[ref]
			if id == "" {
				return errors.New("the model has no ID")
			}
			_, err := c.GrantModel(ctx, adminapi.GrantModelInput{Model: id, Tenant: tid})
			return err
		})
	}
	var grantKeys []grantKey
	for k := range haveGrant {
		grantKeys = append(grantKeys, k)
	}
	slices.SortFunc(grantKeys, func(a, b grantKey) int { return cmp.Or(cmp.Compare(a.ref, b.ref), cmp.Compare(a.tenant, b.tenant)) })
	for _, k := range grantKeys {
		if !inDocGrant[k] {
			p.notInDocument("model_grant " + k.ref + " " + k.tenant)
		}
	}

	// Price overrides: only added.
	type ovKey struct {
		ref string
		at  time.Time
	}
	haveOv := map[ovKey]adminapi.PriceVersion{}
	for _, m := range st.models {
		for _, v := range st.overrides[m.ID] {
			haveOv[ovKey{m.Provider + "/" + m.Name, v.ValidFrom.UTC()}] = v
		}
	}
	docOv := slices.Clone(doc.PriceOverrides)
	slices.SortFunc(docOv, func(a, b PriceVersion) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model), parseInstant(a.ValidFrom).Compare(parseInstant(b.ValidFrom)))
	})
	inDocOv := map[ovKey]bool{}
	for _, o := range docOv {
		ref := o.Provider + "/" + o.Model
		at := parseInstant(o.ValidFrom)
		k := ovKey{ref, at}
		inDocOv[k] = true
		res := "price_override " + ref + " " + instant(at)
		if cur, ok := haveOv[k]; ok {
			if (o.Currency != "" && o.Currency != cur.Currency) || !sameRates(o.Rates, ratesOut(cur.Rates)) {
				p.conflict(res, "a version with this valid_from exists with other rates; versions are only added")
			}
			continue
		}
		if at.Before(p.now) {
			p.add("skipped", res, "in the past", nil)
			continue
		}
		p.create(res, func(ctx context.Context, c *adminapi.Client) error {
			id := p.refs.model[ref]
			if id == "" {
				return errors.New("the model has no ID")
			}
			_, err := c.CreatePriceOverride(ctx, adminapi.CreatePriceOverrideInput{Model: id, Body: adminapi.PriceOverride{Currency: o.Currency, ValidFrom: &at, Note: o.Note, Rates: ratesIn(o.Rates)}})
			return err
		})
	}
	var ovKeys []ovKey
	for k := range haveOv {
		ovKeys = append(ovKeys, k)
	}
	slices.SortFunc(ovKeys, func(a, b ovKey) int { return cmp.Or(cmp.Compare(a.ref, b.ref), a.at.Compare(b.at)) })
	for _, k := range ovKeys {
		if !inDocOv[k] {
			p.notInDocument("price_override " + k.ref + " " + instant(k.at))
		}
	}

	// Ceilings.
	have := map[string]adminapi.PlatformLimitRule{}
	for _, r := range st.limits {
		name, ok := st.tenantName(r.Tenant)
		if !ok {
			continue
		}
		have[limitKey(name, Limit{Provider: r.Provider, Model: r.Model, Metric: string(r.Metric), Currency: r.Currency})] = r
	}
	docLimits := slices.Clone(doc.PlatformLimits)
	slices.SortFunc(docLimits, func(a, b Limit) int { return cmp.Compare(limitKey(a.tenantRef(), a), limitKey(b.tenantRef(), b)) })
	seen := map[string]bool{}
	for _, l := range docLimits {
		key := limitKey(l.tenantRef(), l)
		seen[key] = true
		res := "platform_limit " + limitResource(l.tenantRef(), l)
		cur, ok := have[key]
		if !ok {
			tenantID := ""
			if l.tenantRef() != "default" {
				id, found := p.refs.tenant[l.Tenant]
				if !found {
					p.conflict(res, "the tenant does not exist")
					continue
				}
				tenantID = id
			}
			p.create(res, func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.CreatePlatformLimit(ctx, adminapi.CreatePlatformLimitInput{Body: adminapi.PlatformLimitRuleCreate{Tenant: tenantID, Provider: l.Provider, Model: l.Model,
					Metric: adminapi.LimitMetric(l.Metric), Limit: l.Limit, Amount: l.Amount, Currency: l.Currency, Enforce: l.Enforce, Thresholds: l.Thresholds}})
				return err
			})
			continue
		}
		body, changed := limitUpdate(l, cur.Limit, cur.Amount, cur.Enforce, cur.Thresholds)
		if len(changed) > 0 {
			id := cur.ID
			p.update(res, join(changed), func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.UpdatePlatformLimit(ctx, adminapi.UpdatePlatformLimitInput{Rule: id, Body: body})
				return err
			})
		}
	}
	for _, k := range sortedKeys(have) {
		if !seen[k] {
			r := have[k]
			name, _ := st.tenantName(r.Tenant)
			p.notInDocument("platform_limit " + limitResource(name, Limit{Provider: r.Provider, Model: r.Model, Metric: string(r.Metric), Currency: r.Currency}))
		}
	}
	return nil
}

func limitResource(subject string, l Limit) string {
	s := subject + " " + l.Metric
	if l.Provider != "" || l.Model != "" {
		s += " " + l.Provider + "/" + l.Model
	}
	if l.Currency != "" {
		s += " " + l.Currency
	}
	return s
}

// limitUpdate lists what of a rule in the document differs from the rule held.
func limitUpdate(d Limit, limit int, amount string, enforce bool, thresholds []int) (adminapi.LimitRuleUpdate, []string) {
	var body adminapi.LimitRuleUpdate
	var changed []string
	if d.Limit != limit {
		body.Limit = adminapi.Some(d.Limit)
		changed = append(changed, "limit")
	}
	if d.Amount != amount && (d.Amount == "" || amount == "" || !sameDecimal(d.Amount, amount)) {
		body.Amount = adminapi.Some(d.Amount)
		changed = append(changed, "amount")
	}
	if d.Enforce != enforce {
		body.Enforce = adminapi.Some(d.Enforce)
		changed = append(changed, "enforce")
	}
	if !sameInts(d.Thresholds, thresholds) {
		body.Thresholds = adminapi.Some(nonEmptyIntsOrEmpty(d.Thresholds))
		changed = append(changed, "thresholds")
	}
	return body, changed
}

func nonEmptyIntsOrEmpty(in []int) []int {
	if in == nil {
		return []int{}
	}
	return in
}

// ---- a tenant

func (p *planner) tenant(ctx context.Context, c *adminapi.Client, id string, doc *Document) error {
	st, err := readTenant(ctx, c, id)
	if err != nil {
		return err
	}
	set := doc.Settings
	if set == nil {
		set = &Settings{}
	}

	// Settings: the time zone and the metadata keys first.
	if set.TimeZone != "" && set.TimeZone != st.zone {
		zone := set.TimeZone
		p.update("setting time_zone", zone, func(ctx context.Context, c *adminapi.Client) error {
			_, err := c.SetTenantTimeZone(ctx, adminapi.SetTenantTimeZoneInput{Tenant: id, Body: adminapi.TenantTimeZoneSet{TimeZone: zone}})
			return err
		})
	}
	if set.MetadataKeys != nil && (!st.metadataLimited || !sameSet(*set.MetadataKeys, st.metadataKeys)) {
		keys := nonNilStrings(slices.Clone(*set.MetadataKeys))
		p.update("setting metadata_keys", join(sorted(keys)), func(ctx context.Context, c *adminapi.Client) error {
			_, err := c.SetMetadataKeys(ctx, adminapi.SetMetadataKeysInput{Tenant: id, Body: adminapi.MetadataKeysSet{Keys: keys}})
			return err
		})
	}

	// Custom roles.
	haveRole := map[string]adminapi.Role{}
	for _, r := range st.roles {
		haveRole[r.Name] = r
	}
	roles := slices.Clone(doc.Roles)
	slices.SortFunc(roles, func(a, b Role) int { return cmp.Compare(a.Name, b.Name) })
	seen := map[string]bool{}
	for _, r := range roles {
		seen[r.Name] = true
		res := "role " + r.Name
		cur, ok := haveRole[r.Name]
		if !ok {
			p.create(res, func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.CreateRole(ctx, adminapi.CreateRoleInput{Tenant: id, Body: adminapi.RoleCreate{Name: r.Name, Description: r.Description, Permissions: nonNilStrings(r.Permissions)}})
				return err
			})
			continue
		}
		var body adminapi.RoleUpdate
		var changed []string
		if r.Description != "" && r.Description != cur.Description {
			body.Description = adminapi.Some(r.Description)
			changed = append(changed, "description")
		}
		if !sameSet(r.Permissions, cur.Permissions) {
			body.Permissions = adminapi.Some(nonNilStrings(r.Permissions))
			changed = append(changed, "permissions")
		}
		if len(changed) > 0 {
			name := r.Name
			p.update(res, join(changed), func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.UpdateRole(ctx, adminapi.UpdateRoleInput{Tenant: id, Role: name, Body: body})
				return err
			})
		}
	}
	for _, n := range sortedKeys(haveRole) {
		if !seen[n] {
			p.notInDocument("role " + n)
		}
	}

	// Service accounts with their roles.
	haveSA := map[string]adminapi.ServiceAccount{}
	for _, a := range st.serviceAccounts {
		haveSA[a.Name] = a
		p.refs.serviceAccount[a.Name] = a.ID
	}
	sas := slices.Clone(doc.ServiceAccounts)
	slices.SortFunc(sas, func(a, b ServiceAccount) int { return cmp.Compare(a.Name, b.Name) })
	seen = map[string]bool{}
	for _, a := range sas {
		seen[a.Name] = true
		res := "service_account " + a.Name
		cur, ok := haveSA[a.Name]
		if !ok {
			p.create(res, func(ctx context.Context, c *adminapi.Client) error {
				body := adminapi.ServiceAccountCreate{Name: a.Name, Description: a.Description, Roles: a.Roles}
				if a.ValidFrom != "" {
					t := parseInstant(a.ValidFrom)
					body.ValidFrom = &t
				}
				if a.ValidUntil != "" {
					t := parseInstant(a.ValidUntil)
					body.ValidUntil = &t
				}
				created, err := c.CreateServiceAccount(ctx, adminapi.CreateServiceAccountInput{Tenant: id, Body: body})
				if err != nil {
					return err
				}
				p.refs.serviceAccount[a.Name] = created.ID
				return nil
			})
			continue
		}
		var body adminapi.ServiceAccountUpdate
		var changed []string
		if a.Description != "" && a.Description != cur.Description {
			body.Description = adminapi.Some(a.Description)
			changed = append(changed, "description")
		}
		if a.ValidFrom != "" && (cur.ValidFrom == nil || !parseInstant(a.ValidFrom).Equal(*cur.ValidFrom)) {
			body.ValidFrom = adminapi.Value(parseInstant(a.ValidFrom))
			changed = append(changed, "valid_from")
		}
		if a.ValidUntil != "" && (cur.ValidUntil == nil || !parseInstant(a.ValidUntil).Equal(*cur.ValidUntil)) {
			body.ValidUntil = adminapi.Value(parseInstant(a.ValidUntil))
			changed = append(changed, "valid_until")
		}
		if len(a.Roles) > 0 && !sameSet(a.Roles, cur.Roles) {
			body.Roles = adminapi.Some(a.Roles)
			changed = append(changed, "roles")
		}
		if len(changed) > 0 {
			sid := cur.ID
			p.update(res, join(changed), func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.UpdateServiceAccount(ctx, adminapi.UpdateServiceAccountInput{Tenant: id, ServiceAccount: sid, Body: body})
				return err
			})
		}
	}
	for _, n := range sortedKeys(haveSA) {
		if !seen[n] {
			p.notInDocument("service_account " + n)
		}
	}

	// The model restriction.
	if doc.ModelRestriction != nil {
		models := nonNilStrings(slices.Clone(doc.ModelRestriction.Models))
		if !st.restricted || !sameSet(models, st.restriction) {
			p.update("model_restriction", join(sorted(models)), func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.SetModelRestriction(ctx, adminapi.SetModelRestrictionInput{Tenant: id, Body: adminapi.ModelRestrictionSet{Models: models}})
				return err
			})
		}
	}

	// Limits.
	haveLimit := map[string]adminapi.LimitRule{}
	subjectOfKey := map[string]string{}
	for _, r := range st.limits {
		subject, ok := st.limitSubject(r)
		if !ok {
			continue
		}
		l := limitOf(r, subject)
		haveLimit[subjectKey(l)] = r
		subjectOfKey[subjectKey(l)] = subject
	}
	limits := slices.Clone(doc.Limits)
	slices.SortFunc(limits, func(a, b Limit) int { return cmp.Compare(subjectKey(a), subjectKey(b)) })
	seen = map[string]bool{}
	for _, l := range limits {
		key := subjectKey(l)
		seen[key] = true
		res := "limit " + limitResource(l.Subject, l)
		cur, ok := haveLimit[key]
		if !ok {
			kind, ref, _ := subjectOf(l.Subject)
			if kind == "service_account" {
				if _, known := p.refs.serviceAccount[ref]; !known && !inSAs(sas, ref) {
					p.conflict(res, "the service account does not exist")
					continue
				}
			}
			p.create(res, func(ctx context.Context, c *adminapi.Client) error {
				body := adminapi.LimitRuleCreate{SubjectKind: adminapi.LimitSubjectKind(kind), Provider: l.Provider, Model: l.Model, Metric: adminapi.LimitMetric(l.Metric),
					Limit: l.Limit, Amount: l.Amount, Currency: l.Currency, Enforce: l.Enforce, Thresholds: l.Thresholds}
				switch kind {
				case "service_account":
					body.SubjectID = p.refs.serviceAccount[ref]
					if body.SubjectID == "" {
						return errors.New("the service account has no ID")
					}
				case "key":
					body.SubjectID = ref
				}
				_, err := c.CreateLimit(ctx, adminapi.CreateLimitInput{Tenant: id, Body: body})
				return err
			})
			continue
		}
		body, changed := limitUpdate(l, cur.Limit, cur.Amount, cur.Enforce, cur.Thresholds)
		if len(changed) > 0 {
			rid := cur.ID
			p.update(res, join(changed), func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.UpdateLimit(ctx, adminapi.UpdateLimitInput{Tenant: id, Rule: rid, Body: body})
				return err
			})
		}
	}
	for _, k := range sortedKeys(haveLimit) {
		if !seen[k] {
			r := haveLimit[k]
			p.notInDocument("limit " + limitResource(subjectOfKey[k], Limit{Provider: r.Provider, Model: r.Model, Metric: string(r.Metric), Currency: r.Currency}))
		}
	}

	// Price lists: versions are only added.
	type verKey struct {
		currency, ref string
		at            time.Time
	}
	haveVer := map[verKey]adminapi.TenantPriceVersion{}
	for _, v := range st.prices {
		if v.Kind == adminapi.TenantPriceKindPrice {
			haveVer[verKey{v.Currency, v.Provider + "/" + v.Model, v.ValidFrom.UTC()}] = v
		}
	}
	lists := slices.Clone(doc.PriceLists)
	slices.SortFunc(lists, func(a, b PriceList) int { return cmp.Compare(a.Currency, b.Currency) })
	inDocVer := map[verKey]bool{}
	for _, pl := range lists {
		vs := slices.Clone(pl.Versions)
		slices.SortFunc(vs, func(a, b PriceVersion) int {
			return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model), parseInstant(a.ValidFrom).Compare(parseInstant(b.ValidFrom)))
		})
		for _, v := range vs {
			ref := v.Provider + "/" + v.Model
			at := parseInstant(v.ValidFrom)
			k := verKey{pl.Currency, ref, at}
			inDocVer[k] = true
			res := "price " + pl.Currency + " " + ref + " " + instant(at)
			if cur, ok := haveVer[k]; ok {
				if !sameRates(v.Rates, ratesOut(cur.Rates)) {
					p.conflict(res, "a version with this valid_from exists with other rates; versions are only added")
				}
				continue
			}
			if at.Before(p.now) {
				p.add("skipped", res, "in the past", nil)
				continue
			}
			currency := pl.Currency
			p.create(res, func(ctx context.Context, c *adminapi.Client) error {
				_, err := c.CreatePriceListVersion(ctx, adminapi.CreatePriceListVersionInput{Tenant: id, Body: adminapi.TenantPriceCreate{Provider: v.Provider, Model: v.Model,
					Currency: currency, ValidFrom: &at, Note: v.Note, Rates: ratesIn(v.Rates)}})
				return err
			})
		}
	}
	var verKeys []verKey
	for k := range haveVer {
		verKeys = append(verKeys, k)
	}
	slices.SortFunc(verKeys, func(a, b verKey) int {
		return cmp.Or(cmp.Compare(a.currency, b.currency), cmp.Compare(a.ref, b.ref), a.at.Compare(b.at))
	})
	for _, k := range verKeys {
		if !inDocVer[k] {
			p.notInDocument("price " + k.currency + " " + k.ref + " " + instant(k.at))
		}
	}

	// The active currency last: a budget may need its list first.
	if set.ActiveCurrency != "" && set.ActiveCurrency != st.currency {
		cur := set.ActiveCurrency
		p.update("setting active_currency", cur, func(ctx context.Context, c *adminapi.Client) error {
			_, err := c.SetActiveCurrency(ctx, adminapi.SetActiveCurrencyInput{Tenant: id, Body: adminapi.ActiveCurrencySet{Currency: cur}})
			return err
		})
	}
	return nil
}

func inSAs(sas []ServiceAccount, name string) bool {
	for _, a := range sas {
		if a.Name == name {
			return true
		}
	}
	return false
}

func join(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
