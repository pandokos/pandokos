package cli

import (
	"flag"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
)

func init() { commands = append(commands, issuerCommands...) }

// registerSorted registers the flags of a list that has a sort and no state filter.
func (l *listFlags) registerSorted(fs *flag.FlagSet) {
	fs.StringVar(&l.sort, "sort", "", "order of the items")
	l.registerPaging(fs)
}

// parseEnabled reads --enabled, the filter of the registration lists.
func parseEnabled(v string) (adminapi.EnabledFilter, error) {
	switch f := adminapi.EnabledFilter(v); f {
	case "", adminapi.EnabledFilterTrue, adminapi.EnabledFilterFalse:
		return f, nil
	}
	return "", usagef("--enabled must be true or false")
}

// parseRequire reads the values of --require, each CLAIM=VALUE; a claim named twice is refused.
func parseRequire(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil //nolint:nilnil // the flag was not given
	}
	out := map[string]string{}
	for _, p := range pairs {
		claim, value, ok := strings.Cut(p, "=")
		if !ok || claim == "" || value == "" {
			return nil, usagef("--require is CLAIM=VALUE")
		}
		if _, dup := out[claim]; dup {
			return nil, usagef("--require names a claim twice")
		}
		out[claim] = value
	}
	return out, nil
}

// registrationFlags are the members that registrations of both kinds are written with.
type registrationFlags struct {
	name, issuer, audience, jwksURI, subjectClaim, tenantClaim, description string
	algorithms, require                                                     multi
	noRequire, disabled, enable, disable                                    bool
}

// register registers the flags of a creation (create) or of an update.
func (r *registrationFlags) register(fs *flag.FlagSet, platform, create bool) {
	fs.StringVar(&r.name, "name", "", "name of the registration (unique)")
	fs.StringVar(&r.issuer, "issuer", "", "the iss claim: an https URL")
	fs.StringVar(&r.audience, "audience", "", "the value that must be among the aud entries")
	fs.StringVar(&r.jwksURI, "jwks-uri", "", "https URL of the key set (default: OpenID Connect discovery under the issuer)")
	fs.Var(&r.algorithms, "algorithm", "accepted signature algorithm (repeatable; default RS256 and ES256)")
	fs.StringVar(&r.subjectClaim, "subject-claim", "", "claim that identifies the caller (default sub)")
	if platform {
		fs.StringVar(&r.tenantClaim, "tenant-claim", "", "claim that designates the tenant")
	}
	fs.Var(&r.require, "require", "CLAIM=VALUE a token must carry (repeatable)")
	fs.StringVar(&r.description, "description", "", "description")
	if create {
		fs.BoolVar(&r.disabled, "disabled", false, "register the issuer disabled")
		return
	}
	fs.BoolVar(&r.noRequire, "no-require", false, "remove every required claim")
	fs.BoolVar(&r.enable, "enable", false, "enable the registration")
	fs.BoolVar(&r.disable, "disable", false, "disable the registration")
}

// createValues checks the flags of a creation and returns the required claims.
func (r *registrationFlags) createValues(platform bool) (map[string]string, error) {
	if r.name == "" || r.issuer == "" || r.audience == "" {
		return nil, usagef("--name, --issuer and --audience are required")
	}
	if platform && r.tenantClaim == "" {
		return nil, usagef("--tenant-claim is required")
	}
	return parseRequire(r.require)
}

// updateBody turns the flags that were given into the members of an update; none given is a
// usage error. tenantClaim is for platform-level registrations only.
func (r *registrationFlags) updateBody(fs *flag.FlagSet, platform bool) (adminapi.TokenIssuerUpdate, adminapi.Optional[string], error) {
	var b adminapi.TokenIssuerUpdate
	var tenantClaim adminapi.Optional[string]
	str := func(flagName, value string, dst *adminapi.Optional[string]) {
		if given(fs, flagName) {
			*dst = adminapi.Some(value) // an empty value clears or is refused by the API
		}
	}
	str("name", r.name, &b.Name)
	str("description", r.description, &b.Description)
	str("issuer", r.issuer, &b.Issuer)
	str("audience", r.audience, &b.Audience)
	str("jwks-uri", r.jwksURI, &b.JwksUri)
	str("subject-claim", r.subjectClaim, &b.SubjectClaim)
	if platform {
		str("tenant-claim", r.tenantClaim, &tenantClaim)
	}
	if len(r.algorithms) > 0 {
		b.Algorithms = adminapi.Some([]string(r.algorithms))
	}
	if r.noRequire && len(r.require) > 0 {
		return b, tenantClaim, usagef("--no-require and --require cannot be used together")
	}
	switch claims, err := parseRequire(r.require); {
	case err != nil:
		return b, tenantClaim, err
	case r.noRequire:
		b.RequiredClaims = adminapi.Some(map[string]string{})
	case claims != nil:
		b.RequiredClaims = adminapi.Some(claims)
	}
	if r.enable && r.disable {
		return b, tenantClaim, usagef("--enable and --disable cannot be used together")
	}
	switch {
	case r.enable:
		b.Enabled = adminapi.Some(true)
	case r.disable:
		b.Enabled = adminapi.Some(false)
	}
	empty := adminapi.TokenIssuerUpdate{}
	if b.Name == empty.Name && b.Description == empty.Description && b.Issuer == empty.Issuer && b.Audience == empty.Audience && b.JwksUri == empty.JwksUri &&
		b.SubjectClaim == empty.SubjectClaim && !b.Algorithms.Set && !b.RequiredClaims.Set && !b.Enabled.Set && !tenantClaim.Set {
		return b, tenantClaim, usagef("an update needs at least one flag")
	}
	return b, tenantClaim, nil
}

