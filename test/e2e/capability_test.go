package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestCapabilityAgreesWithGateway is T-545: for a matrix of catalogue and credential states made
// with the administration API, callable is true exactly when a call with an unpinned, unlisted key
// of the tenant passes steps 7 (resolution) and 8 (credential) of the gateway.
//
// Every state has its own provider, tenant and key, so that no cache entry of an earlier state
// can answer; the catalogue and credential caches are invalidated by notification, and the test
// waits for the gateway with testutil.Eventually until it agrees or the bound passes.
func TestCapabilityAgreesWithGateway(t *testing.T) {
	t.Parallel()
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, env: []string{"PANDOKOS_CACHE_CATALOG_TTL=1s", "PANDOKOS_CACHE_CREDENTIAL_TTL=1s"}})
	boot := p.bootstrap(t)
	p.upReady = true
	const secret = "synthetic-credential-capability-0304"
	up.Accept(secret)

	type env struct {
		provider, model string
		tenant, admin   string
		key             string
	}
	n := 0
	// newState makes a provider with an endpoint at the fake and an "all" model, and a tenant with
	// a key; the credential is the state's business.
	newState := func(t *testing.T, withEndpoint bool, availability string) *env {
		t.Helper()
		n++
		e := &env{provider: fmt.Sprintf("cap-%d", n), model: "fake-basic"}
		p.mustCLI(t, boot, "provider", "create", "--family", "anthropic", e.provider)
		if withEndpoint {
			p.mustCLI(t, boot, "endpoint", "create", "--base-url", up.URL(), "--private", "--default", e.provider, "default")
		}
		p.mustCLI(t, boot, "model", "create", "--availability", availability, e.provider+"/"+e.model)
		e.tenant, e.admin = p.newTenant(t, boot, fmt.Sprintf("cap-tenant-%d", n))
		_, e.key = p.newInferenceKey(t, e.admin, e.tenant)
		return e
	}
	gatewayPasses := func(e *env, model string) (bool, int) {
		status, _ := exchangeWith(t, p.client, "POST", p.url("/"+e.provider+"/v1/messages"), anthropicHeader(e.key, true), messagesBody(model, false))
		return status == http.StatusOK, status
	}
	// agree checks the answer of the capability check against the gateway's.
	agree := func(t *testing.T, name string, e *env, model string, wantCallable bool) {
		t.Helper()
		got := p.capabilityOf(t, e.admin, e.tenant, e.provider, model)
		if got.Callable != wantCallable {
			t.Errorf("%s: callable %v (reasons %v), want %v", name, got.Callable, got.Reasons, wantCallable)
		}
		if got.Callable != (len(got.Reasons) == 0) {
			t.Errorf("%s: callable %v with reasons %v", name, got.Callable, got.Reasons)
		}
		var passes bool
		var status int
		testutil.Eventually(t, 8*time.Second, name+": the gateway to agree with the capability check", func() bool {
			passes, status = gatewayPasses(e, model)
			return passes == got.Callable
		})
		if !got.Callable {
			switch status {
			case http.StatusForbidden, http.StatusNotFound, http.StatusConflict: // model_not_allowed, endpoint_not_supported, no_credential
			default:
				t.Errorf("%s: the gateway answered %d for a call the check refused", name, status)
			}
		}
	}
	credential := func(t *testing.T, e *env) string {
		t.Helper()
		return p.addCredential(t, e.admin, e.provider, secret)
	}

	t.Run("everything in place", func(t *testing.T) {
		e := newState(t, true, "all")
		credential(t, e)
		agree(t, "callable", e, e.model, true)
	})
	t.Run("a model that does not exist", func(t *testing.T) {
		e := newState(t, true, "all")
		credential(t, e)
		agree(t, "unknown model", e, "no-such-model", false)
	})
	t.Run("a provider without a default endpoint", func(t *testing.T) {
		e := newState(t, false, "all")
		credential(t, e)
		agree(t, "no endpoint", e, e.model, false)
	})
	t.Run("a disabled provider, then enabled again", func(t *testing.T) {
		e := newState(t, true, "all")
		credential(t, e)
		p.mustCLI(t, boot, "provider", "update", "--enabled=false", e.provider)
		agree(t, "disabled", e, e.model, false)
		p.mustCLI(t, boot, "provider", "update", "--enabled", e.provider)
		agree(t, "enabled again", e, e.model, true)
	})
	t.Run("a retired model", func(t *testing.T) {
		e := newState(t, true, "all")
		credential(t, e)
		p.mustCLI(t, boot, "model", "update", "--status", "retired", e.provider+"/"+e.model)
		agree(t, "retired", e, e.model, false)
	})
	t.Run("a selected model: not granted, then granted, then revoked", func(t *testing.T) {
		e := newState(t, true, "selected")
		credential(t, e)
		agree(t, "not granted", e, e.model, false)
		p.mustCLI(t, boot, "model", "grant", "--tenant", e.tenant, e.provider+"/"+e.model)
		agree(t, "granted", e, e.model, true)
		p.mustCLI(t, boot, "model", "revoke-grant", "--tenant", e.tenant, e.provider+"/"+e.model)
		agree(t, "revoked grant", e, e.model, false)
	})
	t.Run("a restriction of the tenant", func(t *testing.T) {
		e := newState(t, true, "all")
		credential(t, e)
		p.mustCLI(t, e.admin, "model-restriction", "set", "--tenant", e.tenant, "--none")
		agree(t, "restricted to nothing", e, e.model, false)
		p.mustCLI(t, e.admin, "model-restriction", "set", "--tenant", e.tenant, "--model", e.provider+"/"+e.model)
		agree(t, "restricted to the model", e, e.model, true)
		p.mustCLI(t, e.admin, "model-restriction", "clear", "--tenant", e.tenant)
		agree(t, "restriction cleared", e, e.model, true)
	})
	t.Run("no credential", func(t *testing.T) {
		e := newState(t, true, "all")
		agree(t, "no credential", e, e.model, false)
		got := p.capabilityOf(t, e.admin, e.tenant, e.provider, e.model)
		if got.Checks.Credential != adminapi.CredentialAvailabilityMissing {
			t.Errorf("credential %q", got.Checks.Credential)
		}
	})
	t.Run("a disabled credential, enabled again", func(t *testing.T) {
		e := newState(t, true, "all")
		id := credential(t, e)
		p.mustCLI(t, e.admin, "credential", "disable", "--tenant", e.tenant, id)
		agree(t, "disabled credential", e, e.model, false)
		p.mustCLI(t, e.admin, "credential", "enable", "--tenant", e.tenant, id)
		agree(t, "enabled credential", e, e.model, true)
	})
	t.Run("a revoked credential", func(t *testing.T) {
		e := newState(t, true, "all")
		id := credential(t, e)
		p.mustCLI(t, e.admin, "credential", "revoke", "--tenant", e.tenant, id)
		agree(t, "revoked credential", e, e.model, false)
	})
	t.Run("the default credential decides, not any credential", func(t *testing.T) {
		e := newState(t, true, "all")
		first := credential(t, e)
		second := credential(t, e)
		agree(t, "two active credentials", e, e.model, true)
		p.mustCLI(t, e.admin, "credential", "disable", "--tenant", e.tenant, first)
		agree(t, "the default disabled, the other active", e, e.model, false)
		p.mustCLI(t, e.admin, "credential", "set-default", "--tenant", e.tenant, second)
		agree(t, "the other made the default", e, e.model, true)
	})
	t.Run("a suspended tenant is refused by both", func(t *testing.T) {
		e := newState(t, true, "all")
		credential(t, e)
		p.mustCLI(t, boot, "tenant", "suspend", "--reason", "test", e.tenant)
		testutil.Eventually(t, 8*time.Second, "the gateway to refuse a suspended tenant", func() bool {
			ok, status := gatewayPasses(e, e.model)
			return !ok && status == http.StatusForbidden
		})
		code, _, errs := p.cli(t, e.admin, "capability", "check", "--tenant", e.tenant, "--provider", e.provider, "--model", e.model)
		if code == 0 || errs == "" {
			t.Errorf("capability check for a suspended tenant: exit %d, stderr %q", code, errs)
		}
	})
}
