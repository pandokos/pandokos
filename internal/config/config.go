// Package config loads and validates the static configuration: built-in defaults, one YAML
// file and environment variables, with secrets referenced as files.
package config

import (
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
	Skeleton  Skeleton  `yaml:"skeleton"`
}

// Log configures logging.
type Log struct {
	Level string `yaml:"level"`
}

// Listeners configures the listeners.
type Listeners struct {
	Inference Listener `yaml:"inference"`
}

// Listener configures one HTTP listener.
type Listener struct {
	Address           string   `yaml:"address"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	IdleTimeout       Duration `yaml:"idle_timeout"`
	MaxHeaderBytes    int      `yaml:"max_header_bytes"`
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

// Skeleton is the temporary section of the walking skeleton; later sprints remove it.
type Skeleton struct {
	AccessTokenFile string         `yaml:"access_token_file"`
	OpenAI          SkeletonOpenAI `yaml:"openai"`

	AccessToken secret.Secret `yaml:"-"` // filled by Load
}

// SkeletonOpenAI configures the single provider of the walking skeleton.
type SkeletonOpenAI struct {
	BaseURL    string `yaml:"base_url"`
	APIKeyFile string `yaml:"api_key_file"`

	ParsedBaseURL *url.URL      `yaml:"-"` // filled by Load
	APIKey        secret.Secret `yaml:"-"` // filled by Load
}

// Source says where Load reads from.
type Source struct {
	File    string   // "" means no file
	Environ []string // "NAME=value" pairs, as os.Environ()
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
		Listeners: Listeners{Inference: Listener{
			Address:           "0.0.0.0:8080",
			ReadHeaderTimeout: Duration(10 * time.Second),
			IdleTimeout:       Duration(120 * time.Second),
			MaxHeaderBytes:    65536,
		}},
		Limits: Limits{
			MaxRequestBodyBytes:    10485760,
			RequestBodyReadTimeout: Duration(30 * time.Second),
			MaxRequestDuration:     Duration(10 * time.Minute),
		},
		Upstream: Upstream{ConnectTimeout: Duration(10 * time.Second)},
		Shutdown: Shutdown{DrainTimeout: Duration(30 * time.Second)},
		Skeleton: Skeleton{OpenAI: SkeletonOpenAI{BaseURL: "https://api.openai.com/v1"}},
	}
}
