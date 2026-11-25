// Package declare is declarative configuration through the administration API: Export reads the
// configuration of the platform or of a tenant into a Document, Plan compares a Document with the
// current state by natural key, and Apply carries the plan out (ADR 0066). It reads and writes
// only through the generated client: no secret, key, credential, person or usage figure is in a
// document.
package declare

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Version is the value of the pandokos member of a document.
const Version = "v1"

// The kinds of document.
const (
	KindPlatform = "platform"
	KindTenant   = "tenant"
)

// Target is what a document describes: the platform, or one tenant by ID.
type Target struct {
	Platform bool
	Tenant   string
}

// Document is a declarative document (YAML, decoded with KnownFields). The members of its items are
// those of the create bodies of the administration API, minus what the server sets.
type Document struct {
	Pandokos string `yaml:"pandokos"` // Version
	Kind     string `yaml:"kind"`     // KindPlatform or KindTenant

	// Kind platform.
	Providers      []Provider     `yaml:"providers,omitempty"`
	Models         []Model        `yaml:"models,omitempty"`
	ModelGrants    []ModelGrant   `yaml:"model_grants,omitempty"`
	PriceOverrides []PriceVersion `yaml:"price_overrides,omitempty"`
	PlatformLimits []Limit        `yaml:"platform_limits,omitempty"`
	// Tenants carry what the platform sets on an existing tenant (the external reference); apply
	// never creates a tenant.
	Tenants         []PlatformTenant `yaml:"tenants,omitempty"`
	PlatformIssuers []PlatformIssuer `yaml:"platform_issuers,omitempty"`

	// Kind tenant.
	Settings          *Settings          `yaml:"settings,omitempty"`
	ModelRestriction  *Restriction       `yaml:"model_restriction,omitempty"`
	Roles             []Role             `yaml:"roles,omitempty"`
	ServiceAccounts   []ServiceAccount   `yaml:"service_accounts,omitempty"`
	Limits            []Limit            `yaml:"limits,omitempty"`
	PriceLists        []PriceList        `yaml:"price_lists,omitempty"`
	TokenIssuers      []TokenIssuer      `yaml:"token_issuers,omitempty"`
	SubjectMappings   []SubjectMapping   `yaml:"subject_mappings,omitempty"`
	EventDestinations []EventDestination `yaml:"event_destinations,omitempty"`

	// LeftOut lists what an export could not express (a rule on a subject mapping); the command
	// line writes it as comment lines at the end. It is not part of the YAML mapping.
	LeftOut []string `yaml:"-"`
}

// Provider is a provider with its endpoints by name. Natural key: name.
type Provider struct {
	Name              string     `yaml:"name"`
	APIFamily         string     `yaml:"api_family"`
	DisplayName       string     `yaml:"display_name,omitempty"`
	Description       string     `yaml:"description,omitempty"`
	DatasetProvider   string     `yaml:"dataset_provider,omitempty"`
	StreamUsageOption string     `yaml:"stream_usage_option,omitempty"`
	Endpoints         []Endpoint `yaml:"endpoints,omitempty"`
}

