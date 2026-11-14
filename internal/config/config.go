// Package config loads and validates the static configuration: built-in defaults, one YAML
// file and environment variables, with secrets referenced as files.
package config

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/internal/secret"
)

// Duration is a time.Duration written as a string in Go syntax ("30s", "10m") in YAML.
type Duration time.Duration

// UnmarshalYAML accepts only a string scalar parsed by time.ParseDuration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return invalidValue(node)
	}
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return invalidValue(node)
	}
	*d = Duration(v)
	return nil
}

// invalidValue reports a bad value without quoting it: a value typed in the wrong place may be a secret.
func invalidValue(node *yaml.Node) error {
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: invalid value", node.Line)}}
}

// Config is the whole static configuration.
type Config struct {
	Log       Log       `yaml:"log"`
	Listeners Listeners `yaml:"listeners"`
	Limits    Limits    `yaml:"limits"`
	Upstream  Upstream  `yaml:"upstream"`
	Shutdown  Shutdown  `yaml:"shutdown"`
	Cache     Cache     `yaml:"cache"`
	Database  Database  `yaml:"database"`
	Vault     Vault     `yaml:"vault"`
	Pricing   Pricing   `yaml:"pricing"`
	Skeleton  Skeleton  `yaml:"skeleton"`
}

// Log configures logging.
type Log struct {
	Level string `yaml:"level"`
}

// Listeners configures the listeners.
type Listeners struct {
	Inference Listener      `yaml:"inference"`
	Admin     AdminListener `yaml:"admin"`
}

// Listener configures one HTTP listener.
type Listener struct {
	Address           string      `yaml:"address"`
	ReadHeaderTimeout Duration    `yaml:"read_header_timeout"`
	IdleTimeout       Duration    `yaml:"idle_timeout"`
	MaxHeaderBytes    int         `yaml:"max_header_bytes"`
	TLS               ListenerTLS `yaml:"tls"`
}

// AdminListener configures the listener of the administration API.
type AdminListener struct {
	Address           string      `yaml:"address"`
	ReadHeaderTimeout Duration    `yaml:"read_header_timeout"`
	IdleTimeout       Duration    `yaml:"idle_timeout"`
	MaxHeaderBytes    int         `yaml:"max_header_bytes"`
	TLS               ListenerTLS `yaml:"tls"`
	AllowPlaintext    bool        `yaml:"allow_plaintext"`
}

// ListenerTLS configures native TLS on a listener.
type ListenerTLS struct {
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
	MinVersion string `yaml:"min_version"`

	Certificate *tls.Certificate `yaml:"-"` // filled by Load when the purpose is serve and both files are set
}

// Cache configures the caches of the gateway.
type Cache struct {
	PrincipalTTL  Duration `yaml:"principal_ttl"`
	CredentialTTL Duration `yaml:"credential_ttl"`
	DataKeyTTL    Duration `yaml:"data_key_ttl"`
	CatalogTTL    Duration `yaml:"catalog_ttl"`
}

// Pricing configures the price list: the import of the embedded snapshot at start and the
// optional periodic download. Only serve reads and validates the download settings.
type Pricing struct {
	ImportAtStart bool           `yaml:"import_at_start"`
	Refresh       PricingRefresh `yaml:"refresh"`
}

// PricingRefresh configures the periodic download of the price dataset.
type PricingRefresh struct {
	Enabled  bool     `yaml:"enabled"`
	URL      string   `yaml:"url"`
	Interval Duration `yaml:"interval"`

	ParsedURL *url.URL `yaml:"-"` // filled by Load when the purpose is serve
}

// DefaultPriceURL is the default address of the price dataset (the v2 data file of genai-prices).
const DefaultPriceURL = "https://raw.githubusercontent.com/pydantic/genai-prices/main/prices/new_data/v2/data.json"

// Vault configures the credential vault.
type Vault struct {
	KEK VaultKEK `yaml:"kek"`
}

// VaultKEK configures the key-encryption key.
type VaultKEK struct {
	Provider string        `yaml:"provider"`
	Local    VaultKEKLocal `yaml:"local"`
}

// VaultKEKLocal configures the local key provider: a key file.
type VaultKEKLocal struct {
	KeyFile string `yaml:"key_file"`

	Key secret.Secret `yaml:"-"` // filled by Load when the purpose is serve
}

// Limits configures request limits.
type Limits struct {
	MaxRequestBodyBytes    int64    `yaml:"max_request_body_bytes"`
	RequestBodyReadTimeout Duration `yaml:"request_body_read_timeout"`
	MaxRequestDuration     Duration `yaml:"max_request_duration"`
}

// Upstream configures the connection to providers.
type Upstream struct {
	ConnectTimeout Duration `yaml:"connect_timeout"`
}

// Shutdown configures graceful shutdown.
type Shutdown struct {
	DrainTimeout Duration `yaml:"drain_timeout"`
}

