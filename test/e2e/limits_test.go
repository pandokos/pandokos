package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
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

// limitRuleID is the ID in the text output of `limit create`.
func limitRuleID(t *testing.T, out string) string {
	t.Helper()
	m := reID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no rule ID in the output of limit create:\n%s", out)
	}
	return m[1]
}

// priceFake gives the fake model of a provider a price in USD (3 and 6 per million), so that a budget
// can measure it.
func (p *proc) priceFake(t *testing.T, boot, provider string) {
	t.Helper()
	p.mustCLI(t, boot, "price", "override", "--currency", "USD", "--rate", "input=3", "--rate", "output=6", provider+"/fake-basic")
}

// TestMonthlyBudget is T-686: the binary, a budget made with the CLI, calls until 429 quota_exceeded;
// the process is stopped and started and the next call is refused at once; raising the budget with
// the CLI admits the next call within 2 s; the audit log holds the two changes.
func TestMonthlyBudget(t *testing.T) {
	p, up, boot := limitsProc(t, "info")
	p.priceFake(t, boot, "openai")
	tn := p.newLimitsTenant(t, boot, "budget")
	// The reservation of a call is about 0.0246 dollars (4,096 output tokens at 6 per million); a settled
	// call costs 0.000099: this budget admits about nine calls.
	out := p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "cost_per_month", "--amount", "0.0255", "--currency", "USD")
	rule := limitRuleID(t, out)

	admitted := 0
	var refusal string
	for i := 0; i < 40 && refusal == ""; i++ {
		status, text := p.openaiCall(t, tn.key)
		switch status {
		case 200:
			admitted++
		case 429:
			refusal = text
		default:
			t.Fatalf("call %d: %d\n%s", i, status, text)
		}
	}
	if refusal == "" || admitted < 2 {
		t.Fatalf("%d calls admitted and no refusal", admitted)
	}
	assertPandokosError(t, "the refusal", 429, refusal, 429, "quota_exceeded")
	for _, want := range []string{"X-Pandokos-Limit-Metric: cost_per_month", "X-Pandokos-Limit-Currency: USD", "X-Pandokos-Limit-Scope: all", "X-Pandokos-Limit: 0.0255", "Retry-After: "} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal lacks %q\n%s", want, refusal)
		}
	}
	waitRows(t, p.db, tn.idStr, admitted+1)

	// A restart: the counters come back from the ledger and the first call is refused.
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	p2 := startBinary(t, opts{level: "info", up: up, db: p.db})
	p2.platformFile, p2.upReady = boot, true
	status, text := p2.openaiCall(t, tn.key)
	assertPandokosError(t, "the first call after the restart", status, text, 429, "quota_exceeded")

	// Raising the budget admits the next call within 2 s.
	p2.mustCLI(t, tn.admin, "limit", "update", "--tenant", tn.idStr, "--amount", "5", rule)
	testutil.Eventually(t, 2*time.Second, "the raised budget to admit", func() bool {
		status, _ := p2.openaiCall(t, tn.key)
		return status == 200
	})

	actions := auditActions(t, p.db, `SELECT action FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action LIKE 'limit.%' ORDER BY id`, tn.idStr)
	if strings.Join(actions, ",") != "limit.create,limit.update" {
		t.Errorf("audit actions %v, want limit.create and limit.update", actions)
	}
}

