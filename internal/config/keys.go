package config

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Key describes one configuration key.
type Key struct{ Name, Env, Type, Default, Description string }

type keyDef struct {
	Key
	set func(c *Config, v string) error
}

var (
	errNotDuration = errors.New("not a valid duration such as 30s")
	errNotInteger  = errors.New("not an integer")
)

func setString(f func(c *Config) *string) func(*Config, string) error {
	return func(c *Config, v string) error { *f(c) = v; return nil }
}

func setDuration(f func(c *Config) *Duration) func(*Config, string) error {
	return func(c *Config, v string) error {
		d, err := time.ParseDuration(v)
		if err != nil {
			return errNotDuration
		}
		*f(c) = Duration(d)
		return nil
	}
}

func parseInt(v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, errNotInteger
	}
	return n, nil
}

func envName(key string) string {
	return "PANDOKOS_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

func def(name, typ, dflt, desc string, set func(*Config, string) error) keyDef {
	return keyDef{Key: Key{Name: name, Env: envName(name), Type: typ, Default: dflt, Description: desc}, set: set}
}

var keyTable = []keyDef{
	def("log.level", "debug, info, warn, error", "info", "Minimum level written",
		setString(func(c *Config) *string { return &c.Log.Level })),
	def("listeners.inference.address", "host:port", "0.0.0.0:8080", "Listen address of the inference API",
		setString(func(c *Config) *string { return &c.Listeners.Inference.Address })),
	def("listeners.inference.read_header_timeout", "duration", "10s", "Time allowed to read request headers",
		setDuration(func(c *Config) *Duration { return &c.Listeners.Inference.ReadHeaderTimeout })),
	def("listeners.inference.idle_timeout", "duration", "120s", "Keep-alive idle time",
		setDuration(func(c *Config) *Duration { return &c.Listeners.Inference.IdleTimeout })),
	def("listeners.inference.max_header_bytes", "integer", "65536", "Maximum size of request headers",
		func(c *Config, v string) error {
			n, err := parseInt(v)
			if err != nil {
				return err
			}
			c.Listeners.Inference.MaxHeaderBytes = int(n)
			return nil
		}),
	def("limits.max_request_body_bytes", "integer", "10485760", "Larger bodies get 413 request_too_large",
		func(c *Config, v string) error {
			n, err := parseInt(v)
			if err != nil {
				return err
			}
			c.Limits.MaxRequestBodyBytes = n
			return nil
		}),
	def("limits.request_body_read_timeout", "duration", "30s", "Time allowed to read the request body",
		setDuration(func(c *Config) *Duration { return &c.Limits.RequestBodyReadTimeout })),
	def("limits.max_request_duration", "duration", "10m", "Total time for one proxied request, stream included, measured from the end of the body read",
		setDuration(func(c *Config) *Duration { return &c.Limits.MaxRequestDuration })),
	def("upstream.connect_timeout", "duration", "10s", "TCP connect timeout, and separately TLS handshake timeout, towards the provider",
		setDuration(func(c *Config) *Duration { return &c.Upstream.ConnectTimeout })),
	def("shutdown.drain_timeout", "duration", "30s", "Time in-flight requests get to finish after a termination signal",
		setDuration(func(c *Config) *Duration { return &c.Shutdown.DrainTimeout })),
	def("database.address", "host:port", "", "Address of PostgreSQL, or of the connection pooler in front of it (required)",
		setString(func(c *Config) *string { return &c.Database.Address })),
	def("database.name", "name", "pandokos", "Database name",
		setString(func(c *Config) *string { return &c.Database.Name })),
	def("database.tls.mode", "verify-full, disable", "verify-full", "TLS towards the database: verified (certificate chain and host name) or none",
		setString(func(c *Config) *string { return &c.Database.TLS.Mode })),
	def("database.tls.ca_file", "path", "", "PEM file with the certificate authorities to trust; empty means the system roots",
		setString(func(c *Config) *string { return &c.Database.TLS.CAFile })),
	def("database.connect_timeout", "duration", "5s", "Time allowed to establish one database connection",
		setDuration(func(c *Config) *Duration { return &c.Database.ConnectTimeout })),
	def("database.gateway.user", "name", "pandokos_gateway", "Database user of the gateway role",
		setString(func(c *Config) *string { return &c.Database.Gateway.User })),
	def("database.gateway.password_file", "path", "", "File with that user's password (required by serve)",
		setString(func(c *Config) *string { return &c.Database.Gateway.PasswordFile })),
	def("database.gateway.max_connections", "integer", "10", "Size limit of the gateway pool",
		func(c *Config, v string) error {
			n, err := parseInt(v)
			if err != nil {
				return err
			}
			c.Database.Gateway.MaxConnections = int(n)
			return nil
		}),
	def("database.admin.user", "name", "pandokos_admin", "Database user of the admin role",
		setString(func(c *Config) *string { return &c.Database.Admin.User })),
	def("database.admin.password_file", "path", "", "File with that user's password (required by serve)",
		setString(func(c *Config) *string { return &c.Database.Admin.PasswordFile })),
	def("database.admin.max_connections", "integer", "10", "Size limit of the admin pool",
		func(c *Config, v string) error {
			n, err := parseInt(v)
			if err != nil {
				return err
			}
			c.Database.Admin.MaxConnections = int(n)
			return nil
		}),
	def("database.worker.user", "name", "pandokos_worker", "Database user of the worker role",
		setString(func(c *Config) *string { return &c.Database.Worker.User })),
	def("database.worker.password_file", "path", "", "File with that user's password (required by serve)",
		setString(func(c *Config) *string { return &c.Database.Worker.PasswordFile })),
	def("database.worker.max_connections", "integer", "5", "Size limit of the worker pool",
		func(c *Config, v string) error {
			n, err := parseInt(v)
			if err != nil {
				return err
			}
			c.Database.Worker.MaxConnections = int(n)
			return nil
		}),
	def("database.owner.user", "name", "pandokos_owner", "Database user of pandokos migrate",
		setString(func(c *Config) *string { return &c.Database.Owner.User })),
	def("database.owner.password_file", "path", "", "File with that user's password (required by migrate, never read by serve)",
		setString(func(c *Config) *string { return &c.Database.Owner.PasswordFile })),
	def("skeleton.access_token_file", "path", "", "File with the static token callers must present (required)",
		setString(func(c *Config) *string { return &c.Skeleton.AccessTokenFile })),
	def("skeleton.openai.base_url", "URL", "https://api.openai.com/v1", "Provider base URL, in the form SDKs use (ends with /v1)",
		setString(func(c *Config) *string { return &c.Skeleton.OpenAI.BaseURL })),
	def("skeleton.openai.api_key_file", "path", "", "File with the provider API key (required)",
		setString(func(c *Config) *string { return &c.Skeleton.OpenAI.APIKeyFile })),
}

// Keys returns every configuration key, in a fixed order.
func Keys() []Key {
	out := make([]Key, len(keyTable))
	for i, k := range keyTable {
		out[i] = k.Key
	}
	return out
}
