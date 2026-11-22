package telemetry_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/telemetry"
)

// errorCodes are the error codes of ADR 0020, the family.ErrorCode constants.
var errorCodes = []string{
	string(family.CodeUnauthenticated), string(family.CodeEndpointNotSupported), string(family.CodeRequestTooLarge),
	string(family.CodeUpstreamUnreachable), string(family.CodeUpstreamTimeout), string(family.CodeInternal),
	string(family.CodeForbidden), string(family.CodeTenantSuspended), string(family.CodeUnavailable),
	string(family.CodeModelNotAllowed), string(family.CodeNoCredential), string(family.CodeInvalidRequest),
	string(family.CodeRequestTimeout), string(family.CodeRateLimited), string(family.CodeQuotaExceeded), string(family.CodeNoPrice),
}

var (
	threeDigits = regexp.MustCompile(`^[1-5][0-9][0-9]$`)
	currencyRe  = regexp.MustCompile(`^[A-Z]{3}$`)
)

func in(v string, list ...[]string) bool {
	for _, l := range list {
		for _, x := range l {
			if x == v {
				return true
			}
		}
	}
	return false
}

// closedValue reports whether value is allowed for the attribute key on the instrument: a value of
// section 5.3.1, or a name of the platform catalogue (the provider and the model names the test
// gave), or the credential ID the test gave on M9.
func closedValue(instrument, key, value string, catalogue map[string]bool) bool {
	switch key {
	case "http.request.method":
		return in(value, []string{"GET", "POST", "HEAD", "PUT", "DELETE", "PATCH", "OPTIONS", "_OTHER"})
	case "url.scheme":
		return in(value, []string{"http", "https"})
	case "http.response.status_code":
		return threeDigits.MatchString(value)
	case "http.route":
		return value == "/{provider}/v1/chat/completions" || value == "/{provider}/v1/embeddings" || strings.HasPrefix(value, "/{provider}/v1/")
	case "error.type":
		if instrument == "pandokos.telemetry.export_failures" { // the classes of an export failure (M40)
			return in(value, []string{"timeout", "status", "connection", "other"})
		}
		if instrument == "http.server.request.duration" {
			return threeDigits.MatchString(value) && value >= "500" || in(value, outcomes)
		}
		return threeDigits.MatchString(value) && value >= "400" || in(value, callErrors)
	case "pandokos.outcome":
		return in(value, outcomes)
	case "pandokos.error_code":
		return in(value, errorCodes)
	case "pandokos.limit_state":
		return in(value, limitStates)
	case "pandokos.api_family":
		return in(value, families)
	case "gen_ai.operation.name":
		return in(value, operations)
	case "gen_ai.token.type":
		return in(value, tokenTypes)
	case "gen_ai.provider.name", "gen_ai.request.model", "pandokos.credential.id":
		return catalogue[value]
	case "pandokos.currency":
		return currencyRe.MatchString(value)
	case "pandokos.cost.reason":
		return in(value, costReasons)
	case "pandokos.limit.code":
		return in(value, limitCodes)
	case "pandokos.limit.metric":
		for _, m := range limits.Metrics() {
			if string(m) == value {
				return true
			}
		}
		return false
	case "pandokos.limit.level":
		return in(value, limitLevels)
	case "pandokos.limit.subject":
		return in(value, limitKinds)
	case "pandokos.limit.scope":
		return in(value, limitScopes)
	case "pandokos.limit.quantity":
		return in(value, quantities)
	case "pandokos.egress.reason":
		return in(value, egress.BlockReasons)
	case "pandokos.authn.reason":
		return in(value, authn.Reasons)
	case "pandokos.listener":
		return in(value, []string{"inference", "admin"})
	case "pandokos.authn.kind":
		return in(value, []string{"key", "token", "unknown"})
	case "pandokos.authn.result":
		return in(value, []string{"ok", "failed", "unavailable"})
	case "pandokos.jwks.document":
		return in(value, []string{"keys", "metadata"})
	case "pandokos.jwks.state":
		return in(value, []string{"fresh", "stale", "missing"})
	case "pandokos.signin.method":
		return in(value, []string{"password", "oidc"})
	case "pandokos.password.state":
		return in(value, []string{"active", "waiting"})
	case "pandokos.ledger.state":
		return in(value, []string{"in_use", "queued"})
	case "pandokos.support.cause":
		return in(value, []string{"closed", "ended"})
	case "pandokos.telemetry.state":
		return in(value, []string{"ended", "exported", "failed"})
	case "pandokos.telemetry.signal":
		return in(value, []string{"traces", "metrics"})
	case "pandokos.result":
		return in(value, []string{"ok", "failed", "locked", "disabled", "unavailable", "refused", "written", "rejected", "lost", "recorded", "dropped", "skipped",
			"blocked", "timeout", "status", "too_large", "invalid_document", "issuer_mismatch", "no_usable_key", "network"})
	}
	return false
}

