package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func validate(c *Config, purpose Purpose) []Problem {
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
	ps = append(ps, validateAdmin(c)...)
	for _, l := range []struct {
		name string
		tls  *ListenerTLS
	}{{"inference", &c.Listeners.Inference.TLS}, {"admin", &c.Listeners.Admin.TLS}} {
		ps = append(ps, validateTLS(l.name, l.tls, purpose == PurposeServe)...)
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
		{"listeners.admin.read_header_timeout", c.Listeners.Admin.ReadHeaderTimeout},
		{"listeners.admin.idle_timeout", c.Listeners.Admin.IdleTimeout},
		{"database.connect_timeout", c.Database.ConnectTimeout},
	} {
		if time.Duration(d.v) <= 0 {
			add(d.key, "must be greater than zero")
		}
	}

	if n := c.Listeners.Inference.MaxHeaderBytes; n < 4096 || n > 1048576 {
		add("listeners.inference.max_header_bytes", "must be between 4096 and 1048576")
	}
	if n := c.Listeners.Admin.MaxHeaderBytes; n < 4096 || n > 1048576 {
		add("listeners.admin.max_header_bytes", "must be between 4096 and 1048576")
	}
	if d := time.Duration(c.Cache.PrincipalTTL); d < time.Second || d > time.Minute {
		add("cache.principal_ttl", "must be between 1s and 60s")
	}
	if d := time.Duration(c.Cache.CredentialTTL); d < time.Second || d > time.Minute {
		add("cache.credential_ttl", "must be between 1s and 60s")
	}
	if d := time.Duration(c.Cache.DataKeyTTL); d < time.Second || d > 15*time.Minute {
		add("cache.data_key_ttl", "must be between 1s and 15m")
	}
	if d := time.Duration(c.Cache.CatalogTTL); d < time.Second || d > 5*time.Minute {
		add("cache.catalog_ttl", "must be between 1s and 5m")
	}
	if purpose == PurposeServe {
		ps = append(ps, validatePricing(c)...)
		ps = append(ps, validateUpstream(c)...)
		ps = append(ps, validateLedger(c)...)
		ps = append(ps, validateTelemetry(c)...)
	}
	if c.Vault.KEK.Provider != "local" {
		add("vault.kek.provider", "must be local")
	}
	if n := c.Limits.MaxRequestBodyBytes; n < 1 || n > 104857600 {
		add("limits.max_request_body_bytes", "must be between 1 and 104857600")
	}

	ps = append(ps, validateDatabase(c)...)

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
	if d.DirectAddress == "" {
		d.DirectHost, d.DirectPort = d.Host, d.Port
	} else if host, port, msg := checkHostPort(d.DirectAddress); msg != "" {
		add("database.direct_address", msg)
	} else {
		d.DirectHost, d.DirectPort = host, port
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
	host, port, msg := checkHostPort(d.Address)
	if msg != "" {
		return msg
	}
	d.Host, d.Port = host, port
	return ""
}

// checkHostPort applies the three address rules of the database addresses.
func checkHostPort(addr string) (host string, port int, msg string) {
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, "must be host:port"
	}
	n, err := strconv.Atoi(ps)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, "the port must be an integer between 1 and 65535"
	}
	if host == "" || (net.ParseIP(host) == nil && !hostName.MatchString(host)) {
		return "", 0, "the host must be an IP address or a host name"
	}
	return host, n, ""
}

// validateAdmin applies the rules of the administration listener.
func validateAdmin(c *Config) []Problem {
	var ps []Problem
	a := &c.Listeners.Admin
	if msg := checkAddress(a.Address); msg != "" {
		ps = append(ps, Problem{"listeners.admin.address", msg})
		return ps
	}
	host, port, _ := net.SplitHostPort(a.Address) //nolint:errcheck // checkAddress accepted it
	if a.Address == c.Listeners.Inference.Address && port != "0" {
		ps = append(ps, Problem{"listeners.admin.address", "must differ from listeners.inference.address"})
	}
	tlsOn := a.TLS.CertFile != "" && a.TLS.KeyFile != ""
	if !isLoopback(host) && !tlsOn && !a.AllowPlaintext {
		ps = append(ps, Problem{"listeners.admin.address", "the administration API must not be served without TLS on a non-loopback address: set listeners.admin.tls.cert_file and key_file, or set listeners.admin.allow_plaintext when TLS ends in front of it"})
	}
	return ps
}

const (
	minTLS12 = "TLS1.2"
	minTLS13 = "TLS1.3"
)

