//go:build upgrade

// Package upgrade is the upgrade suite (sprint 12.3, build tag upgrade, make test-upgrade): it
// starts the previous release, built from the commit pinned in PREVIOUS by make previous-binary, puts
// traffic on it, upgrades it to this tree with the procedure of the upgrade guide and asserts what
// happened to every request. This file is the scaffolding the tests share: the pin, a traffic
// generator with its outcome record, and the upgrade procedure as named steps.
package upgrade

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// Pin is the content of test/upgrade/PREVIOUS: the previous release, by version, by commit (what
// make previous-binary builds) and by image digest (what the container suite runs).
type Pin struct{ Version, Commit, Image string }

// ReadPin reads PREVIOUS.
func ReadPin(tb testing.TB) Pin {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(tb), "test", "upgrade", "PREVIOUS"))
	if err != nil {
		tb.Fatal(err)
	}
	var p Pin
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "version":
			p.Version = v
		case "commit":
			p.Commit = v
		case "image":
			p.Image = v
		}
	}
	return p
}

// ---- the traffic

// Kind of a call.
type Kind string

// The kinds of call.
const (
	Plain  Kind = "plain"
	Stream Kind = "stream"
)

// Class is how a call ended.
type Class string

// The ways a call ends. Only Complete, Quota and, in the window of the upgrade, NoAnswer and
// Draining are acceptable to the upgrade test; the others are what it looks for.
const (
	Complete    Class = "complete"     // a whole answer (a stream ends with its final marker)
	Quota       Class = "quota"        // refused by a limit rule (429)
	NoAnswer    Class = "no-answer"    // connection refused or closed before any byte of an answer
	Draining    Class = "draining"     // 503 while the instance drains
	Partial     Class = "partial"      // bytes of an answer, then the call broke or the body was cut
	ServerError Class = "server-error" // another 5xx
	Other       Class = "other"        // anything else (a 4xx that is not a quota refusal)
)

// Outcome is the record of one call.
type Outcome struct {
	ID        string      `json:"id"`
	Tenant    string      `json:"tenant"`
	Kind      Kind        `json:"kind"`
	Started   time.Time   `json:"started"`
	FirstByte time.Time   `json:"first_byte"` // zero when no byte came
	Ended     time.Time   `json:"ended"`
	Status    int         `json:"status"` // 0 when there was none
	Class     Class       `json:"class"`
	Error     string      `json:"error"` // the class of the transport error, never a value
	Header    http.Header `json:"-"`     // the headers of the answer (a refusal's quota headers)
	// CredentialSeen is the fingerprint of the credential the provider received for this call, filled
	// by Correlate from the provider's own record; empty when the call never reached it.
	CredentialSeen string `json:"credential_seen"`
}

// Fingerprint is the identifier of a credential that the test compares: the first 8 bytes of its
// SHA-256 in hexadecimal. The provider's record and the test's expectations use it, never the value.
func Fingerprint(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:8])
}

// Client is a tenant's caller: its name, and where and how it calls.
type Client struct {
	Tenant  string
	Key     string // the inference key; sent, never recorded
	Model   string // for plain calls
	Slow    string // the model of the streaming calls (a model that the test gates), "" for Model
	Clients int    // closed-loop clients, default 1
}

// Traffic is a closed-loop generator: every client of every tenant sends a call, waits for its
// outcome, sends the next. The base URL is asked for at each call, so that a test can move the
// clients to the new instance's listener by updating one function.
type Traffic struct {
	BaseURL func() string
	HTTP    *http.Client

	mu       sync.Mutex
	outcomes []Outcome
	wg       sync.WaitGroup
	stop     chan struct{}
	seq      atomic.Int64
}

// NewTraffic returns a generator; base is the function that says where the inference listener is now.
func NewTraffic(base func() string) *Traffic {
	return &Traffic{BaseURL: base, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: false, MaxIdleConnsPerHost: 8}}, stop: make(chan struct{})}
}

