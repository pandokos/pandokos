package admin

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// splitHost reads the Host header of a request: the host in lower case (an IPv6 address keeps its
// brackets), the port as written, "" when absent. ok is false for anything that is not a host
// name or an address with an optional port.
func splitHost(h string) (host, port string, ok bool) {
	if h == "" {
		return "", "", false
	}
	h = strings.ToLower(h)
	rest := h
	if strings.HasPrefix(h, "[") {
		end := strings.Index(h, "]")
		if end < 0 {
			return "", "", false
		}
		ip, err := netip.ParseAddr(h[1:end])
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", "", false
		}
		host, rest = h[:end+1], h[end+1:]
		if rest != "" && !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
	} else {
		host = h
		if i := strings.LastIndex(h, ":"); i >= 0 {
			host, rest = h[:i], h[i:]
		} else {
			rest = ""
		}
		if host == "" {
			return "", "", false
		}
		for _, c := range host {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
				return "", "", false
			}
		}
	}
	if rest != "" {
		n, err := strconv.Atoi(rest[1:])
		if err != nil || rest[1:] != strconv.Itoa(n) || n < 0 || n > 65535 {
			return "", "", false
		}
		port = rest[1:]
	}
	return host, port, true
}

// expectedOrigin is the origin that a browser reaches the administration API at (section 5.7), as a
// function of the request and the options; ok is false when the listener cannot say (plain HTTP
// outside loopback without auth.public_origin).
func expectedOrigin(r *http.Request, o OriginOptions) (origin string, ok bool) {
	if o.Public != "" {
		return o.Public, true
	}
	var scheme, defaultPort string
	switch {
	case o.TLS:
		scheme, defaultPort = "https", "443"
	case o.Loopback:
		scheme, defaultPort = "http", "80"
	default:
		return "", false
	}
	host, port, ok := splitHost(r.Host)
	if !ok {
		return "", false
	}
	if !o.TLS && host != "127.0.0.1" && host != "[::1]" && host != "localhost" {
		return "", false
	}
	if port == "" || port == defaultPort {
		return scheme + "://" + host, true
	}
	return scheme + "://" + host + ":" + port, true
}

// sourceAddr is the address that a sign-in is counted against (section 5.5): the peer's, or, when
// the peer is a trusted proxy, the right-most entry of X-Forwarded-For that is not one. A malformed
// entry met on the way is not trusted: the peer is the answer. The zero address says that the peer
// is not an address.
func sourceAddr(r *http.Request, o OriginOptions) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.WithZone("").Unmap()
	if !trusted(o, peer) {
		return peer
	}
	var entries []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(line, ",") {
			if e = strings.TrimSpace(e); e != "" {
				entries = append(entries, e)
			}
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(entries[i])
		if err != nil {
			return peer
		}
		a = a.WithZone("").Unmap()
		if !trusted(o, a) {
			return a
		}
	}
	return peer
}

func trusted(o OriginOptions, a netip.Addr) bool {
	for _, p := range o.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
