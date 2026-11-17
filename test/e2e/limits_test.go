package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const anthropicLimitsSecret = "synthetic-credential-anthropic-0501"

// limitsProc starts the binary with the fake provider behind both families.
func limitsProc(t *testing.T, level string) (*proc, *fakeprovider.Server, string) {
	t.Helper()
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: level, up: up})
	boot := p.bootstrap(t)
	p.prepareUpstream(t)
	p.useAnthropicFake(t, boot, up, fakeprovider.ModelBasic)
	up.Accept(fixtures.CanaryCredential, anthropicLimitsSecret)
	return p, up, boot
}

// limitsTenant is a tenant with a key and a credential for each family.
type limitsTenant struct {
	id    tenant.ID
	idStr string
	admin string
	key   string
}

func (p *proc) newLimitsTenant(t *testing.T, boot, name string) limitsTenant {
	t.Helper()
	id, admin := p.newTenant(t, boot, name)
	_, key := p.newInferenceKey(t, admin, id)
	p.addCredential(t, admin, "openai", fixtures.CanaryCredential)
	p.addCredential(t, admin, "anthropic", anthropicLimitsSecret)
	tid, err := tenant.ParseID(id)
	if err != nil {
		t.Fatal(err)
	}
	return limitsTenant{id: tid, idStr: id, admin: admin, key: key}
}

func (p *proc) openaiCall(t *testing.T, key string) (int, string) {
	t.Helper()
	return p.chatCall(t, key, fakeprovider.ModelBasic, false)
}

func (p *proc) anthropicCall(t *testing.T, key string) (int, string) {
	t.Helper()
	return exchangeWith(t, p.client, "POST", p.url(anthropicPath), anthropicHeader(key, true), messagesBody(fakeprovider.ModelBasic, false))
}

// assertLimitHeaders checks the headers of section 5.8 of a refusal of the tenant's requests limit.
func assertLimitHeaders(t *testing.T, what, text string, limit int) {
	t.Helper()
	for _, want := range []string{
		"X-Pandokos-Limit-Subject: tenant", "X-Pandokos-Limit-Metric: requests_per_minute",
		fmt.Sprintf("X-Pandokos-Limit: %d", limit), "X-Pandokos-Limit-Remaining: 0", "Retry-After: ", "X-Pandokos-Limit-Reset: ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: the refusal lacks %q\n%s", what, want, text)
		}
	}
}

// TestRateLimit is T-643: the binary, both families.
func TestRateLimit(t *testing.T) {
	p, _, boot := limitsProc(t, "info")

	t.Run("openai: the sixth call of five is the 429, the ledger has six rows, a raised limit admits within 2 s", func(t *testing.T) {
		tn := p.newLimitsTenant(t, boot, "limit-openai")
		p.db.SeedLimitRule(t, tn.id, "tenant", "", "requests_per_minute", 5)
		for i := 1; i <= 5; i++ {
			if status, text := p.openaiCall(t, tn.key); status != 200 {
				t.Fatalf("call %d: %d\n%s", i, status, text)
			}
		}
		status, text := p.openaiCall(t, tn.key)
		assertPandokosError(t, "the sixth call", status, text, 429, "rate_limited")
		assertLimitHeaders(t, "the sixth call", text, 5)

		rows := waitRows(t, p.db, tn.idStr, 6)
		ok, rejected := 0, 0
		for _, r := range rows {
			switch r["outcome"] {
			case "ok":
				ok++
			case "rejected_limit":
				rejected++
				if r["error_code"] != "rate_limited" || r["credential_id"] != nil || r["cost_status"] != "none" {
					t.Errorf("the rejected row: %v", r)
				}
			}
		}
		if ok != 5 || rejected != 1 {
			t.Errorf("%d ok rows and %d rejected_limit rows, want 5 and 1", ok, rejected)
		}

		p.db.SetLimitRule(t, tn.id, "tenant", "", "requests_per_minute", 100)
		testutil.Eventually(t, 2*time.Second, "the raised limit to admit", func() bool {
			status, _ := p.openaiCall(t, tn.key)
			return status == 200
		})
	})

	t.Run("anthropic: the same", func(t *testing.T) {
		tn := p.newLimitsTenant(t, boot, "limit-anthropic")
		p.db.SeedLimitRule(t, tn.id, "tenant", "", "requests_per_minute", 5)
		for i := 1; i <= 5; i++ {
			if status, text := p.anthropicCall(t, tn.key); status != 200 {
				t.Fatalf("call %d: %d\n%s", i, status, text)
			}
		}
		status, text := p.anthropicCall(t, tn.key)
		assertAnthropicPandokosError(t, "the sixth call", status, text, 429, "rate_limited", "rate_limit_error")
		assertLimitHeaders(t, "the sixth call", text, 5)
		rows := waitRows(t, p.db, tn.idStr, 6)
		rejected := 0
		for _, r := range rows {
			if r["outcome"] == "rejected_limit" {
				rejected++
			}
		}
		if rejected != 1 {
			t.Errorf("%d rejected_limit rows, want 1", rejected)
		}
		p.db.SetLimitRule(t, tn.id, "tenant", "", "requests_per_minute", 100)
		testutil.Eventually(t, 2*time.Second, "the raised limit to admit", func() bool {
			status, _ := p.anthropicCall(t, tn.key)
			return status == 200
		})
	})

	t.Run("with no rule nothing is refused", func(t *testing.T) {
		tn := p.newLimitsTenant(t, boot, "limit-none")
		for i := 0; i < 25; i++ {
			if status, text := p.openaiCall(t, tn.key); status != 200 {
				t.Fatalf("call %d: %d\n%s", i, status, text)
			}
		}
		if status, text := p.anthropicCall(t, tn.key); status != 200 {
			t.Fatalf("anthropic: %d\n%s", status, text)
		}
	})
}

