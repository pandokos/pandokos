package gateway_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// messageOf returns the message of an error body of either shape.
func messageOf(t *testing.T, body []byte) string {
	t.Helper()
	var doc struct {
		Error struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return doc.Error.Message
}

// TestUnroutedErrorShape is T-532: a request that reaches no family is answered in the shape of
// the family that recognizes its path or its Anthropic-Version header, else openai; status, code
// and message do not depend on the shape.
func TestUnroutedErrorShape(t *testing.T) {
	t.Parallel()
	version := http.Header{"Anthropic-Version": {anthropicVersion}}
	merge := func(base, extra http.Header) http.Header {
		out := base.Clone()
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	type shape int
	const (
		openaiShape shape = iota
		anthropicShape
	)
	cases := []struct {
		name   string
		method string
		path   string
		hdr    http.Header
		want   shape
	}{
		{"an unknown provider on the messages path", "POST", "/nope/v1/messages", anthropicHdr(), anthropicShape},
		{"an unknown provider on the counting path", "POST", "/nope/v1/messages/count_tokens", anthropicHdr(), anthropicShape},
		{"unauthenticated, the same", "POST", "/nope/v1/messages", http.Header{}, anthropicShape},
		{"the version header on another path", "POST", "/nope/v1/anything", version, anthropicShape},
		{"the version header on a single-segment path", "POST", "/nope", version, anthropicShape},
		{"the version header and an openai path: the first adapter in name order", "POST", "/nope/v1/chat/completions", version, anthropicShape},
		{"a provider name that cannot be one, on the messages path", "POST", "/Nope.X/v1/messages", anthropicHdr(), anthropicShape},
		{"a provider of a family without an adapter, on the messages path", "POST", "/legacy/v1/messages", anthropicHdr(), anthropicShape},
		{"the openai chat path", "POST", "/nope/v1/chat/completions", authed(), openaiShape},
		{"the openai responses path", "POST", "/nope/v1/responses", authed(), openaiShape},
		{"an unknown path with no hint", "POST", "/nope/v1/anything", authed(), openaiShape},
		{"a path with no second segment and no hint", "POST", "/nope", authed(), openaiShape},
		{"the messages path with another method", "GET", "/nope/v1/messages", authed(), openaiShape},
		{"the root", "POST", "/", http.Header{}, openaiShape},
		{"a known anthropic provider on an unknown path, with the version header", "POST", "/anthropic/v1/messages/batches", merge(authed(), version), anthropicShape},
		{"a disabled or unknown provider with the bearer scheme and no hint", "POST", "/nope/v1/embeddings", authed(), openaiShape},
	}
	h := newFamilies(t, func(o *gateway.Options) {
		o.Catalogue = testutil.StaticCatalogue(map[string]string{"openai": "openai", "anthropic": "anthropic", "legacy": "gemini"}, nil)
	})
	var statusCode, message string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.send(t, c.method, c.path, messages("fake-basic", false), c.hdr)
			if got.status != 404 {
				t.Fatalf("status %d, want 404: %s", got.status, got.body)
			}
			if c.want == anthropicShape {
				assertAnthropicContract(t, c.name, got, family.CodeEndpointNotSupported)
			} else {
				assertContract(t, c.name, got, family.CodeEndpointNotSupported)
			}
			// Status, code and message are the same in both shapes.
			if statusCode == "" {
				statusCode, message = got.header.Get("X-Pandokos-Error"), messageOf(t, got.body)
			} else if got.header.Get("X-Pandokos-Error") != statusCode || messageOf(t, got.body) != message {
				t.Errorf("code %q message %q differ from %q %q", got.header.Get("X-Pandokos-Error"), messageOf(t, got.body), statusCode, message)
			}
			if len(h.up.Requests()) != 0 {
				t.Error("the provider was contacted")
			}
		})
	}

	t.Run("the shape is the family's for a known provider, whatever the hint", func(t *testing.T) {
		hh := newFamilies(t, nil)
		// An openai provider reached with an Anthropic-Version header and a bad body: its own shape.
		c := hh.send(t, http.MethodPost, route, []byte(`{}`), merge(authed(), version))
		assertContract(t, "openai provider", c, family.CodeInvalidRequest)
		// An anthropic provider without the header: the Anthropic shape.
		c = hh.send(t, http.MethodPost, anthropicRoute, []byte(`{}`), http.Header{"X-Api-Key": {testKey}})
		assertAnthropicContract(t, "anthropic provider", c, family.CodeInvalidRequest)
	})

	t.Run("a catalogue that cannot be read: the same choice, with 503", func(t *testing.T) {
		cat := testutil.StaticCatalogue(map[string]string{"openai": "openai", "anthropic": "anthropic"}, nil)
		cat.ProviderErr = errors.New("synthetic database failure " + fixtures.CanaryCompletion)
		hh := newFamilies(t, func(o *gateway.Options) { o.Catalogue = cat })
		for _, c := range []struct {
			name, path string
			hdr        http.Header
			want       shape
		}{
			{"messages", anthropicRoute, anthropicHdr(), anthropicShape},
			{"counting", countRoute, bearerHdr(), anthropicShape},
			{"chat", route, authed(), openaiShape},
			{"the version header", "/x/v1/other", merge(authed(), version), anthropicShape},
			{"no hint", "/x/v1/other", authed(), openaiShape},
		} {
			got := hh.send(t, http.MethodPost, c.path, messages("fake-basic", false), c.hdr)
			if got.status != 503 {
				t.Errorf("%s: status %d: %s", c.name, got.status, got.body)
				continue
			}
			if c.want == anthropicShape {
				assertAnthropicContract(t, c.name, got, family.CodeUnavailable)
			} else {
				assertContract(t, c.name, got, family.CodeUnavailable)
			}
			testutil.AssertNoCanary(t, c.name+": the response", got.dump())
		}
		testutil.AssertNoCanary(t, "the logs", hh.logs.String())
	})

	t.Run("the shape follows the path alone: the key is never read first", func(t *testing.T) {
		auth := &orderAuth{inner: testAuth}
		hh := newFamilies(t, func(o *gateway.Options) {
			o.Authenticator = auth
			o.Catalogue = testutil.StaticCatalogue(map[string]string{"openai": "openai"}, nil)
		})
		c := hh.send(t, http.MethodPost, "/nope/v1/messages", messages("fake-basic", false), anthropicHdr())
		assertAnthropicContract(t, "no authentication", c, family.CodeEndpointNotSupported)
		if auth.n.Load() != 0 {
			t.Errorf("the authenticator was asked %d times for a request that reaches no family", auth.n.Load())
		}
	})
}