// Endpoint is an endpoint of a provider. Natural key: name within the provider.
type Endpoint struct {
	Name        string `yaml:"name"`
	BaseURL     string `yaml:"base_url"`
	Region      string `yaml:"region,omitempty"`
	Private     bool   `yaml:"private,omitempty"`
	IsDefault   bool   `yaml:"is_default,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// Model is a model of the catalogue. Natural key: provider and name.
type Model struct {
	Provider            string   `yaml:"provider"`
	Name                string   `yaml:"name"`
	UpstreamID          string   `yaml:"upstream_id,omitempty"`
	DisplayName         string   `yaml:"display_name,omitempty"`
	Description         string   `yaml:"description,omitempty"`
	Status              string   `yaml:"status,omitempty"`
	Availability        string   `yaml:"availability,omitempty"`
	Capabilities        []string `yaml:"capabilities,omitempty"`
	ContextWindow       int      `yaml:"context_window,omitempty"`
	MaxOutputTokens     int      `yaml:"max_output_tokens,omitempty"`
	DefaultOutputTokens int      `yaml:"default_output_tokens,omitempty"`
}

// ModelGrant grants a model to a tenant. Natural key: provider, model and the tenant's name.
type ModelGrant struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Tenant   string `yaml:"tenant"`
}

// PriceVersion is a price version of a model (an override) or of a tenant's price list. Natural
// key: provider, model and valid_from; versions are only added.
type PriceVersion struct {
	Provider  string `yaml:"provider,omitempty"` // platform price overrides; in a price list the list's own
	Model     string `yaml:"model"`
	ValidFrom string `yaml:"valid_from"` // RFC 3339
	Note      string `yaml:"note,omitempty"`
	Currency  string `yaml:"currency,omitempty"`
	Rates     []Rate `yaml:"rates"`
}

// Rate is a price per million units of a category above a threshold of input tokens; the rate is a
// decimal string and is never converted.
type Rate struct {
	Category  string `yaml:"category"`
	Threshold int    `yaml:"threshold"`
	Rate      string `yaml:"rate"`
}

// Limit is a limit rule, of a tenant or a platform ceiling. Natural key: subject, provider, model,
// metric and currency. Subject is "tenant", "service_account: <name>" or "key: <key ID>" in a tenant
// document, or the mapping form (Mapping, written as subject: {mapping: {scope, issuer, subject}}); a
// platform ceiling names a tenant by name or "default".
type Limit struct {
	Subject    string          `yaml:"subject,omitempty"`
	Mapping    *MappingSubject `yaml:"-"` // the subject of the mapping form; then Subject is empty
	Tenant     string          `yaml:"tenant,omitempty"`
	Provider   string          `yaml:"provider,omitempty"`
	Model      string          `yaml:"model,omitempty"`
	Metric     string          `yaml:"metric"`
	Limit      int             `yaml:"limit,omitempty"`
	Amount     string          `yaml:"amount,omitempty"`
	Currency   string          `yaml:"currency,omitempty"`
	Enforce    bool            `yaml:"enforce"`
	Thresholds []int           `yaml:"thresholds,omitempty"`
}

// MappingSubject names a subject mapping of the tenant by the registration it belongs to and the
// subject of the token: the portable form of a rule on a subject mapping.
type MappingSubject struct {
	Scope   string `yaml:"scope"`  // IssuerScopeTenant or IssuerScopePlatform
	Issuer  string `yaml:"issuer"` // the issuer registration's name
	Subject string `yaml:"subject"`
}

// The scopes of the issuer of a subject mapping.
const (
	IssuerScopeTenant   = "tenant"
	IssuerScopePlatform = "platform"
)

// PlatformTenant is a tenant of the installation as a platform document sets it. Natural key: name.
type PlatformTenant struct {
	Name              string `yaml:"name"`
	ExternalReference string `yaml:"external_reference,omitempty"`
}

// For the members that are pointers (jwks_uri, required_claims, enabled, event_types), absent means "leave
// the stored value alone" on an update and the safe default on a create (disabled, empty), and an explicit empty
// value (required_claims: {}, event_types: []) clears. An export always writes them.

// PlatformIssuer is a platform-level token issuer with the tenants it lists. Natural key: name.
type PlatformIssuer struct {
	Name           string             `yaml:"name"`
	Description    string             `yaml:"description,omitempty"`
	Issuer         string             `yaml:"issuer"`
	Audience       string             `yaml:"audience"`
	JWKSURI        *string            `yaml:"jwks_uri,omitempty"`
	Algorithms     []string           `yaml:"algorithms,omitempty"`
	SubjectClaim   string             `yaml:"subject_claim,omitempty"`
	TenantClaim    string             `yaml:"tenant_claim"`
	RequiredClaims *map[string]string `yaml:"required_claims,omitempty"`
	Enabled        *bool              `yaml:"enabled,omitempty"`
	Tenants        []IssuerTenant     `yaml:"tenants,omitempty"`
}

// IssuerTenant lists a tenant, by name, on a platform-level issuer. Natural key: tenant.
// ClaimValue is absent when it is the tenant's ID (so that a document is portable).
type IssuerTenant struct {
	Tenant     string `yaml:"tenant"`
	ClaimValue string `yaml:"claim_value,omitempty"`
}

// TokenIssuer is a token issuer of a tenant. Natural key: name.
type TokenIssuer struct {
	Name           string             `yaml:"name"`
	Description    string             `yaml:"description,omitempty"`
	Issuer         string             `yaml:"issuer"`
	Audience       string             `yaml:"audience"`
	JWKSURI        *string            `yaml:"jwks_uri,omitempty"`
	Algorithms     []string           `yaml:"algorithms,omitempty"`
	SubjectClaim   string             `yaml:"subject_claim,omitempty"`
	RequiredClaims *map[string]string `yaml:"required_claims,omitempty"`
	Enabled        *bool              `yaml:"enabled,omitempty"`
}

// SubjectMapping maps the subject of a token of an issuer to a service account of the tenant.
// Natural key: the issuer (scope and name) and the subject.
type SubjectMapping struct {
	Issuer         MappingIssuer `yaml:"issuer"`
	Subject        string        `yaml:"subject"`
	ServiceAccount string        `yaml:"service_account"` // a name
	Description    string        `yaml:"description,omitempty"`
}

// MappingIssuer is the registration a subject mapping belongs to.
type MappingIssuer struct {
	Scope string `yaml:"scope"` // IssuerScopeTenant or IssuerScopePlatform
	Name  string `yaml:"name"`
}

// EventDestination is an event destination of a tenant, without its signing secret, cursor or
// delivery state. Natural key: name.
type EventDestination struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`
	URL         string    `yaml:"url"`
	EventTypes  *[]string `yaml:"event_types,omitempty"`
	Enabled     *bool     `yaml:"enabled,omitempty"`
}

// Options are what the caller gives Diff and Apply beyond the document.
type Options struct {
	// SecretSink receives the signing secret of an event destination that apply creates, under the
	// name "event-destination-<destination>", once; the secret is in no document, plan line or
	// error. With no sink an absent destination is not created: it is skipped, and the plan line
	// says so.
	SecretSink func(name string, secret []byte) error
}

