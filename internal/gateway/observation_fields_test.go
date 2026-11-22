package gateway_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// fieldNames lists the fields of a struct type, nested struct and pointer-to-struct types included,
// as Type.Field paths.
func fieldNames(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		out = append(out, prefix+f.Name)
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, fieldNames(ft, prefix+f.Name+".")...)
		}
	}
	return out
}

// stringFields collects every string value of a struct, recursively.
func stringFields(v reflect.Value, path string, out map[string]string) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			stringFields(v.Elem(), path, out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			stringFields(v.Field(i), path+"."+v.Type().Field(i).Name, out)
		}
	case reflect.String:
		out[path] = v.String()
	}
}

// TestObservationHasNoTenant is T-911: the fields of RequestObservation and of its nested types are
// exactly the list of section 5.2.1, and a request authenticated with a principal full of canary
// identifiers, with canary metadata, yields an observation in which no string field holds any of
// them, except CredentialID on a provider 429.
func TestObservationHasNoTenant(t *testing.T) {
	want := []string{
		"Method", "Scheme", "Route", "Status", "Outcome", "ErrorCode", "LimitState", "Provider", "Family", "Operation", "Model", "Duration",
		"Forwarded", "Call", "FirstChunk", "Stream", "CallError", "UpstreamStatus", "CredentialID",
		"InputTokens", "OutputTokens", "InputSource", "OutputSource", "CostKnown", "CostAmount", "CostCurrency", "CostReason",
		"Refusal", "Refusal.Code", "Refusal.Metric", "Refusal.Level", "Refusal.SubjectKind", "Refusal.Scope",
		"Estimate", "Estimate.ReservedTokens", "Estimate.SettledTokens", "Estimate.ReservedCost", "Estimate.SettledCost", "Estimate.CostComparable",
	}
	got := fieldNames(reflect.TypeOf(gateway.RequestObservation{}), "")
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the fields of RequestObservation are\n%v\nwant\n%v", got, want)
	}

	// A principal and a request full of identifiers.
	p := testutil.InferencePrincipal()
	canaries := []string{p.TenantID.String(), p.ID, p.KeyID, "tenant-name-" + fixtures.CanaryPrompt, "metadata-canary-9d1f", testKey, "canary-sa-name-77c1"}
	body := []byte(`{"model":"fake-basic","messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}],"metadata":{"order":"metadata-canary-9d1f"}}`)

	assertClean := func(t *testing.T, o gateway.RequestObservation, allowCredential bool) {
		t.Helper()
		strs := map[string]string{}
		stringFields(reflect.ValueOf(o), "", strs)
		for path, v := range strs {
			if allowCredential && path == ".CredentialID" {
				continue
			}
			for _, c := range append(canaries, fixtures.CanaryPrompt, fixtures.CanaryCompletion, fixtures.CanaryProviderKey) {
				if c != "" && strings.Contains(v, c) {
					t.Errorf("%s holds the canary %q", path, c)
				}
			}
		}
	}

	t.Run("an answered request", func(t *testing.T) {
		ob := &observer{}
		h := newHarness(t, observed(ob, nil))
		h.sendAuthed(t, route, body)
		assertClean(t, ob.wait(t, 1)[0], false)
	})
	t.Run("a refusal", func(t *testing.T) {
		ob := &observer{}
		h := newHarness(t, observed(ob, func(o *gateway.Options) {
			o.Limits = testutil.NewStaticLimits().Refuse(gateway.LimitRefusal{
				Code: "rate_limited", Level: "tenant", Subject: "key", Metric: "requests_per_minute", Scope: "all", Limit: 5,
			})
		}))
		h.sendAuthed(t, route, body)
		assertClean(t, ob.wait(t, 1)[0], false)
	})
	t.Run("a provider 429 carries the credential ID and nothing else of the tenant", func(t *testing.T) {
		ob := &observer{}
		h := newHarness(t, observed(ob, nil))
		h.sendAuthed(t, route, chat(fakeprovider.ModelError429, false))
		o := ob.wait(t, 1)[0]
		if o.CredentialID == "" {
			t.Error("no credential ID on a provider 429")
		}
		assertClean(t, o, true)
	})
}
