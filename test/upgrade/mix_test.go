//go:build upgrade

package upgrade

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestVersionsDoNotMix is T-1131: what happens when the versions are not the ones the procedure
// has in mind. What it asserts depends on the schema step of the upgrade, derived (schema_test.go).
//
// When the upgrade crosses migrations (the schemas differ): an old instance left running while migrate
// runs turns not ready, naming the schema; what its listeners then answer is as the upgrade guide
// says; started again it refuses with the "newer than this binary" message; and the new binary
// refuses the schema of the previous release with the "older" message.
//
// Observed first, on the released v0.2.0 (rule 7 of the plan, kept for the guide's "if you forget to
// stop an instance"): once the schema is at 17 under it, v0.2.0 answers 503 {"status":"not_ready"} on
// /readyz of both listeners, and its log says "readiness check failed ... the schema changed: version
// 17, this binary needs version 16" for each of the three database roles; /healthz stays 200; and it
// keeps answering inference calls (200) and administration requests (200) with the code that knows
// the old schema. Readiness is what a load balancer or orchestrator is told; nothing stops the
// process.
//
// When it crosses none (a patch release, ADR 0075 point 3, or a minor without a migration) the property
// is the opposite one: the schemas are the same, so nothing refuses. The new binary starts against the
// schema of the previous release; migrate is a no-op; the previous instance, left running through it,
// stays ready and keeps serving, and started again afterwards it starts.
func TestVersionsDoNotMix(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	newBin := instance.Build(t)
	loopback := []string{"PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0"}
	c := w.tenants["c"]
	cross := schemaCrossing(t)
	t.Logf("the upgrade crosses %s", cross)

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

	prev := ReadPin(t).Version

	if !cross.None() {
		doesCross(t, w, newBin, cross, prev, loopback, do, call)
		return
	}
	sameSchema(t, w, newBin, cross, prev, do, call)
}

