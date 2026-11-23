package config

import (
	"strconv"
	"strings"
	"time"
)

const maxPrivateHosts = 32

// validateTokens applies the rules of the tokens keys (sprint 4.1, section 5.11) and lower-cases
// the host names of tokens.private_hosts; only serve reads them. No message quotes a host name.
func validateTokens(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	t := &c.Tokens
	between := func(key string, d Duration, lo, hi time.Duration, text string) {
		if v := time.Duration(d); v < lo || v > hi {
			add(key, "must be between "+text)
		}
	}

	if n := t.MaxTokenBytes; n < 1024 || n > 16384 {
		add("tokens.max_token_bytes", "must be between 1024 and 16384")
	} else if h := c.Listeners.Inference.MaxHeaderBytes; h >= 4096 && h <= 1048576 && n > h/2 {
		add("tokens.max_token_bytes", "must be at most half of listeners.inference.max_header_bytes ("+strconv.Itoa(h/2)+")")
	}
	between("tokens.clock_skew", t.ClockSkew, 0, 5*time.Minute, "0s and 5m")
	between("tokens.max_lifetime", t.MaxLifetime, time.Minute, 720*time.Hour, "1m and 720h")
	between("tokens.jwks.fetch_timeout", t.JWKS.FetchTimeout, time.Second, 30*time.Second, "1s and 30s")
	between("tokens.jwks.refresh_interval", t.JWKS.RefreshInterval, time.Minute, time.Hour, "1m and 1h")
	between("tokens.jwks.min_refetch_interval", t.JWKS.MinRefetchInterval, time.Second, 5*time.Minute, "1s and 5m")
	switch d := time.Duration(t.JWKS.MaxStale); {
	case d > 72*time.Hour:
		add("tokens.jwks.max_stale", "must be at most 72h")
	case d < 2*time.Duration(t.JWKS.RefreshInterval):
		add("tokens.jwks.max_stale", "must be at least twice tokens.jwks.refresh_interval")
	}

	t.PrivateHosts = validatePrivateHosts("tokens.private_hosts", t.PrivateHosts, add)
	return ps
}

// validDNSName reports whether s is a DNS name: dot-separated labels of letters, digits and
// hyphens, none empty, none starting or ending with a hyphen, at most 63 bytes each and 253 in
// all, with at least one letter so that no IPv4 literal passes.
func validDNSName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	letter := false
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			switch b := label[i]; {
			case b >= 'a' && b <= 'z':
				letter = true
			case b >= '0' && b <= '9', b == '-':
			default:
				return false
			}
		}
	}
	return letter
}

// tokenWarnings returns the two warnings of the tokens section: private hosts, and a key set that
// may be used for more than 24 hours after its last fetch.
func tokenWarnings(c *Config) []Warning {
	var ws []Warning
	if len(c.Tokens.PrivateHosts) > 0 {
		ws = append(ws, Warning{Key: "tokens.private_hosts", Message: "issuer and key URLs of these hosts may resolve to private addresses: " + strings.Join(c.Tokens.PrivateHosts, ", ")})
	}
	if time.Duration(c.Tokens.JWKS.MaxStale) > 24*time.Hour {
		ws = append(ws, Warning{Key: "tokens.jwks.max_stale", Message: "keys of an issuer are used for more than 24h after the last successful fetch: a key withdrawn by the issuer stays trusted that long while it is unreachable"})
	}
	return ws
}
