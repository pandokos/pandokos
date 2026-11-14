package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestCompatibleProviderByConfiguration is T-553: through the CLI only, a provider of family
// openai with a private endpoint that has a base path, a model, a grant and a credential serves
// a streamed chat call; the stream usage option then takes effect.
func TestCompatibleProviderByConfiguration(t *testing.T) {
	const basePath = "/compat-base"
	up := fakeprovider.New(t)
	up.SetBasePath(basePath)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	p.upReady = true

	p.mustCLI(t, boot, "provider", "create", "--family", "openai", "--display-name", "Compatible", "compat")
	p.mustCLI(t, boot, "endpoint", "create", "--base-url", up.URL()+basePath, "--private", "--default", "compat", "default")
	for _, m := range []string{fakeprovider.ModelCompatRefusesOption, fakeprovider.ModelCompatUsageLastChunk, fakeprovider.ModelBasic} {
		p.mustCLI(t, boot, "model", "create", "--availability", "selected", "compat/"+m)
	}
	const secret = "synthetic-credential-compat-0303"
	up.Accept(secret)
	id, admin := p.newTenant(t, boot, "compat-tenant")
	_, key := p.newInferenceKey(t, admin, id)
	for _, m := range []string{fakeprovider.ModelCompatRefusesOption, fakeprovider.ModelCompatUsageLastChunk, fakeprovider.ModelBasic} {
		p.mustCLI(t, boot, "model", "grant", "--tenant", id, "compat/"+m)
	}
	p.addCredential(t, admin, "compat", secret)

	const path = "/compat/v1/chat/completions"
	call := func(model string, stream bool) (int, string) {
		return exchangeWith(t, p.client, "POST", p.url(path), authHeader(key), chatBody(model, stream, fixtures.CanaryPrompt))
	}
	testutil.Eventually(t, 10*time.Second, "the compatible provider to be served", func() bool {
		status, _ := call(fakeprovider.ModelBasic, false)
		return status == 200
	})

	t.Run("by default the option is added, so a server that refuses it answers its own 400", func(t *testing.T) {
		before := len(up.Requests())
		status, text := call(fakeprovider.ModelCompatRefusesOption, true)
		if status != 400 || !strings.Contains(text, "unsupported_parameter") || strings.Contains(text, "X-Pandokos-Error") {
			t.Fatalf("%d\n%s", status, text)
		}
		reqs := up.Requests()[before:]
		if len(reqs) != 1 || reqs[0].Path != basePath+"/v1/chat/completions" || !strings.Contains(string(reqs[0].Body), `"stream_options":{"include_usage":true}`) {
			t.Fatalf("the provider saw %+v", reqs)
		}
	})

	t.Run("a server that reports usage anyway is read", func(t *testing.T) {
		if status, text := call(fakeprovider.ModelCompatUsageLastChunk, true); status != 200 || !strings.Contains(text, "data: [DONE]") {
			t.Fatalf("%d\n%s", status, text)
		}
	})

	t.Run("--stream-usage-option omit takes effect", func(t *testing.T) {
		out := p.mustCLI(t, boot, "provider", "update", "--stream-usage-option", "omit", "compat")
		if !strings.Contains(out, "omit") {
			t.Errorf("the update printed:\n%s", out)
		}
		testutil.Eventually(t, 10*time.Second, "the option to reach the gateway", func() bool {
			status, _ := call(fakeprovider.ModelCompatRefusesOption, true)
			return status == 200
		})
		last := up.Requests()[len(up.Requests())-1]
		if strings.Contains(string(last.Body), "stream_options") {
			t.Errorf("the option was sent to a provider set to omit: %s", last.Body)
		}
		if got := p.mustCLI(t, boot, "provider", "get", "compat"); !strings.Contains(got, "stream_usage_option: omit") {
			t.Errorf("provider get:\n%s", got)
		}
	})

	t.Run("the calls are recorded with the provider's name and family", func(t *testing.T) {
		rows := waitRowsAtLeast(t, id, p, 4)
		var refused, omitted, lastChunk int
		for _, r := range rows {
			if r["provider"] != "compat" || r["api_family"] != "openai" || r["operation"] != "chat.completions" {
				t.Errorf("row %v", r)
			}
			switch {
			case r["model"] == fakeprovider.ModelCompatRefusesOption && r["outcome"] == "upstream_error" && num(r["upstream_status"]) == 400:
				refused++
			case r["model"] == fakeprovider.ModelCompatRefusesOption && r["outcome"] == "ok" && r["stream"] == true:
				omitted++
				if r["input_source"] != "estimated" || r["output_source"] != "estimated" || num(r["estimated_output_tokens"]) < 1 {
					t.Errorf("the streamed call to a server that sends no usage: %v", r)
				}
			case r["model"] == fakeprovider.ModelCompatUsageLastChunk && r["outcome"] == "ok" && r["stream"] == true:
				lastChunk++
				if r["input_source"] != "provider" || num(r["input_tokens"]) != 9 || num(r["output_tokens"]) != 12 {
					t.Errorf("the streamed call with usage on the last chunk: %v", r)
				}
			}
		}
		if refused < 1 || omitted < 1 || lastChunk < 1 {
			t.Errorf("rows: %d refused, %d omitted, %d with usage on the last chunk:\n%v", refused, omitted, lastChunk, rows)
		}
		items := p.usageList(t, admin, id, "--provider", "compat")
		if len(items) < 4 {
			t.Errorf("usage list shows %d items", len(items))
		}
	})

	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	testutil.AssertNoCanary(t, "stdout", p.stdout.String())
	testutil.AssertAbsent(t, "stdout", p.stdout.String(), append(testutil.KeyForms(key), testutil.SecretForms(secret)...)...)
}

// waitRowsAtLeast waits for n rows of the tenant and returns every row.
func waitRowsAtLeast(t *testing.T, tenantID string, p *proc, n int) []map[string]any {
	t.Helper()
	var rows []map[string]any
	testutil.Eventually(t, 15*time.Second, "the usage rows of the tenant", func() bool {
		rows = usageRows(t, p.db, tenantID)
		return len(rows) >= n
	})
	return rows
}
