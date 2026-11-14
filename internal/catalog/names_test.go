package catalog

import (
	"reflect"
	"strings"
	"testing"
)

// TestNames is T-378: provider names, endpoint URLs, model names and references.
func TestNames(t *testing.T) {
	t.Run("provider names", func(t *testing.T) {
		for _, ok := range []string{"openai", "a", "my-provider-1", "o3", "x" + strings.Repeat("y", 62), "v2", "vv", "healthz1", "azure-openai"} {
			if err := ValidateProviderName(ok); err != nil {
				t.Errorf("%q: %v", ok, err)
			}
		}
		for name, bad := range map[string]string{
			"empty": "", "capital": "OpenAI", "starts with a digit": "1a", "starts with a hyphen": "-a", "underscore": "a_b",
			"space": "a b", "slash": "a/b", "dot": "a.b", "64 characters": "x" + strings.Repeat("y", 63), "non-ASCII": "é",
			"trailing newline": "abc\n", "healthz": "healthz", "readyz": "readyz", "v1": "v1",
		} {
			if err := ValidateProviderName(bad); err == nil {
				t.Errorf("%s: %q accepted", name, bad)
			}
		}
		if want := []string{"healthz", "readyz", "v1"}; !reflect.DeepEqual(ReservedProviderNames, want) {
			t.Errorf("ReservedProviderNames = %v, want %v (T-391 keeps the check constraint equal)", ReservedProviderNames, want)
		}
		for _, r := range ReservedProviderNames {
			if err := ValidateProviderName(r); err == nil {
				t.Errorf("the reserved name %q is accepted", r)
			}
		}
	})

	t.Run("endpoint URLs", func(t *testing.T) {
		type c struct {
			in      string
			private bool
			want    string // the normalized form; "" for a refusal
			msg     string // a fragment of the refusal
		}
		long := "https://example.com/" + strings.Repeat("a", 2048-len("https://example.com/"))
		cases := []c{
			{in: "https://api.openai.com", want: "https://api.openai.com"},
			{in: "https://api.openai.com/", want: "https://api.openai.com"},
			{in: "https://api.example.com/v1/", want: "https://api.example.com/v1"},
			{in: "https://api.example.com//", msg: ""},
			{in: "HTTPS://API.Example.COM/V1", want: "https://api.example.com/V1"},
			{in: "https://api.example.com:8443/openai", want: "https://api.example.com:8443/openai"},
			{in: "https://eu.example.com/regional/path", want: "https://eu.example.com/regional/path"},
			{in: "https://8.8.8.8", want: "https://8.8.8.8"},
			{in: "https://[2606:4700:4700::1111]", want: "https://[2606:4700:4700::1111]"},
			{in: "https://localhost:11434", want: "https://localhost:11434"}, // names are not resolved at write time
			{in: long, want: long},
			{in: long + "a", msg: ""},
			// http only for a private endpoint
			{in: "http://llm.internal.example", msg: ""},
			{in: "http://llm.internal.example", private: true, want: "http://llm.internal.example"},
			{in: "https://llm.internal.example", private: true, want: "https://llm.internal.example"},
			// the scheme and the shape
			{in: "ftp://example.com"},
			{in: "example.com"},
			{in: "//example.com"},
			{in: "https://"},
			{in: "https:///path"},
			{in: "https:example.com"},
			{in: ""},
			{in: " https://example.com"},
			{in: "https://example.com "},
			{in: "https://exa mple.com"},
			{in: "https://example.com/a b"},
			{in: "file:///etc/passwd", private: true},
			{in: "javascript:alert(1)", private: true},
			// no user information, query or fragment
			{in: "https://user@example.com"},
			{in: "https://user:synthetic-password@example.com"},
			{in: "https://:@example.com"},
			{in: "https://example.com?x=1"},
			{in: "https://example.com/v1?x=1"},
			{in: "https://example.com/v1?"},
			{in: "https://example.com#frag"},
			{in: "https://example.com/v1#"},
			// IP literals of every range are refused unless the endpoint is private
			{in: "https://127.0.0.1", msg: "private: true"},
			{in: "https://127.255.255.254", msg: "private: true"},
			{in: "https://10.0.0.1", msg: "private: true"},
			{in: "https://172.16.0.1", msg: "private: true"},
			{in: "https://172.31.255.255", msg: "private: true"},
			{in: "https://192.168.1.1", msg: "private: true"},
			{in: "https://169.254.169.254", msg: "private: true"},
			{in: "https://0.0.0.0", msg: "private: true"},
			{in: "https://[::1]", msg: "private: true"},
			{in: "https://[::]", msg: "private: true"},
			{in: "https://[fd00::1]", msg: "private: true"},
			{in: "https://[fe80::1]", msg: "private: true"},
			{in: "https://[::ffff:127.0.0.1]", msg: "private: true"},
			{in: "https://[::ffff:10.1.2.3]", msg: "private: true"},
			{in: "https://127.0.0.1:8443/v1", msg: "private: true"},
			{in: "https://127.0.0.1", private: true, want: "https://127.0.0.1"},
			{in: "http://10.0.0.5:8000", private: true, want: "http://10.0.0.5:8000"},
			{in: "http://[::1]:8080/v1", private: true, want: "http://[::1]:8080/v1"},
			{in: "https://172.15.0.1", want: "https://172.15.0.1"}, // just outside 172.16.0.0/12
			{in: "https://172.32.0.1", want: "https://172.32.0.1"},
			{in: "https://11.0.0.1", want: "https://11.0.0.1"},
		}
		for _, k := range cases {
			got, err := ValidateEndpointURL(k.in, k.private)
			if k.want == "" {
				if err == nil {
					t.Errorf("%q (private %v): accepted as %q", k.in, k.private, got)
					continue
				}
				if k.msg != "" && !strings.Contains(err.Error(), k.msg) {
					t.Errorf("%q: the refusal is %q, want a mention of %q", k.in, err, k.msg)
				}
				for _, leak := range []string{"synthetic-password", "user@"} {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("%q: the refusal quotes user information: %v", k.in, err)
					}
				}
				continue
			}
			if err != nil {
				t.Errorf("%q (private %v): %v", k.in, k.private, err)
				continue
			}
			if got != k.want {
				t.Errorf("%q: normalized to %q, want %q", k.in, got, k.want)
			}
			if again, err := ValidateEndpointURL(got, k.private); err != nil || again != got {
				t.Errorf("%q: the normalized form is not stable: %q %v", k.in, again, err)
			}
		}
	})

	t.Run("model names", func(t *testing.T) {
		for _, ok := range []string{"gpt-4o", "gpt-4.1-mini", "o3", "a", "0x", "claude-3.5:latest", "meta-llama/Llama-3-70b", "org/team/model@v1+x",
			"Qwen2.5-7B-Instruct", "A" + strings.Repeat("b", 127)} {
			if err := ValidateModelName(ok); err != nil {
				t.Errorf("%q: %v", ok, err)
			}
		}
		for name, bad := range map[string]string{
			"empty": "", "leading slash": "/x", "leading hyphen": "-x", "leading dot": ".x", "leading colon": ":x", "space": "a b",
			"newline": "a\n", "tab": "a\tb", "non-ASCII": "é", "question mark": "a?b", "hash": "a#b", "backslash": `a\b`, "percent": "a%2fb",
			"129 characters": "A" + strings.Repeat("b", 128), "control": "a\x00b", "star": "a*",
		} {
			if err := ValidateModelName(bad); err == nil {
				t.Errorf("%s: %q accepted", name, bad)
			}
		}
	})

	t.Run("upstream identifiers", func(t *testing.T) {
		for _, ok := range []string{"gpt-4o", "a", "accounts/fireworks/models/llama-v3", "claude-3-5-sonnet@20240620", strings.Repeat("a", 256), "ft:gpt-4o:org::id", "~x"} {
			if err := ValidateUpstreamID(ok); err != nil {
				t.Errorf("%q: %v", ok, err)
			}
		}
		for name, bad := range map[string]string{
			"empty": "", "space": "a b", "tab": "a\tb", "newline": "a\n", "non-ASCII": "é", "DEL": "a\x7f", "NUL": "a\x00", "257 characters": strings.Repeat("a", 257),
			"leading space": " a",
		} {
			if err := ValidateUpstreamID(bad); err == nil {
				t.Errorf("%s: %q accepted", name, bad)
			}
		}
	})

	t.Run("references", func(t *testing.T) {
		for in, want := range map[string][2]string{
			"openai/gpt-4o":                 {"openai", "gpt-4o"},
			"ollama/meta-llama/Llama-3-70b": {"ollama", "meta-llama/Llama-3-70b"},
			"a/b":                           {"a", "b"},
			"anthropic/claude-sonnet-4-6":   {"anthropic", "claude-sonnet-4-6"},
			"my-host/org/team/model@v1":     {"my-host", "org/team/model@v1"},
		} {
			p, n, err := ParseRef(in)
			if err != nil || p != want[0] || n != want[1] {
				t.Errorf("ParseRef(%q) = %q, %q, %v; want %q, %q", in, p, n, err, want[0], want[1])
			}
		}
		for name, bad := range map[string]string{
			"empty": "", "no slash": "gpt-4o", "no provider": "/gpt-4o", "no model": "openai/", "only a slash": "/",
			"capital provider": "OpenAI/gpt-4o", "reserved provider": "v1/gpt-4o", "invalid model": "openai/-x", "space": "openai/gpt 4o",
			"provider with a space": "open ai/gpt-4o", "129 character model": "openai/A" + strings.Repeat("b", 128), "leading space": " openai/gpt-4o",
		} {
			if p, n, err := ParseRef(bad); err == nil {
				t.Errorf("%s: %q parsed as %q, %q", name, bad, p, n)
			}
		}
	})
}
