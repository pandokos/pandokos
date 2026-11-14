package gateway_test

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// compatModels are the compatible profiles of the fake provider.
var compatModels = []string{
	fakeprovider.ModelCompatUsageLastChunk, fakeprovider.ModelCompatExtraUsage,
	fakeprovider.ModelCompatIgnoresOption, fakeprovider.ModelCompatRefusesOption,
}

// addCompat gives the harness the provider "compat" (family openai): one target per model of the
// fake and of the profiles, at the fake's address plus basePath, with the stream usage option as
// given. The fake answers under basePath too.
func addCompat(h *harness, basePath string, streamUsage bool) {
	h.up.SetBasePath(basePath)
	for _, m := range append(append([]string(nil), anthropicModels...), compatModels...) {
		tgt := testutil.TargetFor(h.up, "compat", m)
		u, _ := url.Parse(h.up.URL() + basePath)
		tgt.BaseURL, tgt.StreamUsage = u, streamUsage
		h.targets["compat/"+m] = tgt
	}
}

// compatOp is an operation of the openai family reached through the provider "compat".
func compatOp(o opSpec) opSpec {
	o.path = strings.Replace(o.path, "/openai/", "/compat/", 1)
	return o
}

// TestCompatibleProvider is T-539: a provider "compat" of family openai with a base path serves
// every profile through all three operations, and its records carry its name and family.
func TestCompatibleProvider(t *testing.T) {
	const base = "/compat-base"
	for _, model := range compatModels {
		for _, op := range allOps {
			for _, stream := range []bool{false, true} {
				if stream && !op.streams {
					continue
				}
				name := model + " " + op.name
				if stream {
					name += " stream"
				}
				t.Run(name, func(t *testing.T) {
					h := newFamilies(t, nil)
					addCompat(h, base, false) // the provider needs no stream_options: refuses-option must work
					o := compatOp(op)
					c := h.sendAuthed(t, o.path, o.body(model, stream))
					if c.status != 200 {
						t.Fatalf("status %d: %s", c.status, c.body)
					}
					reqs := h.up.Requests()
					if len(reqs) != 1 || reqs[0].Path != base+strings.TrimPrefix(op.path, "/openai") {
						t.Fatalf("the provider saw %+v", reqs)
					}
					if reqs[0].Header.Get("Authorization") != "Bearer "+fixtures.CanaryProviderKey {
						t.Errorf("Authorization %q", reqs[0].Header.Get("Authorization"))
					}
					if bytes.Contains(reqs[0].Body, []byte("stream_options")) {
						t.Errorf("the option was spliced into a provider that omits it: %s", reqs[0].Body)
					}
					r := h.oneRecord(t)
					if r.Provider != "compat" || r.APIFamily != "openai" || r.Operation != op.name || r.Outcome != usage.OutcomeOK {
						t.Fatalf("record %+v", r)
					}
					if r.Endpoint == "" || r.Model != model {
						t.Errorf("record endpoint %q model %q", r.Endpoint, r.Model)
					}
					// A chat stream of a server that sends no usage is estimated; everything else is read.
					noUsageOnTheWire := op.name == opChat.name && stream && (model == fakeprovider.ModelCompatIgnoresOption || model == fakeprovider.ModelCompatRefusesOption)
					wantProvider := !noUsageOnTheWire
					if wantProvider {
						if r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider {
							t.Errorf("sources %q %q, want provider", r.InputSource, r.OutputSource)
						}
					} else {
						if r.InputSource != usage.SourceEstimated || r.OutputSource != usage.SourceEstimated {
							t.Errorf("sources %q %q, want estimated", r.InputSource, r.OutputSource)
						}
						if want := contentEvents(op, c.body); r.Estimated.OutputTokens != want {
							t.Errorf("estimated output %d, want the %d content events", r.Estimated.OutputTokens, want)
						}
					}
					if line := h.lastRequestLine(t); line["provider"] != "compat" || line["api_family"] != "openai" {
						t.Errorf("request line %v", line)
					}
				})
			}
		}
	}

	t.Run("the figures of each profile", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, base, false)
		c := compatOp(opChat)
		h.sendAuthed(t, c.path, c.body(fakeprovider.ModelCompatUsageLastChunk, true))
		h.sendAuthed(t, c.path, c.body(fakeprovider.ModelCompatExtraUsage, true))
		recs := h.settle(t, 2)
		for i, r := range recs {
			if r.Usage.InputTokens != 9 || r.Usage.OutputTokens != 12 {
				t.Errorf("record %d usage %+v", i, r.Usage)
			}
		}
		if r := recs[0]; len(r.Usage.Reported) != 3 {
			t.Errorf("last-chunk provider usage %v", r.Usage.Reported)
		}
		extra := recs[1].Usage.Reported
		if extra["prompt_cache_hit_tokens"] != 3 || extra["total_tokens"] != 21 {
			t.Errorf("the provider's integer members were not kept: %v", extra)
		}
		for k := range extra {
			if k == "queue_time" || strings.HasPrefix(k, "x_vendor") {
				t.Errorf("a fractional member %q was kept", k)
			}
		}
	})

	t.Run("a base path is kept and the model is replaced", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, base, true)
		alias := h.targets["compat/"+fakeprovider.ModelBasic]
		alias.Model = "synthetic-compat-alias"
		h.targets["compat/synthetic-compat-alias"] = alias
		c := compatOp(opChat)
		if got := h.sendAuthed(t, c.path, c.body("synthetic-compat-alias", false)); got.status != 200 {
			t.Fatalf("status %d", got.status)
		}
		reqs := h.up.Requests()
		if len(reqs) != 1 || reqs[0].Path != base+"/v1/chat/completions" || !strings.Contains(string(reqs[0].Body), `"model":"fake-basic"`) {
			t.Errorf("the provider saw %+v", reqs)
		}
	})

	t.Run("an anthropic-family path of a compat provider is not served", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, base, true)
		c := h.sendA(t, "/compat/v1/messages", messages(fakeprovider.ModelBasic, false), anthropicHdr())
		assertContract(t, "a messages call on an openai-family provider", c, family.CodeEndpointNotSupported)
		if len(h.up.Requests()) != 0 {
			t.Error("the provider was contacted")
		}
	})

	t.Run("a provider that is not in the catalogue is not served", func(t *testing.T) {
		h := newFamilies(t, func(o *gateway.Options) {
			o.Catalogue = testutil.StaticCatalogue(map[string]string{"openai": "openai"}, nil)
		})
		c := compatOp(opChat)
		assertContract(t, "unknown", h.sendAuthed(t, c.path, c.body("fake-basic", false)), family.CodeEndpointNotSupported)
	})
}
