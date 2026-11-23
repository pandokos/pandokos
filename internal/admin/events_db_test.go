package admin_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/outbox"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// lines splits an NDJSON body.
func lines(t *testing.T, r *apitest.Response) [][]byte {
	t.Helper()
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(r.Body))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

// TestEventExport is T-1042: the lines of the export are the documents of T-1026; after, limit and
// type; a consumer that follows sequence while writers commit gets every event once; the export of
// the platform's log.
func TestEventExport(t *testing.T) {
	w := newEW(t)
	tid := parseTenant(t, w.tid)
	if _, err := w.api.Super(t).Exec(ctx(t), `UPDATE pandokos.tenant SET external_reference = external_reference WHERE id = $1::uuid`, w.tid); err != nil {
		t.Fatal(err)
	}
	// The platform sets the reference: the lines carry it as it is when they are rendered.
	if r := w.api.PlatformCaller().Do(t, "PATCH", "/v1/tenants/"+w.tid, map[string]any{"external_reference": "cust-42"}, nil); r.Status != 200 {
		t.Fatalf("set the reference: %d %s", r.Status, r.Body)
	}
	w.api.DB.SeedUsageRecords(t, tid, 5)
	if _, err := w.api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
		VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', 80, 10, 8)`, w.tid); err != nil {
		t.Fatal(err)
	}
	events := w.api.DB.OutboxEvents(t, tid)
	if len(events) != 6 {
		t.Fatalf("%d events", len(events))
	}
	expected := func(e dbEvent) []byte {
		return outbox.Render(outbox.Event{Seq: e.Seq, ID: e.ID, Type: e.Type, Subject: e.Subject, Time: e.OccurredAt, Data: e.Data}, outbox.Envelope{Source: "urn:pandokos", TenantID: tid, TenantRef: "cust-42"})
	}

	t.Run("the lines are the documents of the envelope", func(t *testing.T) {
		r := w.owner.Do(t, "GET", "/v1/tenants/"+w.tid+"/events/export", nil, nil)
		if r.Status != 200 || r.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Fatalf("%d %s", r.Status, r.Header.Get("Content-Type"))
		}
		got := lines(t, r)
		if len(got) != 6 {
			t.Fatalf("%d lines for 6 events", len(got))
		}
		for i, l := range got {
			if !bytes.Equal(l, expected(toDB(events[i]))) {
				t.Errorf("line %d is\n%s\nwant\n%s", i, l, expected(toDB(events[i])))
			}
		}
	})
	t.Run("after, limit and type", func(t *testing.T) {
		g := func(q string) [][]byte {
			t.Helper()
			r := w.owner.Do(t, "GET", "/v1/tenants/"+w.tid+"/events/export"+q, nil, nil)
			if r.Status != 200 {
				t.Fatalf("%s: %d %s", q, r.Status, r.Body)
			}
			return lines(t, r)
		}
		if got := g("?after=4"); len(got) != 2 || !bytes.Equal(got[0], expected(toDB(events[4]))) {
			t.Errorf("after=4: %d lines", len(got))
		}
		if got := g("?after=2&limit=2"); len(got) != 2 || !bytes.Equal(got[1], expected(toDB(events[3]))) {
			t.Errorf("after=2&limit=2: %d lines", len(got))
		}
		if got := g("?type=io.pandokos.limit.threshold_reached.v1"); len(got) != 1 {
			t.Errorf("one type: %d lines", len(got))
		}
		if got := g("?type=io.pandokos.limit.threshold_reached.v1&type=io.pandokos.limit.exhausted.v1"); len(got) != 1 {
			t.Errorf("two types: %d lines", len(got))
		}
		if got := g("?type=io.pandokos.usage.recorded.v1&type=io.pandokos.limit.threshold_reached.v1"); len(got) != 6 {
			t.Errorf("repeated type parameters: %d lines", len(got))
		}
		if got := g("?after=6"); len(got) != 0 {
			t.Errorf("after the head: %d lines", len(got))
		}
		if got := g("?after=1000000"); len(got) != 0 {
			t.Errorf("after a number beyond the head: %d lines", len(got))
		}
		for _, bad := range []string{"?limit=0", "?limit=10001", "?after=-1", "?after=x", "?type=io.pandokos.nothing.v1", "?type=io.pandokos.tenant.deleted.v1"} {
			if r := w.owner.Do(t, "GET", "/v1/tenants/"+w.tid+"/events/export"+bad, nil, nil); r.Status != 400 {
				t.Errorf("%s: %d", bad, r.Status)
			}
		}
	})
	t.Run("a consumer that follows the sequence while writers commit sees every event once", func(t *testing.T) {
		x := newEW(t)
		xt := parseTenant(t, x.tid)
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					x.api.DB.SeedUsageRecords(t, xt, 2)
					time.Sleep(3 * time.Millisecond)
				}
			}()
		}
		var after int64
		seen := map[string]int{}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			r := x.owner.Do(t, "GET", fmt.Sprintf("/v1/tenants/%s/events/export?after=%d&limit=25", x.tid, after), nil, nil)
			if r.Status != 200 {
				t.Fatalf("export: %d %s", r.Status, r.Body)
			}
			for _, l := range lines(t, r) {
				var doc struct{ ID, Sequence string }
				if err := json.Unmarshal(l, &doc); err != nil {
					t.Fatal(err)
				}
				seq, _ := strconv.ParseInt(doc.Sequence, 10, 64)
				if seq <= after {
					t.Fatalf("sequence %d after %d", seq, after)
				}
				after = seq
				seen[doc.ID]++
			}
		}
		close(stop)
		wg.Wait()
		for { // the rest
			r := x.owner.Do(t, "GET", fmt.Sprintf("/v1/tenants/%s/events/export?after=%d&limit=1000", x.tid, after), nil, nil)
			ls := lines(t, r)
			if len(ls) == 0 {
				break
			}
			for _, l := range ls {
				var doc struct{ ID, Sequence string }
				_ = json.Unmarshal(l, &doc)
				after, _ = strconv.ParseInt(doc.Sequence, 10, 64)
				seen[doc.ID]++
			}
		}
		all := x.api.DB.OutboxEvents(t, xt)
		if len(seen) != len(all) || len(all) < 20 {
			t.Fatalf("the consumer saw %d events of %d", len(seen), len(all))
		}
		for _, e := range all {
			if seen[e.ID] != 1 {
				t.Errorf("event %s seen %d times", e.ID, seen[e.ID])
			}
		}
	})
	t.Run("the platform's export", func(t *testing.T) {
		plat := w.api.PlatformCaller()
		victim, _ := w.api.Tenant(t, apitest.UniqueName("exported"))
		if r := plat.Do(t, "PATCH", "/v1/tenants/"+victim.ID, map[string]any{"external_reference": "cust-9"}, nil); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		w.api.DB.RequestDeletion(t, parseTenant(t, victim.ID), time.Now().Add(time.Hour))
		r := plat.Do(t, "GET", "/v1/platform-events/export", nil, nil)
		if r.Status != 200 || r.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Fatalf("%d %s", r.Status, r.Header.Get("Content-Type"))
		}
		var found bool
		for _, l := range lines(t, r) {
			var doc map[string]any
			if err := json.Unmarshal(l, &doc); err != nil {
				t.Fatal(err)
			}
			if doc["type"] == "io.pandokos.tenant.deletion_requested.v1" && doc["tenantid"] == victim.ID {
				found = true
				if doc["tenantref"] != "cust-9" || doc["source"] != "urn:pandokos" || doc["sequence"] == nil {
					t.Errorf("the platform event: %s", l)
				}
			}
			if t2, _ := doc["type"].(string); t2 == "io.pandokos.kek.version_activated.v1" && (doc["tenantid"] != nil || doc["tenantref"] != nil) {
				t.Errorf("a key event with tenant attributes: %s", l)
			}
		}
		if !found {
			t.Error("the deletion request is not in the platform's export")
		}
		only := plat.Do(t, "GET", "/v1/platform-events/export?type=io.pandokos.kek.version_activated.v1", nil, nil)
		for _, l := range lines(t, only) {
			var doc map[string]any
			_ = json.Unmarshal(l, &doc)
			if doc["type"] != "io.pandokos.kek.version_activated.v1" {
				t.Errorf("a line of another type: %s", l)
			}
		}
		// No tenant event and no count of one is in it.
		for _, l := range lines(t, r) {
			var doc map[string]any
			_ = json.Unmarshal(l, &doc)
			if tp, _ := doc["type"].(string); tp == "io.pandokos.usage.recorded.v1" || tp == "io.pandokos.limit.exhausted.v1" {
				t.Errorf("a tenant's event in the platform's export: %s", l)
			}
		}
	})
}

