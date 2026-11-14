package config

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testcert"
	"github.com/pandokos/pandokos/test/testutil"
)

// proxyVariables are the environment variables that name a proxy and that Pandokos never uses.
var proxyVariables = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"}

// TestUpstreamConfiguration is T-447: the two keys of sprint 3.1, section 5.11.
func TestUpstreamConfiguration(t *testing.T) {
	envOf := envOfKeys()
	const caKey, proxyKey = "upstream.tls.ca_file", "upstream.proxy.url"

	t.Run("the keys, their environment names and defaults", func(t *testing.T) {
		if envOf[caKey] != "PANDOKOS_UPSTREAM_TLS_CA_FILE" || envOf[proxyKey] != "PANDOKOS_UPSTREAM_PROXY_URL" {
			t.Errorf("environment names: %q %q", envOf[caKey], envOf[proxyKey])
		}
		found := 0
		for _, k := range Keys() {
			if k.Name == caKey || k.Name == proxyKey {
				found++
				if k.Default != "" || k.Type == "" || k.Description == "" {
					t.Errorf("%s: %+v", k.Name, k)
				}
			}
		}
		if found != 2 {
			t.Errorf("%d of the two keys are in Keys()", found)
		}
		d := Defaults()
		if d.Upstream.TLS.CAFile != "" || d.Upstream.TLS.RootCAs != nil || d.Upstream.Proxy.URL != "" || d.Upstream.Proxy.ParsedURL != nil {
			t.Errorf("defaults: %+v", d.Upstream)
		}
		c, _ := mustLoad(t, validSource(t))
		if c.Upstream.TLS.RootCAs != nil || c.Upstream.Proxy.ParsedURL != nil {
			t.Errorf("a configuration that sets neither key has roots or a proxy: %+v", c.Upstream)
		}
	})

	t.Run("upstream.tls.ca_file", func(t *testing.T) {
		const marker = "synthetic-marker-ca-content"
		dir := t.TempDir()
		write := func(name, content string) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}
		crl := write("crl.pem", "-----BEGIN X509 CRL-----\n"+marker+"\n-----END X509 CRL-----\n")
		cases := []struct {
			name, file, msg string
		}{
			{"missing", filepath.Join(dir, "missing.pem"), "cannot be read"},
			{"a directory", dir, "cannot be read"},
			{"empty", write("empty.pem", ""), "holds no certificate"},
			{"not PEM", write("text.pem", marker+"\n"), "holds no certificate"},
			{"a PEM block of another type", crl, "holds no certificate"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				src := validSource(t)
				src.Environ = append(src.Environ, envOf[caKey]+"="+c.file)
				_, _, err := Load(src)
				msg, ok := problemMessage(err, caKey)
				if !ok || !strings.Contains(msg, c.msg) {
					t.Fatalf("problem %q (%v), want one containing %q", msg, ok, c.msg)
				}
				testutil.AssertAbsent(t, "the error", err.Error(), marker)
			})
		}

		t.Run("a valid file adds its certificates to the roots", func(t *testing.T) {
			ca, err := testcert.NewCA(time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			certPEM, _, err := ca.Issue([]string{"provider.example.test"}, []net.IP{net.ParseIP("127.0.0.1")}, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			file := write("ca.pem", string(ca.CertPEM()))
			src := validSource(t)
			src.Environ = append(src.Environ, envOf[caKey]+"="+file)
			c, _ := mustLoad(t, src)
			if c.Upstream.TLS.CAFile != file || c.Upstream.TLS.RootCAs == nil {
				t.Fatalf("CAFile %q, RootCAs %v", c.Upstream.TLS.CAFile, c.Upstream.TLS.RootCAs)
			}
			block, _ := pem.Decode(certPEM)
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: c.Upstream.TLS.RootCAs, DNSName: "provider.example.test"}); err != nil {
				t.Errorf("a leaf of the test authority does not verify against RootCAs: %v", err)
			}
		})

		t.Run("a file of the YAML configuration is read too", func(t *testing.T) {
			file := write("ca2.pem", string(mustCA(t).CertPEM()))
			src := validSource(t)
			src.File = writeFile(t, "upstream:\n  tls:\n    ca_file: "+file+"\n")
			c, _ := mustLoad(t, src)
			if c.Upstream.TLS.RootCAs == nil {
				t.Error("RootCAs not built")
			}
		})
	})

	t.Run("upstream.proxy.url", func(t *testing.T) {
		bad := []string{
			"ftp://proxy.example.test:3128",
			"http://",
			"http:///x",
			"proxy.example.test:3128",
			"//proxy.example.test:3128",
			"http://user:synthetic-pw@proxy.example.test:3128",
			"http://user@proxy.example.test:3128",
			"http://proxy.example.test:3128?a=b",
			"http://proxy.example.test:3128#f",
			"http://proxy.example.test:3128/path",
			"socks5://proxy.example.test:1080",
		}
		for _, v := range bad {
			t.Run(v, func(t *testing.T) {
				src := validSource(t)
				src.Environ = append(src.Environ, envOf[proxyKey]+"="+v)
				_, _, err := Load(src)
				if !hasProblem(err, proxyKey) {
					t.Fatalf("expected a problem under %s, got %v", proxyKey, err)
				}
				testutil.AssertAbsent(t, "the error", err.Error(), "synthetic-pw")
			})
		}
		for _, v := range []string{"http://proxy.example.test:3128", "https://proxy.example.test", "http://127.0.0.1:3128/", "http://[::1]:3128"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf[proxyKey]+"="+v)
			c, _ := mustLoad(t, src)
			if c.Upstream.Proxy.URL != v || c.Upstream.Proxy.ParsedURL == nil || c.Upstream.Proxy.ParsedURL.Host == "" {
				t.Errorf("%s: %+v", v, c.Upstream.Proxy)
			}
		}
	})

	t.Run("both keys are read for serve only", func(t *testing.T) {
		for name, src := range map[string]Source{"init": validInitSource(t), "migrate": validMigrateSource(t)} {
			src.Environ = append(src.Environ, envOf[proxyKey]+"=ftp://not-a-proxy", envOf[caKey]+"=/nonexistent/ca.pem")
			c, _ := mustLoad(t, src)
			if c.Upstream.TLS.RootCAs != nil || c.Upstream.Proxy.ParsedURL != nil {
				t.Errorf("%s read the upstream settings: %+v", name, c.Upstream)
			}
		}
	})

	t.Run("a proxy variable of the environment gives a warning that names it, not its value", func(t *testing.T) {
		const value = "http://synthetic-proxy-value.example.test:3128"
		for _, name := range proxyVariables {
			src := validSource(t)
			src.Environ = append(src.Environ, name+"="+value)
			_, ws := mustLoad(t, src)
			var msgs []string
			for _, w := range ws {
				if w.Key == proxyKey {
					msgs = append(msgs, w.Message)
				}
			}
			want := name + " is set in the environment and is not used; set upstream.proxy.url to send provider traffic through a proxy"
			if len(msgs) != 1 || msgs[0] != want {
				t.Errorf("%s: warnings %q, want %q", name, msgs, want)
			}
			for _, w := range ws {
				testutil.AssertAbsent(t, "a warning for "+name, w.Key+w.Message, value, "synthetic-proxy-value")
			}
		}
		t.Run("no warning when the key is set", func(t *testing.T) {
			src := validSource(t)
			src.Environ = append(src.Environ, "HTTPS_PROXY="+value, envOf[proxyKey]+"=http://proxy.example.test:3128")
			_, ws := mustLoad(t, src)
			for _, w := range ws {
				if w.Key == proxyKey {
					t.Errorf("unexpected warning: %s", w.Message)
				}
			}
		})
		t.Run("NO_PROXY alone is not a proxy", func(t *testing.T) {
			src := validSource(t)
			src.Environ = append(src.Environ, "NO_PROXY=localhost", "no_proxy=localhost")
			_, ws := mustLoad(t, src)
			for _, w := range ws {
				if w.Key == proxyKey {
					t.Errorf("unexpected warning: %s", w.Message)
				}
			}
		})
	})
}

