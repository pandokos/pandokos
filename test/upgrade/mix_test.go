//go:build upgrade

package upgrade

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestVersionsDoNotMix is T-1131: an old instance left running while migrate runs turns not ready,
// naming the schema; what its listeners then answer is as the upgrade guide says; started again it
// refuses with the "newer than this binary" message; and the new binary refuses the schema of the
// previous release with the "older" message.
//
// Observed first, on the released v0.2.0 (rule 7 of the plan, kept for the guide's "if you forget to
// stop an instance"): once the schema is at 17 under it, v0.2.0 answers 503 {"status":"not_ready"} on
// /readyz of both listeners, and its log says "readiness check failed ... the schema changed: version
// 17, this binary needs version 16" for each of the three database roles; /healthz stays 200; and it
// keeps answering inference calls (200) and administration requests (200) with the code that knows
// the old schema. Readiness is what a load balancer or orchestrator is told; nothing stops the
// process.
func TestVersionsDoNotMix(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	newBin := instance.Build(t)
	loopback := []string{"PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0"}
	c := w.tenants["c"]

	do := func(method, url string, hdr http.Header, body string) (int, string) {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = hdr
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	call := func() int {
		status, _ := do("POST", "http://"+w.proc.Inference+"/openai/v1/chat/completions", http.Header{"Authorization": {"Bearer " + c.InferenceKey}, "Content-Type": {"application/json"}},
			`{"model":"`+fakeprovider.ModelBasic+`","messages":[{"role":"user","content":"mix"}]}`)
		return status
	}

	t.Run("the new binary refuses the schema of the previous release", func(t *testing.T) {
		code, out, errs := newBin.Run(t, append(append([]string{}, w.env...), loopback...), "serve")
		text := out + errs
		if code != 1 || !strings.Contains(text, "the database schema is older than this binary") || !strings.Contains(text, "schema version 16, this binary needs version 17") || !strings.Contains(text, "pandokos migrate") {
			t.Errorf("serve of this tree on the schema of v0.2.0: exit %d\n%s", code, text)
		}
		if strings.Contains(text, `"listening"`) {
			t.Error("the refused process listened")
		}
	})

	if status := call(); status != 200 {
		t.Fatalf("the previous release before the migration: %d", status)
	}
	code, out, errs := newBin.Run(t, w.env, "migrate")
	if code != 0 || !strings.Contains(out, "schema is at version 17") {
		t.Fatalf("migrate while the previous release runs: exit %d\n%s%s", code, out, errs)
	}

	t.Run("the instance left running turns not ready, naming the schema", func(t *testing.T) {
		for _, addr := range []string{w.proc.Inference, w.proc.Admin} {
			testutil.Eventually(t, 30*time.Second, "readiness of "+addr+" to fail", func() bool {
				status, _ := do("GET", "http://"+addr+"/readyz", nil, "")
				return status == http.StatusServiceUnavailable
			})
			if status, body := do("GET", "http://"+addr+"/readyz", nil, ""); status != 503 || !strings.Contains(body, "not_ready") {
				t.Errorf("/readyz of %s: %d %s, want 503 not_ready", addr, status, body)
			}
		}
		testutil.Eventually(t, 30*time.Second, "the log line that names the schema", func() bool {
			for _, l := range completeLogLines(w.proc.Stdout.String(), "readiness check failed") {
				if e, _ := l["error"].(string); strings.Contains(e, "the schema changed: version 17, this binary needs version 16") {
					return true
				}
			}
			return false
		})
	})

	t.Run("what it answers meanwhile", func(t *testing.T) {
		if status, body := do("GET", "http://"+w.proc.Inference+"/healthz", nil, ""); status != 200 || !strings.Contains(body, "ok") {
			t.Errorf("/healthz: %d %s, want 200: the process is alive", status, body)
		}
		if status := call(); status != 200 {
			t.Errorf("an inference call: %d, observed 200 on v0.2.0: it keeps serving with the code of the old schema", status)
		}
		if status, _ := do("GET", "http://"+w.proc.Admin+"/v1/me", http.Header{"Authorization": {"Bearer " + readFileTrim(t, c.Admin)}}, ""); status != 200 {
			t.Errorf("an administration request: %d, observed 200 on v0.2.0", status)
		}
	})

	t.Run("started again it refuses, naming the newer schema", func(t *testing.T) {
		w.proc.Stop(t)
		code, out, errs := w.inst.Bin.Run(t, append(append([]string{}, w.env...), loopback...), "serve")
		text := out + errs
		if code != 1 || !strings.Contains(text, "the database schema is newer than this binary") || !strings.Contains(text, "schema version 17, this binary knows version 16") {
			t.Errorf("serve of v0.2.0 on the schema 17: exit %d\n%s", code, text)
		}
	})
}
