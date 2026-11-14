package gateway_test

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/server"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestFamilies is T-529: Families holds both adapters and returns a new map each time; Routes
// lists five routes with both permissions; server.New serves the Anthropic route.
func TestFamilies(t *testing.T) {
	t.Run("both adapters", func(t *testing.T) {
		fams := gateway.Families()
		var names []string
		for n, a := range fams {
			names = append(names, n)
			if a == nil || a.Name() != n {
				t.Errorf("the adapter of %q is %v", n, a)
			}
		}
		sort.Strings(names)
		if strings.Join(names, ",") != "anthropic,openai" {
			t.Fatalf("families %v", names)
		}
	})
	t.Run("a new map every time", func(t *testing.T) {
		a := gateway.Families()
		delete(a, "openai")
		a["synthetic"] = nil
		b := gateway.Families()
		if len(b) != 2 || b["openai"] == nil || b["synthetic"] != nil {
			t.Errorf("the second call returned %v", b)
		}
	})
	t.Run("five routes, both permissions each", func(t *testing.T) {
		want := map[string]bool{
			"POST /{provider}/v1/chat/completions": false, "POST /{provider}/v1/responses": false, "POST /{provider}/v1/embeddings": false,
			"POST /{provider}/v1/messages": false, "POST /{provider}/v1/messages/count_tokens": false,
		}
		routes := gateway.Routes()
		if len(routes) != 5 {
			t.Fatalf("%d routes: %+v", len(routes), routes)
		}
		for _, r := range routes {
			key := r.Method + " " + r.Path
			if _, ok := want[key]; !ok {
				t.Errorf("unexpected route %s", key)
				continue
			}
			want[key] = true
			perms := append([]string(nil), r.Permissions...)
			sort.Strings(perms)
			if strings.Join(perms, ",") != authz.CredentialUse+","+authz.ModelInvoke {
				t.Errorf("%s: permissions %v", key, r.Permissions)
			}
		}
		for key, seen := range want {
			if !seen {
				t.Errorf("no route %s", key)
			}
		}
	})
	t.Run("every operation of every adapter is a route", func(t *testing.T) {
		have := map[string]bool{}
		for _, r := range gateway.Routes() {
			have[r.Method+" "+r.Path] = true
		}
		for name, a := range gateway.Families() {
			for _, op := range a.Operations() {
				if !have[op.Method+" /{provider}"+op.Path] {
					t.Errorf("%s %s has no route", name, op.Name)
				}
			}
		}
	})
	t.Run("server.New serves /anthropic/v1/messages", func(t *testing.T) {
		up := fakeprovider.New(t)
		rec := &testutil.MemoryRecorder{}
		tp, _ := testutil.SpanRecorder(t)
		cfg := config.Defaults()
		cfg.Listeners.Inference.Address = "127.0.0.1:0"
		cfg.Listeners.Admin.Address = "127.0.0.1:0"
		srv, err := server.New(server.Options{
			Config: cfg, Logger: telemetry.NewLogger(io.Discard, "info"), Admin: http.NotFoundHandler(),
			Authenticator: testutil.StaticAuthenticator(map[string]authz.Principal{testKey: testPrincipal}),
			Catalogue: testutil.StaticCatalogue(map[string]string{"openai": "openai", "anthropic": "anthropic"}, map[string]gateway.Target{
				"anthropic/fake-basic": testutil.TargetForFamily(up, "anthropic", "anthropic", "fake-basic"),
				"openai/fake-basic":    testutil.TargetFor(up, "openai", "fake-basic"),
			}),
			Credentials: testutil.StaticCredentials(map[string]string{"openai": fixtures.CanaryProviderKey, "anthropic": fixtures.CanaryProviderKey}),
			Egress:      testutil.TestGuard(t), Recorder: rec, Tracer: tp,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.Listen(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.Serve(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		})
		testutil.Eventually(t, 5*time.Second, "readiness", func() bool { return srv.Health().Ready(context.Background()) })
		base := "http://" + srv.InferenceAddr().String()
		do := func(path string) (int, string) {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+path, strings.NewReader(string(messages("fake-basic", false))))
			req.Header.Set("X-Api-Key", testKey)
			req.Header.Set("Authorization", "Bearer "+testKey) // the same value in both: accepted by both families
			req.Header.Set("Anthropic-Version", anthropicVersion)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		status, body := do("/anthropic/v1/messages")
		if status != 200 || body != string(fixtures.ReadAnthropic("message.json")) {
			t.Fatalf("/anthropic/v1/messages: %d %s", status, body)
		}
		if status, _ := do("/anthropic/v1/messages/count_tokens"); status != 200 {
			t.Errorf("count_tokens: %d", status)
		}
		if status, _ := do("/openai/v1/chat/completions"); status != 200 {
			t.Errorf("the openai route no longer works: %d", status)
		}
		if n := len(up.Requests()); n != 3 {
			t.Errorf("the provider saw %d requests", n)
		}
	})
}
