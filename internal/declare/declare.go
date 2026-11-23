// Package declare is declarative configuration through the administration API: Export reads the
// configuration of the platform or of a tenant into a Document, Plan compares a Document with the
// current state by natural key, and Apply carries the plan out (ADR 0066). It reads and writes
// only through the generated client: no secret, key, credential, person or usage figure is in a
// document.
package declare

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

	// Kind tenant.
	Settings         *Settings        `yaml:"settings,omitempty"`
	ModelRestriction *Restriction     `yaml:"model_restriction,omitempty"`
	Roles            []Role           `yaml:"roles,omitempty"`
	ServiceAccounts  []ServiceAccount `yaml:"service_accounts,omitempty"`
	Limits           []Limit          `yaml:"limits,omitempty"`
	PriceLists       []PriceList      `yaml:"price_lists,omitempty"`

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
// document; a platform ceiling names a tenant by name or "default".
type Limit struct {
	Subject    string `yaml:"subject,omitempty"`
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

// Settings are the settings of a tenant.
type Settings struct {
	TimeZone       string   `yaml:"time_zone,omitempty"`
	ActiveCurrency string   `yaml:"active_currency,omitempty"`
	MetadataKeys   []string `yaml:"metadata_keys,omitempty"`
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

	run step // the call that carries a create or an update out; nil for the other actions
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
}

// Result is what Apply did, or with a dry run what it would do.
type Result struct {
	Changes, Conflicts, LeftAlone int
	Lines                         []string
}
