package config

import (
	"crypto/x509"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

const minTokenBytes = 32

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func validate(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level", "must be one of debug, info, warn, error")
	}

	if msg := checkAddress(c.Listeners.Inference.Address); msg != "" {
		add("listeners.inference.address", msg)
	}

	for _, d := range []struct {
		key string
		v   Duration
	}{
		{"listeners.inference.read_header_timeout", c.Listeners.Inference.ReadHeaderTimeout},
		{"listeners.inference.idle_timeout", c.Listeners.Inference.IdleTimeout},
		{"limits.request_body_read_timeout", c.Limits.RequestBodyReadTimeout},
		{"limits.max_request_duration", c.Limits.MaxRequestDuration},
		{"upstream.connect_timeout", c.Upstream.ConnectTimeout},
		{"shutdown.drain_timeout", c.Shutdown.DrainTimeout},
		{"database.connect_timeout", c.Database.ConnectTimeout},
	} {
		if time.Duration(d.v) <= 0 {
			add(d.key, "must be greater than zero")
		}
	}

	if n := c.Listeners.Inference.MaxHeaderBytes; n < 4096 || n > 1048576 {
		add("listeners.inference.max_header_bytes", "must be between 4096 and 1048576")
	}
	if n := c.Limits.MaxRequestBodyBytes; n < 1 || n > 104857600 {
		add("limits.max_request_body_bytes", "must be between 1 and 104857600")
	}

	ps = append(ps, validateDatabase(c)...)

	u, msg := parseBaseURL(c.Skeleton.OpenAI.BaseURL)
	if msg != "" {
		add("skeleton.openai.base_url", msg)
	} else {
		c.Skeleton.OpenAI.ParsedBaseURL = u
	}
	return ps
}

const maxCAFileBytes = 1048576

