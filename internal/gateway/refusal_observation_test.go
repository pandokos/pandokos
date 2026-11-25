package gateway_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/limitstest"
)

// TestRefusalObservation is T-914: a refusal by a rule of a key scoped to a model gives
// SubjectKind=key, Scope=model, the metric and the code of the rule and Level=tenant; a platform
// ceiling gives Level=platform; no field holds the rule's ID, the subject's ID or the limit value.
func TestRefusalObservation(t *testing.T) {
	t.Parallel()
	const limit = 7
	const ruleID = "rule-id-canary-5b3e"
	const providerID = "synthetic-provider-openai"

	refused := func(t *testing.T, opts limitstest.Options) gateway.RequestObservation {
		t.Helper()
		ob := &observer{}
		real := limitstest.NewWith(t, opts)
		h := newHarness(t, observed(ob, func(o *gateway.Options) { o.Limits = real }))
		// A model's rule is found through the provider of the target: the harness's targets carry none.
		tgt := h.targets["openai/fake-basic"]
		tgt.ProviderID = providerID
		h.targets["openai/fake-basic"] = tgt
		for i := 0; i <= limit+1; i++ {
			h.sendAuthed(t, route, chat("fake-basic", false))
		}
		got := ob.wait(t, limit+2)
		for _, o := range got {
			if o.Refusal != nil {
				return o
			}
		}
		t.Fatalf("no observation of %d carries a refusal", len(got))
		return gateway.RequestObservation{}
	}

	// noIdentifiers fails on a string field holding an identifier of the rule or its subject, and on
	// a number equal to the limit's value.
	noIdentifiers := func(t *testing.T, o gateway.RequestObservation, ids ...string) {
		t.Helper()
		strs := map[string]string{}
		stringFields(reflect.ValueOf(o), "", strs)
		for path, v := range strs {
			for _, id := range ids {
				if id != "" && strings.Contains(v, id) {
					t.Errorf("%s holds the identifier %q", path, id)
				}
			}
			if v == strconv.Itoa(limit) {
				t.Errorf("%s holds the limit's value", path)
			}
		}
	}

	t.Run("a rule of a key, scoped to a model", func(t *testing.T) {
		rule := limits.Rule{
			ID: ruleID, SubjectKind: limits.SubjectKey, SubjectID: testPrincipal.KeyID, Metric: limits.RequestsPerMinute,
			Value: limit, ProviderID: providerID, ModelID: "synthetic-model-openai-fake-basic", Model: "fake-basic", Enforce: true,
		}
		o := refused(t, limitstest.Options{Rules: map[tenant.ID][]limits.Rule{testPrincipal.TenantID: {rule}}})
		want := gateway.LimitRefusalObservation{Code: "rate_limited", Metric: "requests_per_minute", Level: "tenant", SubjectKind: "key", Scope: "model"}
		if *o.Refusal != want {
			t.Errorf("refusal %+v, want %+v", *o.Refusal, want)
		}
		if o.Status != 429 || o.ErrorCode != "rate_limited" || o.Forwarded {
			t.Errorf("status %d code %q forwarded %v", o.Status, o.ErrorCode, o.Forwarded)
		}
		noIdentifiers(t, o, ruleID, testPrincipal.KeyID, testPrincipal.ID, testPrincipal.TenantID.String())
	})

	t.Run("a platform ceiling", func(t *testing.T) {
		rule := limits.Rule{ID: ruleID + "-platform", SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: limit, Enforce: true}
		o := refused(t, limitstest.Options{PlatformDefault: []limits.Rule{rule}})
		want := gateway.LimitRefusalObservation{Code: "rate_limited", Metric: "requests_per_minute", Level: "platform", SubjectKind: "tenant", Scope: "all"}
		if *o.Refusal != want {
			t.Errorf("refusal %+v, want %+v", *o.Refusal, want)
		}
		noIdentifiers(t, o, ruleID, testPrincipal.KeyID, testPrincipal.ID, testPrincipal.TenantID.String())
	})

	t.Run("a quota is a quota_exceeded refusal with its metric", func(t *testing.T) {
		rule := limits.Rule{ID: ruleID, SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerDay, Value: limit, Enforce: true}
		o := refused(t, limitstest.Options{Rules: map[tenant.ID][]limits.Rule{testPrincipal.TenantID: {rule}}})
		want := gateway.LimitRefusalObservation{Code: "quota_exceeded", Metric: "requests_per_day", Level: "tenant", SubjectKind: "tenant", Scope: "all"}
		if *o.Refusal != want {
			t.Errorf("refusal %+v, want %+v", *o.Refusal, want)
		}
	})
}