var issuerCommands = []command{
	{[]string{"token-issuer", "list"}, "listTokenIssuers", func(a *app, args []string) error {
		fs := a.flags("token-issuer list")
		var l listFlags
		l.registerSorted(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		name := fs.String("name", "", "only the registration with this name")
		enabled := fs.String("enabled", "", "only registrations that are enabled (true) or disabled (false)")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		filter, err := parseEnabled(*enabled)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTokenIssuers(a.ctx, adminapi.ListTokenIssuersInput{Tenant: tid, Name: *name, Enabled: filter, Sort: adminapi.NamedSort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"token-issuer", "create"}, "createTokenIssuer", func(a *app, args []string) error {
		fs := a.flags("token-issuer create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		var r registrationFlags
		r.register(fs, false, true)
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		claims, err := r.createValues(false)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.CreateTokenIssuer(a.ctx, adminapi.CreateTokenIssuerInput{Tenant: tid, Body: adminapi.TokenIssuerCreate{
			Name: r.name, Description: r.description, Issuer: r.issuer, Audience: r.audience, JwksUri: r.jwksURI, Algorithms: r.algorithms,
			SubjectClaim: r.subjectClaim, RequiredClaims: claims, Enabled: !r.disabled}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"token-issuer", "get"}, "getTokenIssuer", func(a *app, args []string) error {
		fs := a.flags("token-issuer get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.GetTokenIssuer(a.ctx, adminapi.GetTokenIssuerInput{Tenant: tid, Issuer: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"token-issuer", "update"}, "updateTokenIssuer", func(a *app, args []string) error {
		fs := a.flags("token-issuer update")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		var r registrationFlags
		r.register(fs, false, false)
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		body, _, err := r.updateBody(fs, false)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.UpdateTokenIssuer(a.ctx, adminapi.UpdateTokenIssuerInput{Tenant: tid, Issuer: pos[0], Body: body})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"token-issuer", "delete"}, "deleteTokenIssuer", func(a *app, args []string) error {
		fs := a.flags("token-issuer delete")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.DeleteTokenIssuer(a.ctx, adminapi.DeleteTokenIssuerInput{Tenant: tid, Issuer: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"token-issuer", "platform"}, "listTenantPlatformIssuers", func(a *app, args []string) error {
		fs := a.flags("token-issuer platform")
		var l listFlags
		l.registerSorted(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTenantPlatformIssuers(a.ctx, adminapi.ListTenantPlatformIssuersInput{Tenant: tid, Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"subject-mapping", "list"}, "listSubjectMappings", func(a *app, args []string) error {
		fs := a.flags("subject-mapping list")
		var l listFlags
		l.registerSorted(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		issuer := fs.String("issuer", "", "only mappings of this registration (ID)")
		account := fs.String("service-account", "", "only mappings to this service account (ID or name)")
		subject := fs.String("subject", "", "only mappings of this subject")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		sa := ""
		if *account != "" {
			if sa, err = a.resolveServiceAccount(tid, *account); err != nil {
				return err
			}
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListSubjectMappings(a.ctx, adminapi.ListSubjectMappingsInput{Tenant: tid, Issuer: *issuer, ServiceAccount: sa, Subject: *subject,
				Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"subject-mapping", "create"}, "createSubjectMapping", func(a *app, args []string) error {
		fs := a.flags("subject-mapping create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		issuer := fs.String("issuer", "", "ID of the registration (required)")
		platform := fs.Bool("platform", false, "the registration is a platform-level one that lists the tenant")
		subject := fs.String("subject", "", "the subject of the token (required)")
		account := fs.String("service-account", "", "service account the subject acts as: ID or name (required)")
		desc := fs.String("description", "", "description")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *issuer == "" || *subject == "" || *account == "" {
			return usagef("--issuer, --subject and --service-account are required")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		sa, err := a.resolveServiceAccount(tid, *account)
		if err != nil {
			return err
		}
		scope := adminapi.IssuerScopeTenant
		if *platform {
			scope = adminapi.IssuerScopePlatform
		}
		out, err := a.client.CreateSubjectMapping(a.ctx, adminapi.CreateSubjectMappingInput{Tenant: tid, Body: adminapi.SubjectMappingCreate{
			Issuer: adminapi.IssuerRef{Scope: scope, ID: *issuer}, Subject: *subject, ServiceAccountID: sa, Description: *desc}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"subject-mapping", "get"}, "getSubjectMapping", func(a *app, args []string) error {
		fs := a.flags("subject-mapping get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.GetSubjectMapping(a.ctx, adminapi.GetSubjectMappingInput{Tenant: tid, Mapping: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"subject-mapping", "delete"}, "deleteSubjectMapping", func(a *app, args []string) error {
		fs := a.flags("subject-mapping delete")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.DeleteSubjectMapping(a.ctx, adminapi.DeleteSubjectMappingInput{Tenant: tid, Mapping: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-issuer", "list"}, "listPlatformIssuers", func(a *app, args []string) error {
		fs := a.flags("platform-issuer list")
		var l listFlags
		l.registerSorted(fs)
		name := fs.String("name", "", "only the registration with this name")
		enabled := fs.String("enabled", "", "only registrations that are enabled (true) or disabled (false)")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		filter, err := parseEnabled(*enabled)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListPlatformIssuers(a.ctx, adminapi.ListPlatformIssuersInput{Name: *name, Enabled: filter, Sort: adminapi.NamedSort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"platform-issuer", "create"}, "createPlatformIssuer", func(a *app, args []string) error {
		fs := a.flags("platform-issuer create")
		var r registrationFlags
		r.register(fs, true, true)
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		claims, err := r.createValues(true)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		out, err := a.client.CreatePlatformIssuer(a.ctx, adminapi.CreatePlatformIssuerInput{Body: adminapi.PlatformIssuerCreate{
			Name: r.name, Description: r.description, Issuer: r.issuer, Audience: r.audience, JwksUri: r.jwksURI, Algorithms: r.algorithms,
			SubjectClaim: r.subjectClaim, TenantClaim: r.tenantClaim, RequiredClaims: claims, Enabled: !r.disabled}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-issuer", "get"}, "getPlatformIssuer", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("platform-issuer get"), args, 1)
		if err != nil {
			return err
		}
		out, err := a.client.GetPlatformIssuer(a.ctx, adminapi.GetPlatformIssuerInput{Issuer: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-issuer", "update"}, "updatePlatformIssuer", func(a *app, args []string) error {
		fs := a.flags("platform-issuer update")
		var r registrationFlags
		r.register(fs, true, false)
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		b, tenantClaim, err := r.updateBody(fs, true)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		out, err := a.client.UpdatePlatformIssuer(a.ctx, adminapi.UpdatePlatformIssuerInput{Issuer: pos[0], Body: adminapi.PlatformIssuerUpdate{
			Name: b.Name, Description: b.Description, Issuer: b.Issuer, Audience: b.Audience, JwksUri: b.JwksUri, Algorithms: b.Algorithms,
			SubjectClaim: b.SubjectClaim, TenantClaim: tenantClaim, RequiredClaims: b.RequiredClaims, Enabled: b.Enabled}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-issuer", "delete"}, "deletePlatformIssuer", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("platform-issuer delete"), args, 1)
		if err != nil {
			return err
		}
		out, err := a.client.DeletePlatformIssuer(a.ctx, adminapi.DeletePlatformIssuerInput{Issuer: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-issuer", "tenants"}, "listPlatformIssuerTenants", func(a *app, args []string) error {
		fs := a.flags("platform-issuer tenants")
		var l listFlags
		l.registerSorted(fs)
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListPlatformIssuerTenants(a.ctx, adminapi.ListPlatformIssuerTenantsInput{Issuer: pos[0], Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"platform-issuer", "add-tenant"}, "putPlatformIssuerTenant", func(a *app, args []string) error {
		fs := a.flags("platform-issuer add-tenant")
		claimValue := fs.String("claim-value", "", "the value of the tenant claim that designates the tenant (default: the tenant ID)")
		pos, err := a.begin(fs, args, 2)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(pos[1])
		if err != nil {
			return err
		}
		out, err := a.client.PutPlatformIssuerTenant(a.ctx, adminapi.PutPlatformIssuerTenantInput{Issuer: pos[0], Tenant: tid, Body: adminapi.PlatformIssuerTenantPut{ClaimValue: *claimValue}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-issuer", "remove-tenant"}, "deletePlatformIssuerTenant", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("platform-issuer remove-tenant"), args, 2)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(pos[1])
		if err != nil {
			return err
		}
		out, err := a.client.DeletePlatformIssuerTenant(a.ctx, adminapi.DeletePlatformIssuerTenantInput{Issuer: pos[0], Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},
}
