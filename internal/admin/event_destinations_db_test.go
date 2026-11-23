package admin_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

const hookURL = "https://hooks.example.test/in"

// TestEventDestinationAPI is T-1040: create, read, list, update, delete; the validation table; the
// secret once and absent on an idempotent replay; delivery as section 5.8; the six audit actions
// with the host only and no secret.
func TestEventDestinationAPI(t *testing.T) {
	w := newEW(t)
	tid := parseTenant(t, w.tid)

	t.Run("create answers 201 with the destination and the secret once", func(t *testing.T) {
		const secretPath = "/in/CANARY-path-token?token=CANARY-query-token"
		before := time.Now().Add(-2 * time.Second)
		r := w.owner.Do(t, "POST", w.path(""), map[string]any{"name": "billing", "description": "the billing system", "url": "https://hooks.example.test" + secretPath,
			"event_types": []string{"io.pandokos.usage.recorded.v1"}, "enabled": true}, nil)
		got := decode[adminapi.EventDestinationCreated](t, r, 201)
		d := got.Destination
		if !strings.HasPrefix(got.Secret, "whsec_") || len(got.Secret) != len("whsec_")+44 {
			t.Errorf("the secret is %d characters: want whsec_ and the base64 of 32 bytes", len(got.Secret))
		}
		if d.Name != "billing" || d.TenantID != w.tid || d.Description != "the billing system" || d.URL != "https://hooks.example.test"+secretPath || !d.Enabled ||
			len(d.EventTypes) != 1 || d.EventTypes[0] != adminapi.EventTypeIoPandokosUsageRecordedV1 || d.CreatedAt.Before(before) || d.PreviousSecretExpiresAt != nil || d.Test != nil {
			t.Errorf("the destination: %+v", d)
		}
		if d.Delivery.State != adminapi.EventDeliveryStateIdle || d.Delivery.Cursor != 0 || d.Delivery.Head != 0 || d.Delivery.Failures != 0 || d.Delivery.DeadLetters != 0 || d.Delivery.ExpiredEvents != 0 {
			t.Errorf("the delivery of a new destination: %+v", d.Delivery)
		}
		// The secret is not in any later response, nor in the stored row's readable columns.
		again := w.owner.Do(t, "GET", w.path("/"+d.ID), nil, nil)
		list := w.owner.Do(t, "GET", w.path(""), nil, nil)
		for what, body := range map[string]string{"get": string(again.Body), "list": string(list.Body)} {
			if strings.Contains(body, got.Secret) || strings.Contains(body, `"secret"`) {
				t.Errorf("the %s response shows the secret or a member for it", what)
			}
		}
		// A destination starts at the head of the log.
		w.api.DB.SeedUsageRecords(t, tid, 3)
		next := w.create(t, "second", hookURL)
		if next.Destination.Delivery.Cursor != 3 || next.Destination.Delivery.Head != 3 {
			t.Errorf("a destination created after 3 events: %+v, want the cursor at the head", next.Destination.Delivery)
		}
		// Enabled defaults to true.
		def := w.owner.Do(t, "POST", w.path(""), map[string]any{"name": "defaults", "url": hookURL}, nil)
		if d := decode[adminapi.EventDestinationCreated](t, def, 201); !d.Destination.Enabled || len(d.Destination.EventTypes) != 0 {
			t.Errorf("the defaults: %+v", d.Destination)
		}
	})

	t.Run("the secret is absent on an idempotent replay", func(t *testing.T) {
		body := map[string]any{"name": "replayed", "url": hookURL, "enabled": true}
		first := w.owner.Do(t, "POST", w.path(""), body, idem("create-1"))
		a := decode[adminapi.EventDestinationCreated](t, first, 201)
		second := w.owner.Do(t, "POST", w.path(""), body, idem("create-1"))
		b := decode[adminapi.EventDestinationCreated](t, second, 201)
		if second.Header.Get("Idempotency-Replayed") != "true" || b.Destination.ID != a.Destination.ID || b.Secret != "" || a.Secret == "" {
			t.Errorf("replayed %q, ids %s %s, secrets %q %q", second.Header.Get("Idempotency-Replayed"), a.Destination.ID, b.Destination.ID, a.Secret, b.Secret)
		}
		if strings.Contains(string(second.Body), a.Secret) {
			t.Error("the replay shows the secret")
		}
		if other := w.owner.Do(t, "POST", w.path(""), map[string]any{"name": "replayed-2", "url": hookURL}, idem("create-1")); other.Status != 422 {
			t.Errorf("another body under the same key: %d %s", other.Status, other.Body)
		}
	})

	t.Run("read, list, update, delete", func(t *testing.T) {
		d := w.create(t, "crud", hookURL).Destination
		if got := w.get(t, d.ID); got.ID != d.ID || got.Name != "crud" {
			t.Errorf("get: %+v", got)
		}
		list := decode[adminapi.EventDestinationList](t, w.owner.Do(t, "GET", w.path("?limit=1"), nil, nil), 200)
		if len(list.Items) != 1 || list.NextCursor == "" {
			t.Fatalf("a page of one: %+v", list)
		}
		seen := map[string]bool{list.Items[0].ID: true}
		for cursor := list.NextCursor; cursor != ""; {
			page := decode[adminapi.EventDestinationList](t, w.owner.Do(t, "GET", w.path("?limit=1&cursor="+cursor), nil, nil), 200)
			for _, it := range page.Items {
				if seen[it.ID] {
					t.Errorf("%s listed twice", it.ID)
				}
				seen[it.ID] = true
			}
			cursor = page.NextCursor
		}
		if !seen[d.ID] || len(seen) < 4 {
			t.Errorf("the pages listed %d destinations", len(seen))
		}
		upd := decode[adminapi.EventDestination](t, w.owner.Do(t, "PATCH", w.path("/"+d.ID), map[string]any{"description": "changed", "url": "https://other.example.test/x", "event_types": []string{"io.pandokos.limit.exhausted.v1"}, "enabled": false}, nil), 200)
		if upd.Description != "changed" || upd.URL != "https://other.example.test/x" || upd.Enabled || len(upd.EventTypes) != 1 || upd.Delivery.State != adminapi.EventDeliveryStateDisabled || !upd.UpdatedAt.After(d.UpdatedAt) {
			t.Errorf("the update: %+v", upd)
		}
		if r := w.owner.Do(t, "PATCH", w.path("/"+d.ID), map[string]any{"event_types": []string{}}, nil); r.Status != 200 {
			t.Errorf("an empty list of types (every type): %d %s", r.Status, r.Body)
		}
		if del := w.owner.Do(t, "DELETE", w.path("/"+d.ID), nil, nil); del.Status != 204 {
			t.Errorf("delete: %d %s", del.Status, del.Body)
		}
		if r := w.owner.Do(t, "GET", w.path("/"+d.ID), nil, nil); r.Status != 404 || r.Problem() != "not_found" {
			t.Errorf("get after delete: %d %s", r.Status, r.Body)
		}
		if n := w.api.DB.Count(t, `SELECT count(*)::int FROM pandokos.event_destination_directory WHERE destination_id = $1::uuid`, d.ID); n != 0 {
			t.Errorf("the directory still lists the deleted destination")
		}
	})

	t.Run("the validation table", func(t *testing.T) {
		base := func(o map[string]any) map[string]any {
			b := map[string]any{"name": apitest.UniqueName("v"), "url": hookURL, "enabled": true}
			for k, v := range o {
				b[k] = v
			}
			return b
		}
		for what, c := range map[string]struct {
			body  map[string]any
			field string
		}{
			"an http URL for a host that is not listed": {base(map[string]any{"url": "http://hooks.example.test/in"}), "url"},
			"an ftp URL":                      {base(map[string]any{"url": "ftp://hooks.example.test/in"}), "url"},
			"no scheme":                       {base(map[string]any{"url": "hooks.example.test/in"}), "url"},
			"user information":                {base(map[string]any{"url": "https://user:pass@hooks.example.test/in"}), "url"},
			"a fragment":                      {base(map[string]any{"url": "https://hooks.example.test/in#frag"}), "url"},
			"a loopback literal":              {base(map[string]any{"url": "https://127.0.0.1/in"}), "url"},
			"the metadata address":            {base(map[string]any{"url": "https://169.254.169.254/latest"}), "url"},
			"a private literal":               {base(map[string]any{"url": "https://10.0.0.8/in"}), "url"},
			"a URL of 2049 characters":        {base(map[string]any{"url": "https://hooks.example.test/" + strings.Repeat("a", 2049-len("https://hooks.example.test/"))}), "url"},
			"a URL that is not one":           {base(map[string]any{"url": "not a url at all"}), "url"},
			"an unknown type":                 {base(map[string]any{"event_types": []string{"io.pandokos.nothing.v1"}}), "event_types"},
			"a platform type":                 {base(map[string]any{"event_types": []string{"io.pandokos.tenant.deleted.v1"}}), "event_types"},
			"the test type":                   {base(map[string]any{"event_types": []string{"io.pandokos.destination.test.v1"}}), "event_types"},
			"a repeated type":                 {base(map[string]any{"event_types": []string{"io.pandokos.usage.recorded.v1", "io.pandokos.usage.recorded.v1"}}), "event_types"},
			"an upper-case name":              {base(map[string]any{"name": "Billing"}), "name"},
			"an empty name":                   {base(map[string]any{"name": ""}), "name"},
			"a name with a dot":               {base(map[string]any{"name": "a.b"}), "name"},
			"a name of 64 characters":         {base(map[string]any{"name": strings.Repeat("a", 64)}), "name"},
			"a description of 501 characters": {base(map[string]any{"description": strings.Repeat("a", 501)}), "description"},
			"no name":                         {map[string]any{"url": hookURL}, "name"},
			"no URL":                          {map[string]any{"name": "nourl"}, "url"},
			"an unknown member":               {base(map[string]any{"secret": "whsec_mine"}), "secret"},
		} {
			r := w.owner.Do(t, "POST", w.path(""), c.body, nil)
			if r.Status != 400 || r.Problem() != "invalid_request" || !hasField(problemOf(t, r), c.field) {
				t.Errorf("%s: %d %s, want 400 invalid_request on %s", what, r.Status, r.Body, c.field)
			}
			if strings.Contains(string(r.Body), "pass@") || strings.Contains(string(r.Body), "whsec_mine") {
				t.Errorf("%s: the error quotes the value: %s", what, r.Body)
			}
		}
		if r := w.owner.Do(t, "PATCH", w.path("/"+w.create(t, apitest.UniqueName("p"), hookURL).Destination.ID), map[string]any{}, nil); r.Status != 400 {
			t.Errorf("an update with no member: %d", r.Status)
		}
	})

	t.Run("a name already used and a ninth destination are 409", func(t *testing.T) {
		x := newEW(t)
		for i := 0; i < 8; i++ {
			x.create(t, fmt.Sprintf("d%d", i), hookURL)
		}
		if r := x.owner.Do(t, "POST", x.path(""), map[string]any{"name": "d8", "url": hookURL, "enabled": true}, nil); r.Status != 409 || r.Problem() != "conflict" {
			t.Errorf("a ninth destination: %d %s", r.Status, r.Body)
		}
		if r := x.owner.Do(t, "POST", x.path(""), map[string]any{"name": "d0", "url": hookURL, "enabled": true}, nil); r.Status != 409 || r.Problem() != "conflict" {
			t.Errorf("a name already used: %d %s", r.Status, r.Body)
		}
		// A rename to a name in use is the same conflict.
		list := decode[adminapi.EventDestinationList](t, x.owner.Do(t, "GET", x.path(""), nil, nil), 200)
		if r := x.owner.Do(t, "PATCH", x.path("/"+list.Items[0].ID), map[string]any{"name": list.Items[1].Name}, nil); r.Status != 409 {
			t.Errorf("a rename to a name in use: %d %s", r.Status, r.Body)
		}
	})

	t.Run("delivery as section 5.8", func(t *testing.T) {
		x := newEW(t)
		xt := parseTenant(t, x.tid)
		d := x.create(t, "state", hookURL).Destination
		x.api.DB.SeedUsageRecords(t, xt, 4)
		if got := x.get(t, d.ID).Delivery; got.State != adminapi.EventDeliveryStateDelivering || got.Cursor != 0 || got.Head != 4 {
			t.Errorf("behind the head: %+v, want delivering, cursor 0, head 4", got)
		}
		x.api.DB.Age(t, "event_destination", "id = '"+d.ID+"'::uuid", 0, "created_at")
		sql := func(q string, args ...any) {
			t.Helper()
			if _, err := x.api.Super(t).Exec(ctx(t), q, args...); err != nil {
				t.Fatalf("%v\n%s", err, q)
			}
		}
		sql(`UPDATE pandokos.event_destination SET cursor_seq = 4 WHERE id = $1::uuid`, d.ID)
		if got := x.get(t, d.ID).Delivery.State; got != adminapi.EventDeliveryStateIdle {
			t.Errorf("at the head: %s, want idle", got)
		}
		sql(`UPDATE pandokos.event_destination SET failures = 3, failing_since = now() - interval '1 minute', last_error = 'status', last_status = 503, last_failure_at = now(), expired_events = 7 WHERE id = $1::uuid`, d.ID)
		sql(`INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason, status) VALUES ($1::uuid, $2::uuid, 2, pandokos.uuid_v7(), 'io.pandokos.usage.recorded.v1', 10, 'status', 503)`, x.tid, d.ID)
		got := x.get(t, d.ID).Delivery
		if got.State != adminapi.EventDeliveryStateFailing || got.Failures != 3 || got.FailingSince == nil || got.LastError != "status" || got.LastStatus != 503 || got.LastFailureAt == nil || got.DeadLetters != 1 || got.ExpiredEvents != 7 {
			t.Errorf("a failing destination: %+v", got)
		}
		sql(`UPDATE pandokos.event_destination SET enabled = false WHERE id = $1::uuid`, d.ID)
		if got := x.get(t, d.ID).Delivery.State; got != adminapi.EventDeliveryStateDisabled {
			t.Errorf("a disabled destination: %s", got)
		}
		// Enabling a disabled destination clears the failure state and resumes at its cursor.
		on := decode[adminapi.EventDestination](t, x.owner.Do(t, "PATCH", x.path("/"+d.ID), map[string]any{"enabled": true}, nil), 200)
		if on.Delivery.Failures != 0 || on.Delivery.FailingSince != nil || on.Delivery.Cursor != 4 || on.Delivery.State != adminapi.EventDeliveryStateIdle {
			t.Errorf("after enabling: %+v", on.Delivery)
		}
	})

	t.Run("the six audit actions hold the host only and no secret", func(t *testing.T) {
		x := newEW(t)
		xt := parseTenant(t, x.tid)
		const urlWithSecrets = "https://audit.example.test:8443/in/CANARY-path?token=CANARY-query"
		created := x.create(t, "audited", urlWithSecrets, "io.pandokos.usage.recorded.v1")
		id := created.Destination.ID
		x.owner.Do(t, "PATCH", x.path("/"+id), map[string]any{"description": "audited change", "url": "https://audit2.example.test/other/CANARY-path2?t=CANARY-query2"}, nil)
		rot := decode[adminapi.EventDestinationCreated](t, x.owner.Do(t, "POST", x.path("/"+id+"/rotate-secret"), map[string]any{}, nil), 200)
		x.api.DB.SeedUsageRecords(t, xt, 2)
		x.owner.Do(t, "POST", x.path("/"+id+"/rewind"), map[string]any{"after": 1}, nil)
		if _, err := x.api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason) VALUES ($1::uuid, $2::uuid, 2, pandokos.uuid_v7(), 'io.pandokos.usage.recorded.v1', 10, 'status')`, x.tid, id); err != nil {
			t.Fatal(err)
		}
		x.owner.Do(t, "POST", x.path("/"+id+"/dead-letters/replay"), map[string]any{}, nil)
		x.owner.Do(t, "DELETE", x.path("/"+id), nil, nil)

		byAction := map[string]map[string]any{}
		for _, row := range x.api.DB.AuditRows(t, xt) {
			if strings.HasPrefix(row.Action, "event_destination.") {
				if row.ResourceType != "event_destination" || row.ResourceID != id || row.Outcome != "success" || row.ActorType != "service_account" {
					t.Errorf("the row %+v", row)
				}
				byAction[row.Action] = row.Detail
			}
		}
		for _, action := range []string{"event_destination.create", "event_destination.update", "event_destination.rotate_secret", "event_destination.rewind", "event_destination.replay", "event_destination.delete"} {
			if byAction[action] == nil {
				t.Errorf("no audit row %s (rows: %v)", action, byAction)
			}
		}
		if d := byAction["event_destination.create"]; d["name"] != "audited" || d["host"] != "audit.example.test:8443" && d["host"] != "audit.example.test" || d["enabled"] != true {
			t.Errorf("the detail of create: %v, want the name, the host and enabled", d)
		}
		if d := byAction["event_destination.update"]; d["host"] != "audit2.example.test" {
			t.Errorf("the detail of update: %v, want the new host", d)
		}
		if d := byAction["event_destination.rewind"]; d["cursor"] != float64(1) {
			t.Errorf("the detail of rewind: %v, want the cursor", d)
		}
		if d := byAction["event_destination.replay"]; d["count"] != float64(1) {
			t.Errorf("the detail of replay: %v, want the count", d)
		}
		all := x.api.DB.AllAuditText(t)
		if found := contains(all, created.Secret, rot.Secret, strings.TrimPrefix(created.Secret, "whsec_"), "CANARY-path", "CANARY-query", "whsec_"); len(found) > 0 {
			t.Errorf("the audit log holds %v: the host only, never a path, a query or a secret", found)
		}
	})
}

// TestEventDestinationOperations is T-1041: rotation, the test request, rewind, dead letters.
func TestEventDestinationOperations(t *testing.T) {
	w := newEW(t)
	tid := parseTenant(t, w.tid)

	t.Run("rotation keeps the previous key for the time asked, at most 7 days", func(t *testing.T) {
		d := w.create(t, "rotate", hookURL)
		r := w.owner.Do(t, "POST", w.path("/"+d.Destination.ID+"/rotate-secret"), map[string]any{}, idem("rot-1"))
		rot := decode[adminapi.EventDestinationCreated](t, r, 200)
		if rot.Secret == "" || rot.Secret == d.Secret || !strings.HasPrefix(rot.Secret, "whsec_") {
			t.Errorf("the new secret %q", rot.Secret)
		}
		exp := rot.Destination.PreviousSecretExpiresAt
		if exp == nil || time.Until(*exp) < 23*time.Hour || time.Until(*exp) > 25*time.Hour || !rot.Destination.SecretCreatedAt.After(d.Destination.SecretCreatedAt) {
			t.Errorf("the previous key expires at %v, want 24 h from now by default", exp)
		}
		again := w.owner.Do(t, "POST", w.path("/"+d.Destination.ID+"/rotate-secret"), map[string]any{}, idem("rot-1"))
		if b := decode[adminapi.EventDestinationCreated](t, again, 200); b.Secret != "" || again.Header.Get("Idempotency-Replayed") != "true" {
			t.Errorf("a replay of the rotation shows a secret or is not marked: %q", b.Secret)
		}
		custom := decode[adminapi.EventDestinationCreated](t, w.owner.Do(t, "POST", w.path("/"+d.Destination.ID+"/rotate-secret"), map[string]any{"previous_valid_for_seconds": 3600}, nil), 200)
		if e := custom.Destination.PreviousSecretExpiresAt; e == nil || time.Until(*e) > 61*time.Minute || time.Until(*e) < 59*time.Minute {
			t.Errorf("an hour asked: %v", e)
		}
		none := decode[adminapi.EventDestinationCreated](t, w.owner.Do(t, "POST", w.path("/"+d.Destination.ID+"/rotate-secret"), map[string]any{"previous_valid_for_seconds": 0}, nil), 200)
		if none.Destination.PreviousSecretExpiresAt != nil {
			t.Errorf("no overlap asked: the previous key stays valid until %v", none.Destination.PreviousSecretExpiresAt)
		}
		week := decode[adminapi.EventDestinationCreated](t, w.owner.Do(t, "POST", w.path("/"+d.Destination.ID+"/rotate-secret"), map[string]any{"previous_valid_for_seconds": 604800}, nil), 200)
		if week.Destination.PreviousSecretExpiresAt == nil {
			t.Error("7 days asked: no expiry")
		}
		for _, bad := range []int{604801, -1} {
			if r := w.owner.Do(t, "POST", w.path("/"+d.Destination.ID+"/rotate-secret"), map[string]any{"previous_valid_for_seconds": bad}, nil); r.Status != 400 {
				t.Errorf("%d seconds: %d %s", bad, r.Status, r.Body)
			}
		}
		// The sealed previous secret is the replaced one: the column of the previous secret is set, and differs from the new one.
		var same bool
		if err := w.api.Super(t).QueryRow(ctx(t), `SELECT previous_secret IS NOT NULL AND previous_secret <> secret FROM pandokos.event_destination WHERE id = $1::uuid`, d.Destination.ID).Scan(&same); err != nil || !same {
			t.Errorf("the previous secret is not kept: %v %v", same, err)
		}
	})

	t.Run("the test request answers 202 and the handler sends nothing", func(t *testing.T) {
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(204) }))
		t.Cleanup(srv.Close)
		d := w.create(t, "nosend", strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/t").Destination
		r := w.owner.Do(t, "POST", w.path("/"+d.ID+"/test"), nil, nil)
		got := decode[adminapi.EventDestinationTest](t, r, 202)
		if got.EventID == "" || got.RequestedAt.IsZero() || got.CompletedAt != nil || got.Outcome != "" {
			t.Errorf("the answer: %+v", got)
		}
		time.Sleep(300 * time.Millisecond)
		if hits.Load() != 0 {
			t.Errorf("the handler made %d requests: the worker sends the test", hits.Load())
		}
		if shown := w.get(t, d.ID).Test; shown == nil || shown.EventID != got.EventID || shown.CompletedAt != nil {
			t.Errorf("the destination shows the test as %+v", shown)
		}
		if n := len(w.api.DB.OutboxEvents(t, tid)); n != 0 {
			t.Errorf("the test wrote %d events to the log", n)
		}
	})

	t.Run("with a relay running, test shows each outcome, nothing is written to the log", func(t *testing.T) {
		x := newEW(t)
		xt := parseTenant(t, x.tid)
		x.startRelay(t, 400*time.Millisecond)
		var mode atomic.Value
		mode.Store("204")
		var lastID, lastSeq atomic.Value
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lastID.Store(r.Header.Get("webhook-id"))
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			lastSeq.Store(string(body[:n]))
			switch mode.Load().(string) {
			case "500":
				w.WriteHeader(500)
			case "302":
				http.Redirect(w, r, "http://localhost:1/elsewhere", http.StatusFound)
			case "stall":
				// Ends with the client that gave up, or srv.Close would wait for it.
				select {
				case <-release:
				case <-r.Context().Done():
				}
			default:
				w.WriteHeader(204)
			}
		}))
		t.Cleanup(srv.Close)
		url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/t"
		d := x.create(t, "outcomes", url).Destination
		x.api.DB.SeedUsageRecords(t, xt, 2)
		eventsBefore := len(x.api.DB.OutboxEvents(t, xt))
		// The events seeded above are delivered first: the test requests start from a settled cursor.
		for deadline := time.Now().Add(15 * time.Second); x.get(t, d.ID).Delivery.Cursor < 2 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		}
		run := func(want string, status int, disabled bool) {
			t.Helper()
			if disabled {
				x.owner.Do(t, "PATCH", x.path("/"+d.ID), map[string]any{"enabled": false}, nil)
			}
			before := x.get(t, d.ID).Delivery
			req := decode[adminapi.EventDestinationTest](t, x.owner.Do(t, "POST", x.path("/"+d.ID+"/test"), nil, nil), 202)
			var got *adminapi.EventDestinationTest
			for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				if g := x.get(t, d.ID).Test; g != nil && g.EventID == req.EventID && g.CompletedAt != nil {
					got = g
					break
				}
			}
			if got == nil {
				t.Fatalf("%s: the test request was not sent", want)
			}
			if string(got.Outcome) != want || got.Status != status {
				t.Errorf("%s: the outcome is %q (status %d), want %q (%d)", want, got.Outcome, got.Status, want, status)
			}
			if id, _ := lastID.Load().(string); want != "connection" && id != req.EventID {
				t.Errorf("%s: the event ID of the request is %q, want the one answered with 202", want, id)
			}
			if body, _ := lastSeq.Load().(string); want != "connection" && (strings.Contains(body, `"sequence"`) || !strings.Contains(body, "io.pandokos.destination.test.v1")) {
				t.Errorf("%s: the test event is %s: a test event has no sequence", want, body)
			}
			after := x.get(t, d.ID).Delivery
			if after.Cursor != before.Cursor && !disabled || after.Failures != before.Failures || after.DeadLetters != before.DeadLetters {
				t.Errorf("%s: the test moved the cursor or the failure state: %+v then %+v", want, before, after)
			}
		}
		run("delivered", 204, false)
		mode.Store("500")
		run("status", 500, false)
		mode.Store("302")
		run("redirect", 302, false)
		mode.Store("stall")
		run("timeout", 0, false)
		mode.Store("204")
		run("delivered", 204, true) // a disabled destination is tested too
		if n := len(x.api.DB.OutboxEvents(t, xt)); n != eventsBefore {
			t.Errorf("%d events in the log after the tests, want %d", n, eventsBefore)
		}
		// A destination that cannot be reached: connection.
		srv.Close()
		run("connection", 0, false)
	})

	t.Run("rewind, in both forms and their validation", func(t *testing.T) {
		x := newEW(t)
		xt := parseTenant(t, x.tid)
		d := x.create(t, "rewind", hookURL).Destination
		x.api.DB.SeedUsageRecords(t, xt, 6)
		if _, err := x.api.Super(t).Exec(ctx(t), `UPDATE pandokos.event_destination SET cursor_seq = 6, failures = 2, failing_since = now(), lease_token = pandokos.uuid_v7(), lease_until = now() + interval '1 minute' WHERE id = $1::uuid`, d.ID); err != nil {
			t.Fatal(err)
		}
		got := decode[adminapi.EventDestination](t, x.owner.Do(t, "POST", x.path("/"+d.ID+"/rewind"), map[string]any{"after": 3}, nil), 200)
		if got.Delivery.Cursor != 3 || got.Delivery.Failures != 0 || got.Delivery.FailingSince != nil {
			t.Errorf("a rewind to 3: %+v, want the cursor set back and the failure state cleared", got.Delivery)
		}
		if n := x.api.DB.Count(t, `SELECT count(*)::int FROM pandokos.event_destination WHERE id = $1::uuid AND lease_token IS NULL`, d.ID); n != 1 {
			t.Error("a rewind keeps the lease: a round in flight would settle")
		}
		// It never moves the cursor forward.
		if got := decode[adminapi.EventDestination](t, x.owner.Do(t, "POST", x.path("/"+d.ID+"/rewind"), map[string]any{"after": 5}, nil), 200); got.Delivery.Cursor != 3 {
			t.Errorf("a rewind to 5 from 3: cursor %d", got.Delivery.Cursor)
		}
		x.api.DB.AgeOutboxUpTo(t, xt, 3*time.Hour, 4)
		since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		if got := decode[adminapi.EventDestination](t, x.owner.Do(t, "POST", x.path("/"+d.ID+"/rewind"), map[string]any{"since": since}, nil), 200); got.Delivery.Cursor != 3 {
			t.Errorf("a rewind to a time: cursor %d, want the last event recorded before it (3)", got.Delivery.Cursor)
		}
		for what, body := range map[string]map[string]any{"both": {"after": 1, "since": since}, "neither": {}, "a negative number": {"after": -1}, "a time that is not one": {"since": "yesterday"}} {
			if r := x.owner.Do(t, "POST", x.path("/"+d.ID+"/rewind"), body, nil); r.Status != 400 || r.Problem() != "invalid_request" {
				t.Errorf("%s: %d %s, want 400 invalid_request", what, r.Status, r.Body)
			}
		}
	})

	t.Run("dead letters are listed and replayed", func(t *testing.T) {
		x := newEW(t)
		d := x.create(t, "letters", hookURL).Destination
		for _, seq := range []int{3, 5, 8} {
			if _, err := x.api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason, status) VALUES ($1::uuid, $2::uuid, $3, pandokos.uuid_v7(), 'io.pandokos.usage.recorded.v1', 10, 'status', 500)`, x.tid, d.ID, seq); err != nil {
				t.Fatal(err)
			}
		}
		list := decode[adminapi.EventDeadLetterList](t, x.owner.Do(t, "GET", x.path("/"+d.ID+"/dead-letters?limit=2"), nil, nil), 200)
		if len(list.Items) != 2 || list.Items[0].Seq != 3 || list.Items[1].Seq != 5 || list.NextCursor == "" || list.Items[0].State != adminapi.EventDeadLetterStateDead || list.Items[0].Reason != adminapi.DeliveryOutcomeStatus || list.Items[0].Status != 500 || list.Items[0].Attempts != 10 {
			t.Fatalf("the first page: %+v", list)
		}
		next := decode[adminapi.EventDeadLetterList](t, x.owner.Do(t, "GET", x.path("/"+d.ID+"/dead-letters?cursor="+list.NextCursor), nil, nil), 200)
		if len(next.Items) != 1 || next.Items[0].Seq != 8 || next.NextCursor != "" {
			t.Errorf("the second page: %+v", next)
		}
		if got := decode[adminapi.EventDeadLetterReplayed](t, x.owner.Do(t, "POST", x.path("/"+d.ID+"/dead-letters/replay"), map[string]any{"seqs": []int{5, 99}}, nil), 200); got.Replayed != 1 {
			t.Errorf("replay of chosen sequence numbers: %+v, want 1", got)
		}
		if st := decode[adminapi.EventDeadLetterList](t, x.owner.Do(t, "GET", x.path("/"+d.ID+"/dead-letters"), nil, nil), 200); st.Items[1].State != adminapi.EventDeadLetterStateReplay || st.Items[1].ReplayRequestedAt == nil {
			t.Errorf("the letter in replay: %+v", st.Items)
		}
		if got := decode[adminapi.EventDeadLetterReplayed](t, x.owner.Do(t, "POST", x.path("/"+d.ID+"/dead-letters/replay"), map[string]any{}, nil), 200); got.Replayed != 2 {
			t.Errorf("replay of all that are dead: %+v, want the two that were not in replay", got)
		}
		if got := decode[adminapi.EventDeadLetterReplayed](t, x.owner.Do(t, "POST", x.path("/"+d.ID+"/dead-letters/replay"), map[string]any{}, nil), 200); got.Replayed != 0 {
			t.Errorf("a replay with nothing dead: %+v", got)
		}
		for what, body := range map[string]map[string]any{"a sequence number of 0": {"seqs": []int{0}}, "a repeated number": {"seqs": []int{4, 4}}, "an unknown member": {"all": true}} {
			if r := x.owner.Do(t, "POST", x.path("/"+d.ID+"/dead-letters/replay"), body, nil); r.Status != 400 {
				t.Errorf("%s: %d", what, r.Status)
			}
		}
		if r := x.owner.Do(t, "GET", x.path("/"+apitest.NoUser+"/dead-letters"), nil, nil); r.Status != 404 {
			t.Errorf("the letters of a destination that does not exist: %d", r.Status)
		}
	})
}