// TestLimitsPerTenant is T-644.
func TestLimitsPerTenant(t *testing.T) {
	p, _, boot := limitsProc(t, "info")
	admitted := func(t *testing.T, tn limitsTenant, n int) int {
		t.Helper()
		got := 0
		for i := 0; i < n; i++ {
			status, text := p.openaiCall(t, tn.key)
			switch status {
			case 200:
				got++
			case 429:
				return got
			default:
				t.Fatalf("status %d\n%s", status, text)
			}
		}
		return got
	}

	t.Run("one tenant exhausted, the other unaffected", func(t *testing.T) {
		a, b := p.newLimitsTenant(t, boot, "limit-a"), p.newLimitsTenant(t, boot, "limit-b")
		for _, tn := range []limitsTenant{a, b} {
			p.db.SeedLimitRule(t, tn.id, "tenant", "", "requests_per_minute", 3)
		}
		if n := admitted(t, a, 10); n != 3 {
			t.Fatalf("A: %d admitted, want 3", n)
		}
		if status, text := p.openaiCall(t, a.key); status != 429 {
			t.Fatalf("A is not exhausted: %d\n%s", status, text)
		}
		if n := admitted(t, b, 10); n != 3 {
			t.Errorf("B: %d admitted, want 3: A's traffic took B's room", n)
		}
	})

	t.Run("a platform default applies to both", func(t *testing.T) {
		p.db.SeedPlatformLimit(t, nil, "requests_per_minute", 4)
		c, d := p.newLimitsTenant(t, boot, "limit-c"), p.newLimitsTenant(t, boot, "limit-d")
		for _, tn := range []limitsTenant{c, d} {
			if n := admitted(t, tn, 10); n != 4 {
				t.Errorf("%s: %d admitted, want 4", tn.idStr, n)
			}
		}
	})

	t.Run("a platform row for one tenant changes that tenant only", func(t *testing.T) {
		e, f := p.newLimitsTenant(t, boot, "limit-e"), p.newLimitsTenant(t, boot, "limit-f")
		p.db.SeedPlatformLimit(t, &e.id, "requests_per_minute", 2)
		if n := admitted(t, e, 10); n != 2 {
			t.Errorf("E: %d admitted, want 2", n)
		}
		if n := admitted(t, f, 10); n != 4 {
			t.Errorf("F: %d admitted, want 4 (the default)", n)
		}
	})
}

// TestLimitCanary is T-645: at log level debug nothing of a refused and a settled request leaks:
// not the body, not the provider credential, not the virtual key.
func TestLimitCanary(t *testing.T) {
	p, _, boot := limitsProc(t, "debug")
	tn := p.newLimitsTenant(t, boot, "limit-canary")
	p.db.SeedLimitRule(t, tn.id, "tenant", "", "requests_per_minute", 2)

	type response struct{ what, text string }
	var responses []response
	record := func(what string, status int, text string, want int) {
		t.Helper()
		if status != want {
			t.Fatalf("%s: status %d, want %d\n%s", what, status, want, text)
		}
		responses = append(responses, response{what, text})
	}
	status, text := p.chatCall(t, tn.key, fakeprovider.ModelBasic, true)
	record("settled stream", status, text, 200)
	status, text = p.chatCall(t, tn.key, fakeprovider.ModelBasic, false)
	record("settled", status, text, 200)
	status, text = p.chatCall(t, tn.key, fakeprovider.ModelBasic, false)
	record("refused", status, text, 429)
	assertPandokosError(t, "refused", status, text, 429, "rate_limited")
	status, text = p.anthropicCall(t, tn.key)
	record("refused, anthropic", status, text, 429)

	waitRows(t, p.db, tn.idStr, 4)
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	outputs := []response{{"stdout", p.stdout.String()}, {"stderr", p.stderr.String()}}
	for _, table := range []string{"usage_record", "audit_event", "platform_audit_event", "limit_rule", "platform_limit_rule"} {
		var dump string
		if err := p.db.Super(t).QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '') FROM pandokos."+table+" t").Scan(&dump); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, response{table, dump})
	}
	secrets := testutil.KeyForms(tn.key)
	secrets = append(secrets, testutil.SecretForms(fixtures.CanaryCredential)...)
	secrets = append(secrets, testutil.SecretForms(anthropicLimitsSecret)...)
	secrets = append(secrets, testutil.SecretForms(fixtures.CanaryProviderKey)...)
	secrets = append(secrets, p.db.Passwords()...)
	for _, o := range outputs {
		testutil.AssertNoCanary(t, o.what, o.text)
		testutil.AssertAbsent(t, o.what, o.text, secrets...)
	}
	for _, r := range responses {
		testutil.AssertAbsent(t, "the response "+r.what, r.text, secrets...)
		if strings.Contains(r.text, "X-Pandokos-Error") {
			testutil.AssertNoCanary(t, "the Pandokos error "+r.what, r.text)
		}
	}
	// The refusals were logged: a test that finds nothing proves nothing.
	if n := len(logLines(t, p.stdout.String(), "request")); n < 4 {
		t.Errorf("%d request log lines at debug level, want at least 4", n)
	}
}
