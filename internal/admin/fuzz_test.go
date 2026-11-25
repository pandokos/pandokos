package admin

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
)

// FuzzDecodeCursor is T-1110: decodeFullCursor never panics; a cursor it accepts was made for the
// sort asked, holds members within their bounds and an ID that parses, and encodes back to a cursor
// that decodes to the same value. Cursors are never made from the database in this test: the seeds
// are made by encodeFullCursor with synthetic values.
func FuzzDecodeCursor(f *testing.F) {
	const id = "0192f3a4-5b6c-7d8e-9fa0-b1c2d3e4f506"
	for _, c := range []cursor{
		{S: "name", N: "synthetic-name", I: id},
		{S: "seq", N: "42", I: id, Q: "action=tenant.create&actor=synthetic"},
		{S: "started_at", I: id, F: "2026-11-01T00:00:00Z", T: "2026-11-24T00:00:00Z", P: "openai", L: "limited"},
		{S: "name", N: strings.Repeat("n", 201), I: id},
		{S: "name", N: "x", I: "not-an-id"},
		{S: "other", N: "x", I: id},
		{S: "name", Q: strings.Repeat("q", 2049), I: id},
	} {
		f.Add(encodeFullCursor(c), c.S)
		f.Add(encodeFullCursor(c), "name")
	}
	for _, s := range []string{"", "!", "e30", "e30=", "bnVsbA", "W10", "eyJzIjoibmFtZSIsImkiOjd9", strings.Repeat("A", 4096)} {
		f.Add(s, "name")
	}
	f.Fuzz(func(t *testing.T, raw, sort string) {
		if len(raw) > 1<<20 || len(sort) > 1<<10 {
			t.Skip()
		}
		c, err := decodeFullCursor(raw, sort)
		if err != nil {
			if (c != cursor{}) {
				t.Fatalf("a cursor and an error: %+v, %v", c, err)
			}
			return
		}
		if c.S != sort {
			t.Fatalf("a cursor made for %q accepted for %q", c.S, sort)
		}
		if len(c.N) > 200 || len(c.F) > 64 || len(c.T) > 64 || len(c.P) > 64 || len(c.Q) > 2048 {
			t.Fatalf("a member out of its bound: %d %d %d %d %d", len(c.N), len(c.F), len(c.T), len(c.P), len(c.Q))
		}
		if _, err := tenant.ParseID(c.I); err != nil {
			t.Fatalf("an accepted cursor whose ID does not parse: %q", c.I)
		}
		again, err := decodeFullCursor(encodeFullCursor(c), sort)
		if err != nil || again != c {
			t.Fatalf("the cursor does not survive an encoding: %+v, %+v, %v", c, again, err)
		}
	})
}

// FuzzOrigin is T-1113: for any Host header, peer address and forwarding header, sourceAddr is a
// valid address and, when the peer is not a trusted proxy, it is the peer whatever the headers say;
// expectedOrigin, when it answers, answers an origin with a scheme, a host and no path; splitHost
// answers a lower-case host and a port in range.
func FuzzOrigin(f *testing.F) {
	f.Add("localhost:8080", []byte{127, 0, 0, 1}, "203.0.113.9", uint8(0b001))
	f.Add("EXAMPLE.test:443", []byte{10, 0, 0, 1}, "198.51.100.7, 10.0.0.2", uint8(0b110))
	f.Add("[::1]:9000", []byte{0x20, 0x01, 0x0d, 0xb8, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, "2001:db8::1", uint8(0b010))
	f.Add("[::1", []byte{192, 0, 2, 1}, "not-an-address, 10.0.0.9", uint8(0b111))
	f.Add("a:b", []byte{10, 1, 2, 3}, "10.0.0.1, 10.0.0.2", uint8(0b100))
	f.Add("", []byte{10, 1, 2, 3}, "", uint8(0))
	f.Add("host:99999", []byte{127, 0, 0, 1}, "::ffff:203.0.113.9", uint8(0b101))
	f.Add("x.example.test:08", []byte(nil), "1.2.3.4%eth0", uint8(0b011))
	f.Fuzz(func(t *testing.T, host string, peerBytes []byte, xff string, flags uint8) {
		if len(host) > 1<<16 || len(xff) > 1<<16 {
			t.Skip()
		}
		o := OriginOptions{TLS: flags&1 != 0, Loopback: flags&2 != 0}
		if flags&4 != 0 {
			o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:ffff::/48"), netip.MustParsePrefix("::ffff:192.0.2.0/120")}
		}
		if flags&8 != 0 {
			o.Public = "https://admin.example.test"
		}
		var peer netip.Addr
		switch len(peerBytes) {
		case 4:
			peer = netip.AddrFrom4([4]byte(peerBytes))
		case 16:
			peer = netip.AddrFrom16([16]byte(peerBytes))
		}
		r := &http.Request{Host: host, Header: http.Header{}}
		if peer.IsValid() {
			r.RemoteAddr = net.JoinHostPort(peer.String(), "40000")
		} else {
			r.RemoteAddr = "not an address"
		}
		if xff != "" {
			r.Header.Add("X-Forwarded-For", xff)
		}

		got := sourceAddr(r, o)
		if !peer.IsValid() {
			if got.IsValid() {
				t.Fatalf("a source %v for a peer that is not an address", got)
			}
		} else {
			if !got.IsValid() {
				t.Fatalf("no source for the peer %v", peer)
			}
			want := peer.WithZone("").Unmap()
			if !trusted(o, want) && got != want {
				t.Fatalf("the peer %v is not a trusted proxy and the source is %v (X-Forwarded-For %.60q)", want, got, xff)
			}
			if got.Zone() != "" || got.Is4In6() {
				t.Fatalf("the source %v keeps a zone or a mapped form", got)
			}
		}

		if origin, ok := expectedOrigin(r, o); ok {
			u, err := url.Parse(origin)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
				t.Fatalf("the expected origin %q has no scheme and host alone", origin)
			}
			if o.Public != "" && origin != o.Public {
				t.Fatalf("auth.public_origin %q is not the expected origin %q", o.Public, origin)
			}
		}

		if h, p, ok := splitHost(host); ok {
			if h != strings.ToLower(h) || h == "" {
				t.Fatalf("the host %q is not lower case", h)
			}
			if p != "" {
				if n, err := strconv.Atoi(p); err != nil || n < 0 || n > 65535 || p != strconv.Itoa(n) {
					t.Fatalf("the port %q", p)
				}
			}
		}
	})
}