// Database configures the connection to PostgreSQL: one address, one database, and one database
// user per process role, each with its password in a file.
type Database struct {
	Address        string        `yaml:"address"`
	DirectAddress  string        `yaml:"direct_address"`
	Name           string        `yaml:"name"`
	TLS            DatabaseTLS   `yaml:"tls"`
	ConnectTimeout Duration      `yaml:"connect_timeout"`
	Gateway        DatabaseRole  `yaml:"gateway"`
	Admin          DatabaseRole  `yaml:"admin"`
	Worker         DatabaseRole  `yaml:"worker"`
	Owner          DatabaseOwner `yaml:"owner"`

	Host string `yaml:"-"` // filled by Load from Address
	Port int    `yaml:"-"` // filled by Load from Address

	DirectHost string `yaml:"-"` // filled by Load; equal to Host when DirectAddress is empty
	DirectPort int    `yaml:"-"`
}

// DatabaseTLS configures TLS towards the database.
type DatabaseTLS struct {
	Mode   string `yaml:"mode"`
	CAFile string `yaml:"ca_file"`
}

// DatabaseRole configures the database user and pool of one runtime role.
type DatabaseRole struct {
	User           string `yaml:"user"`
	PasswordFile   string `yaml:"password_file"`
	MaxConnections int    `yaml:"max_connections"`

	Password secret.Secret `yaml:"-"` // filled by Load when the purpose is serve
}

// DatabaseOwner configures the database user of the migrate command.
type DatabaseOwner struct {
	User         string `yaml:"user"`
	PasswordFile string `yaml:"password_file"`

	Password secret.Secret `yaml:"-"` // filled by Load when the purpose is migrate
}

// Skeleton is the temporary section of the walking skeleton; later sprints remove it.
type Skeleton struct {
	OpenAI SkeletonOpenAI `yaml:"openai"`
}

// SkeletonOpenAI configures the single provider of the walking skeleton.
type SkeletonOpenAI struct {
	BaseURL    string `yaml:"base_url"`
	APIKeyFile string `yaml:"api_key_file"`

	ParsedBaseURL *url.URL      `yaml:"-"` // filled by Load
	APIKey        secret.Secret `yaml:"-"` // filled by Load
}

// Purpose says which command the configuration is loaded for. It decides which secrets are
// required and read. The zero value is PurposeServe.
type Purpose int

// The purposes of a configuration.
const (
	PurposeServe   Purpose = iota // serve and config check: runtime secrets
	PurposeMigrate                // migrate and migrate status: the owner's password only
	PurposeInit                   // init and config check --for init: the admin password only
)

// Source says where Load reads from.
type Source struct {
	File    string   // "" means no file
	Environ []string // "NAME=value" pairs, as os.Environ()
	Purpose Purpose  // which secrets are required and read
}

// Warning is a condition that does not prevent start.
type Warning struct{ Key, Message string }

// Problem is one violation found by Load.
type Problem struct{ Key, Message string }

// ValidationError lists every problem found by Load.
type ValidationError struct{ Problems []Problem }

// Error joins "key: message" with "; ".
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Key + ": " + p.Message
	}
	return strings.Join(parts, "; ")
}

// Defaults returns the built-in defaults.
func Defaults() *Config {
	return &Config{
		Log: Log{Level: "info"},
		Listeners: Listeners{
			Inference: Listener{
				Address:           "0.0.0.0:8080",
				ReadHeaderTimeout: Duration(10 * time.Second),
				IdleTimeout:       Duration(120 * time.Second),
				MaxHeaderBytes:    65536,
				TLS:               ListenerTLS{MinVersion: "TLS1.2"},
			},
			Admin: AdminListener{
				Address:           "127.0.0.1:8081",
				ReadHeaderTimeout: Duration(10 * time.Second),
				IdleTimeout:       Duration(120 * time.Second),
				MaxHeaderBytes:    65536,
				TLS:               ListenerTLS{MinVersion: "TLS1.2"},
			},
		},
		Limits: Limits{
			MaxRequestBodyBytes:    10485760,
			RequestBodyReadTimeout: Duration(30 * time.Second),
			MaxRequestDuration:     Duration(10 * time.Minute),
		},
		Upstream: Upstream{ConnectTimeout: Duration(10 * time.Second)},
		Shutdown: Shutdown{DrainTimeout: Duration(30 * time.Second)},
		Cache: Cache{
			PrincipalTTL:  Duration(30 * time.Second),
			CredentialTTL: Duration(30 * time.Second),
			DataKeyTTL:    Duration(5 * time.Minute),
			CatalogTTL:    Duration(30 * time.Second),
		},
		Pricing: Pricing{
			ImportAtStart: true,
			Refresh:       PricingRefresh{URL: DefaultPriceURL, Interval: Duration(24 * time.Hour)},
		},
		Vault: Vault{KEK: VaultKEK{Provider: "local"}},
		Database: Database{
			Name:           "pandokos",
			TLS:            DatabaseTLS{Mode: "verify-full"},
			ConnectTimeout: Duration(5 * time.Second),
			Gateway:        DatabaseRole{User: "pandokos_gateway", MaxConnections: 10},
			Admin:          DatabaseRole{User: "pandokos_admin", MaxConnections: 10},
			Worker:         DatabaseRole{User: "pandokos_worker", MaxConnections: 5},
			Owner:          DatabaseOwner{User: "pandokos_owner"},
		},
		Skeleton: Skeleton{OpenAI: SkeletonOpenAI{BaseURL: "https://api.openai.com/v1"}},
	}
}
