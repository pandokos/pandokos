package config

import (
	"crypto/x509"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

var (
	reTransitPath    = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)
	reTransitKeyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

const transitPrefix = "vault.kek.transit."

// validateTransit applies the rules of the keys of the transit key provider (sprint 12.1, section
// 5.11); only serve reads them. No message quotes an address, which may hold user information, or
// a file.
func validateTransit(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: transitPrefix + key, Message: msg}) }
	t := &c.Vault.KEK.Transit
	d := Defaults().Vault.KEK.Transit

	if !t.Configured() {
		if c.Vault.KEK.Provider == "transit" {
			add("address", "must be set when vault.kek.provider is transit")
		}
		for _, k := range []struct {
			name string
			set  bool
		}{
			{"namespace", t.Namespace != ""},
			{"mount", t.Mount != d.Mount},
			{"key_name", t.KeyName != ""},
			{"tls.ca_file", t.TLS.CAFile != ""},
			{"auth.method", t.Auth.Method != ""},
			{"auth.token_file", t.Auth.TokenFile != ""},
			{"auth.approle.mount", t.Auth.AppRole.Mount != d.Auth.AppRole.Mount},
			{"auth.approle.role_id_file", t.Auth.AppRole.RoleIDFile != ""},
			{"auth.approle.secret_id_file", t.Auth.AppRole.SecretIDFile != ""},
			{"request_timeout", t.RequestTimeout != d.RequestTimeout},
			{"max_concurrent_requests", t.MaxConcurrentRequests != d.MaxConcurrentRequests},
			{"start_timeout", t.StartTimeout != d.StartTimeout},
		} {
			if k.set {
				add(k.name, "must not be set without vault.kek.transit.address")
			}
		}
		return ps
	}

	u, err := url.Parse(t.Address)
	switch {
	case err != nil || strings.ContainsAny(t.Address, " \t\r\n") || u.Host == "" || u.Hostname() == "":
		add("address", "must be a URL with a scheme and a host")
	case u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname())):
		add("address", "must be an https URL (http only for a loopback host)")
	case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(t.Address, "#"):
		add("address", "must have no user information, path, query or fragment")
	default:
		if _, _, e := net.SplitHostPort(u.Host); e != nil && strings.Contains(u.Host, ":") && !strings.HasPrefix(u.Host, "[") {
			add("address", "must be a URL with a scheme and a host")
			break
		}
		if p := u.Port(); p != "" {
			if n, ok := atoiPort(p); !ok || n < 1 || n > 65535 {
				add("address", "must have a port between 1 and 65535")
				break
			}
		}
		t.URL = u
	}

	if t.Namespace != "" && (len(t.Namespace) > 255 || !reTransitPath.MatchString(t.Namespace)) {
		add("namespace", "must be segments of letters, digits, - and _ joined by /, 255 characters at most")
	}
	if len(t.Mount) > 128 || !reTransitPath.MatchString(t.Mount) {
		add("mount", "must be segments of letters, digits, - and _ joined by /, 128 characters at most")
	}
	if !reTransitKeyName.MatchString(t.KeyName) {
		if t.KeyName == "" {
			add("key_name", "must be set")
		} else {
			add("key_name", "must start with a letter or digit and hold letters, digits, - _ and . only, 128 characters at most")
		}
	}

	needFile := func(key, v string, want bool) {
		switch {
		case want && v == "":
			add(key, "must be set")
		case !want && v != "":
			add(key, "must be empty with vault.kek.transit.auth.method "+t.Auth.Method)
		}
	}
	switch t.Auth.Method {
	case "token":
		needFile("auth.token_file", t.Auth.TokenFile, true)
		needFile("auth.approle.role_id_file", t.Auth.AppRole.RoleIDFile, false)
		needFile("auth.approle.secret_id_file", t.Auth.AppRole.SecretIDFile, false)
	case "approle":
		needFile("auth.token_file", t.Auth.TokenFile, false)
		needFile("auth.approle.role_id_file", t.Auth.AppRole.RoleIDFile, true)
		needFile("auth.approle.secret_id_file", t.Auth.AppRole.SecretIDFile, true)
		if len(t.Auth.AppRole.Mount) > 128 || !reTransitPath.MatchString(t.Auth.AppRole.Mount) {
			add("auth.approle.mount", "must be segments of letters, digits, - and _ joined by /, 128 characters at most")
		}
	case "":
		add("auth.method", "must be set to token or approle")
	default:
		add("auth.method", "must be token or approle")
	}

	if d := time.Duration(t.RequestTimeout); d < 100*time.Millisecond || d > 30*time.Second {
		add("request_timeout", "must be between 100ms and 30s")
	}
	if n := t.MaxConcurrentRequests; n < 1 || n > 1024 {
		add("max_concurrent_requests", "must be between 1 and 1024")
	}
	if d := time.Duration(t.StartTimeout); d < 0 || d > time.Hour {
		add("start_timeout", "must be between 0 and 1h")
	}
	return ps
}