// Start starts the clients. Each alternates plain and streaming calls.
func (tr *Traffic) Start(clients ...Client) {
	for _, c := range clients {
		n := c.Clients
		if n == 0 {
			n = 1
		}
		for range n {
			tr.wg.Add(1)
			go func() {
				defer tr.wg.Done()
				for i := 0; ; i++ {
					select {
					case <-tr.stop:
						return
					default:
					}
					kind := Plain
					if i%2 == 1 {
						kind = Stream
					}
					o := tr.call(c, kind)
					tr.mu.Lock()
					tr.outcomes = append(tr.outcomes, o)
					tr.mu.Unlock()
					if o.Class == NoAnswer || o.Class == Draining {
						time.Sleep(20 * time.Millisecond) // do not spin on a listener that is not there
					}
				}
			}()
		}
	}
}

// Stop ends the clients (a call in flight finishes) and returns every outcome in the order they ended.
func (tr *Traffic) Stop() []Outcome {
	close(tr.stop)
	tr.wg.Wait()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]Outcome(nil), tr.outcomes...)
}

// Outcomes returns a copy of what has ended so far.
func (tr *Traffic) Outcomes() []Outcome {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]Outcome(nil), tr.outcomes...)
}

// callID is the identifier a call carries in its prompt, so that the provider's record can be matched.
func (tr *Traffic) callID(tenant string) string {
	return fmt.Sprintf("call-%s-%d", tenant, tr.seq.Add(1))
}

