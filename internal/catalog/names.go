package catalog

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// ReservedProviderNames are the names no provider may take: the probe routes of the gateway and
// the prefix of the unified endpoint. The check constraint of the provider table lists the same.
var ReservedProviderNames = []string{"healthz", "readyz", "v1"}

// Limits of names and URLs.
const (
	maxProviderName = 63
	maxModelName    = 128
	maxUpstreamID   = 256
	maxEndpointURL  = 2048
)

// The refusals of this file. None quotes the value it refused.
var (
	errProviderName   = errors.New("catalog: a provider name starts with a lower-case letter and has at most 63 lower-case letters, digits and hyphens")
	errProviderReused = errors.New("catalog: this provider name is reserved")
	errModelName      = errors.New("catalog: a model name starts with a letter or digit and has at most 128 letters, digits and . _ : @ / + -")
	errUpstreamID     = errors.New("catalog: an upstream identifier has 1 to 256 printable ASCII characters and no space")
	errRef            = errors.New("catalog: a model reference is <provider>/<model name>")
	errURLShape       = errors.New("catalog: the base URL must be an absolute URL without user information, query or fragment")
	errURLScheme      = errors.New("catalog: the base URL must use https (http only with private: true)")
	errURLTrailing    = errors.New("catalog: the base URL must not end in a slash")
	errURLLength      = errors.New("catalog: the base URL is longer than 2048 characters")
	errURLPrivate     = errors.New("catalog: a private address needs private: true")
)

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlnum(c byte) bool { return isLower(c) || isDigit(c) || (c >= 'A' && c <= 'Z') }

// ValidateProviderName checks ^[a-z][a-z0-9-]{0,62}$ and the reserved names.
func ValidateProviderName(name string) error {
	if len(name) == 0 || len(name) > maxProviderName || !isLower(name[0]) {
		return errProviderName
	}
	for i := 1; i < len(name); i++ {
		if c := name[i]; !isLower(c) && !isDigit(c) && c != '-' {
			return errProviderName
		}
	}
	for _, r := range ReservedProviderNames {
		if name == r {
			return errProviderReused
		}
	}
	return nil
}

// ValidateModelName checks ^[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127}$.
func ValidateModelName(name string) error {
	if len(name) == 0 || len(name) > maxModelName || !isAlnum(name[0]) {
		return errModelName
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if !isAlnum(c) && !strings.ContainsRune("._:@/+-", rune(c)) {
			return errModelName
		}
	}
	return nil
}

// ValidateUpstreamID checks the identifier sent to the provider: 1 to 256 printable ASCII
// characters, no space.
func ValidateUpstreamID(id string) error {
	if len(id) == 0 || len(id) > maxUpstreamID {
		return errUpstreamID
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return errUpstreamID
		}
	}
	return nil
}

// ParseRef splits a model reference <provider>/<model name> at its first slash and checks both
// parts.
func ParseRef(ref string) (provider, name string, err error) {
	provider, name, ok := strings.Cut(ref, "/")
	if !ok || ValidateProviderName(provider) != nil || ValidateModelName(name) != nil {
		return "", "", errRef
	}
	return provider, name, nil
}

// ValidateEndpointURL checks a base URL and returns its normalized form: scheme and host in
// lower case, one trailing slash removed. The scheme is https, or http only when private is
// true; there is a host and no user information, query or fragment; an address literal of a
// loopback, private, link-local or unspecified range needs private. Host names are not
// resolved: the egress guard checks the address that is dialled. An error never quotes the URL.
func ValidateEndpointURL(raw string, private bool) (string, error) {
	if len(raw) > maxEndpointURL {
		return "", errURLLength
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x21 || raw[i] > 0x7e || raw[i] == '?' || raw[i] == '#' {
			return "", errURLShape
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host == "" || u.Hostname() == "" {
		return "", errURLShape
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !private {
			return "", errURLScheme
		}
	default:
		return "", errURLScheme
	}
	if addr, err := netip.ParseAddr(u.Hostname()); err == nil && !private {
		addr = addr.Unmap()
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
			addr.IsInterfaceLocalMulticast() || addr.IsUnspecified() {
			return "", errURLPrivate
		}
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	if strings.HasSuffix(path, "/") {
		return "", errURLTrailing
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + path, nil
}
