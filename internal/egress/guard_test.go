package egress_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
)

func newGuard(t *testing.T, o egress.Options) *egress.Guard {
	t.Helper()
	if o.ConnectTimeout == 0 {
		o.ConnectTimeout = 2 * time.Second
	}
	g, err := egress.New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.CloseIdleConnections)
	return g
}

func blocked(t *testing.T, err error) *egress.BlockedError {
	t.Helper()
	var be *egress.BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("error %v is not a *BlockedError", err)
	}
	return be
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := egress.New(egress.Options{}); err == nil {
		t.Error("a zero ConnectTimeout is accepted")
	}
	if _, err := egress.New(egress.Options{ConnectTimeout: -time.Second}); err == nil {
		t.Error("a negative ConnectTimeout is accepted")
	}
	for _, raw := range []string{"ftp://proxy.example.test:3128", "socks5://proxy.example.test:1080", "http://", "proxy.example.test:3128"} {
		u, _ := url.Parse(raw)
		if _, err := egress.New(egress.Options{ConnectTimeout: time.Second, Proxy: u}); err == nil {
			t.Errorf("the proxy %q is accepted", raw)
		}
	}
	for _, raw := range []string{"http://proxy.example.test:3128", "https://proxy.example.test:3128"} {
		u, _ := url.Parse(raw)
		if _, err := egress.New(egress.Options{ConnectTimeout: time.Second, Proxy: u}); err != nil {
			t.Errorf("the proxy %q: %v", raw, err)
		}
	}
	g := newGuard(t, egress.Options{})
	if g.Transport(false) == nil || g.Transport(true) == nil || g.Transport(false) == g.Transport(true) {
		t.Error("the two transports must exist and differ")
	}
}

// TestDialControl is T-414: the control hook of each transport.
func TestDialControl(t *testing.T) {
	proxy, _ := url.Parse("http://proxy.example.test:3128")
	public := newGuard(t, egress.Options{})
	proxied := newGuard(t, egress.Options{Proxy: proxy})

	type row struct {
		network, addr string
		public        string // "" means accepted
		private       string
		proxied       string
	}
	rows := []row{
		{"tcp4", "8.8.8.8:443", "", "", ""},
		{"tcp6", "[2001:4860:4860::8888]:443", "", "", ""},
		{"tcp4", "10.1.2.3:443", "private_address", "", ""},
		{"tcp4", "127.0.0.1:8080", "private_address", "", ""},
		{"tcp6", "[::1]:8080", "private_address", "", ""},
		{"tcp6", "[::ffff:192.168.1.1]:443", "private_address", "", ""},
		{"tcp4", "169.254.169.254:80", "forbidden_address", "forbidden_address", "forbidden_address"},
		{"tcp4", "0.0.0.0:80", "forbidden_address", "forbidden_address", "forbidden_address"},
		{"tcp6", "[fd00:ec2::254]:80", "forbidden_address", "forbidden_address", "forbidden_address"},
		{"tcp4", "224.0.0.1:80", "forbidden_address", "forbidden_address", "forbidden_address"},
		{"udp4", "8.8.8.8:53", "not_tcp", "not_tcp", "not_tcp"},
		{"udp6", "[2001:4860:4860::8888]:53", "not_tcp", "not_tcp", "not_tcp"},
		{"unix", "/var/run/docker.sock", "not_tcp", "not_tcp", "not_tcp"},
		{"tcp4", "not-an-address", "unparsable", "unparsable", "unparsable"},
		{"tcp4", "localhost:443", "unparsable", "unparsable", "unparsable"},
		{"tcp4", "8.8.8.8", "unparsable", "unparsable", "unparsable"},
		{"tcp4", "", "unparsable", "unparsable", "unparsable"},
	}
	check := func(name string, ctl egress.ControlFunc, r row, want string) {
		t.Helper()
		err := ctl(r.network, r.addr, nil)
		if want == "" {
			if err != nil {
				t.Errorf("%s %s %s: refused: %v", name, r.network, r.addr, err)
			}
			return
		}
		if err == nil {
			t.Errorf("%s %s %s: accepted, want %s", name, r.network, r.addr, want)
			return
		}
		be := blocked(t, err)
		if be.Reason != want || be.BlockedReason() != want {
			t.Errorf("%s %s %s: reason %q, want %q", name, r.network, r.addr, be.Reason, want)
		}
		if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "169.254") || strings.Contains(err.Error(), "10.1.2.3") {
			t.Errorf("%s %s: the error text holds an address: %v", name, r.addr, err)
		}
	}
	for _, r := range rows {
		check("public", public.PublicControl(), r, r.public)
		check("private", public.PrivateControl(), r, r.private)
		check("proxy", proxied.PublicControl(), r, r.proxied)
		check("private with a proxy", proxied.PrivateControl(), r, r.private)
	}

	// The address of a refusal is carried for the operator log; a refusal before an address is known has none.
	err := public.PublicControl()("tcp4", "10.1.2.3:443", nil)
	if be := blocked(t, err); be.Addr != netip.MustParseAddr("10.1.2.3") || be.BlockedAddr() != "10.1.2.3" {
		t.Errorf("the refused address: %v %q", be.Addr, be.BlockedAddr())
	}
	err = public.PublicControl()("udp4", "10.1.2.3:443", nil)
	if be := blocked(t, err); be.BlockedAddr() != "" && !be.Addr.IsValid() {
		t.Errorf("BlockedAddr = %q for an invalid Addr", be.BlockedAddr())
	}
	if (&egress.BlockedError{Reason: "scheme"}).BlockedAddr() != "" {
		t.Error("BlockedAddr of a refusal without an address is not empty")
	}
	if got := (&egress.BlockedError{Reason: "private_address", Addr: netip.MustParseAddr("10.1.2.3")}).Error(); strings.Contains(got, "10.1.2.3") || !strings.Contains(got, "private_address") {
		t.Errorf("Error() = %q", got)
	}
}

