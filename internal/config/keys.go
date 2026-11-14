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
	set       func(c *Config, v string) error
	serveOnly bool // read from the environment only when the purpose is serve
}

var (
	errNotDuration = errors.New("not a valid duration such as 30s")
	errNotInteger  = errors.New("not an integer")
	errNotBoolean  = errors.New("not a boolean (true or false)")
	errNotDecimal  = errors.New("not a decimal number")
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

func setBool(f func(c *Config) *bool) func(*Config, string) error {
	return func(c *Config, v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errNotBoolean
		}
		*f(c) = b
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

func setInt(f func(c *Config) *int) func(*Config, string) error {
	return func(c *Config, v string) error {
		n, err := parseInt(v)
		if err != nil {
			return err
		}
		*f(c) = int(n)
		return nil
	}
}

func setDecimal(f func(c *Config) *float64) func(*Config, string) error {
	return func(c *Config, v string) error {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return errNotDecimal
		}
		*f(c) = x
		return nil
	}
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
	def("listeners.inference.tls.cert_file", "path", "", "PEM file with the server certificate and any intermediate certificates; with key_file, turns TLS on",
		setString(func(c *Config) *string { return &c.Listeners.Inference.TLS.CertFile })),
	def("listeners.inference.tls.key_file", "path", "", "PEM file with the private key of the certificate",
		setString(func(c *Config) *string { return &c.Listeners.Inference.TLS.KeyFile })),
	def("listeners.inference.tls.min_version", "TLS1.2, TLS1.3", "TLS1.2", "Lowest protocol version accepted",
		setString(func(c *Config) *string { return &c.Listeners.Inference.TLS.MinVersion })),
	def("listeners.admin.address", "host:port", "127.0.0.1:8081", "Listen address of the administration API",
		setString(func(c *Config) *string { return &c.Listeners.Admin.Address })),
	def("listeners.admin.read_header_timeout", "duration", "10s", "Time allowed to read request headers",
		setDuration(func(c *Config) *Duration { return &c.Listeners.Admin.ReadHeaderTimeout })),
	def("listeners.admin.idle_timeout", "duration", "120s", "Keep-alive idle time",
		setDuration(func(c *Config) *Duration { return &c.Listeners.Admin.IdleTimeout })),
	def("listeners.admin.max_header_bytes", "integer", "65536", "Maximum size of request headers",
		func(c *Config, v string) error {
			n, err := parseInt(v)
			if err != nil {
				return err
			}
			c.Listeners.Admin.MaxHeaderBytes = int(n)
			return nil
		}),
	def("listeners.admin.tls.cert_file", "path", "", "PEM file with the server certificate and any intermediate certificates; with key_file, turns TLS on",
		setString(func(c *Config) *string { return &c.Listeners.Admin.TLS.CertFile })),
	def("listeners.admin.tls.key_file", "path", "", "PEM file with the private key of the certificate",
		setString(func(c *Config) *string { return &c.Listeners.Admin.TLS.KeyFile })),
	def("listeners.admin.tls.min_version", "TLS1.2, TLS1.3", "TLS1.2", "Lowest protocol version accepted",
		setString(func(c *Config) *string { return &c.Listeners.Admin.TLS.MinVersion })),
	def("listeners.admin.allow_plaintext", "boolean", "false", "Acknowledges that the administration API is served without TLS on an address that is not loopback because TLS ends in front of it",
		setBool(func(c *Config) *bool { return &c.Listeners.Admin.AllowPlaintext })),
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
	def("upstream.tls.ca_file", "path", "", "PEM file with certificate authorities added to the system roots for every outbound TLS connection (providers, validation, price download)",
		setString(func(c *Config) *string { return &c.Upstream.TLS.CAFile })),
	def("upstream.proxy.url", "URL", "", "Proxy for provider calls to public endpoints, credential validation and the price download; http or https, no user information",
		setString(func(c *Config) *string { return &c.Upstream.Proxy.URL })),
	def("shutdown.drain_timeout", "duration", "30s", "Time in-flight requests get to finish after a termination signal",
		setDuration(func(c *Config) *Duration { return &c.Shutdown.DrainTimeout })),
	def("cache.principal_ttl", "duration", "30s", "Lifetime of a cached principal in the gateway: the upper bound on the time a revocation or a suspension takes to reach the inference listener",
		setDuration(func(c *Config) *Duration { return &c.Cache.PrincipalTTL })),
	def("cache.credential_ttl", "duration", "30s", "Lifetime of a cached credential in the gateway: the upper bound on the time a disable, revocation or replacement takes to reach the inference listener when notifications are not delivered; 1s to 60s",
		setDuration(func(c *Config) *Duration { return &c.Cache.CredentialTTL })),
	def("cache.data_key_ttl", "duration", "5m", "Lifetime of an unwrapped data key in the gateway; 1s to 15m",
		setDuration(func(c *Config) *Duration { return &c.Cache.DataKeyTTL })),
	def("cache.catalog_ttl", "duration", "30s", "Lifetime of the cached catalogue in the gateway: the upper bound on the time a catalogue or price change takes to reach the inference listener when notifications are not delivered; 1s to 5m",
		setDuration(func(c *Config) *Duration { return &c.Cache.CatalogTTL })),
	def("database.address", "host:port", "", "Address of PostgreSQL, or of the connection pooler in front of it (required)",
		setString(func(c *Config) *string { return &c.Database.Address })),
	def("database.direct_address", "host:port", "", "Address of PostgreSQL itself, used only by the notification listener; set it when database.address is a pooler in transaction mode",
		setString(func(c *Config) *string { return &c.Database.DirectAddress })),
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
	def("vault.kek.provider", "local", "local", "Key-encryption key provider; only local",
		setString(func(c *Config) *string { return &c.Vault.KEK.Provider })),
	def("vault.kek.local.key_file", "path", "", "File with the key-encryption key: 32 random bytes in standard base64 (required by serve; create it with pandokos kek generate)",
		setString(func(c *Config) *string { return &c.Vault.KEK.Local.KeyFile })),
	def("pricing.import_at_start", "boolean", "true", "Import the embedded price snapshot when serve starts, unless a newer or identical import exists",
		setBool(func(c *Config) *bool { return &c.Pricing.ImportAtStart })),
	def("pricing.refresh.enabled", "boolean", "false", "Download the price dataset periodically (an outbound request to pricing.refresh.url)",
		setBool(func(c *Config) *bool { return &c.Pricing.Refresh.Enabled })),
	def("pricing.refresh.url", "URL", DefaultPriceURL, "Address of the price dataset; https only, without user information",
		setString(func(c *Config) *string { return &c.Pricing.Refresh.URL })),
	def("pricing.refresh.interval", "duration", "24h", "Time between downloads; 1h to 720h",
		setDuration(func(c *Config) *Duration { return &c.Pricing.Refresh.Interval })),
	serveDef("ledger.flush_interval", "duration", "50ms", "Longest time a usage record waits in memory before it is written; 1ms to 10s",
		setDuration(func(c *Config) *Duration { return &c.Ledger.FlushInterval })),
	serveDef("ledger.batch_size", "integer", "500", "Most usage records written in one transaction; 1 to 5000",
		setInt(func(c *Config) *int { return &c.Ledger.BatchSize })),
	serveDef("ledger.buffer_size", "integer", "10000", "Usage records held in memory, admitted requests included; when it is full the gateway answers 503; 100 to 1000000, at least ledger.batch_size",
		setInt(func(c *Config) *int { return &c.Ledger.BufferSize })),
	serveDef("ledger.shutdown_flush_timeout", "duration", "10s", "Time allowed to write the records still in memory when the process stops; 1s to 5m",
		setDuration(func(c *Config) *Duration { return &c.Ledger.ShutdownFlushTimeout })),
	serveDef("telemetry.otlp.endpoint", "URL", "", "Base URL of the OTLP/HTTP collector, http or https, without user information, query or fragment; /v1/traces is appended; empty means no export",
		setString(func(c *Config) *string { return &c.Telemetry.OTLP.Endpoint })),
	serveDef("telemetry.otlp.protocol", "http/protobuf", "http/protobuf", "Export protocol; only http/protobuf",
		setString(func(c *Config) *string { return &c.Telemetry.OTLP.Protocol })),
	serveDef("telemetry.otlp.headers_file", "path", "", "File with the headers sent to the collector, one Name: value per line; the values are secrets",
		setString(func(c *Config) *string { return &c.Telemetry.OTLP.HeadersFile })),
	serveDef("telemetry.otlp.timeout", "duration", "10s", "Time allowed for one export; 1s to 60s",
		setDuration(func(c *Config) *Duration { return &c.Telemetry.OTLP.Timeout })),
	serveDef("telemetry.traces.sample_ratio", "decimal", "1", "Share of the traces started here that is sampled, 0 to 1; a caller's sampled flag is followed",
		setDecimal(func(c *Config) *float64 { return &c.Telemetry.Traces.SampleRatio })),
	serveDef("telemetry.traces.propagate", "boolean", "false", "Send the trace context to the provider in a traceparent header",
		setBool(func(c *Config) *bool { return &c.Telemetry.Traces.Propagate })),
	serveDef("telemetry.resource.attributes", "list", "", "Resource attributes of the traces: key=value pairs separated by commas, at most 32",
		setString(func(c *Config) *string { return &c.Telemetry.Resource.Attributes })),
}

// serveDef is def for a key that only serve reads.
func serveDef(name, typ, dflt, desc string, set func(*Config, string) error) keyDef {
	k := def(name, typ, dflt, desc, set)
	k.serveOnly = true
	return k
}

// Keys returns every configuration key, in a fixed order.
func Keys() []Key {
	out := make([]Key, len(keyTable))
	for i, k := range keyTable {
		out[i] = k.Key
	}
	return out
}
