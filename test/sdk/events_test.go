package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// eventSchemas compiles the published schema of each type of event (docs/reference/events/) with a
// validator that is not ours.
func eventSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	dir := filepath.Join(testutil.RepoRoot(t), "docs", "reference", "events")
	out := map[string]*jsonschema.Schema{}
	for _, typ := range []string{
		"io.pandokos.usage.recorded.v1", "io.pandokos.limit.threshold_reached.v1", "io.pandokos.limit.exhausted.v1",
		"io.pandokos.tenant.deletion_requested.v1", "io.pandokos.tenant.deletion_cancelled.v1", "io.pandokos.tenant.deleted.v1",
		"io.pandokos.kek.version_activated.v1", "io.pandokos.kek.version_retired.v1", "io.pandokos.destination.test.v1",
	} {
		file := filepath.Join(dir, strings.TrimPrefix(typ, "io.pandokos.")+".schema.json")
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("the published schema of %s: %v", typ, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s is not JSON: %v", file, err)
		}
		url := "urn:pandokos:schema:event:" + typ
		c := jsonschema.NewCompiler()
		if err := c.AddResource(url, doc); err != nil {
			t.Fatalf("add %s: %v", file, err)
		}
		if out[typ], err = c.Compile(url); err != nil {
			t.Fatalf("compile %s: %v", file, err)
		}
	}
	return out
}

