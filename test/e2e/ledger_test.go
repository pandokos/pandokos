package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	responsesPath  = "/openai/v1/responses"
	embeddingsPath = "/openai/v1/embeddings"
)

var reUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// openStream sends a streaming request and returns the response; the caller closes it.
func (p *proc) openStream(ctx context.Context, t *testing.T, key, path string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url(path), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = authHeader(key)
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// waitRows waits until the tenant has n usage rows and returns them.
func waitRows(t *testing.T, d *dbtest.Database, tenantID string, n int) []map[string]any {
	t.Helper()
	var rows []map[string]any
	testutil.Eventually(t, 15*time.Second, fmt.Sprintf("%d usage rows of the tenant", n), func() bool {
		rows = usageRows(t, d, tenantID)
		return len(rows) >= n
	})
	if len(rows) != n {
		t.Fatalf("%d usage rows, want %d: %v", len(rows), n, rows)
	}
	return rows
}

func countRows(t *testing.T, d *dbtest.Database) int {
	t.Helper()
	return tableCount(t, d, "usage_record")
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// TestLedgerExitScenario is T-508: the built binary, PostgreSQL and a fake provider.
func TestLedgerExitScenario(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	p.useFakeProvider(t, boot, "openai", up)
	p.addFakeModels(t, boot)
	p.upReady = true
	p.mustCLI(t, boot, "model", "create", "--availability", "selected", "openai/fake-selected")
	p.mustCLI(t, boot, "price", "override", "--currency", "USD", "--rate", "input=3", "--rate", "output=6", "openai/fake-basic")

	const credA, credB = "synthetic-credential-tenant-a-0101", "synthetic-credential-tenant-b-0102"
	up.Accept(credA, credB)
	a, b := p.newCaller2(t, boot, "alpha", credA), p.newCaller2(t, boot, "beta", credB)
	none := p.newCaller2(t, boot, "gamma", "")
	scoped := p.newCaller2(t, boot, "delta", credA, "--scope", "model:invoke")

	t.Run("a cancelled stream leaves a row cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		resp := p.openStream(ctx, t, a.key, chatPath, chatBody(fakeprovider.ModelGatedStream, true, fixtures.CanaryPrompt))
		defer resp.Body.Close()
		if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
			t.Fatal(err)
		}
		cancel()
		if !up.WaitCancelled(3 * time.Second) {
			t.Fatal("the provider was not cancelled")
		}
		rows := waitRows(t, p.db, a.id, 1)
		if rows[0]["outcome"] != "cancelled" || rows[0]["input_source"] != "estimated" || rows[0]["stream"] != true {
			t.Errorf("the row: %v", rows[0])
		}
	})

	t.Run("a streamed call read event by event while the provider is gated", func(t *testing.T) {
		up.GateEachEvent()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		resp := p.openStream(ctx, t, a.key, chatPath, chatBody(fakeprovider.ModelBasic, true, fixtures.CanaryPrompt))
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		events := bytes.SplitAfter(fixtures.Read("chat_stream_usage.sse"), []byte("\n\n"))
		for i, ev := range events {
			if len(ev) == 0 {
				continue
			}
			if !bytes.Contains(ev, []byte(`"choices":[]`)) { // the usage-only event is removed
				got := make(chan string, 1)
				go func() { s, _ := testutil.ReadEvent(br); got <- s }()
				select {
				case s := <-got:
					if s != string(ev) {
						t.Fatalf("event %d: %q", i, s)
					}
				case <-time.After(4 * time.Second):
					t.Fatalf("event %d did not arrive while the provider waits: the stream is buffered", i)
				}
			}
			up.Advance()
		}
		if rest, _ := io.ReadAll(br); len(rest) != 0 {
			t.Errorf("after the last event: %q", rest)
		}
		up.Release()
		rows := waitRows(t, p.db, a.id, 2)
		r := rows[1]
		if r["outcome"] != "ok" || r["input_source"] != "provider" || r["output_source"] != "provider" || num(r["input_tokens"]) != 9 || num(r["output_tokens"]) != 12 || r["stream"] != true {
			t.Fatalf("the row: %v", r)
		}
		if math.Abs(num(r["cost"])-0.000099) > 1e-9 || r["currency"] != "USD" || r["cost_status"] != "known" {
			t.Errorf("cost %v %v %v", r["cost"], r["currency"], r["cost_status"])
		}
		if id, _ := r["price_version_id"].(string); !reUUID.MatchString(id) {
			t.Errorf("price_version_id %v", r["price_version_id"])
		}
		if id, _ := r["credential_id"].(string); !reUUID.MatchString(id) {
			t.Errorf("credential_id %v", r["credential_id"])
		}
		if r["key_id"] != a.keyID || r["model"] != "fake-basic" || r["operation"] != "chat.completions" || r["provider"] != "openai" || r["funding"] != "byok" {
			t.Errorf("the row: %v", r)
		}
		if id, _ := r["trace_id"].(string); len(id) != 32 {
			t.Errorf("trace_id %v", r["trace_id"])
		}
	})

	t.Run("a plain call, responses and embeddings", func(t *testing.T) {
		for _, c := range []struct {
			path string
			body []byte
		}{
			{chatPath, chatBody("fake-basic", false, fixtures.CanaryPrompt)},
			{responsesPath, []byte(`{"model":"fake-basic","input":"` + fixtures.CanaryPrompt + `"}`)},
			{responsesPath, []byte(`{"model":"fake-basic","stream":true,"input":"` + fixtures.CanaryPrompt + `"}`)},
			{embeddingsPath, []byte(`{"model":"fake-basic","input":"` + fixtures.CanaryPrompt + `"}`)},
		} {
			if status, text := exchangeWith(t, p.client, "POST", p.url(c.path), authHeader(a.key), c.body); status != 200 {
				t.Fatalf("%s: %d\n%s", c.path, status, text)
			}
		}
		rows := waitRows(t, p.db, a.id, 6)
		want := []struct {
			op       string
			in, out  float64
			stream   bool
			reasoned float64
		}{{"chat.completions", 9, 12, false, 0}, {"responses", 20, 15, false, 4}, {"responses", 20, 15, true, 4}, {"embeddings", 8, 0, false, 0}}
		for i, w := range want {
			r := rows[2+i]
			if r["operation"] != w.op || r["outcome"] != "ok" || num(r["input_tokens"]) != w.in || num(r["output_tokens"]) != w.out || r["stream"] != w.stream || num(r["reasoning_tokens"]) != w.reasoned {
				t.Errorf("row %d: %v", 2+i, r)
			}
			if r["cost_status"] != "known" {
				t.Errorf("row %d: cost_status %v", 2+i, r["cost_status"])
			}
		}
	})

	t.Run("refusals after authentication leave rows with their codes", func(t *testing.T) {
		for _, c := range []struct {
			what string
			key  string
			tid  string
			body []byte
			code string
		}{
			{"a model that is not allowed", a.key, a.id, chatBody("fake-selected", false, "x"), "model_not_allowed"},
			{"a tenant without a credential", none.key, none.id, chatBody("fake-basic", false, "x"), "no_credential"},
			{"a key scoped to model:invoke", scoped.key, scoped.id, chatBody("fake-basic", false, "x"), "forbidden"},
		} {
			before := len(usageRows(t, p.db, c.tid))
			if status, _ := exchangeWith(t, p.client, "POST", p.url(chatPath), authHeader(c.key), c.body); status/100 != 4 {
				t.Fatalf("%s: %d", c.what, status)
			}
			rows := waitRows(t, p.db, c.tid, before+1)
			r := rows[len(rows)-1]
			if r["outcome"] != "rejected_policy" || r["error_code"] != c.code || r["input_source"] != "none" || r["cost_status"] != "none" {
				t.Errorf("%s: %v", c.what, r)
			}
		}
	})

	t.Run("an unknown key leaves no row, and tenant B's rows are B's", func(t *testing.T) {
		total := countRows(t, p.db)
		exchangeWith(t, p.client, "POST", p.url(chatPath), authHeader(testutil.NewKey(t)), chatBody("fake-basic", false, "x"))
		exchangeWith(t, p.client, "POST", p.url(chatPath), http.Header{"Content-Type": {"application/json"}}, chatBody("fake-basic", false, "x"))
		exchangeWith(t, p.client, "POST", p.url("/nope/v1/chat/completions"), authHeader(a.key), chatBody("fake-basic", false, "x"))
		if status, _ := p.chatCall(t, b.key, fakeprovider.ModelBasic, false); status != 200 {
			t.Fatalf("B: %d", status)
		}
		rows := waitRows(t, p.db, b.id, 1)
		if rows[0]["tenant_id"] != b.id || rows[0]["key_id"] != b.keyID {
			t.Errorf("B's row: %v", rows[0])
		}
		for _, id := range []string{a.id, none.id, scoped.id} {
			for _, r := range usageRows(t, p.db, id) {
				if r["tenant_id"] != id {
					t.Errorf("a row of %v among the rows of %s", r["tenant_id"], id)
				}
			}
		}
		// The number of rows is the number of authenticated requests: A made 6 calls and 1 refusal
		// after the stream, B one, the two refused tenants one each.
		testutil.Eventually(t, 10*time.Second, "the row count", func() bool { return countRows(t, p.db) == total+1 })
		if got, want := countRows(t, p.db), 7+1+1+1; got != want {
			t.Errorf("%d rows, want %d (one per authenticated request)", got, want)
		}
	})
}