// validateTLS applies the TLS rules of one listener. With load, the files are read and the
// certificate is checked and loaded into t.Certificate.
func validateTLS(name string, t *ListenerTLS, load bool) []Problem {
	var ps []Problem
	pre := "listeners." + name + ".tls."
	add := func(key, msg string) { ps = append(ps, Problem{Key: pre + key, Message: msg}) }
	if t.MinVersion != minTLS12 && t.MinVersion != minTLS13 {
		add("min_version", "must be TLS1.2 or TLS1.3")
	}
	switch {
	case t.CertFile != "" && t.KeyFile == "":
		add("key_file", "must be set when "+pre+"cert_file is set")
		return ps
	case t.CertFile == "" && t.KeyFile != "":
		add("cert_file", "must be set when "+pre+"key_file is set")
		return ps
	case t.CertFile == "" || !load:
		return ps
	}
	certPEM, msg := readLimited(t.CertFile)
	if msg != "" {
		add("cert_file", msg)
	}
	keyPEM, msg := readLimited(t.KeyFile)
	if msg != "" {
		add("key_file", msg)
	}
	if certPEM == nil || keyPEM == nil {
		return ps
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		add("cert_file", "the certificate and the key are not a valid pair")
		return ps
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		add("cert_file", "the certificate and the key are not a valid pair")
		return ps
	}
	pair.Leaf = leaf
	now := time.Now()
	switch {
	case now.Before(leaf.NotBefore):
		add("cert_file", "the certificate is not valid before "+leaf.NotBefore.UTC().Format(time.RFC3339))
	case now.After(leaf.NotAfter):
		add("cert_file", "the certificate expired on "+leaf.NotAfter.UTC().Format(time.RFC3339))
	default:
		t.Certificate = &pair
	}
	return ps
}

