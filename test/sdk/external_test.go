//go:build sdkexternal

package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// This file runs the Python and TypeScript SDK suites (python/ and ts/) as subprocesses against
// the in-process Pandokos and the fake provider. It is built only with the tag sdkexternal, which
// the make targets and CI jobs that provide the interpreters set: a test behind the tag fails,
// never skips, when what the tag promises is missing.

// compatBasePath is the base path of the endpoint of the provider compat.
const compatBasePath = "/compat-base"

// suiteModels are the models of the fake every provider of the suites serves.
var suiteModels = []string{
	fakeprovider.ModelBasic, fakeprovider.ModelCache, fakeprovider.ModelStallAfterFirst, fakeprovider.ModelError429,
	fakeprovider.ModelError529, fakeprovider.ModelStreamError, fakeprovider.ModelDetails, fakeprovider.ModelGatedStream,
	fakeprovider.ModelNoUsage, fakeprovider.ModelUsageOnLastChunk, fakeprovider.ModelCompatUsageLastChunk,
	fakeprovider.ModelCompatExtraUsage, fakeprovider.ModelCompatIgnoresOption, fakeprovider.ModelCompatRefusesOption,
}

// external is what a suite talks to.
type external struct {
	baseURL    string // http://127.0.0.1:port, no provider prefix
	controlURL string
	up         *fakeprovider.Server
	rec        *testutil.MemoryRecorder
}

// startExternal starts the in-process server with three providers (openai; anthropic, family
// anthropic; compat, family openai, base path /compat-base, no stream usage option) over one fake
// provider, and a control server on loopback.
func startExternal(t *testing.T) *external {
	t.Helper()
	up := fakeprovider.New(t)
	up.SetBasePath(compatBasePath)
	targets := map[string]gateway.Target{}
	for _, m := range suiteModels {
		targets["openai/"+m] = testutil.TargetFor(up, "openai", m)
		targets["anthropic/"+m] = testutil.TargetForFamily(up, "anthropic", "anthropic", m)
		compat := testutil.TargetFor(up, "compat", m)
		compat.BaseURL, _ = url.Parse(up.URL() + compatBasePath)
		compat.StreamUsage = false
		targets["compat/"+m] = compat
	}
	addr, rec := startServer(t,
		map[string]string{"openai": "openai", "anthropic": "anthropic", "compat": "openai"}, targets,
		map[string]string{"openai": fixtures.CanaryProviderKey, "anthropic": fixtures.CanaryProviderKey, "compat": fixtures.CanaryProviderKey})
	e := &external{baseURL: "http://" + addr, up: up, rec: rec}
	ctl := httptest.NewServer(e.control())
	t.Cleanup(ctl.Close)
	e.controlURL = ctl.URL
	return e
}

// control serves what the suites read: counts and names only, never content.
func (e *external) control() http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /records", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("request_id")
		out := []map[string]any{}
		for _, rec := range e.rec.Records() {
			if rec.RequestID != id {
				continue
			}
			stream := rec.Stream != nil && *rec.Stream
			metadata := map[string]string{}
			for k, v := range rec.Metadata {
				metadata[k] = v
			}
			units := map[string]int64{}
			for k, v := range rec.Usage.Extra {
				units[k] = v
			}
			out = append(out, map[string]any{
				"provider": rec.Provider, "api_family": rec.APIFamily, "operation": rec.Operation, "outcome": string(rec.Outcome),
				"error_code": rec.ErrorCode, "upstream_status": rec.UpstreamStatus, "stream": stream,
				"input_source": string(rec.InputSource), "output_source": string(rec.OutputSource),
				"input_tokens": rec.Usage.InputTokens, "output_tokens": rec.Usage.OutputTokens,
				"cache_read_tokens": rec.Usage.CacheReadTokens, "cache_write_tokens": rec.Usage.CacheWriteTokens,
				"estimated_input_tokens": rec.Estimated.InputTokens, "estimated_output_tokens": rec.Estimated.OutputTokens,
				"other_units": units, "cost_status": rec.CostStatus, "metadata": metadata, "metadata_dropped": rec.MetadataDropped,
				"trace_id": rec.TraceID,
			})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /upstream/last", func(w http.ResponseWriter, _ *http.Request) {
		reqs := e.up.Requests()
		if len(reqs) == 0 {
			http.Error(w, "no request", http.StatusNotFound)
			return
		}
		last := reqs[len(reqs)-1]
		names := []string{}
		for n := range last.Header {
			names = append(names, n)
		}
		var body struct {
			Model         string                     `json:"model"`
			StreamOptions map[string]json.RawMessage `json:"stream_options"`
		}
		var members map[string]json.RawMessage
		_ = json.Unmarshal(last.Body, &body)
		_ = json.Unmarshal(last.Body, &members)
		_, has := members["stream_options"]
		writeJSON(w, map[string]any{"path": last.Path, "header_names": names, "body_has_stream_options": has, "model": body.Model})
	})
	mux.HandleFunc("GET /upstream/cancelled", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("wait"))
		if err != nil || d < 0 || d > 30*time.Second {
			d = 0
		}
		writeJSON(w, map[string]any{"cancelled": e.up.WaitCancelled(d)})
	})
	return mux
}

