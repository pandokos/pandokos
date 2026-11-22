package admin

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func prefixes(t *testing.T, list ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range list {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func request(remote string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/sign-in", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

// TestSourceAddress is T-785.
func TestSourceAddress(t *testing.T) {
	trusted := OriginOptions{TrustedProxies: prefixes(t, "10.0.0.0/8", "2001:db8:ffff::/48")}
	none := OriginOptions{}
	cases := []struct {
		name string
		r    *http.Request
		o    OriginOptions
		want string // "" is the zero address
	}{
		{"the peer", request("192.0.2.5:4444"), none, "192.0.2.5"},
		{"the peer, IPv6", request("[2001:db8::1]:4444"), none, "2001:db8::1"},
		{"an IPv4 peer in IPv6 form", request("[::ffff:192.0.2.5]:4444"), none, "192.0.2.5"},
		{"a header of an untrusted peer is ignored", request("192.0.2.5:4444", "X-Forwarded-For", "198.51.100.7"), trusted, "192.0.2.5"},
		{"a header with no proxies configured is ignored", request("10.1.2.3:4444", "X-Forwarded-For", "198.51.100.7"), none, "10.1.2.3"},
		{"a trusted proxy, one entry", request("10.1.2.3:4444", "X-Forwarded-For", "198.51.100.7"), trusted, "198.51.100.7"},
		{"a trusted proxy, several entries: the right-most", request("10.1.2.3:4444", "X-Forwarded-For", "203.0.113.1, 198.51.100.7"), trusted, "198.51.100.7"},
		{"entries of proxies are skipped", request("10.1.2.3:4444", "X-Forwarded-For", "203.0.113.1, 198.51.100.7, 10.9.9.9"), trusted, "198.51.100.7"},
		{"a forged entry on the left", request("10.1.2.3:4444", "X-Forwarded-For", "1.2.3.4, 198.51.100.7, 10.0.0.2"), trusted, "198.51.100.7"},
		{"several header lines: the last entry of the last", request("10.1.2.3:4444", "X-Forwarded-For", "1.2.3.4", "X-Forwarded-For", "5.6.7.8, 198.51.100.7"), trusted, "198.51.100.7"},
		{"IPv6 entries", request("10.1.2.3:4444", "X-Forwarded-For", "2001:db8:1::5"), trusted, "2001:db8:1::5"},
		{"an IPv6 proxy", request("[2001:db8:ffff::9]:4444", "X-Forwarded-For", "198.51.100.7"), trusted, "198.51.100.7"},
		{"every entry a proxy: the peer", request("10.1.2.3:4444", "X-Forwarded-For", "10.4.4.4, 10.5.5.5"), trusted, "10.1.2.3"},
		{"a malformed entry: the peer", request("10.1.2.3:4444", "X-Forwarded-For", "garbage"), trusted, "10.1.2.3"},
		{"a malformed right-most entry: the peer", request("10.1.2.3:4444", "X-Forwarded-For", "198.51.100.7, garbage"), trusted, "10.1.2.3"},
		{"an entry with a port is malformed", request("10.1.2.3:4444", "X-Forwarded-For", "198.51.100.7:1234"), trusted, "10.1.2.3"},
		{"an empty header", request("10.1.2.3:4444", "X-Forwarded-For", ""), trusted, "10.1.2.3"},
		{"a peer that is not an address", request("not-an-address"), none, ""},
	}
	for _, c := range cases {
		got := sourceAddr(c.r, c.o)
		want := netip.Addr{}
		if c.want != "" {
			want = netip.MustParseAddr(c.want)
		}
		if got != want {
			t.Errorf("%s: %v, want %v", c.name, got, want)
		}
	}
}

// TestExpectedOrigin is T-799: the table of section 5.7.
func TestExpectedOrigin(t *testing.T) {
	req := func(host string, secure bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/sign-in", nil)
		r.Host = host
		if secure {
			r.TLS = &tls.ConnectionState{}
		}
		return r
	}
	cases := []struct {
		name   string
		r      *http.Request
		o      OriginOptions
		want   string
		wantOK bool
	}{
		{"the configured origin", req("anything.example.test", false), OriginOptions{Public: "https://admin.example.test"}, "https://admin.example.test", true},
		{"the configured origin wins over TLS and Host", req("other.example.test:8443", true), OriginOptions{Public: "https://admin.example.test", TLS: true}, "https://admin.example.test", true},
		{"the configured origin behind a plain listener", req("10.1.2.3:8081", false), OriginOptions{Public: "https://admin.example.test"}, "https://admin.example.test", true},
		{"the configured origin with a port", req("x", false), OriginOptions{Public: "https://admin.example.test:8443"}, "https://admin.example.test:8443", true},
		{"a loopback origin on http", req("127.0.0.1:8081", false), OriginOptions{Public: "http://127.0.0.1:8081", Loopback: true}, "http://127.0.0.1:8081", true},
		{"TLS with the Host header", req("admin.example.test:8443", true), OriginOptions{TLS: true}, "https://admin.example.test:8443", true},
		{"TLS, the case of the Host header", req("Admin.Example.TEST:8443", true), OriginOptions{TLS: true}, "https://admin.example.test:8443", true},
		{"TLS, the default port is not part of an origin", req("admin.example.test:443", true), OriginOptions{TLS: true}, "https://admin.example.test", true},
		{"TLS without a port", req("admin.example.test", true), OriginOptions{TLS: true}, "https://admin.example.test", true},
		{"TLS with an IPv6 host", req("[2001:db8::1]:8443", true), OriginOptions{TLS: true}, "https://[2001:db8::1]:8443", true},
		{"loopback, 127.0.0.1", req("127.0.0.1:8081", false), OriginOptions{Loopback: true}, "http://127.0.0.1:8081", true},
		{"loopback, ::1", req("[::1]:8081", false), OriginOptions{Loopback: true}, "http://[::1]:8081", true},
		{"loopback, localhost", req("localhost:8081", false), OriginOptions{Loopback: true}, "http://localhost:8081", true},
		{"loopback, LOCALHOST", req("LOCALHOST:8081", false), OriginOptions{Loopback: true}, "http://localhost:8081", true},
		{"loopback, port 80", req("localhost:80", false), OriginOptions{Loopback: true}, "http://localhost", true},
		{"a loopback listener, a Host that is not loopback", req("admin.example.test", false), OriginOptions{Loopback: true}, "", false},
		{"a loopback listener, a name that starts like loopback", req("127.0.0.1.evil.test:8081", false), OriginOptions{Loopback: true}, "", false},
		{"a loopback listener, localhost as a suffix", req("evil-localhost:8081", false), OriginOptions{Loopback: true}, "", false},
		{"a loopback listener, another private address", req("10.1.2.3:8081", false), OriginOptions{Loopback: true}, "", false},
		{"a plain listener elsewhere, no origin configured", req("10.1.2.3:8081", false), OriginOptions{}, "", false},
		{"a plain listener elsewhere, a Host that looks like loopback", req("localhost:8081", false), OriginOptions{}, "", false},
		{"no Host at all on TLS", req("", true), OriginOptions{TLS: true}, "", false},
	}
	for _, c := range cases {
		got, ok := expectedOrigin(c.r, c.o)
		if ok != c.wantOK || got != c.want {
			t.Errorf("%s: (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.wantOK)
		}
	}
}