// doesCross is the test when the upgrade crosses migrations: the schemas differ, and every mix is
// refused or reported, naming the versions.
func doesCross(t *testing.T, w *world, newBin instance.Binary, cross crossing, prev string, loopback []string,
	do func(method, url string, hdr http.Header, body string) (int, string), call func() int) {
	c := w.tenants["c"]
	t.Run("the new binary refuses the schema of the previous release", func(t *testing.T) {
		code, out, errs := newBin.Run(t, append(append([]string{}, w.env...), loopback...), "serve")
		text := out + errs
		if want := fmt.Sprintf("schema version %d, this binary needs version %d", cross.Previous, cross.This); code != 1 || !strings.Contains(text, "the database schema is older than this binary") || !strings.Contains(text, want) || !strings.Contains(text, "pandokos migrate") {
			t.Errorf("serve of this tree on the schema of %s: exit %d, want %q\n%s", prev, code, want, text)
		}
		if strings.Contains(text, `"listening"`) {
			t.Error("the refused process listened")
		}
	})

	if status := call(); status != 200 {
		t.Fatalf("the previous release before the migration: %d", status)
	}
	code, out, errs := newBin.Run(t, w.env, "migrate")
	if want := fmt.Sprintf("schema is at version %d", cross.This); code != 0 || !strings.Contains(out, want) {
		t.Fatalf("migrate while the previous release runs: exit %d, want %q\n%s%s", code, want, out, errs)
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
		want := fmt.Sprintf("the schema changed: version %d, this binary needs version %d", cross.This, cross.Previous)
		testutil.Eventually(t, 30*time.Second, "the log line that names the schema", func() bool {
			for _, l := range completeLogLines(w.proc.Stdout.String(), "readiness check failed") {
				if e, _ := l["error"].(string); strings.Contains(e, want) {
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
		if want := fmt.Sprintf("schema version %d, this binary knows version %d", cross.This, cross.Previous); code != 1 || !strings.Contains(text, "the database schema is newer than this binary") || !strings.Contains(text, want) {
			t.Errorf("serve of %s on the schema %d: exit %d, want %q\n%s", prev, cross.This, code, want, text)
		}
	})
}

// sameSchema is the test when the upgrade crosses no migration: the previous release and this tree
// have the same schema, so no refusal is expected anywhere. The new binary starts on the schema the
// previous release made, next to the previous instance; migrate applies nothing and the previous
// instance stays ready and keeps serving through it; and the previous binary started again on the
// schema after migrate starts.
func sameSchema(t *testing.T, w *world, newBin instance.Binary, cross crossing, prev string,
	do func(method, url string, hdr http.Header, body string) (int, string), call func() int) {
	c := w.tenants["c"]
	adminOK := func(p *instance.Process) int {
		status, _ := do("GET", "http://"+p.Admin+"/v1/me", http.Header{"Authorization": {"Bearer " + readFileTrim(t, c.Admin)}}, "")
		return status
	}
	ready := func(t *testing.T, p *instance.Process, what string) {
		for _, addr := range []string{p.Inference, p.Admin} {
			if status, body := do("GET", "http://"+addr+"/readyz", nil, ""); status != 200 {
				t.Errorf("/readyz of %s on %s: %d %s, want 200", what, addr, status, body)
			}
		}
	}
	notReadyLogged := func(p *instance.Process) bool {
		return len(completeLogLines(p.Stdout.String(), "readiness check failed")) > 0
	}

	t.Run("the new binary starts on the schema of the previous release", func(t *testing.T) {
		p := newBin.WithCanaries(w.inst.Canaries).Serve(t, w.env)
		p.Ready(t)
		ready(t, p, "this tree")
		if status, _ := p.Call(t, c.InferenceKey, fakeprovider.ModelBasic); status != 200 {
			t.Errorf("an inference call on this tree against the schema of %s: %d, want 200", prev, status)
		}
		if status := adminOK(p); status != 200 {
			t.Errorf("an administration request on this tree against the schema of %s: %d, want 200", prev, status)
		}
		if code := p.Stop(t); code != 0 {
			t.Errorf("this tree exited %d", code)
		}
	})

	if status := call(); status != 200 {
		t.Fatalf("the previous release before migrate: %d", status)
	}
	code, out, errs := newBin.Run(t, w.env, "migrate")
	if want := fmt.Sprintf("schema is at version %d\n", cross.This); code != 0 || !strings.Contains(out, want) || strings.Contains(out, "applied ") {
		t.Fatalf("migrate with nothing to cross: exit %d, want it to apply nothing and say %q\n%s%s", code, strings.TrimSpace(want), out, errs)
	}

	t.Run("the previous instance left running stays ready and keeps serving", func(t *testing.T) {
		// Readiness is rechecked by the process every few seconds; wait past a few of its checks.
		time.Sleep(5 * time.Second)
		ready(t, w.proc, prev)
		if notReadyLogged(w.proc) {
			t.Errorf("%s logged a failed readiness check although the schema did not change:\n%s", prev, w.proc.Stdout.String())
		}
		if status, body := do("GET", "http://"+w.proc.Inference+"/healthz", nil, ""); status != 200 || !strings.Contains(body, "ok") {
			t.Errorf("/healthz: %d %s, want 200", status, body)
		}
		if status := call(); status != 200 {
			t.Errorf("an inference call on %s after migrate: %d, want 200", prev, status)
		}
		if status := adminOK(w.proc); status != 200 {
			t.Errorf("an administration request on %s after migrate: %d, want 200", prev, status)
		}
	})

	t.Run("started again the previous binary starts", func(t *testing.T) {
		if code := w.proc.Stop(t); code != 0 {
			t.Errorf("%s exited %d", prev, code)
		}
		p := w.inst.Serve(t)
		p.Ready(t)
		ready(t, p, prev)
		if status, _ := p.Call(t, c.InferenceKey, fakeprovider.ModelBasic); status != 200 {
			t.Errorf("an inference call on %s started again after migrate: %d, want 200", prev, status)
		}
		if code := p.Stop(t); code != 0 {
			t.Errorf("%s exited %d", prev, code)
		}
	})
}