// TestAttributesAreClosed is T-900: across a table of observations (every outcome, every error
// code, refusals, costs known and unknown, a 429) and every source, each data point's attribute keys
// are a subset of its instrument's list of section 5.3, each value is in section 5.3.1 or is the
// catalogue name the test gave, and no tenant-looking value or canary appears.
func TestAttributesAreClosed(t *testing.T) {
	canaries := []string{"tenant-canary-7f3a", "key-canary-91bd", "request-canary-2c4e", "sa-canary-55aa"}
	r := newRig(t, withFullSources)

	var table []telemetry.Request
	for _, o := range outcomes {
		for _, code := range append([]string{""}, errorCodes...) {
			q := chatRequest("model-a")
			q.Outcome, q.ErrorCode = o, code
			if code != "" {
				q.Status, q.Forwarded, q.Call = 400, false, 0
			}
			table = append(table, q)
		}
	}
	for _, st := range limitStates {
		q := chatRequest("model-a")
		q.LimitState = st
		table = append(table, q)
	}
	for _, op := range operations {
		q := chatRequest("model-b")
		q.Operation = op
		table = append(table, q)
	}
	for _, ce := range []string{"429", "500", "503", "timeout", "upstream_unreachable", "upstream_aborted", "cancelled", "_OTHER"} {
		q := streamRequest("model-a")
		q.CallError = ce
		table = append(table, q)
	}
	for _, reason := range costReasons {
		q := chatRequest("model-b")
		q.CostKnown, q.CostAmount, q.CostCurrency, q.CostReason = false, 0, "", reason
		table = append(table, q)
	}
	for _, cur := range []string{"EUR", "USD", "JPY"} {
		q := chatRequest("model-b")
		q.CostCurrency = cur
		table = append(table, q)
	}
	for _, code := range limitCodes {
		for _, kind := range limitKinds {
			for _, scope := range limitScopes {
				for _, level := range limitLevels {
					q := chatRequest("model-a")
					q.Forwarded, q.Status = false, 429
					q.Refusal = &telemetry.RefusalObservation{Code: code, Metric: "tokens_per_minute", Level: level, SubjectKind: kind, Scope: scope}
					table = append(table, q)
				}
			}
		}
	}
	rate := streamRequest("model-a")
	rate.UpstreamStatus, rate.CallError, rate.CredentialID = 429, "429", "cred-0007"
	table = append(table, rate)
	table = append(table, chatRequest("model-a"))
	est := chatRequest("model-b")
	est.Estimate = &telemetry.EstimateObservation{ReservedTokens: 10, SettledTokens: 5, ReservedCost: 1, SettledCost: 1, CostComparable: true}
	table = append(table, est)

	snap := r.observe(table...)
	catalogue := map[string]bool{"acme": true, "model-a": true, "model-b": true, "cred-0007": true}
	byName := wantByName()

	for name, s := range snap.Series {
		w, ok := byName[name]
		if !ok {
			t.Errorf("unknown instrument %s", name)
			continue
		}
		allowed := map[string]bool{}
		for _, a := range w.Attrs {
			allowed[a] = true
		}
		for _, p := range s.Points {
			for k, v := range p.Attrs {
				if !allowed[k] {
					t.Errorf("%s: attribute key %q is not in its list %v", name, k, w.Attrs)
				}
				if v == "_OTHER" && k != "http.request.method" && k != "error.type" {
					t.Errorf("%s: %s=_OTHER for a value of the table of observations", name, k)
				}
				if !closedValue(name, k, v, catalogue) {
					t.Errorf("%s: %s=%q is not a closed value or a catalogue name", name, k, v)
				}
				for _, c := range canaries {
					if strings.Contains(v, c) {
						t.Errorf("%s: %s holds a canary", name, k)
					}
				}
			}
			if name != "pandokos.provider.rate_limited" {
				if _, ok := p.Attrs["pandokos.credential.id"]; ok {
					t.Errorf("%s holds a credential ID", name)
				}
			}
		}
	}

	t.Run("a value outside its closed set is recorded as _OTHER, never as given", func(t *testing.T) {
		r := newRig(t, nil)
		q := chatRequest("model-a")
		q.Outcome, q.LimitState, q.CostKnown, q.CostReason = canaries[0], canaries[1], false, canaries[2]
		q.Refusal = &telemetry.RefusalObservation{Code: canaries[3], Metric: "tokens_per_minute", Level: canaries[0], SubjectKind: canaries[1], Scope: canaries[2]}
		snap := r.observe(q)
		for name, s := range snap.Series {
			for _, p := range s.Points {
				for k, v := range p.Attrs {
					for _, c := range canaries {
						if strings.Contains(v, c) {
							t.Errorf("%s: %s carries %q", name, k, v)
						}
					}
				}
			}
		}
		if got := snap.find(t, "pandokos.gateway.requests", attrs("pandokos.outcome", "_OTHER", "pandokos.limit_state", "_OTHER")); len(got) != 1 {
			t.Errorf("the guard did not record _OTHER for the outcome and the limit state: %+v", snap.points(t, "pandokos.gateway.requests"))
		}
		_ = time.Second
	})
}