func (tr *Traffic) call(c Client, kind Kind) Outcome {
	o := Outcome{ID: tr.callID(c.Tenant), Tenant: c.Tenant, Kind: kind, Started: time.Now()}
	model := c.Model
	if kind == Stream && c.Slow != "" {
		model = c.Slow
	}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]`, model, o.ID)
	if kind == Stream {
		body += `,"stream":true`
	}
	body += "}"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, tr.BaseURL()+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		o.Class, o.Error, o.Ended = Other, "request", time.Now()
		return o
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := tr.HTTP.Do(req)
	if err != nil {
		o.Class, o.Error, o.Ended = NoAnswer, errorClass(err), time.Now()
		return o
	}
	defer resp.Body.Close()
	o.Status, o.Header = resp.StatusCode, resp.Header.Clone()
	br := bufio.NewReader(resp.Body)
	first, err := br.Peek(1)
	if len(first) > 0 {
		o.FirstByte = time.Now()
	}
	var read []byte
	if err == nil || len(first) > 0 {
		read, err = io.ReadAll(br)
	}
	o.Ended = time.Now()
	switch {
	case resp.StatusCode == http.StatusOK && err == nil && complete(kind, read):
		o.Class = Complete
	case resp.StatusCode == http.StatusOK:
		o.Class, o.Error = Partial, errorClass(err)
	case resp.StatusCode == http.StatusTooManyRequests:
		o.Class = Quota
	case resp.StatusCode == http.StatusServiceUnavailable:
		o.Class = Draining
	case resp.StatusCode >= 500:
		o.Class = ServerError
	default:
		o.Class = Other
	}
	return o
}

// complete is whether a body is a whole answer: a JSON document with choices, or a stream that ends
// with its final marker.
func complete(kind Kind, body []byte) bool {
	if kind == Stream {
		return bytes.Contains(body, []byte("data: [DONE]"))
	}
	var v struct {
		Choices []json.RawMessage `json:"choices"`
	}
	return json.Unmarshal(body, &v) == nil && len(v.Choices) > 0
}

// errorClass names a transport error without any of its text, which may quote an address.
func errorClass(err error) string {
	var ne interface{ Timeout() bool }
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "refused"):
		return "refused"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "cut"
	case errors.Is(err, io.EOF), strings.Contains(err.Error(), "reset"):
		return "closed"
	}
	return "transport"
}

// Correlate fills CredentialSeen of each outcome from what the provider recorded: the call's
// identifier is in the user message, the credential in the Authorization header.
func Correlate(outcomes []Outcome, up *fakeprovider.Server) []Outcome {
	seen := map[string]string{}
	for _, r := range up.Requests() {
		var m struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(r.Body, &m) != nil || len(m.Messages) == 0 {
			continue
		}
		seen[m.Messages[0].Content] = Fingerprint(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	out := append([]Outcome(nil), outcomes...)
	for i := range out {
		out[i].CredentialSeen = seen[out[i].ID]
	}
	return out
}

// ---- the report

// WriteReport adds a member to the JSON report named by PANDOKOS_UPGRADE_REPORT, when it is set. A
// report holds the figures of a run (the interval without service) and nothing else.
var reportMu sync.Mutex

// WriteReport records v under key.
func WriteReport(tb testing.TB, key string, v any) {
	tb.Helper()
	path := os.Getenv("PANDOKOS_UPGRADE_REPORT")
	if path == "" {
		return
	}
	reportMu.Lock()
	defer reportMu.Unlock()
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &doc)
	}
	doc[key] = v
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		tb.Fatal(err)
	}
}

// ---- the procedure

// Procedure is the upgrade of an installation as the guide gives it, in named steps: check, backup,
// stop, migrate, start. Old is the binary that serves now, New the one it is upgraded to.
type Procedure struct {
	Old, New instance.Binary
	DB       *dbtest.Database
	Env      []string // the configuration, as environment: the same for both versions
	Canaries *instance.Canaries

	Steps     []StepRecord
	StopSent  time.Time // when SIGTERM went to the old process
	NewReady  time.Time // when the new process was ready
	OldExited int
}

// StepRecord is one step of the procedure with its time.
type StepRecord struct {
	Name       string
	Start, End time.Time
}

func (p *Procedure) step(name string, fn func()) {
	s := StepRecord{Name: name, Start: time.Now()}
	fn()
	s.End = time.Now()
	p.Steps = append(p.Steps, s)
}

// Check is "pandokos config check" with the NEW binary against the CURRENT configuration; it must
// exit 0.
func (p *Procedure) Check(tb testing.TB) {
	tb.Helper()
	p.step("check", func() {
		code, out, errs := p.New.WithCanaries(p.Canaries).Run(tb, p.Env, "config", "check")
		if code != 0 {
			tb.Fatalf("config check with the new binary exited %d\nstdout: %s\nstderr: %s", code, out, errs)
		}
	})
}

// Backup takes the dump of the database that the going-back test restores: pg_dump in the custom
// format, by a superuser, run in the test PostgreSQL container over its local socket (no password
// is on any command line), written to a file of the test. It returns the path.
func (p *Procedure) Backup(tb testing.TB) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "before-migrate.dump")
	p.step("backup", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "compose", "-f", filepath.Join(testutil.RepoRoot(tb), "test", "db", "compose.yaml"),
			"exec", "-T", "postgres", "pg_dump", "-U", "postgres", "-Fc", "-d", p.DB.Name)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			tb.Fatalf("pg_dump: %v\n%s", err, errb.String())
		}
		if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
			tb.Fatal(err)
		}
	})
	return path
}

// Stop sends SIGTERM to the old process and waits for it: the instance drains.
func (p *Procedure) Stop(tb testing.TB, old *instance.Process) {
	tb.Helper()
	p.step("stop", func() {
		p.StopSent = time.Now()
		p.OldExited = old.Stop(tb)
	})
}

// Migrate runs "pandokos migrate" with the new binary and returns its standard output.
func (p *Procedure) Migrate(tb testing.TB) string {
	tb.Helper()
	var out string
	p.step("migrate", func() {
		code, stdout, errs := p.New.WithCanaries(p.Canaries).Run(tb, p.Env, "migrate")
		if code != 0 {
			tb.Fatalf("migrate exited %d\nstdout: %s\nstderr: %s", code, stdout, errs)
		}
		out = stdout
	})
	return out
}

// Start starts the new binary on the same configuration and waits for its readiness.
func (p *Procedure) Start(tb testing.TB) *instance.Process {
	tb.Helper()
	var proc *instance.Process
	p.step("start", func() {
		proc = p.New.WithCanaries(p.Canaries).Serve(tb, p.Env)
		proc.Ready(tb)
		p.NewReady = time.Now()
	})
	return proc
}

// Gap is the interval without service: from the signal to the old process to the readiness of the new.
func (p *Procedure) Gap() time.Duration { return p.NewReady.Sub(p.StopSent) }
