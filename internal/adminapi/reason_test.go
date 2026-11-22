package adminapi_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestReasonConstruct is the generated half of T-883: x-pandokos-reason: required sets
// Operation.ReasonRequired on exactly the nine operations of section 5.8 and generates the
// check of the trimmed reason, so that no handler has to remember it.
func TestReasonConstruct(t *testing.T) {
	want := []string{"activateRole", "deactivateRole", "disableTenantCredential", "openSupportAction", "reactivateTenant",
		"resetMemberPassword", "resetUserPassword", "revokeTenantCredential", "suspendTenant"}
	var got []string
	for _, op := range adminapi.Operations {
		if op.ReasonRequired {
			got = append(got, op.ID)
			if !op.HasBody || op.Method == "GET" || op.Method == "DELETE" {
				t.Errorf("%s requires a reason and is a %s with HasBody %v", op.ID, op.Method, op.HasBody)
			}
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the operations that require a reason are %v, want the nine %v", got, want)
	}

	fill := regexp.MustCompile(`\{(\w+)\}`)
	values := map[string]string{"tenant": tid, "credential": cid, "user": sid, "role": "reader", "action": sid}
	pathOf := func(op adminapi.Operation) string {
		return fill.ReplaceAllStringFunc(op.Path, func(m string) string {
			v, ok := values[m[1:len(m)-1]]
			if !ok {
				t.Fatalf("%s: no value for %s", op.ID, m)
			}
			return v
		})
	}
	extra := func(op adminapi.Operation, reason string) string {
		if op.ID == "openSupportAction" {
			return `{"reason":` + reason + `,"permissions":["member:read"]}`
		}
		return `{"reason":` + reason + `}`
	}
	for _, op := range adminapi.Operations {
		if !op.ReasonRequired {
			continue
		}
		t.Run(op.ID, func(t *testing.T) {
			r := newRig(t, 1<<20)
			path := pathOf(op)
			refused := map[string]string{
				"no body":           "",
				"an empty object":   `{}`,
				"an empty reason":   extra(op, `""`),
				"a blank reason":    extra(op, `"   \t\n "`),
				"a null reason":     extra(op, `null`),
				"a number":          extra(op, `3`),
				"another member":    `{"why":"because"}`,
				"a reason too long": extra(op, `"`+strings.Repeat("r", 501)+`"`),
			}
			for name, body := range refused {
				status, pb, raw := r.raw(t, op.Method, path, "application/json", body)
				if status != 400 || pb.Code != "invalid_request" {
					t.Errorf("%s: %d %s %s, want 400 invalid_request", name, status, pb.Code, raw)
				}
				if name == "an empty reason" || name == "a blank reason" {
					onReason := false
					for _, e := range pb.Errors {
						onReason = onReason || e.Field == "reason"
					}
					if !onReason {
						t.Errorf("%s: the errors %v are not on the member reason", name, pb.Errors)
					}
				}
				if in := r.f.last(op.ID); in != nil {
					t.Errorf("%s: the handler was called with %+v", name, in)
				}
			}
			// A reason is trimmed only to be checked: one with white space around text is accepted.
			status, _, raw := r.raw(t, op.Method, path, "application/json", extra(op, `"  because  "`))
			if status/100 != 2 {
				t.Errorf("a reason with text: %d %s", status, raw)
			}
			if r.f.last(op.ID) == nil {
				t.Error("the handler was not called with a valid reason")
			}
		})
	}

	// The check is in the generated code, not in the handlers.
	for _, id := range want {
		var found bool
		for _, op := range adminapi.Operations {
			found = found || op.ID == id
		}
		if !found {
			t.Errorf("the document has no operation %s", id)
		}
	}
}