// suiteEnv is the environment of a suite: the gateway, the key, the control server, the canaries
// the scripts use as their prompt (so that they hold none of their own), no proxy.
func (e *external) suiteEnv() []string {
	env := []string{}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "PANDOKOS_BASE_URL", "PANDOKOS_KEY", "PANDOKOS_CONTROL_URL":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PANDOKOS_BASE_URL="+e.baseURL,
		"PANDOKOS_KEY="+testKey,
		"PANDOKOS_CONTROL_URL="+e.controlURL,
		"CANARY_PROMPT="+fixtures.CanaryPrompt,
		"CANARY_COMPLETION="+fixtures.CanaryCompletion,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	)
}

// runSuite runs interpreter with args in dir, with a 5-minute bound, and returns the combined
// output. It fails when the interpreter variable is unset (interpreter is empty) and when the
// process exits non-zero.
func runSuite(varName, interpreter, dir string, args []string, env []string) (string, error) {
	if interpreter == "" {
		return "", fmt.Errorf("%s is not set: the tag sdkexternal is set only by the make targets and CI jobs that provide the interpreter", varName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, interpreter, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("the suite failed: %w", err)
	}
	return out.String(), err
}

func suiteDir(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPythonSDK runs the Python suite (the official openai and anthropic packages under pytest).
func TestPythonSDK(t *testing.T) {
	e := startExternal(t)
	out, err := runSuite("PANDOKOS_SDK_PYTHON", os.Getenv("PANDOKOS_SDK_PYTHON"), suiteDir(t, "python"),
		[]string{"-m", "pytest", "-q", "-p", "no:cacheprovider", "."}, e.suiteEnv())
	t.Log(out)
	if err != nil {
		t.Fatal(err)
	}
}

// TestTypeScriptSDK runs the TypeScript suite (the official openai and @anthropic-ai/sdk packages
// under node --test).
func TestTypeScriptSDK(t *testing.T) {
	e := startExternal(t)
	out, err := runSuite("PANDOKOS_SDK_NODE", os.Getenv("PANDOKOS_SDK_NODE"), suiteDir(t, "ts"),
		[]string{"--test", "--test-concurrency=1"}, e.suiteEnv())
	t.Log(out)
	if err != nil {
		t.Fatal(err)
	}
}

// TestExternalHarness is T-555: the harness fails when the interpreter variable is missing or the
// suite fails, passes the suite's output through, scrubs the proxy variables, and the control
// server returns no content.
func TestExternalHarness(t *testing.T) {
	e := startExternal(t)
	t.Run("a missing interpreter variable is an error that names it", func(t *testing.T) {
		_, err := runSuite("PANDOKOS_SDK_NODE", "", ".", nil, e.suiteEnv())
		if err == nil || !strings.Contains(err.Error(), "PANDOKOS_SDK_NODE") {
			t.Errorf("%v", err)
		}
	})
	t.Run("a suite that fails fails the harness and its output is returned", func(t *testing.T) {
		out, err := runSuite("X", "/bin/sh", ".", []string{"-c", "echo synthetic-suite-output; exit 3"}, e.suiteEnv())
		if err == nil || !strings.Contains(out, "synthetic-suite-output") {
			t.Errorf("%v %q", err, out)
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Errorf("the exit status is lost: %v", err)
		}
	})
	t.Run("a suite that passes passes, with the environment of section 7", func(t *testing.T) {
		t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
		t.Setenv("https_proxy", "http://127.0.0.1:1")
		t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")
		out, err := runSuite("X", "/bin/sh", ".", []string{"-c", "env"}, e.suiteEnv())
		if err != nil {
			t.Fatal(err)
		}
		env := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				env[k] = v
			}
		}
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			if v, ok := env[k]; ok {
				t.Errorf("%s=%s reached the suite", k, v)
			}
		}
		if env["PANDOKOS_BASE_URL"] != e.baseURL || env["PANDOKOS_KEY"] != testKey || env["PANDOKOS_CONTROL_URL"] != e.controlURL ||
			!strings.Contains(env["NO_PROXY"], "127.0.0.1") || env["CANARY_PROMPT"] != fixtures.CanaryPrompt {
			t.Errorf("the environment of the suite: %v", env)
		}
	})
	t.Run("the control server returns counts and names, no content", func(t *testing.T) {
		do := func(path string) string {
			resp, err := http.Get(e.controlURL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.Status + "\n" + string(b)
		}
		if got := do("/upstream/last"); !strings.HasPrefix(got, "404") {
			t.Errorf("before any request: %s", got)
		}
		// One call of each family through the gateway, with a metadata header.
		call := func(path string, body string, hdr http.Header) string {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.baseURL+path, strings.NewReader(body))
			for k, v := range hdr {
				req.Header[k] = v
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.Header.Get("X-Pandokos-Request-Id")
		}
		prompt := fixtures.CanaryPrompt
		id := call("/anthropic/v1/messages", `{"model":"fake-basic","max_tokens":4,"stream":true,"messages":[{"role":"user","content":"`+prompt+`"}]}`,
			http.Header{"Authorization": {"Bearer " + testKey}, "Anthropic-Version": {"2026-10-21"}, "X-Pandokos-Metadata-Team": {"alpha"}})
		call("/compat/v1/chat/completions", `{"model":"fake-basic","stream":true,"messages":[{"role":"user","content":"`+prompt+`"}]}`,
			http.Header{"Authorization": {"Bearer " + testKey}})
		if id == "" {
			t.Fatal("no request ID")
		}
		var recs []map[string]any
		testutil.Eventually(t, 5*time.Second, "the record", func() bool {
			_ = json.Unmarshal([]byte(strings.SplitN(do("/records?request_id="+id), "\n", 2)[1]), &recs)
			return len(recs) == 1
		})
		r := recs[0]
		if r["provider"] != "anthropic" || r["api_family"] != "anthropic" || r["operation"] != "messages" || r["outcome"] != string(usage.OutcomeOK) ||
			r["input_source"] != "provider" || r["output_source"] != "provider" || r["input_tokens"] != float64(12) || r["output_tokens"] != float64(7) ||
			r["stream"] != true || r["cost_status"] == nil {
			t.Errorf("the record: %v", r)
		}
		if md, _ := r["metadata"].(map[string]any); md["team"] != "alpha" {
			t.Errorf("metadata %v", r["metadata"])
		}
		last := do("/upstream/last")
		var lastDoc struct {
			Path                 string   `json:"path"`
			HeaderNames          []string `json:"header_names"`
			BodyHasStreamOptions bool     `json:"body_has_stream_options"`
			Model                string   `json:"model"`
		}
		if err := json.Unmarshal([]byte(strings.SplitN(last, "\n", 2)[1]), &lastDoc); err != nil {
			t.Fatalf("%v: %s", err, last)
		}
		if lastDoc.Path != compatBasePath+"/v1/chat/completions" || lastDoc.BodyHasStreamOptions || lastDoc.Model != "fake-basic" || len(lastDoc.HeaderNames) == 0 {
			t.Errorf("upstream/last: %+v", lastDoc)
		}
		if got := do("/upstream/cancelled?wait=10ms"); !strings.Contains(got, `"cancelled":false`) {
			t.Errorf("cancelled: %s", got)
		}
		if got := do("/records?request_id=unknown"); !strings.HasSuffix(strings.TrimSpace(got), "[]") {
			t.Errorf("an unknown request ID: %s", got)
		}
		for _, p := range []string{"/records?request_id=" + id, "/upstream/last", "/upstream/cancelled?wait=1ms"} {
			text := do(p)
			testutil.AssertNoCanary(t, "the control server "+p, text)
			testutil.AssertAbsent(t, "the control server "+p, text, testKey, fixtures.CanaryProviderKey)
		}
	})
}