func atoiPort(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' || n > 99999 {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, s != ""
}

// loadTransitSecrets reads the CA file into the pool and each secret file of the chosen method,
// only to check them: the provider reads them again each time it authenticates.
func loadTransitSecrets(c *Config) []Problem {
	t := &c.Vault.KEK.Transit
	if !t.Configured() {
		return nil
	}
	var ps []Problem
	if t.TLS.CAFile != "" {
		b, err := os.ReadFile(t.TLS.CAFile) //nolint:gosec // the path is operator configuration
		pool := x509.NewCertPool()
		switch {
		case err != nil:
			ps = append(ps, Problem{Key: transitPrefix + "tls.ca_file", Message: "cannot read the file"})
		case !pool.AppendCertsFromPEM(b):
			ps = append(ps, Problem{Key: transitPrefix + "tls.ca_file", Message: "must hold at least one PEM certificate"})
		default:
			t.TLS.RootCAs = pool
		}
	}
	var files []struct{ key, path string }
	switch t.Auth.Method {
	case "token":
		files = append(files, struct{ key, path string }{"auth.token_file", t.Auth.TokenFile})
	case "approle":
		files = append(files,
			struct{ key, path string }{"auth.approle.role_id_file", t.Auth.AppRole.RoleIDFile},
			struct{ key, path string }{"auth.approle.secret_id_file", t.Auth.AppRole.SecretIDFile})
	}
	for _, f := range files {
		if f.path == "" {
			continue // already a problem of validateTransit
		}
		if _, err := secret.ReadFile(f.path); err != nil {
			ps = append(ps, Problem{Key: transitPrefix + f.key, Message: err.Error()})
		}
	}
	return ps
}

// transitWarnings: a secret file that other users can access, an http address.
func transitWarnings(c *Config) []Warning {
	t := c.Vault.KEK.Transit
	if !t.Configured() {
		return nil
	}
	var ws []Warning
	if t.URL != nil && t.URL.Scheme == "http" {
		ws = append(ws, Warning{Key: transitPrefix + "address", Message: "the connection to the key service is not encrypted"})
	}
	if runtime.GOOS == "windows" {
		return ws
	}
	for _, f := range []struct{ key, path string }{
		{"auth.token_file", t.Auth.TokenFile},
		{"auth.approle.role_id_file", t.Auth.AppRole.RoleIDFile},
		{"auth.approle.secret_id_file", t.Auth.AppRole.SecretIDFile},
	} {
		if f.path == "" {
			continue
		}
		if fi, err := os.Stat(f.path); err == nil && fi.Mode().Perm()&0o007 != 0 {
			ws = append(ws, Warning{Key: transitPrefix + f.key, Message: "the file can be accessed by other users; restrict it to the service user (chmod 600)"})
		}
	}
	return ws
}