// newCaller2 makes a tenant with a key and, when credential is not empty, a credential of that
// secret; extra flags go to the key.
func (p *proc) newCaller2(t *testing.T, boot, name, credential string, keyFlags ...string) callerTenant {
	t.Helper()
	id, admin := p.newTenant(t, boot, name)
	keyID, key := p.newInferenceKey(t, admin, id, keyFlags...)
	if credential != "" {
		p.addCredential(t, admin, "openai", credential)
	}
	return callerTenant{id: id, adminFile: admin, keyID: keyID, key: key}
}

// TestLedgerDatabaseDown is T-509: with the buffer at its minimum and the database stopped, calls
// succeed from the caches and their records wait; when the buffer is full a call answers 503; the
// readiness log names the check ledger; when the database returns every admitted request has one
// row and /readyz is 200 again.
func TestLedgerDatabaseDown(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, ledgerBuffer: 100})
	key := p.inferenceKey(t)
	callerID := tenantOfKey(t, p)
	if status, text := p.chatCall(t, key, fakeprovider.ModelBasic, false); status != 200 {
		t.Fatalf("before the stop: %d\n%s", status, text)
	}
	waitRows(t, p.db, callerID, 1)

	p.db.SetAcceptingConnections(t, false)
	admitted := 0
	for i := 0; i < 100; i++ {
		status, text := p.chatCall(t, key, fakeprovider.ModelBasic, i%2 == 1)
		if status != 200 {
			t.Fatalf("call %d with the database away (the caches should serve it): %d\n%s", i, status, text)
		}
		admitted++
	}
	status, text := p.chatCall(t, key, fakeprovider.ModelBasic, false)
	assertPandokosError(t, "a call with the buffer full", status, text, 503, "unavailable")
	testutil.Eventually(t, 10*time.Second, "the readiness log names the ledger", func() bool {
		if _, err := p.client.Get(p.url("/readyz")); err != nil {
			return false
		}
		for _, l := range completeLines(p.stdout, "readiness check failed") {
			if l["check"] == "ledger" {
				return true
			}
		}
		return false
	})
	if resp, err := p.client.Get(p.url("/readyz")); err != nil || resp.StatusCode != 503 {
		t.Errorf("/readyz while the buffer is full: %v %v", resp, err)
	}

	p.db.SetAcceptingConnections(t, true)
	rows := waitRows(t, p.db, callerID, 1+admitted)
	seen := map[any]bool{}
	for _, r := range rows {
		if seen[r["id"]] {
			t.Errorf("the row %v appears twice", r["id"])
		}
		seen[r["id"]] = true
		if r["outcome"] != "ok" {
			t.Errorf("a row of the outage: %v", r)
		}
	}
	testutil.Eventually(t, 20*time.Second, "/readyz to be 200 again", func() bool {
		resp, err := p.client.Get(p.url("/readyz"))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	if status, text := p.chatCall(t, key, fakeprovider.ModelBasic, false); status != 200 {
		t.Errorf("after the database came back: %d\n%s", status, text)
	}
	if code := p.stop(t); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// tenantOfKey returns the ID of the tenant that inferenceKey made.
func tenantOfKey(t *testing.T, p *proc) string {
	t.Helper()
	var id string
	conn := p.db.Super(t)
	if err := conn.QueryRow(context.Background(), `SELECT id::text FROM pandokos.tenant WHERE name = 'tenant-1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestLedgerAtShutdown is T-510.
func TestLedgerAtShutdown(t *testing.T) {
	t.Run("streams in flight at SIGTERM leave a row each", func(t *testing.T) {
		up := fakeprovider.New(t)
		p := startBinary(t, opts{level: "info", up: up})
		key := p.inferenceKey(t)
		tid := tenantOfKey(t, p)
		up.GateEachEvent()
		var resps []*http.Response
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := 0; i < 3; i++ {
			resp := p.openStream(ctx, t, key, chatPath, chatBody(fakeprovider.ModelBasic, true, fixtures.CanaryPrompt))
			if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
				t.Fatal(err)
			}
			resps = append(resps, resp)
		}
		testutil.Eventually(t, 5*time.Second, "three streams in flight", func() bool { return up.InFlight() == 3 })
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond) // the drain has begun; the streams are still held
		for i := 0; i < 30; i++ {
			up.Advance()
		}
		for _, r := range resps {
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		select {
		case <-p.exited:
		case <-time.After(10 * time.Second):
			t.Fatal("the process did not exit")
		}
		if p.code != 0 {
			t.Errorf("exit code %d", p.code)
		}
		rows := usageRows(t, p.db, tid)
		if len(rows) != 3 {
			t.Fatalf("%d rows after exit, want 3", len(rows))
		}
		for _, r := range rows {
			if r["outcome"] != "ok" {
				t.Errorf("row %v", r)
			}
		}
	})

	t.Run("with the database away: exit within the bound, every record logged", func(t *testing.T) {
		up := fakeprovider.New(t)
		p := startBinary(t, opts{level: "info", up: up, flushTimeout: "2s"})
		key := p.inferenceKey(t)
		tid := tenantOfKey(t, p)
		if status, _ := p.chatCall(t, key, fakeprovider.ModelBasic, false); status != 200 {
			t.Fatal("the first call failed")
		}
		waitRows(t, p.db, tid, 1)
		p.db.SetAcceptingConnections(t, false)
		for i := 0; i < 3; i++ {
			if status, text := p.chatCall(t, key, fakeprovider.ModelBasic, false); status != 200 {
				t.Fatalf("call %d: %d\n%s", i, status, text)
			}
		}
		began := time.Now()
		p.stop(t)
		// The drain (5 s in the test configuration) plus the flush timeout (2 s) is the bound.
		if took := time.Since(began); took > 5*time.Second+2*time.Second+time.Second {
			t.Errorf("the process took %s to exit", took)
		}
		lines := completeLines(p.stdout, "usage record not written")
		if len(lines) != 3 {
			t.Fatalf("%d `usage record not written` lines, want 3", len(lines))
		}
		for _, l := range lines {
			if l["level"] != "ERROR" || l["record_id"] == nil || l["request_id"] == nil || l["tenant_id"] != tid {
				t.Errorf("the line: %v", l)
			}
		}
		testutil.AssertNoCanary(t, "stdout", p.stdout.String())
		p.db.SetAcceptingConnections(t, true)
	})
}