// TestEventPermissions is T-1043: the twelve operations for every built-in role; another tenant's
// principal and another tenant's destination are 404, as for every tenant operation (T-270); a
// platform principal gets 403 without a support action and, with one, only what
// event_destination:read allows; exportEvents never under a support action.
func TestEventPermissions(t *testing.T) {
	w := newEW(t)
	other := newEW(t)
	dest := w.api.SeedEventDestination(t, w.tid)
	otherDest := other.api.SeedEventDestination(t, other.tid)
	_ = otherDest
	ops := map[string]adminapi.Operation{}
	for _, op := range adminapi.Operations {
		switch op.ID {
		case "listEventDestinations", "createEventDestination", "getEventDestination", "updateEventDestination", "deleteEventDestination", "rotateEventDestinationSecret",
			"testEventDestination", "rewindEventDestination", "listEventDeadLetters", "replayEventDeadLetters", "exportEvents", "exportPlatformEvents":
			ops[op.ID] = op
		}
	}
	if len(ops) != 12 {
		t.Fatalf("%d operations of events, want 12", len(ops))
	}
	params := apitest.Params{Tenant: w.tid, Destination: dest}
	// A probe of deleteEventDestination that succeeds removes its destination: it deletes a fresh one,
	// and removes it after the call if the call was refused, so the other probes still find theirs.
	probe := func(who apitest.Doer, op adminapi.Operation) *apitest.Response {
		t.Helper()
		if op.ID != "deleteEventDestination" {
			return apitest.Probe(t, who, op, params)
		}
		p := params
		p.Destination = w.api.SeedEventDestination(t, w.tid)
		r := apitest.Probe(t, who, op, p)
		if _, err := w.api.Super(t).Exec(ctx(t), `DELETE FROM pandokos.event_destination WHERE id = $1::uuid`, p.Destination); err != nil {
			t.Fatal(err)
		}
		return r
	}
	holds := map[string][]string{
		authz.EventDestinationRead:   {"owner", "tenant-admin", "auditor", "read-only"},
		authz.EventDestinationWrite:  {"owner", "tenant-admin"},
		authz.EventDestinationDelete: {"owner", "tenant-admin"},
		authz.EventRead:              {"owner", "tenant-admin", "usage-viewer", "read-only"},
		authz.PlatformEventRead:      {"platform-admin", "platform-auditor"},
	}
	has := func(role, perm string) bool {
		for _, r := range holds[perm] {
			if r == role {
				return true
			}
		}
		return false
	}
	tenantRoles := []string{"owner", "tenant-admin", "developer", "credential-manager", "usage-viewer", "auditor", "read-only", "inference"}
	for _, role := range tenantRoles {
		who := w.api.AsRole(t, w.tid, role)
		if role == "owner" {
			who = w.owner
		}
		for _, op := range ops {
			if op.ID == "exportPlatformEvents" {
				continue
			}
			r := probe(who, op)
			if has(role, op.Permission) {
				if r.Status < 200 || r.Status > 299 {
					t.Errorf("%s as %s: %d %s, want success", op.ID, role, r.Status, r.Body)
				}
			} else if !apitest.Refused(r) {
				t.Errorf("%s as %s: %d %s, want 403 forbidden", op.ID, role, r.Status, r.Body)
			}
		}
	}
	for _, role := range []string{"platform-admin", "platform-auditor"} {
		who, _ := w.api.PlatformAs(t, role)
		for _, op := range ops {
			r := probe(who, op)
			switch {
			case op.ID == "exportPlatformEvents" && has(role, op.Permission):
				if r.Status != 200 {
					t.Errorf("%s as %s: %d %s", op.ID, role, r.Status, r.Body)
				}
			case op.ID != "exportPlatformEvents":
				// A platform principal reaches no tenant operation of events without a support action.
				if !apitest.Refused(r) {
					t.Errorf("%s as %s without a support action: %d %s, want 403", op.ID, role, r.Status, r.Body)
				}
			}
		}
	}
	// A tenant principal does not read the platform's log.
	for _, role := range []string{"owner", "tenant-admin", "read-only"} {
		who := w.api.AsRole(t, w.tid, role)
		if r := apitest.Probe(t, who, ops["exportPlatformEvents"], params); !apitest.Refused(r) {
			t.Errorf("%s reads the platform's events: %d", role, r.Status)
		}
	}
	// Another tenant's principal: 404 for the tenant and for its destination, as for any tenant operation.
	for _, op := range ops {
		if op.ID == "exportPlatformEvents" {
			continue
		}
		r := apitest.Probe(t, other.owner, op, params)
		missing := apitest.Probe(t, other.owner, op, apitest.Params{Tenant: apitest.NoUser, Destination: apitest.NoUser})
		if r.Status != 404 || missing.Status != 404 {
			t.Errorf("%s from another tenant: %d, from a tenant that does not exist: %d, want 404 for both", op.ID, r.Status, missing.Status)
		}
	}
	// The destination of another tenant, under the tenant's own path.
	for _, id := range []string{"getEventDestination", "updateEventDestination", "deleteEventDestination", "rotateEventDestinationSecret", "testEventDestination", "rewindEventDestination", "listEventDeadLetters", "replayEventDeadLetters"} {
		r := apitest.Probe(t, w.owner, ops[id], apitest.Params{Tenant: w.tid, Destination: otherDest})
		m := apitest.Probe(t, w.owner, ops[id], apitest.Params{Tenant: w.tid, Destination: apitest.NoUser})
		if r.Status != 404 || m.Status != 404 {
			t.Errorf("%s: another tenant's destination %d, a missing one %d, want 404", id, r.Status, m.Status)
		}
	}
	// A support action holds event_destination:read and nothing else of events.
	plat := w.api.PlatformCaller()
	action := apitest.OpenSupport(t, plat, w.tid, authz.EventDestinationRead)
	_ = action
	for _, op := range ops {
		if op.ID == "exportPlatformEvents" {
			continue
		}
		r := probe(plat, op)
		if op.Permission == authz.EventDestinationRead {
			if r.Status < 200 || r.Status > 299 {
				t.Errorf("%s under a support action that holds event_destination:read: %d %s", op.ID, r.Status, r.Body)
			}
		} else if !apitest.Refused(r) {
			t.Errorf("%s under a support action: %d %s, want 403", op.ID, r.Status, r.Body)
		}
	}
	// Support can carry event_destination:read and cannot carry the export of usage data.
	if r := plat.Do(t, http.MethodPost, "/v1/tenant-support/"+w.tid+"/actions", map[string]any{"reason": "x", "permissions": []string{authz.EventRead}}, nil); r.Status != 400 {
		t.Errorf("a support action that asks for event:read: %d %s, want 400", r.Status, r.Body)
	}
	if r := plat.Do(t, http.MethodPost, "/v1/tenant-support/"+w.tid+"/actions", map[string]any{"reason": "x", "permissions": []string{authz.EventDestinationWrite}}, nil); r.Status != 400 {
		t.Errorf("a support action that asks for event_destination:write: %d %s, want 400", r.Status, r.Body)
	}
}

// dbEvent is the part of an event the export test needs, whatever the helper's type is called.
type dbEvent struct {
	Seq        int64
	ID         string
	Type       string
	Subject    string
	OccurredAt time.Time
	Data       json.RawMessage
}

type dbtestOutboxEvent = dbtest.OutboxEvent

func toDB(e dbtestOutboxEvent) dbEvent {
	return dbEvent{Seq: e.Seq, ID: e.ID, Type: e.Type, Subject: e.Subject, OccurredAt: e.OccurredAt, Data: e.Data}
}