var databaseName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`)

// validateDatabase applies the rules of sprint 1.2, section 5.2, in the order of the key table.
func validateDatabase(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	d := &c.Database

	if msg := checkDatabaseAddress(d); msg != "" {
		add("database.address", msg)
	}
	const nameMsg = "must be 1 to 63 characters: letters, digits, underscore, dot, hyphen"
	if !databaseName.MatchString(d.Name) {
		add("database.name", nameMsg)
	}
	switch d.TLS.Mode {
	case "verify-full", "disable":
	default:
		add("database.tls.mode", "must be verify-full or disable")
	}
	switch {
	case d.TLS.CAFile == "":
	case d.TLS.Mode != "verify-full":
		add("database.tls.ca_file", "is only used when database.tls.mode is verify-full")
	default:
		if msg := checkCAFile(d.TLS.CAFile); msg != "" {
			add("database.tls.ca_file", msg)
		}
	}
	users := []struct{ key, user string }{
		{"database.gateway.user", d.Gateway.User},
		{"database.admin.user", d.Admin.User},
		{"database.worker.user", d.Worker.User},
		{"database.owner.user", d.Owner.User},
	}
	for i, u := range users {
		if !databaseName.MatchString(u.user) {
			add(u.key, nameMsg)
		}
		for _, earlier := range users[:i] {
			if u.user == earlier.user {
				add(u.key, "must differ from "+earlier.key+": each role needs its own database user")
			}
		}
	}
	for _, m := range []struct {
		key string
		n   int
	}{
		{"database.gateway.max_connections", d.Gateway.MaxConnections},
		{"database.admin.max_connections", d.Admin.MaxConnections},
		{"database.worker.max_connections", d.Worker.MaxConnections},
	} {
		if m.n < 1 || m.n > 1000 {
			add(m.key, "must be between 1 and 1000")
		}
	}
	return ps
}

// checkDatabaseAddress validates database.address and fills Host and Port.
func checkDatabaseAddress(d *Database) string {
	if d.Address == "" {
		return "must be set"
	}
	host, port, err := net.SplitHostPort(d.Address)
	if err != nil {
		return "must be host:port"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "the port must be an integer between 1 and 65535"
	}
	if host == "" || (net.ParseIP(host) == nil && !hostName.MatchString(host)) {
		return "the host must be an IP address or a host name"
	}
	d.Host, d.Port = host, n
	return ""
}

func checkCAFile(path string) string {
	f, err := os.Open(path) //nolint:gosec // the path is operator configuration
	if err != nil {
		return "cannot read the file: " + err.Error()
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCAFileBytes+1))
	if err != nil {
		return "cannot read the file: " + err.Error()
	}
	if len(b) > maxCAFileBytes {
		return "the file is larger than 1048576 bytes"
	}
	if !x509.NewCertPool().AppendCertsFromPEM(b) {
		return "the file contains no PEM certificate"
	}
	return ""
}

// loadDatabaseSecrets reads the password files the purpose needs: the three runtime passwords
// for serve, the owner's for migrate. A file that the purpose does not need is not read and
// need not exist.
func loadDatabaseSecrets(c *Config, migrate bool) []Problem {
	var ps []Problem
	read := func(key, path string) secret.Secret {
		if path == "" {
			ps = append(ps, Problem{Key: key, Message: "must be set"})
			return secret.Secret{}
		}
		s, err := secret.ReadFile(path)
		if err != nil {
			ps = append(ps, Problem{Key: key, Message: err.Error()})
			return secret.Secret{}
		}
		return s
	}
	d := &c.Database
	if migrate {
		d.Owner.Password = read("database.owner.password_file", d.Owner.PasswordFile)
		return ps
	}
	d.Gateway.Password = read("database.gateway.password_file", d.Gateway.PasswordFile)
	d.Admin.Password = read("database.admin.password_file", d.Admin.PasswordFile)
	d.Worker.Password = read("database.worker.password_file", d.Worker.PasswordFile)
	return ps
}

func checkAddress(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "must be host:port"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "the port must be an integer between 0 and 65535"
	}
	if host != "" && net.ParseIP(host) == nil && !hostName.MatchString(host) {
		return "the host must be empty, an IP address or a host name"
	}
	return ""
}

// parseBaseURL validates the provider base URL and removes a trailing slash from its path.
func parseBaseURL(raw string) (*url.URL, string) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, "must be a valid URL"
	case !u.IsAbs():
		return nil, "must be an absolute URL"
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, "the scheme must be http or https"
	case u.Host == "":
		return nil, "must have a host"
	case u.User != nil:
		return nil, "must not contain user information"
	case u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "?"):
		return nil, "must not contain a query"
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return nil, "must not contain a fragment"
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, ""
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func listenerAndURLWarnings(c *Config) []Warning {
	var ws []Warning
	if u := c.Skeleton.OpenAI.ParsedBaseURL; u != nil && u.Scheme == "http" && !isLoopback(u.Hostname()) {
		ws = append(ws, Warning{Key: "skeleton.openai.base_url", Message: "the provider key is sent without TLS"})
	}
	if host, _, err := net.SplitHostPort(c.Listeners.Inference.Address); err == nil && !isLoopback(host) {
		ws = append(ws, Warning{Key: "listeners.inference.address", Message: "the listener serves plain HTTP; terminate TLS in front of it"})
	}
	if c.Database.TLS.Mode == "disable" && c.Database.Host != "" && !isLoopback(c.Database.Host) {
		ws = append(ws, Warning{Key: "database.tls.mode", Message: "the database connection is not encrypted"})
	}
	return ws
}

// loadSecrets reads the secret files and cross-checks them.
func loadSecrets(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	const tokenKey, apiKey = "skeleton.access_token_file", "skeleton.openai.api_key_file"

	var token, provider secret.Secret
	tokenOK, providerOK := false, false

	if c.Skeleton.AccessTokenFile == "" {
		add(tokenKey, "must be set")
	} else if s, err := secret.ReadFile(c.Skeleton.AccessTokenFile); err != nil {
		add(tokenKey, err.Error())
	} else if len(s.Reveal()) < minTokenBytes {
		add(tokenKey, "the token must be at least 32 bytes long")
	} else {
		token, tokenOK = s, true
	}

	if c.Skeleton.OpenAI.APIKeyFile == "" {
		add(apiKey, "must be set")
	} else if s, err := secret.ReadFile(c.Skeleton.OpenAI.APIKeyFile); err != nil {
		add(apiKey, err.Error())
	} else {
		provider, providerOK = s, true
	}

	if tokenOK && providerOK && token.Equal(provider.Reveal()) {
		add(tokenKey, "the access token must differ from the provider key")
	}
	c.Skeleton.AccessToken, c.Skeleton.OpenAI.APIKey = token, provider
	return ps
}