// TestLimitAdministration is T-687: with the CLI, a rate rule of 5.1, a quota, a scoped rule, a
// platform default, a soft rule; list, change, delete; `capability check` says exhausted; `limit
// events` shows the threshold.
func TestLimitAdministration(t *testing.T) {
	p, _, boot := limitsProc(t, "info")
	p.priceFake(t, boot, "openai")
	tn := p.newLimitsTenant(t, boot, "admin-limits")
	create := func(args ...string) string {
		t.Helper()
		return limitRuleID(t, p.mustCLI(t, tn.admin, append([]string{"limit", "create", "--tenant", tn.idStr}, args...)...))
	}
	rate := create("--metric", "requests_per_minute", "--limit", "1000")
	quota := create("--metric", "requests_per_day", "--limit", "3")
	scoped := create("--metric", "tokens_per_month", "--limit", "9000000", "--provider", "openai", "--model", "fake-basic")
	soft := create("--metric", "cost_per_month", "--amount", "0.0003", "--currency", "USD", "--soft", "--threshold", "80", "--threshold", "100")
	platform := limitRuleID(t, p.mustCLI(t, boot, "platform-limit", "create", "--for-tenant", tn.idStr, "--metric", "concurrent_requests", "--limit", "50"))

	list := p.mustCLI(t, tn.admin, "limit", "list", "--tenant", tn.idStr, "-o", "json")
	var doc struct {
		Items []adminapi.LimitRule `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &doc); err != nil || len(doc.Items) != 4 {
		t.Fatalf("limit list: %v, %d items\n%s", err, len(doc.Items), list)
	}
	for _, r := range doc.Items {
		switch r.ID {
		case soft:
			if r.Enforce || r.Amount != "0.0003" || r.Currency != "USD" || len(r.Thresholds) != 2 {
				t.Errorf("the soft rule: %+v", r)
			}
		case scoped:
			if r.Provider != "openai" || r.Model != "fake-basic" || r.Limit != 9000000 {
				t.Errorf("the scoped rule: %+v", r)
			}
		}
	}
	if out := p.mustCLI(t, tn.admin, "limit", "platform", "--tenant", tn.idStr, "-o", "json"); !strings.Contains(out, platform) {
		t.Errorf("limit platform does not show the ceiling of the tenant:\n%s", out)
	}
	if out := p.mustCLI(t, boot, "platform-limit", "list", "-o", "json"); !strings.Contains(out, platform) {
		t.Errorf("platform-limit list lacks the rule:\n%s", out)
	}
	if out := p.mustCLI(t, tn.admin, "limit", "get", "--tenant", tn.idStr, rate); !strings.Contains(out, "requests_per_minute") {
		t.Errorf("limit get:\n%s", out)
	}

	// Change and delete.
	p.mustCLI(t, tn.admin, "limit", "update", "--tenant", tn.idStr, "--limit", "2000", rate)
	if out := p.mustCLI(t, tn.admin, "limit", "get", "--tenant", tn.idStr, "-o", "json", rate); !strings.Contains(out, `"limit": 2000`) && !strings.Contains(out, `"limit":2000`) {
		t.Errorf("limit update did not change the value:\n%s", out)
	}
	p.mustCLI(t, boot, "platform-limit", "update", "--limit", "60", platform)
	p.mustCLI(t, boot, "platform-limit", "delete", platform)
	p.mustCLI(t, tn.admin, "limit", "delete", "--tenant", tn.idStr, rate)
	if code, _, _ := p.cli(t, tn.admin, "limit", "get", "--tenant", tn.idStr, rate); code == 0 {
		t.Error("a deleted rule is still there")
	}

	// Three calls use the quota of the day; the capability check says exhausted.
	for i := 0; i < 3; i++ {
		if status, text := p.openaiCall(t, tn.key); status != 200 {
			t.Fatalf("call %d: %d\n%s", i, status, text)
		}
	}
	waitRows(t, p.db, tn.idStr, 3)
	var got adminapi.Capability
	testutil.Eventually(t, 5*time.Second, "the capability check to say exhausted", func() bool {
		got = p.capabilityOf(t, tn.admin, tn.idStr, "openai", fakeprovider.ModelBasic)
		return got.Checks.Budget == adminapi.BudgetAvailabilityExhausted
	})
	if got.Callable || !strings.Contains(strings.Join(got.Reasons, ","), "budget_exhausted") {
		t.Errorf("capability: callable %v, reasons %v", got.Callable, got.Reasons)
	}
	if status, text := p.openaiCall(t, tn.key); status != 429 {
		t.Errorf("a fourth call: %d\n%s", status, text)
	}
	_ = quota

	// The soft budget crossed 80 % (3 × 99 millionths of 300) on the third call: the event is there.
	var events string
	testutil.Eventually(t, 5*time.Second, "the threshold event", func() bool {
		events = p.mustCLI(t, tn.admin, "limit", "events", "--tenant", tn.idStr, "-o", "json")
		return strings.Contains(events, "threshold_reached")
	})
	if !strings.Contains(events, soft) {
		t.Errorf("the event does not name the soft rule %s:\n%s", soft, events)
	}
	if out := p.mustCLI(t, tn.admin, "limit", "events", "--tenant", tn.idStr, "--kind", "exhausted", "-o", "json"); !strings.Contains(out, "exhausted") {
		t.Errorf("limit events --kind exhausted shows no exhausted event:\n%s", out)
	}
	if out := p.mustCLI(t, tn.admin, "limit", "events", "--tenant", tn.idStr, "--rule", soft, "-o", "json"); strings.Contains(out, quota) {
		t.Errorf("limit events --rule names another rule:\n%s", out)
	}
}

// TestQuotaCanary is T-688: at log level debug, with canaries in the body, the provider credential and
// the virtual key, no log line, record, event, audit row, header or error body holds them.
func TestQuotaCanary(t *testing.T) {
	p, _, boot := limitsProc(t, "debug")
	p.priceFake(t, boot, "openai")
	tn := p.newLimitsTenant(t, boot, "quota-canary")
	p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "requests_per_day", "--limit", "2", "--threshold", "50", "--soft")
	p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "cost_per_month", "--amount", "0.0001", "--currency", "USD", "--provider", "openai", "--soft", "--threshold", "100")
	p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "tokens_per_month", "--limit", "30", "--provider", "anthropic")

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
	record("a settled stream", status, text, 200)
	status, text = p.chatCall(t, tn.key, fakeprovider.ModelBasic, false)
	record("a settled call", status, text, 200)
	status, text = p.anthropicCall(t, tn.key) // the token quota of the anthropic provider: its estimate alone is over 30 tokens
	record("a quota refusal, anthropic", status, text, 429)
	assertAnthropicPandokosError(t, "the anthropic refusal", status, text, 429, "quota_exceeded", "rate_limit_error")
	// An unpriced model under an enforced budget: no_price (a soft budget never refuses).
	p.mustCLI(t, boot, "model", "create", "--availability", "all", "openai/fake-unpriced")
	// The budget is on the provider, whose other models have a price (a budget on a scope without any is refused).
	p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "cost_per_day", "--amount", "100", "--currency", "USD", "--provider", "openai")
	status, text = p.chatCall(t, tn.key, "fake-unpriced", false)
	record("no_price", status, text, 409)
	assertPandokosError(t, "no_price", status, text, 409, "no_price")

	// The rows of sprint 5.3: a rule deleted with its cause, a zone change, and (the tenant has rules,
	// so the second tenant) one day of unlimited traffic; the dumps below hold them and no secret.
	revoked, revokedKey := p.newInferenceKey(t, tn.admin, tn.idStr)
	p.mustCLI(t, tn.admin, "limit", "create", "--tenant", tn.idStr, "--metric", "requests_per_day", "--limit", "9", "--subject", "key", "--key", revoked)
	p.mustCLI(t, tn.admin, "key", "revoke", "--tenant", tn.idStr, "--reason", "leaked", revoked)
	p.mustCLI(t, tn.admin, "limit", "time-zone", "set", "--tenant", tn.idStr, "Europe/Rome")
	free := p.newLimitsTenant(t, boot, "quota-canary-free")
	if status, text := p.openaiCall(t, free.key); status != 200 {
		t.Fatalf("unlimited call: %d\n%s", status, text)
	}
	for _, c := range []struct{ what, tenant, action string }{
		{"limit.delete", tn.idStr, "limit.delete"}, {"tenant.time_zone_update", tn.idStr, "tenant.time_zone_update"},
		{"limit.unlimited_traffic", free.idStr, "limit.unlimited_traffic"},
	} {
		testutil.Eventually(t, 10*time.Second, "the audit row "+c.what, func() bool {
			return len(auditActions(t, p.db, `SELECT action FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = $2`, c.tenant, c.action)) > 0
		})
	}

	waitRows(t, p.db, tn.idStr, 4)
	testutil.Eventually(t, 5*time.Second, "an event row", func() bool {
		var n int
		_ = p.db.Super(t).QueryRow(t.Context(), `SELECT count(*) FROM pandokos.limit_event`).Scan(&n)
		return n > 0
	})
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(p.stdout.String()+p.stderr.String(), "limit_state") {
		t.Error("the request log lines carry no limit_state")
	}
	outputs := []response{{"stdout", p.stdout.String()}, {"stderr", p.stderr.String()}}
	for _, table := range []string{"usage_record", "audit_event", "platform_audit_event", "limit_rule", "platform_limit_rule", "limit_event"} {
		var dump string
		if err := p.db.Super(t).QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '') FROM pandokos."+table+" t").Scan(&dump); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, response{table, dump})
	}
	secrets := testutil.KeyForms(tn.key)
	secrets = append(secrets, testutil.KeyForms(revokedKey)...)
	secrets = append(secrets, testutil.KeyForms(free.key)...)
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
		// A settled response carries the model's completion, which is the canary of the fake provider.
		if strings.Contains(r.text, "X-Pandokos-Error") {
			testutil.AssertNoCanary(t, "the Pandokos error "+r.what, r.text)
		}
	}
	if n := len(logLines(t, p.stdout.String(), "request")); n < 4 {
		t.Errorf("%d request log lines at debug level, want at least 4", n)
	}
}