// readLimited reads a file of at most maxCAFileBytes; the message never holds file content.
func readLimited(path string) ([]byte, string) {
	f, err := os.Open(path) //nolint:gosec // the path is operator configuration
	if err != nil {
		return nil, "cannot read the file: " + err.Error()
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCAFileBytes+1))
	if err != nil {
		return nil, "cannot read the file: " + err.Error()
	}
	if len(b) > maxCAFileBytes {
		return nil, "the file is larger than 1048576 bytes"
	}
	return b, ""
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
func loadDatabaseSecrets(c *Config, purpose Purpose) []Problem {
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
	switch purpose {
	case PurposeMigrate:
		d.Owner.Password = read("database.owner.password_file", d.Owner.PasswordFile)
		return ps
	case PurposeInit:
		d.Admin.Password = read("database.admin.password_file", d.Admin.PasswordFile)
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

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func listenerAndURLWarnings(c *Config) []Warning {
	var ws []Warning
	if host, _, err := net.SplitHostPort(c.Listeners.Inference.Address); err == nil && !isLoopback(host) && (c.Listeners.Inference.TLS.CertFile == "" || c.Listeners.Inference.TLS.KeyFile == "") {
		ws = append(ws, Warning{Key: "listeners.inference.address", Message: "the listener serves plain HTTP; set listeners.inference.tls.cert_file and key_file, or terminate TLS in front of it"})
	}
	a := c.Listeners.Admin
	if host, _, err := net.SplitHostPort(a.Address); err == nil {
		tlsOn := a.TLS.CertFile != "" && a.TLS.KeyFile != ""
		if !isLoopback(host) && !tlsOn && a.AllowPlaintext {
			ws = append(ws, Warning{Key: "listeners.admin.address", Message: "the administration API is served without TLS; keys travel in clear text unless TLS ends in front of it"})
		}
		if a.AllowPlaintext && (tlsOn || isLoopback(host)) {
			ws = append(ws, Warning{Key: "listeners.admin.allow_plaintext", Message: "has no effect"})
		}
	}
	for _, l := range []struct {
		name string
		tls  ListenerTLS
	}{{"inference", c.Listeners.Inference.TLS}, {"admin", c.Listeners.Admin.TLS}} {
		if l.tls.Certificate != nil && l.tls.Certificate.Leaf != nil && time.Until(l.tls.Certificate.Leaf.NotAfter) < 14*24*time.Hour {
			ws = append(ws, Warning{Key: "listeners." + l.name + ".tls.cert_file", Message: "the certificate expires on " + l.tls.Certificate.Leaf.NotAfter.UTC().Format(time.RFC3339)})
		}
	}
	if c.Database.TLS.Mode == "disable" && c.Database.Host != "" && !isLoopback(c.Database.Host) {
		ws = append(ws, Warning{Key: "database.tls.mode", Message: "the database connection is not encrypted"})
	}
	return append(ws, keyFileWarning(c)...)
}

// loadSecrets reads the key-encryption key.
func loadSecrets(c *Config) []Problem {
	return loadKEK(c)
}

// loadKEK reads the key file and checks that it holds 32 bytes in standard base64. No message
// quotes any of the content.
func loadKEK(c *Config) []Problem {
	const key = "vault.kek.local.key_file"
	path := c.Vault.KEK.Local.KeyFile
	if path == "" {
		return []Problem{{Key: key, Message: "must be set"}}
	}
	s, err := secret.ReadFile(path)
	if err != nil {
		return []Problem{{Key: key, Message: err.Error()}}
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(s.Reveal())
	if err != nil || len(raw) != 32 {
		clear(raw)
		return []Problem{{Key: key, Message: "must hold 32 bytes in standard base64 (44 characters); create one with pandokos kek generate"}}
	}
	clear(raw)
	c.Vault.KEK.Local.Key = s
	return nil
}

// keyFileWarning says when the key file can be accessed by users other than its owner and group.
func keyFileWarning(c *Config) []Warning {
	if c.Vault.KEK.Local.Key.IsZero() || runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(c.Vault.KEK.Local.KeyFile) // follows links: the mode of the target counts
	if err != nil || fi.Mode().Perm()&0o007 == 0 {
		return nil
	}
	return []Warning{{Key: "vault.kek.local.key_file", Message: "the file can be accessed by other users; restrict it to the service user (chmod 600)"}}
}

// validatePricing applies the rules of the download settings; only serve reads them. No message
// quotes the URL: it may hold a token in its path or query.
func validatePricing(c *Config) []Problem {
	var ps []Problem
	r := &c.Pricing.Refresh
	if d := time.Duration(r.Interval); d < time.Hour || d > 720*time.Hour {
		ps = append(ps, Problem{"pricing.refresh.interval", "must be between 1h and 720h"})
	}
	u, err := url.Parse(r.URL)
	switch {
	case err != nil || strings.ContainsAny(r.URL, " \t\r\n"):
		ps = append(ps, Problem{"pricing.refresh.url", "must be a valid URL"})
	case u.Scheme != "https":
		ps = append(ps, Problem{"pricing.refresh.url", "must be an https URL"})
	case u.Host == "" || u.Hostname() == "":
		ps = append(ps, Problem{"pricing.refresh.url", "must have a host"})
	case u.User != nil:
		ps = append(ps, Problem{"pricing.refresh.url", "must not contain user information"})
	default:
		r.ParsedURL = u
	}
	return ps
}

// validateUpstream applies the rules of upstream.tls.ca_file and upstream.proxy.url; only serve
// reads them. No message quotes the content of the file or the URL.
func validateUpstream(c *Config) []Problem {
	var ps []Problem
	if file := c.Upstream.TLS.CAFile; file != "" {
		const key = "upstream.tls.ca_file"
		b, err := readCAFile(file)
		switch {
		case err != nil:
			ps = append(ps, Problem{key, "the file cannot be read"})
		default:
			pool, perr := x509.SystemCertPool()
			if perr != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(b) {
				ps = append(ps, Problem{key, "the file holds no certificate"})
			} else {
				c.Upstream.TLS.RootCAs = pool
			}
		}
	}
	if raw := c.Upstream.Proxy.URL; raw != "" {
		const key = "upstream.proxy.url"
		u, err := url.Parse(raw)
		switch {
		case err != nil || strings.ContainsAny(raw, " \t\r\n"):
			ps = append(ps, Problem{key, "must be a valid URL"})
		case u.Scheme != "http" && u.Scheme != "https":
			ps = append(ps, Problem{key, "the scheme must be http or https"})
		case u.Host == "" || u.Hostname() == "":
			ps = append(ps, Problem{key, "must have a host"})
		case u.User != nil:
			ps = append(ps, Problem{key, "must not contain user information"})
		case u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "?"):
			ps = append(ps, Problem{key, "must not contain a query"})
		case u.Fragment != "" || strings.Contains(raw, "#"):
			ps = append(ps, Problem{key, "must not contain a fragment"})
		case u.Path != "" && u.Path != "/":
			ps = append(ps, Problem{key, "must not contain a path"})
		default:
			c.Upstream.Proxy.ParsedURL = u
		}
	}
	return ps
}

// readCAFile reads at most maxCAFileBytes of a regular file.
func readCAFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is operator configuration
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxCAFileBytes))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// proxyEnvVars are the environment variables that name a proxy and that Pandokos never uses.
var proxyEnvVars = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"}

// proxyEnvironmentWarnings names each proxy variable set in the environment, never its value.
func proxyEnvironmentWarnings(c *Config, environ []string) []Warning {
	if c.Upstream.Proxy.URL != "" {
		return nil
	}
	set := map[string]bool{}
	for _, e := range environ {
		if name, v, ok := strings.Cut(e, "="); ok && v != "" {
			set[name] = true
		}
	}
	var ws []Warning
	for _, name := range proxyEnvVars {
		if set[name] {
			ws = append(ws, Warning{Key: "upstream.proxy.url", Message: name + " is set in the environment and is not used; set upstream.proxy.url to send provider traffic through a proxy"})
		}
	}
	return ws
}