// Settings are the settings of a tenant.
type Settings struct {
	TimeZone       string `yaml:"time_zone,omitempty"`
	ActiveCurrency string `yaml:"active_currency,omitempty"`
	// MetadataKeys is the caller metadata keys the tenant accepts. Absent leaves the policy alone;
	// an explicit empty list is the empty policy (no key accepted), which is not the same as no policy.
	MetadataKeys *[]string `yaml:"metadata_keys,omitempty"`
}

// Restriction is the model restriction of a tenant, by model reference.
type Restriction struct {
	Models []string `yaml:"models"`
}

// Role is a custom role of a tenant. Natural key: name.
type Role struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Permissions []string `yaml:"permissions"`
}

// ServiceAccount is a service account of a tenant with its roles. Natural key: name.
type ServiceAccount struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	ValidFrom   string   `yaml:"valid_from,omitempty"`
	ValidUntil  string   `yaml:"valid_until,omitempty"`
	Roles       []string `yaml:"roles,omitempty"`
}

// PriceList is the price list of a tenant in one currency. Natural key: currency.
type PriceList struct {
	Currency string         `yaml:"currency"`
	Versions []PriceVersion `yaml:"versions"`
}

// Change is one line of a plan.
type Change struct {
	Action   string // create, update, conflict, skipped, not in document
	Resource string // the resource type and its natural key, as the output line writes it
	Detail   string

	run   step // the call that carries a create or an update out; nil for the other actions
	alone bool // a skipped line that counts as left alone (a destination that needs --secrets-out)
}

// Line is the line the command prints for the change.
func (c Change) Line() string {
	if c.Detail == "" {
		return c.Action + " " + c.Resource
	}
	return c.Action + " " + c.Resource + ": " + c.Detail
}

// Plan is what Apply would do to bring the state in line with a document.
type Plan struct {
	Target  Target
	Changes []Change
	refs    *refs
	opts    Options
}

// Result is what Apply did, or with a dry run what it would do.
type Result struct {
	Changes, Conflicts, LeftAlone int
	Lines                         []string
}

var limitMembers = map[string]bool{"subject": true, "tenant": true, "provider": true, "model": true, "metric": true,
	"limit": true, "amount": true, "currency": true, "enforce": true, "thresholds": true}

// UnmarshalYAML decodes a limit whose subject is a string or the mapping form. The members are
// checked here because a node decoded by hand is not strict.
func (l *Limit) UnmarshalYAML(n *yaml.Node) error {
	type plain Limit
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a limit is a mapping", n.Line)
	}
	rest := *n
	rest.Content = nil
	var mapping *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !limitMembers[k.Value] {
			return fmt.Errorf("line %d: field %s not found in type declare.Limit", k.Line, k.Value)
		}
		if k.Value == "subject" && v.Kind == yaml.MappingNode {
			mapping = v
			continue
		}
		rest.Content = append(rest.Content, k, v)
	}
	var p plain
	if err := rest.Decode(&p); err != nil {
		return err
	}
	if mapping != nil {
		if len(mapping.Content) != 2 || mapping.Content[0].Value != "mapping" || mapping.Content[1].Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: the subject of a limit is a string or mapping: {scope, issuer, subject}", mapping.Line)
		}
		inner := mapping.Content[1]
		for i := 0; i+1 < len(inner.Content); i += 2 {
			if k := inner.Content[i].Value; k != "scope" && k != "issuer" && k != "subject" {
				return fmt.Errorf("line %d: field %s not found in type declare.MappingSubject", inner.Content[i].Line, k)
			}
		}
		var m MappingSubject
		if err := inner.Decode(&m); err != nil {
			return err
		}
		p.Mapping = &m
	}
	*l = Limit(p)
	return nil
}

// MarshalYAML writes a limit with its members in order, the mapping form as subject: {mapping: …}.
func (l Limit) MarshalYAML() (any, error) {
	type out struct {
		Subject    any    `yaml:"subject,omitempty"`
		Tenant     string `yaml:"tenant,omitempty"`
		Provider   string `yaml:"provider,omitempty"`
		Model      string `yaml:"model,omitempty"`
		Metric     string `yaml:"metric"`
		Limit      int    `yaml:"limit,omitempty"`
		Amount     string `yaml:"amount,omitempty"`
		Currency   string `yaml:"currency,omitempty"`
		Enforce    bool   `yaml:"enforce"`
		Thresholds []int  `yaml:"thresholds,omitempty"`
	}
	o := out{Tenant: l.Tenant, Provider: l.Provider, Model: l.Model, Metric: l.Metric, Limit: l.Limit, Amount: l.Amount,
		Currency: l.Currency, Enforce: l.Enforce, Thresholds: l.Thresholds}
	switch {
	case l.Mapping != nil:
		o.Subject = map[string]any{"mapping": l.Mapping}
	case l.Subject != "":
		o.Subject = l.Subject
	}
	return o, nil
}