func validate(t *testing.T, schemas map[string]*jsonschema.Schema, line []byte, where string) string {
	t.Helper()
	var head struct{ Type string }
	if err := json.Unmarshal(line, &head); err != nil {
		t.Fatalf("%s: %v\n%s", where, err, line)
	}
	s, ok := schemas[head.Type]
	if !ok {
		t.Errorf("%s: no published schema for the type %q", where, head.Type)
		return head.Type
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(v); err != nil {
		t.Errorf("%s: a %s does not validate against its published schema: %v\n%s", where, head.Type, err, line)
	}
	return head.Type
}

// TestEventSchemas is T-1051: events of every type taken from the exports of a running binary and
// from a webhook validate against the published schemas with a third-party validator; a document with
// a missing required member does not; a delivered request verifies with the Standard Webhooks library.
func TestEventSchemas(t *testing.T) {
	d := dbtest.NewMigrated(t)
	schemas := eventSchemas(t)

	// The platform's key, a tenant with a reference and its owner.
	keyFile := filepath.Join(t.TempDir(), "platform.key")
	env := d.EnvWith(t, map[string]string{"PANDOKOS_EVENTS_RELAY_INTERVAL": "100ms", "PANDOKOS_EVENTS_WEBHOOK_PRIVATE_HOSTS": "localhost"})
	if code, out, errs := run(t, env, "init", "--key-out", keyFile); code != 0 {
		t.Fatalf("init: exit %d\n%s%s", code, out, errs)
	}
	platform := []string{"PANDOKOS_CLI_KEY_FILE=" + keyFile}
	// Events of the platform's log, made before the binary starts: a deletion requested and cancelled,
	// one completed, a key version retired.
	pre := d.SeedTenant(t, "sdk-cancelled")
	d.RequestDeletion(t, pre, time.Now().Add(time.Hour))
	if _, err := d.Super(t).Exec(context.Background(), `DELETE FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, pre.String()); err != nil {
		t.Fatal(err)
	}
	d.CompleteDeletion(t, d.SeedTenant(t, "sdk-deleted"))
	if _, err := d.Super(t).Exec(context.Background(), `INSERT INTO pandokos.kek_version (id, provider, state) VALUES ('local-9999999999999999', 'local', 'retiring')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Super(t).Exec(context.Background(), `UPDATE pandokos.kek_version SET state = 'retired', retired_at = now() WHERE id = 'local-9999999999999999'`); err != nil {
		t.Fatal(err)
	}

	admin := serveWith(t, d, env)
	platform = append(platform, "PANDOKOS_CLI_SERVER=http://"+admin)
	code, out, errs := run(t, platform, "tenant", "create", "--external-reference", "cust-42", "sdkevents")
	if code != 0 {
		t.Fatalf("tenant create: exit %d\n%s%s", code, out, errs)
	}
	tidMatch := regexp.MustCompile(`(?m)^id:\s+([0-9a-f-]{36})`).FindStringSubmatch(out)
	keyMatch := regexp.MustCompile(`(?m)^key:\s+(pdk_t\S+)`).FindStringSubmatch(out)
	if tidMatch == nil || keyMatch == nil {
		t.Fatalf("the tenant and its key are not in\n%s", out)
	}
	tid := tidMatch[1]
	ownerFile := testutil.WriteSecret(t, "owner-key", keyMatch[1])
	owner := []string{"PANDOKOS_CLI_SERVER=http://" + admin, "PANDOKOS_CLI_KEY_FILE=" + ownerFile}

	// A receiver that verifies every request with the library and keeps the documents.
	var mu sync.Mutex
	var bodies [][]byte
	var secretText string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		wh, err := standardwebhooks.NewWebhook(secretText)
		if err != nil {
			t.Errorf("the secret is not accepted by the library: %v", err)
		} else if err := wh.Verify(body, r.Header); err != nil {
			t.Errorf("the library does not verify a delivery: %v\nheaders: %v", err, r.Header)
		}
		bodies = append(bodies, body)
		w.WriteHeader(204)
	}))
	t.Cleanup(recv.Close)
	url := strings.Replace(recv.URL, "127.0.0.1", "localhost", 1) + "/hook"
	mu.Lock()
	code, out, errs = run(t, owner, "event-destination", "create", "--tenant", tid, "--url", url, "sdkhook")
	secretMatch := regexp.MustCompile(`whsec_[A-Za-z0-9+/]{43}=`).FindString(out)
	secretText = secretMatch
	mu.Unlock()
	if code != 0 || secretMatch == "" {
		t.Fatalf("event-destination create: exit %d, no secret in\n%s%s", code, out, errs)
	}

	// Events of the tenant: usage records and both kinds of limit event; and a test request.
	tenantID := mustTenant(t, tid)
	d.SeedUsageRecords(t, tenantID, 4)
	for _, kind := range []struct{ kind, percent string }{{"threshold_reached", "80"}, {"exhausted", "0"}} {
		if _, err := d.Super(t).Exec(context.Background(), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
			VALUES ($1::uuid, $2, pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', `+kind.percent+`, 10, 8)`, tid, kind.kind); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, errs := run(t, owner, "event-destination", "test", "--wait", "--tenant", tid, firstDestination(t, owner, tid)); code != 0 {
		t.Fatalf("event-destination test --wait: exit %d\n%s%s", code, out, errs)
	}
	testutil.Eventually(t, 30*time.Second, "the six events and the test event at the receiver", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(bodies) >= 7
	})

	seen := map[string]bool{}
	mu.Lock()
	for i, b := range bodies {
		seen[validate(t, schemas, b, fmt.Sprintf("webhook delivery %d", i))] = true
	}
	mu.Unlock()

	// The exports: the tenant's own and the platform's.
	dir := t.TempDir()
	tenantOut, platformOut := filepath.Join(dir, "tenant.ndjson"), filepath.Join(dir, "platform.ndjson")
	if code, out, errs := run(t, owner, "event", "export", "--tenant", tid, "--out", tenantOut); code != 0 {
		t.Fatalf("event export: exit %d\n%s%s", code, out, errs)
	}
	if code, out, errs := run(t, platform, "platform-event", "export", "--out", platformOut); code != 0 {
		t.Fatalf("platform-event export: exit %d\n%s%s", code, out, errs)
	}
	for _, f := range []string{tenantOut, platformOut} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			seen[validate(t, schemas, line, fmt.Sprintf("%s line %d", filepath.Base(f), i+1))] = true
		}
	}
	for typ := range schemas {
		if !seen[typ] {
			t.Errorf("no event of the type %s was validated: the export and the webhook must show every type", typ)
		}
	}

	// A document with a missing required member does not validate.
	b, _ := os.ReadFile(tenantOut)
	line := bytes.SplitN(b, []byte("\n"), 2)[0]
	var doc map[string]any
	if err := json.Unmarshal(line, &doc); err != nil {
		t.Fatal(err)
	}
	for _, drop := range []string{"id", "source", "type", "time", "specversion", "data"} {
		broken := map[string]any{}
		for k, v := range doc {
			if k != drop {
				broken[k] = v
			}
		}
		raw, _ := json.Marshal(broken)
		v, _ := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		typ, _ := doc["type"].(string)
		if drop == "type" {
			typ = "io.pandokos.usage.recorded.v1"
		}
		if s := schemas[typ]; s != nil && s.Validate(v) == nil {
			t.Errorf("a document without %q validates", drop)
		}
	}
	data, _ := doc["data"].(map[string]any)
	delete(data, "record_id")
	raw, _ := json.Marshal(doc)
	v, _ := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if schemas["io.pandokos.usage.recorded.v1"].Validate(v) == nil {
		t.Error("a usage event without record_id in its data validates")
	}
}

func mustTenant(t *testing.T, id string) tenant.ID {
	t.Helper()
	tid, err := tenant.ParseID(id)
	if err != nil {
		t.Fatal(err)
	}
	return tid
}

// firstDestination returns the ID of the only destination of the tenant, read with the CLI.
func firstDestination(t *testing.T, env []string, tid string) string {
	t.Helper()
	code, out, errs := run(t, env, "event-destination", "list", "--tenant", tid, "-o", "json")
	if code != 0 {
		t.Fatalf("event-destination list: exit %d\n%s%s", code, out, errs)
	}
	m := regexp.MustCompile(`"id":\s*"([0-9a-f-]{36})"`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no destination in\n%s", out)
	}
	return m[1]
}
