package outbox

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	testTenant = "01a2096c-0745-7db9-b65e-3f2f525e04af"
	testEvent  = "01a2096c-074d-750b-9e85-c37222e3b9c9"
)

func tenantID(t *testing.T) tenant.ID {
	t.Helper()
	id, err := tenant.ParseID(testTenant)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var at = time.Date(2026, 11, 23, 17, 20, 4, 426849000, time.UTC)

// TestRender is T-1026: the envelope of every type, written from section 5.3 of the design and not
// from the output of the implementation: the members in their order, tenantref absent when empty,
// sequence a string, the time in UTC with microseconds, the source from the option, and the data
// passed through byte for byte.
func TestRender(t *testing.T) {
	tid := tenantID(t)
	envOf := func(ref string) Envelope { return Envelope{Source: "urn:pandokos", TenantID: tid, TenantRef: ref} }
	for _, c := range []struct {
		name string
		e    Event
		env  Envelope
		want string
	}{
		{"usage recorded",
			Event{Seq: 1, ID: testEvent, Type: "io.pandokos.usage.recorded.v1", Subject: "eccbc87e4b5ce2fe28308fd9f2a7baf3", Time: at,
				Data: json.RawMessage(`{"cost": {"amount": "0.0042", "status": "known", "currency": "EUR"}, "model": "gpt-4o-mini", "record_id": "` + testEvent + `"}`)},
			envOf("cust-42"),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.usage.recorded.v1","time":"2026-11-23T17:20:04.426849Z","subject":"eccbc87e4b5ce2fe28308fd9f2a7baf3","datacontenttype":"application/json","tenantid":"` + testTenant + `","tenantref":"cust-42","sequence":"1","data":{"cost": {"amount": "0.0042", "status": "known", "currency": "EUR"}, "model": "gpt-4o-mini", "record_id": "` + testEvent + `"}}`},
		{"limit threshold reached, a long sequence",
			Event{Seq: 12345678901, ID: testEvent, Type: "io.pandokos.limit.threshold_reached.v1", Subject: "0192f0f0-0000-7000-8000-0000000000a1", Time: at,
				Data: json.RawMessage(`{"metric": "tokens_per_day", "percent": 80, "limit": 100, "observed": "81.5"}`)},
			envOf("cust-42"),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.limit.threshold_reached.v1","time":"2026-11-23T17:20:04.426849Z","subject":"0192f0f0-0000-7000-8000-0000000000a1","datacontenttype":"application/json","tenantid":"` + testTenant + `","tenantref":"cust-42","sequence":"12345678901","data":{"metric": "tokens_per_day", "percent": 80, "limit": 100, "observed": "81.5"}}`},
		{"limit exhausted without a reference",
			Event{Seq: 7, ID: testEvent, Type: "io.pandokos.limit.exhausted.v1", Subject: "0192f0f0-0000-7000-8000-0000000000a1", Time: at,
				Data: json.RawMessage(`{"metric": "cost_per_month", "amount": "250.5", "observed": "251"}`)},
			envOf(""),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.limit.exhausted.v1","time":"2026-11-23T17:20:04.426849Z","subject":"0192f0f0-0000-7000-8000-0000000000a1","datacontenttype":"application/json","tenantid":"` + testTenant + `","sequence":"7","data":{"metric": "cost_per_month", "amount": "250.5", "observed": "251"}}`},
		{"tenant deletion requested",
			Event{Seq: 3, ID: testEvent, Type: "io.pandokos.tenant.deletion_requested.v1", Subject: testTenant, Time: at,
				Data: json.RawMessage(`{"tenant_id": "` + testTenant + `", "not_before": "2026-11-23T18:20:04.426849Z"}`)},
			envOf("cust-42"),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.tenant.deletion_requested.v1","time":"2026-11-23T17:20:04.426849Z","subject":"` + testTenant + `","datacontenttype":"application/json","tenantid":"` + testTenant + `","tenantref":"cust-42","sequence":"3","data":{"tenant_id": "` + testTenant + `", "not_before": "2026-11-23T18:20:04.426849Z"}}`},
		{"tenant deletion cancelled",
			Event{Seq: 4, ID: testEvent, Type: "io.pandokos.tenant.deletion_cancelled.v1", Subject: testTenant, Time: at, Data: json.RawMessage(`{"tenant_id": "` + testTenant + `"}`)},
			envOf(""),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.tenant.deletion_cancelled.v1","time":"2026-11-23T17:20:04.426849Z","subject":"` + testTenant + `","datacontenttype":"application/json","tenantid":"` + testTenant + `","sequence":"4","data":{"tenant_id": "` + testTenant + `"}}`},
		{"tenant deleted",
			Event{Seq: 5, ID: testEvent, Type: "io.pandokos.tenant.deleted.v1", Subject: testTenant, Time: at, Data: json.RawMessage(`{"completion_seq": 12, "kek_versions": ["local-aaaaaaaaaaaaaaaa"]}`)},
			envOf("cust-42"),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.tenant.deleted.v1","time":"2026-11-23T17:20:04.426849Z","subject":"` + testTenant + `","datacontenttype":"application/json","tenantid":"` + testTenant + `","tenantref":"cust-42","sequence":"5","data":{"completion_seq": 12, "kek_versions": ["local-aaaaaaaaaaaaaaaa"]}}`},
		{"kek version activated: no tenant attributes",
			Event{Seq: 6, ID: testEvent, Type: "io.pandokos.kek.version_activated.v1", Subject: "local-2222222222222222", Time: at, Data: json.RawMessage(`{"version": "local-2222222222222222", "provider": "local"}`)},
			Envelope{Source: "urn:pandokos", TenantRef: "ignored without a tenant"},
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.kek.version_activated.v1","time":"2026-11-23T17:20:04.426849Z","subject":"local-2222222222222222","datacontenttype":"application/json","sequence":"6","data":{"version": "local-2222222222222222", "provider": "local"}}`},
		{"kek version retired",
			Event{Seq: 8, ID: testEvent, Type: "io.pandokos.kek.version_retired.v1", Subject: "local-1111111111111111", Time: at, Data: json.RawMessage(`{"version": "local-1111111111111111", "provider": "local"}`)},
			Envelope{Source: "urn:pandokos"},
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.kek.version_retired.v1","time":"2026-11-23T17:20:04.426849Z","subject":"local-1111111111111111","datacontenttype":"application/json","sequence":"8","data":{"version": "local-1111111111111111", "provider": "local"}}`},
		{"the test event has no sequence",
			Event{ID: testEvent, Type: "io.pandokos.destination.test.v1", Subject: "", Time: at, Data: json.RawMessage(`{"destination_id": "0192f0f0-0000-7000-8000-0000000000d1", "name": "billing"}`)},
			envOf("cust-42"),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.destination.test.v1","time":"2026-11-23T17:20:04.426849Z","datacontenttype":"application/json","tenantid":"` + testTenant + `","tenantref":"cust-42","data":{"destination_id": "0192f0f0-0000-7000-8000-0000000000d1", "name": "billing"}}`},
		{"the source is the option, the time is UTC with microseconds whatever the zone",
			Event{Seq: 9, ID: testEvent, Type: "io.pandokos.usage.recorded.v1", Subject: "s", Time: time.Date(2026, 11, 23, 19, 20, 4, 0, time.FixedZone("CEST", 2*3600)), Data: json.RawMessage(`{}`)},
			Envelope{Source: "https://pandokos.example.test/billing?x=1", TenantID: tid},
			`{"specversion":"1.0","id":"` + testEvent + `","source":"https://pandokos.example.test/billing?x=1","type":"io.pandokos.usage.recorded.v1","time":"2026-11-23T17:20:04.000000Z","subject":"s","datacontenttype":"application/json","tenantid":"` + testTenant + `","sequence":"9","data":{}}`},
		{"a reference with quotes, a backslash and non-ASCII characters is a JSON string",
			Event{Seq: 10, ID: testEvent, Type: "io.pandokos.usage.recorded.v1", Subject: "s", Time: at, Data: json.RawMessage(`{"a" :  "é" ,"b":[1, 2]}`)},
			envOf(`ACME "EU" \ Café é`),
			`{"specversion":"1.0","id":"` + testEvent + `","source":"urn:pandokos","type":"io.pandokos.usage.recorded.v1","time":"2026-11-23T17:20:04.426849Z","subject":"s","datacontenttype":"application/json","tenantid":"` + testTenant + `","tenantref":"ACME \"EU\" \\ Caf\\u00e9 é","sequence":"10","data":{"a" :  "é" ,"b":[1, 2]}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Render(c.e, c.env)
			if string(got) != c.want {
				t.Errorf("the document is\n%s\nwant\n%s", got, c.want)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("not JSON: %v", err)
			}
			if !bytes.Contains(got, c.e.Data) {
				t.Error("the data is not in the document byte for byte")
			}
			if seq, ok := doc["sequence"]; ok && !strings.HasPrefix(string(seq), `"`) {
				t.Errorf("the sequence is %s, want a string", seq)
			}
			if bytes.HasSuffix(got, []byte("\n")) {
				t.Error("the document ends with a newline: a line of an export adds its own")
			}
		})
	}
}

// TestRenderOrder checks the order of the members on a document whose members all exist.
func TestRenderOrder(t *testing.T) {
	got := Render(Event{Seq: 2, ID: testEvent, Type: "io.pandokos.usage.recorded.v1", Subject: "s", Time: at, Data: json.RawMessage(`{"x":1}`)},
		Envelope{Source: "urn:pandokos", TenantID: tenantID(t), TenantRef: "r"})
	dec := json.NewDecoder(bytes.NewReader(got))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"specversion", "id", "source", "type", "time", "subject", "datacontenttype", "tenantid", "tenantref", "sequence", "data"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("the members are %v, want %v", keys, want)
	}
}

// TestSchemas is part of T-1026 and T-R82: the types, and a schema for each of the nine.
func TestSchemas(t *testing.T) {
	if got, want := Types(), []string{"io.pandokos.limit.exhausted.v1", "io.pandokos.limit.threshold_reached.v1", "io.pandokos.usage.recorded.v1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Types() = %v, want the three types of a tenant's log, sorted", got)
	}
	all := append(Types(), "io.pandokos.tenant.deletion_requested.v1", "io.pandokos.tenant.deletion_cancelled.v1", "io.pandokos.tenant.deleted.v1",
		"io.pandokos.kek.version_activated.v1", "io.pandokos.kek.version_retired.v1", "io.pandokos.destination.test.v1")
	for _, typ := range all {
		b, ok := Schema(typ)
		if !ok {
			t.Errorf("no schema for %s", typ)
			continue
		}
		var doc struct {
			Schema     string `json:"$schema"`
			Properties map[string]struct {
				Const string `json:"const"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Errorf("the schema of %s is not JSON: %v", typ, err)
			continue
		}
		if doc.Schema != "https://json-schema.org/draft/2020-12/schema" || doc.Properties["type"].Const != typ || doc.Properties["specversion"].Const != "1.0" {
			t.Errorf("the schema of %s: $schema %q, type const %q, specversion const %q", typ, doc.Schema, doc.Properties["type"].Const, doc.Properties["specversion"].Const)
		}
	}
	if _, ok := Schema("io.pandokos.nothing.v1"); ok {
		t.Error("a schema of a type that does not exist")
	}
}