func mustCA(t *testing.T) *testcert.CA {
	t.Helper()
	ca, err := testcert.NewCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// TestSkeletonRemoved is T-448: the section of sprint 1.1 is gone without a compatibility window.
func TestSkeletonRemoved(t *testing.T) {
	t.Run("a file that still has the section is refused", func(t *testing.T) {
		for name, content := range map[string]string{
			"the provider settings": "skeleton:\n  openai:\n    base_url: https://api.example.test/v1\n",
			"the section alone":     "skeleton: {}\n",
			"the old access token":  "skeleton:\n  access_token_file: /run/secrets/access_token\n",
		} {
			src := validSource(t)
			src.File = writeFile(t, content)
			_, _, err := Load(src)
			msg, ok := problemMessage(err, "file")
			if !ok || !strings.Contains(msg, "unknown key skeleton") {
				t.Errorf("%s: %q %v", name, msg, err)
			}
		}
	})

	t.Run("the old variables give the warning, without their value", func(t *testing.T) {
		const value = "synthetic-skeleton-value"
		for _, name := range []string{
			"PANDOKOS_SKELETON_OPENAI_BASE_URL",
			"PANDOKOS_SKELETON_OPENAI_API_KEY_FILE",
			"PANDOKOS_SKELETON_ACCESS_TOKEN_FILE",
		} {
			src := validSource(t)
			src.Environ = append(src.Environ, name+"="+value)
			_, ws := mustLoad(t, src)
			found := false
			for _, w := range ws {
				found = found || (w.Key == "environment" && w.Message == "unknown variable "+name+" ignored")
				testutil.AssertAbsent(t, "a warning for "+name, w.Key+w.Message, value)
			}
			if !found {
				t.Errorf("no warning for %s: %+v", name, ws)
			}
		}
	})

	t.Run("a valid serve configuration needs no provider key file", func(t *testing.T) {
		c, ws := mustLoad(t, validSource(t))
		if c == nil {
			t.Fatal("no configuration")
		}
		for _, w := range ws {
			if strings.Contains(strings.ToLower(w.Key+w.Message), "provider key") {
				t.Errorf("unexpected warning: %+v", w)
			}
		}
	})
}