// TestEventSecretCanary is T-1045: after creation and rotation the signing secret is in no
// response, audit row, log line or error; the stored column is not the secret and the admin pool
// cannot select it.
func TestEventSecretCanary(t *testing.T) {
	w := newEW(t)
	created := w.create(t, "canary", hookURL)
	id := created.Destination.ID
	rot := decode[adminapi.EventDestinationCreated](t, w.owner.Do(t, "POST", w.path("/"+id+"/rotate-secret"), map[string]any{}, idem("canary-rot")), 200)
	replay := w.owner.Do(t, "POST", w.path("/"+id+"/rotate-secret"), map[string]any{}, idem("canary-rot"))
	secrets := []string{created.Secret, rot.Secret}
	raw := []string{strings.TrimPrefix(created.Secret, "whsec_"), strings.TrimPrefix(rot.Secret, "whsec_")}
	if secrets[0] == "" || secrets[1] == "" || secrets[0] == secrets[1] {
		t.Fatalf("the two secrets: %q %q", secrets[0], secrets[1])
	}
	// Every other response of the API, error responses included.
	responses := []*apitest.Response{
		w.owner.Do(t, "GET", w.path(""), nil, nil), w.owner.Do(t, "GET", w.path("/"+id), nil, nil), replay,
		w.owner.Do(t, "PATCH", w.path("/"+id), map[string]any{"url": "https://hooks.example.test/changed", "secret": created.Secret}, nil),
		w.owner.Do(t, "POST", w.path(""), map[string]any{"name": "canary", "url": hookURL, "secret": rot.Secret}, nil),
		w.owner.Do(t, "POST", w.path("/"+id+"/test"), nil, nil), w.owner.Do(t, "POST", w.path("/"+id+"/rewind"), map[string]any{"after": 0}, nil),
		w.owner.Do(t, "GET", w.path("/"+id+"/dead-letters"), nil, nil),
	}
	for i, r := range responses {
		if found := contains(string(r.Body), append(secrets, raw...)...); len(found) > 0 {
			t.Errorf("response %d (%d) shows a secret: %s", i, r.Status, r.Body)
		}
	}
	if !strings.Contains(string(responses[3].Body), "invalid_request") && responses[3].Status != 400 {
		t.Errorf("a secret in a body is not an unknown member: %d", responses[3].Status)
	}
	if found := contains(w.api.DB.AllAuditText(t), append(secrets, raw...)...); len(found) > 0 {
		t.Errorf("an audit row holds the secret")
	}
	if found := contains(w.api.Logs.String(), append(secrets, raw...)...); len(found) > 0 {
		t.Errorf("the log of the API holds the secret: %s", w.api.Logs.String())
	}
	if found := contains(w.api.Snapshot(t), append(secrets, raw...)...); len(found) > 0 {
		t.Errorf("a table holds the secret in the clear (the idempotency store keeps the response without it)")
	}
	// The stored column is the sealed secret, and the admin pool cannot read it.
	var stored []byte
	if err := w.api.Super(t).QueryRow(ctx(t), `SELECT secret FROM pandokos.event_destination WHERE id = $1::uuid`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), raw[1]) || strings.Contains(string(stored), secrets[1]) || len(stored) < 30 {
		t.Errorf("the stored column is %d bytes and holds the secret", len(stored))
	}
	for _, col := range []string{"secret", "previous_secret"} {
		var may bool
		if err := w.api.Super(t).QueryRow(ctx(t), `SELECT pg_catalog.has_column_privilege('pandokos_admin', 'pandokos.event_destination', $1, 'SELECT')`, col).Scan(&may); err != nil || may {
			t.Errorf("the admin role may select %s: %v (%v)", col, may, err)
		}
	}
}
