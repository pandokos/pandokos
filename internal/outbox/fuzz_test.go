package outbox

import (
	"bytes"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/egress"
)

// noInputEcho fails when text holds a substring of input longer than 8 bytes that is not part of
// the fixed words: an error never repeats what was registered. Inputs of 8 bytes or fewer cannot
// show it and are skipped.
func noInputEcho(t *testing.T, text string, input []byte, fixed ...string) {
	t.Helper()
	if len(input) < 9 {
		return
	}
	for _, f := range fixed {
		text = strings.ReplaceAll(text, f, "\x00")
	}
	for _, part := range strings.Split(text, "\x00") {
		for i := 0; i+9 <= len(part); i++ {
			if bytes.Contains(input, []byte(part[i:i+9])) {
				t.Fatalf("the error repeats %q of the input: %q", part[i:i+9], text)
			}
		}
	}
}

// FuzzValidateURL is T-1114: ValidateURL never panics; an accepted URL parses, is https (or http
// for a host that is listed), has no user information or fragment, and a literal address in it is
// not of a class the guard refuses; the error never contains the input.
func FuzzValidateURL(f *testing.F) {
	for _, s := range []string{
		"https://hooks.example.test/in", "https://hooks.example.test:8443/a/b?c=d", "https://93.184.216.34:8443/in",
		"http://localhost:8080/in", "http://hooks.internal.example/in", "https://localhost/in", "https://169.254.169.254/latest",
		"https://[fd00:ec2::254]/", "https://[::ffff:127.0.0.1]/in", "https://10.1.2.3/in", "http://127.0.0.1/in",
		"https://user@hooks.example.test/in", "https://hooks.example.test/in#frag", "https://hooks.example.test/a b", "https:///in",
		"mailto:someone@example.test", "HTTPS://hooks.example.test", "https://hooks.example.test:99999/", "https://[::1", "", "x",
		"https://hooks.example.test/" + strings.Repeat("a", 2100), "https://[fe80::1%25eth0]/in", "https://0x7f.1/in", "https://2130706433/in",
		"https://hooks.example.test\x00/in", "https://hooks.example.test/\u202e",
	} {
		f.Add(s, "localhost,hooks.internal.example")
		f.Add(s, "")
	}
	f.Fuzz(func(t *testing.T, raw, hosts string) {
		if len(raw) > 1<<20 || len(hosts) > 1<<12 {
			t.Skip()
		}
		var private []string
		if hosts != "" {
			private = strings.Split(hosts, ",")
		}
		err := ValidateURL(raw, private)
		if err != nil {
			noInputEcho(t, err.Error(), []byte(raw), "outbox: the URL of a destination is not valid", "outbox: ", "the URL of a destination is not valid")
			return
		}
		if len(raw) > maxURL {
			t.Fatalf("a URL of %d bytes accepted", len(raw))
		}
		u, perr := url.Parse(raw)
		if perr != nil {
			t.Fatalf("an accepted URL does not parse: %v", perr)
		}
		if u.User != nil || u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") || u.Hostname() == "" || u.Opaque != "" {
			t.Fatalf("an accepted URL with user information, a fragment or no host: %.60q", raw)
		}
		host := u.Hostname()
		listed := false
		for _, h := range private {
			listed = listed || strings.EqualFold(h, host)
		}
		switch u.Scheme {
		case "https":
		case "http":
			if !listed {
				t.Fatalf("an http URL for a host that is not listed: %.60q", raw)
			}
		default:
			t.Fatalf("an accepted URL with the scheme %q", u.Scheme)
		}
		if a, err := netip.ParseAddr(host); err == nil {
			switch c := egress.Classify(a.Unmap().WithZone("")); {
			case c == egress.AddrForbidden:
				t.Fatalf("a literal address the guard forbids was accepted: %.60q", raw)
			case c == egress.AddrPrivate && !listed:
				t.Fatalf("a literal private address was accepted for a host that is not listed: %.60q", raw)
			}
		}
		for i := 0; i < len(raw); i++ {
			if raw[i] <= ' ' || raw[i] == 0x7f {
				t.Fatalf("a URL with a control character or a space was accepted: %.60q", raw)
			}
		}
	})
}
