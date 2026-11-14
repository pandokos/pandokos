package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
)

func init() { commands = append(commands, catalogueCommands...) }

// registerPaging registers the flags of a list that has no state filter and no sort.
func (l *listFlags) registerPaging(fs *flag.FlagSet) {
	fs.StringVar(&l.cursor, "cursor", "", "the next_cursor of the previous page")
	fs.IntVar(&l.limit, "limit", 0, "page size")
	fs.BoolVar(&l.all, "all", false, "follow the cursors to the end")
}

func itemsOf(items []any) any { return map[string]any{"items": items} }

// splitRef splits a model reference "provider/name" at the first slash; a name may hold slashes.
func splitRef(v string) (provider, name string, ok bool) {
	provider, name, ok = strings.Cut(v, "/")
	return provider, name, ok && provider != "" && name != ""
}

// checkRefs refuses a model list with an entry that is not a reference.
func checkRefs(flagName string, refs []string) error {
	for _, r := range refs {
		if _, _, ok := splitRef(r); !ok {
			return usagef("%s takes a model reference provider/name", flagName)
		}
	}
	return nil
}

// resolveModel turns a model ID or a reference provider/name into an ID, looking a reference up
// with listModels.
func (a *app) resolveModel(v string) (string, error) {
	if reUUID.MatchString(v) {
		return v, nil
	}
	p, n, ok := splitRef(v)
	if !ok {
		return "", usagef("a model is an ID or a reference provider/name")
	}
	l, err := a.client.ListModels(a.ctx, adminapi.ListModelsInput{Provider: p, Name: n})
	if err != nil {
		return "", err
	}
	for _, m := range l.Items {
		if m.Provider == p && m.Name == n {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("no model %q", v)
}

// modelTarget parses the flags and the model argument of a command on one model, connects and
// resolves the model.
func (a *app) modelTarget(fs *flag.FlagSet, args []string, extra int) (id string, rest []string, err error) {
	pos, err := a.begin(fs, args, 1+extra)
	if err != nil {
		return "", nil, err
	}
	id, err = a.resolveModel(pos[0])
	return id, pos[1:], err
}

func parseFamily(v string) (adminapi.ApiFamily, error) {
	f := adminapi.ApiFamily(v)
	if f != adminapi.ApiFamilyOpenai && f != adminapi.ApiFamilyAnthropic {
		return "", usagef("--family must be openai or anthropic")
	}
	return f, nil
}

func parseStatus(v string) (adminapi.ModelStatus, error) {
	s := adminapi.ModelStatus(v)
	switch s {
	case adminapi.ModelStatusActive, adminapi.ModelStatusDeprecated, adminapi.ModelStatusRetired:
		return s, nil
	}
	return "", usagef("--status must be active, deprecated or retired")
}

func parseAvailability(v string) (adminapi.ModelAvailability, error) {
	s := adminapi.ModelAvailability(v)
	switch s {
	case adminapi.ModelAvailabilityAll, adminapi.ModelAvailabilitySelected:
		return s, nil
	}
	return "", usagef("--availability must be all or selected")
}

// parseRate parses "category=rate" or "category@threshold=rate"; the rate stays decimal text.
func parseRate(v string) (adminapi.PriceRate, error) {
	left, rate, ok := strings.Cut(v, "=")
	if !ok || left == "" || rate == "" {
		return adminapi.PriceRate{}, usagef("--rate is category=rate or category@threshold=rate")
	}
	cat, thr, hasThr := strings.Cut(left, "@")
	r := adminapi.PriceRate{Category: adminapi.PriceCategory(cat), Rate: rate}
	if hasThr {
		n, err := strconv.Atoi(thr)
		if err != nil || n < 0 {
			return r, usagef("--rate: the threshold must be a non-negative integer")
		}
		r.Threshold = n
	}
	switch r.Category {
	case adminapi.PriceCategoryInput, adminapi.PriceCategoryOutput, adminapi.PriceCategoryCacheRead, adminapi.PriceCategoryCacheWrite,
		adminapi.PriceCategoryCacheWrite1h, adminapi.PriceCategoryInputAudio, adminapi.PriceCategoryOutputAudio:
	default:
		return r, usagef("--rate: unknown category")
	}
	return r, nil
}

var catalogueCommands = []command{
	{[]string{"provider", "list"}, "listProviders", func(a *app, args []string) error {
		fs := a.flags("provider list")
		var l listFlags
		l.registerPaging(fs)
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListProviders(a.ctx, adminapi.ListProvidersInput{Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"provider", "create"}, "createProvider", func(a *app, args []string) error {
		fs := a.flags("provider create")
		family := fs.String("family", "", "API family: openai or anthropic (required)")
		display := fs.String("display-name", "", "display name")
		desc := fs.String("description", "", "description")
		dataset := fs.String("dataset-provider", "", "provider id in the price dataset")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		f, err := parseFamily(*family)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		p, err := a.client.CreateProvider(a.ctx, adminapi.CreateProviderInput{Body: adminapi.ProviderCreate{
			Name: pos[0], ApiFamily: f, DisplayName: *display, Description: *desc, DatasetProvider: *dataset}})
		if err != nil {
			return err
		}
		return a.printResource(p)
	}},

	{[]string{"provider", "get"}, "getProvider", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("provider get"), args, 1)
		if err != nil {
			return err
		}
		p, err := a.client.GetProvider(a.ctx, adminapi.GetProviderInput{Provider: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(p)
	}},

	{[]string{"provider", "update"}, "updateProvider", func(a *app, args []string) error {
		fs := a.flags("provider update")
		display := fs.String("display-name", "", "new display name (empty clears)")
		desc := fs.String("description", "", "new description (empty clears)")
		dataset := fs.String("dataset-provider", "", "provider id in the price dataset (empty stops imports)")
		enabled := fs.Bool("enabled", false, "enable (--enabled) or disable (--enabled=false)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		var body adminapi.ProviderUpdate
		if given(fs, "display-name") {
			body.DisplayName = adminapi.Some(*display)
		}
		if given(fs, "description") {
			body.Description = adminapi.Some(*desc)
		}
		if given(fs, "dataset-provider") {
			body.DatasetProvider = adminapi.Some(*dataset)
		}
		if given(fs, "enabled") {
			body.Enabled = adminapi.Some(*enabled)
		}
		if !body.DisplayName.Set && !body.Description.Set && !body.DatasetProvider.Set && !body.Enabled.Set {
			return usagef("provider update needs a member to change")
		}
		if err := a.connect(); err != nil {
			return err
		}
		p, err := a.client.UpdateProvider(a.ctx, adminapi.UpdateProviderInput{Provider: pos[0], Body: body})
		if err != nil {
			return err
		}
		return a.printResource(p)
	}},

	{[]string{"provider", "delete"}, "deleteProvider", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("provider delete"), args, 1)
		if err != nil {
			return err
		}
		p, err := a.client.DeleteProvider(a.ctx, adminapi.DeleteProviderInput{Provider: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(p)
	}},

	{[]string{"endpoint", "list"}, "listEndpoints", func(a *app, args []string) error {
		fs := a.flags("endpoint list")
		var l listFlags
		l.registerPaging(fs)
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListEndpoints(a.ctx, adminapi.ListEndpointsInput{Provider: pos[0], Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"endpoint", "create"}, "createEndpoint", func(a *app, args []string) error {
		fs := a.flags("endpoint create")
		baseURL := fs.String("base-url", "", "base URL of the endpoint (required)")
		region := fs.String("region", "", "region")
		private := fs.Bool("private", false, "the endpoint is private to the installation")
		isDefault := fs.Bool("default", false, "make it the default endpoint of the provider")
		desc := fs.String("description", "", "description")
		pos, err := parse(fs, args, 2)
		if err != nil {
			return err
		}
		if *baseURL == "" {
			return usagef("endpoint create needs --base-url")
		}
		if err := a.connect(); err != nil {
			return err
		}
		e, err := a.client.CreateEndpoint(a.ctx, adminapi.CreateEndpointInput{Provider: pos[0], Body: adminapi.EndpointCreate{
			Name: pos[1], BaseURL: *baseURL, Region: *region, Private: *private, IsDefault: *isDefault, Description: *desc}})
		if err != nil {
			return err
		}
		return a.printResource(e)
	}},

	{[]string{"endpoint", "get"}, "getEndpoint", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("endpoint get"), args, 2)
		if err != nil {
			return err
		}
		e, err := a.client.GetEndpoint(a.ctx, adminapi.GetEndpointInput{Provider: pos[0], Endpoint: pos[1]})
		if err != nil {
			return err
		}
		return a.printResource(e)
	}},

	{[]string{"endpoint", "update"}, "updateEndpoint", func(a *app, args []string) error {
		fs := a.flags("endpoint update")
		baseURL := fs.String("base-url", "", "new base URL")
		region := fs.String("region", "", "new region (empty clears)")
		private := fs.Bool("private", false, "--private or --private=false")
		enabled := fs.Bool("enabled", false, "--enabled or --enabled=false")
		isDefault := fs.Bool("default", false, "make it the default (--default); false is refused for the default")
		desc := fs.String("description", "", "new description (empty clears)")
		pos, err := parse(fs, args, 2)
		if err != nil {
			return err
		}
		var body adminapi.EndpointUpdate
		if given(fs, "base-url") {
			body.BaseURL = adminapi.Some(*baseURL)
		}
		if given(fs, "region") {
			body.Region = adminapi.Some(*region)
		}
		if given(fs, "private") {
			body.Private = adminapi.Some(*private)
		}
		if given(fs, "enabled") {
			body.Enabled = adminapi.Some(*enabled)
		}
		if given(fs, "default") {
			body.IsDefault = adminapi.Some(*isDefault)
		}
		if given(fs, "description") {
			body.Description = adminapi.Some(*desc)
		}
		if !body.BaseURL.Set && !body.Region.Set && !body.Private.Set && !body.Enabled.Set && !body.IsDefault.Set && !body.Description.Set {
			return usagef("endpoint update needs a member to change")
		}
		if err := a.connect(); err != nil {
			return err
		}
		e, err := a.client.UpdateEndpoint(a.ctx, adminapi.UpdateEndpointInput{Provider: pos[0], Endpoint: pos[1], Body: body})
		if err != nil {
			return err
		}
		return a.printResource(e)
	}},

	{[]string{"endpoint", "delete"}, "deleteEndpoint", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("endpoint delete"), args, 2)
		if err != nil {
			return err
		}
		e, err := a.client.DeleteEndpoint(a.ctx, adminapi.DeleteEndpointInput{Provider: pos[0], Endpoint: pos[1]})
		if err != nil {
			return err
		}
		return a.printResource(e)
	}},

	{[]string{"model", "list"}, "listModels", func(a *app, args []string) error {
		fs := a.flags("model list")
		var l listFlags
		l.registerPaging(fs)
		provider := fs.String("provider", "", "only models of this provider")
		name := fs.String("name", "", "only the model with this name")
		status := fs.String("status", "", "only models in this status")
		availability := fs.String("availability", "", "only models with this availability")
		sort := fs.String("sort", "", "order of the items")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		in := adminapi.ListModelsInput{Provider: *provider, Name: *name, Sort: adminapi.Sort(*sort), Limit: l.limit}
		if *status != "" {
			s, err := parseStatus(*status)
			if err != nil {
				return err
			}
			in.Status = s
		}
		if *availability != "" {
			v, err := parseAvailability(*availability)
			if err != nil {
				return err
			}
			in.Availability = v
		}
		if err := a.connect(); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			in.Cursor = cursor
			out, err := a.client.ListModels(a.ctx, in)
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"model", "create"}, "createModel", func(a *app, args []string) error {
		fs := a.flags("model create")
		upstream := fs.String("upstream-id", "", "model identifier at the provider (default: the name)")
		display := fs.String("display-name", "", "display name")
		desc := fs.String("description", "", "description")
		status := fs.String("status", "", "active, deprecated or retired")
		availability := fs.String("availability", "", "all or selected")
		var caps multi
		fs.Var(&caps, "capability", "capability (repeatable)")
		window := fs.Int("context-window", 0, "context window in tokens")
		maxOut := fs.Int("max-output-tokens", 0, "largest output in tokens")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		p, n, ok := splitRef(pos[0])
		if !ok {
			return usagef("a model is created with a reference provider/name")
		}
		body := adminapi.ModelCreate{Provider: p, Name: n, UpstreamID: *upstream, DisplayName: *display, Description: *desc,
			Capabilities: caps, ContextWindow: *window, MaxOutputTokens: *maxOut}
		if *status != "" {
			if body.Status, err = parseStatus(*status); err != nil {
				return err
			}
		}
		if *availability != "" {
			if body.Availability, err = parseAvailability(*availability); err != nil {
				return err
			}
		}
		if err := a.connect(); err != nil {
			return err
		}
		m, err := a.client.CreateModel(a.ctx, adminapi.CreateModelInput{Body: body})
		if err != nil {
			return err
		}
		return a.printResource(m)
	}},

	{[]string{"model", "get"}, "getModel", func(a *app, args []string) error {
		id, _, err := a.modelTarget(a.flags("model get"), args, 0)
		if err != nil {
			return err
		}
		m, err := a.client.GetModel(a.ctx, adminapi.GetModelInput{Model: id})
		if err != nil {
			return err
		}
		return a.printResource(m)
	}},

	{[]string{"model", "update"}, "updateModel", func(a *app, args []string) error {
		fs := a.flags("model update")
		upstream := fs.String("upstream-id", "", "model identifier at the provider")
		display := fs.String("display-name", "", "new display name (empty clears)")
		desc := fs.String("description", "", "new description (empty clears)")
		status := fs.String("status", "", "active, deprecated or retired")
		availability := fs.String("availability", "", "all or selected")
		var caps multi
		fs.Var(&caps, "capability", "capability (repeatable; replaces the set)")
		noCaps := fs.Bool("no-capabilities", false, "remove every capability")
		window := fs.Int("context-window", 0, "context window in tokens (0 clears)")
		maxOut := fs.Int("max-output-tokens", 0, "largest output in tokens (0 clears)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		var body adminapi.ModelUpdate
		if given(fs, "upstream-id") {
			body.UpstreamID = adminapi.Some(*upstream)
		}
		if given(fs, "display-name") {
			body.DisplayName = adminapi.Some(*display)
		}
		if given(fs, "description") {
			body.Description = adminapi.Some(*desc)
		}
		if given(fs, "status") {
			s, err := parseStatus(*status)
			if err != nil {
				return err
			}
			body.Status = adminapi.Some(s)
		}
		if given(fs, "availability") {
			v, err := parseAvailability(*availability)
			if err != nil {
				return err
			}
			body.Availability = adminapi.Some(v)
		}
		switch {
		case *noCaps && len(caps) > 0:
			return usagef("--no-capabilities cannot be combined with --capability")
		case *noCaps:
			body.Capabilities = adminapi.Some([]string{})
		case len(caps) > 0:
			body.Capabilities = adminapi.Some([]string(caps))
		}
		if given(fs, "context-window") {
			body.ContextWindow = adminapi.Some(*window)
		}
		if given(fs, "max-output-tokens") {
			body.MaxOutputTokens = adminapi.Some(*maxOut)
		}
		if !body.UpstreamID.Set && !body.DisplayName.Set && !body.Description.Set && !body.Status.Set && !body.Availability.Set &&
			!body.Capabilities.Set && !body.ContextWindow.Set && !body.MaxOutputTokens.Set {
			return usagef("model update needs a member to change")
		}
		if err := a.connect(); err != nil {
			return err
		}
		id, err := a.resolveModel(pos[0])
		if err != nil {
			return err
		}
		m, err := a.client.UpdateModel(a.ctx, adminapi.UpdateModelInput{Model: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(m)
	}},

	{[]string{"model", "delete"}, "deleteModel", func(a *app, args []string) error {
		id, _, err := a.modelTarget(a.flags("model delete"), args, 0)
		if err != nil {
			return err
		}
		m, err := a.client.DeleteModel(a.ctx, adminapi.DeleteModelInput{Model: id})
		if err != nil {
			return err
		}
		return a.printResource(m)
	}},

	{[]string{"model", "grants"}, "listModelGrants", func(a *app, args []string) error {
		fs := a.flags("model grants")
		var l listFlags
		l.registerPaging(fs)
		id, _, err := a.modelTarget(fs, args, 0)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListModelGrants(a.ctx, adminapi.ListModelGrantsInput{Model: id, Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"model", "grant"}, "grantModel", func(a *app, args []string) error {
		fs := a.flags("model grant")
		tenantFlag := fs.String("tenant", "", "tenant ID or name (required)")
		id, _, err := a.modelTarget(fs, args, 0)
		if err != nil {
			return err
		}
		if *tenantFlag == "" {
			return usagef("model grant needs --tenant")
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		g, err := a.client.GrantModel(a.ctx, adminapi.GrantModelInput{Model: id, Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(g)
	}},

	{[]string{"model", "revoke-grant"}, "revokeModelGrant", func(a *app, args []string) error {
		fs := a.flags("model revoke-grant")
		tenantFlag := fs.String("tenant", "", "tenant ID or name (required)")
		id, _, err := a.modelTarget(fs, args, 0)
		if err != nil {
			return err
		}
		if *tenantFlag == "" {
			return usagef("model revoke-grant needs --tenant")
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		g, err := a.client.RevokeModelGrant(a.ctx, adminapi.RevokeModelGrantInput{Model: id, Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(g)
	}},

	{[]string{"price", "list"}, "listPriceVersions", func(a *app, args []string) error {
		fs := a.flags("price list")
		var l listFlags
		l.registerPaging(fs)
		at := fs.String("at", "", "the version in force at this time (RFC 3339)")
		id, _, err := a.modelTarget(fs, args, 0)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListPriceVersions(a.ctx, adminapi.ListPriceVersionsInput{Model: id, At: *at, Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"price", "override"}, "createPriceOverride", func(a *app, args []string) error {
		fs := a.flags("price override")
		currency := fs.String("currency", "", "ISO 4217 currency of the rates (required)")
		validFrom := fs.String("valid-from", "", "start of validity (RFC 3339; default now)")
		note := fs.String("note", "", "note recorded with the override")
		var rates multi
		fs.Var(&rates, "rate", "category[@threshold]=price per million units, as decimal text (repeatable, required)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if *currency == "" {
			return usagef("price override needs --currency")
		}
		if len(rates) == 0 {
			return usagef("price override needs at least one --rate")
		}
		body := adminapi.PriceOverride{Currency: *currency, Note: *note}
		for _, r := range rates {
			pr, err := parseRate(r)
			if err != nil {
				return err
			}
			body.Rates = append(body.Rates, pr)
		}
		if body.ValidFrom, err = parseTime("--valid-from", *validFrom); err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		id, err := a.resolveModel(pos[0])
		if err != nil {
			return err
		}
		v, err := a.client.CreatePriceOverride(a.ctx, adminapi.CreatePriceOverrideInput{Model: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(v)
	}},

	{[]string{"price", "delete"}, "deletePriceVersion", func(a *app, args []string) error {
		id, rest, err := a.modelTarget(a.flags("price delete"), args, 1)
		if err != nil {
			return err
		}
		v, err := a.client.DeletePriceVersion(a.ctx, adminapi.DeletePriceVersionInput{Model: id, PriceVersion: rest[0]})
		if err != nil {
			return err
		}
		return a.printResource(v)
	}},

	{[]string{"price", "clear-override"}, "clearPriceOverride", func(a *app, args []string) error {
		fs := a.flags("price clear-override")
		reason := fs.String("reason", "", "recorded in the audit log")
		id, _, err := a.modelTarget(fs, args, 0)
		if err != nil {
			return err
		}
		m, err := a.client.ClearPriceOverride(a.ctx, adminapi.ClearPriceOverrideInput{Model: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(m)
	}},

	{[]string{"price-import", "list"}, "listPriceImports", func(a *app, args []string) error {
		fs := a.flags("price-import list")
		var l listFlags
		l.registerPaging(fs)
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListPriceImports(a.ctx, adminapi.ListPriceImportsInput{Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"price-import", "get"}, "getPriceImport", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("price-import get"), args, 1)
		if err != nil {
			return err
		}
		i, err := a.client.GetPriceImport(a.ctx, adminapi.GetPriceImportInput{PriceImport: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(i)
	}},

	{[]string{"price-import", "run"}, "runPriceImport", func(a *app, args []string) error {
		fs := a.flags("price-import run")
		source := fs.String("source", "embedded", "embedded or remote")
		dryRun := fs.Bool("dry-run", false, "report what would change and write nothing")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		src := adminapi.PriceImportRunSource(*source)
		if src != adminapi.PriceImportRunSourceEmbedded && src != adminapi.PriceImportRunSourceRemote {
			return usagef("--source must be embedded or remote")
		}
		if err := a.connect(); err != nil {
			return err
		}
		i, err := a.client.RunPriceImport(a.ctx, adminapi.RunPriceImportInput{Body: adminapi.PriceImportRun{Source: src, DryRun: *dryRun}})
		if err != nil {
			return err
		}
		return a.printResource(i)
	}},

	{[]string{"tenant-model", "list"}, "listTenantModels", func(a *app, args []string) error {
		fs := a.flags("tenant-model list")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		provider := fs.String("provider", "", "only models of this provider")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTenantModels(a.ctx, adminapi.ListTenantModelsInput{Tenant: tid, Provider: *provider, Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"model-restriction", "get"}, "getModelRestriction", func(a *app, args []string) error {
		fs := a.flags("model-restriction get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		r, err := a.client.GetModelRestriction(a.ctx, adminapi.GetModelRestrictionInput{Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},

	{[]string{"model-restriction", "set"}, "setModelRestriction", func(a *app, args []string) error {
		fs := a.flags("model-restriction set")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		var models multi
		fs.Var(&models, "model", "model reference provider/name the tenant may use (repeatable)")
		none := fs.Bool("none", false, "allow no model at all")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		switch {
		case *none && len(models) > 0:
			return usagef("--none cannot be combined with --model")
		case !*none && len(models) == 0:
			return usagef("model-restriction set needs --model (repeatable) or --none")
		}
		if err := checkRefs("--model", models); err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		list := []string(models)
		if *none {
			list = []string{}
		}
		r, err := a.client.SetModelRestriction(a.ctx, adminapi.SetModelRestrictionInput{Tenant: tid, Body: adminapi.ModelRestrictionSet{Models: list}})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},

	{[]string{"model-restriction", "clear"}, "clearModelRestriction", func(a *app, args []string) error {
		fs := a.flags("model-restriction clear")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		r, err := a.client.ClearModelRestriction(a.ctx, adminapi.ClearModelRestrictionInput{Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},
}