// TestPreflight is T-416: refusals made before any connection attempt.
func TestPreflight(t *testing.T) {
	g := newGuard(t, egress.Options{})
	var dials atomic.Int64
	for _, tr := range []*http.Transport{g.PublicTransport(), g.PrivateTransport()} {
		inner := tr.DialContext
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return inner(ctx, network, addr)
		}
	}
	cases := []struct {
		name    string
		private bool
		url     string
		reason  string
	}{
		{"http on the public transport", false, "http://example.test/v1/chat/completions", "scheme"},
		{"http to a public literal on the public transport", false, "http://8.8.8.8/v1", "scheme"},
		{"another scheme", true, "ftp://example.test/x", "scheme"},
		{"user information, public", false, "https://user:synthetic-pw@example.test/v1", "user_info"},
		{"user name only, private", true, "https://user@example.test/v1", "user_info"},
		{"a forbidden literal on the private transport", true, "https://169.254.169.254/latest/meta-data", "forbidden_address"},
		{"the unspecified literal on the private transport", true, "http://0.0.0.0:8080/v1", "forbidden_address"},
		{"an IPv6 metadata literal", true, "https://[fd00:ec2::254]/", "forbidden_address"},
		{"a mapped metadata literal", true, "https://[::ffff:169.254.169.254]/", "forbidden_address"},
		{"a private literal on the public transport", false, "https://10.0.0.5/v1", "private_address"},
		{"a loopback literal on the public transport", false, "https://127.0.0.1:8443/v1", "private_address"},
		{"an IPv6 loopback literal", false, "https://[::1]:8443/v1", "private_address"},
		{"a mapped loopback literal", false, "https://[::ffff:127.0.0.1]:8443/v1", "private_address"},
		{"a NAT64 literal of loopback", false, "https://[64:ff9b::7f00:1]/v1", "private_address"},
		{"a forbidden literal on the public transport", false, "https://169.254.169.254/", "forbidden_address"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := g.Transport(c.private).RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
				t.Fatalf("answered %d", resp.StatusCode)
			}
			be := blocked(t, err)
			if be.Reason != c.reason {
				t.Errorf("reason %q, want %q", be.Reason, c.reason)
			}
			u, _ := url.Parse(c.url)
			for _, leak := range []string{u.Hostname(), "synthetic-pw", "169.254", "127.0.0.1", "10.0.0.5"} {
				if leak != "" && strings.Contains(err.Error(), leak) && !strings.Contains(c.reason, leak) {
					t.Errorf("the error text holds %q: %v", leak, err)
				}
			}
		})
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("%d dial attempts for requests the guard refuses before dialling", n)
	}
}
