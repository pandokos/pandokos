package admin_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/test/apitest"
)

// auditPages reads every page of a list of audit events, newest first, following the cursor.
func auditPages(t *testing.T, w *pw, key, path, query string) []adminapi.AuditEvent {
	t.Helper()
	var all []adminapi.AuditEvent
	cursor := ""
	for i := 0; i < 100; i++ {
		q := "limit=7"
		if query != "" {
			q += "&" + query
		}
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		page := decodeInto[adminapi.AuditEventList](t, w.do(t, key, "GET", path+"?"+q, nil), 200)
		all = append(all, page.Items...)
		if page.NextCursor == "" {
			return all
		}
		cursor = page.NextCursor
	}
	t.Fatal("the pages do not end")
	return nil
}

func auditPath(tn adminapi.Tenant) string { return "/v1/tenants/" + tn.ID + "/audit-events" }

// auditWorld writes events of many kinds in tenant A: member, service account, role and price
// changes by the owner, rows under a support action, and authentication failures of both listeners.
func auditWorld(t *testing.T) (w *pw, supportAction string) {
	t.Helper()
	w = newPW(t)
	owner := w.api.Owner(t, w.a.ID)
	base := "/v1/tenants/" + w.a.ID
	for i := 0; i < 3; i++ {
		decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", base+"/service-accounts", map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"inference"}}), 201)
	}
	decodeInto[adminapi.Role](t, w.do(t, w.ka, "POST", base+"/roles", map[string]any{"name": apitest.UniqueName("role"), "permissions": []string{"member:read"}}), 201)
	decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", base+"/virtual-keys", map[string]any{"service_account_id": w.saA.ID}), 201)
	first := w.createPrice(t, priceBody(w.cat.All, w.cat.Provider.Name, "USD", tpRates("input", "0", "2.50", "output", "0", "10"), nil))
	_ = first
	second := w.createPrice(t, priceBody(w.cat.All, w.cat.Provider.Name, "USD", nil, map[string]any{"valid_from": scheduled(4, 0)}))
	decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "DELETE", w.pricesPath(w.a)+"/"+second.ID, nil), 200)
	w.createPrice(t, priceBody(w.cat.All, w.cat.Provider.Name, "EUR", nil, map[string]any{"valid_from": scheduled(3, 0)}))
	decodeInto[adminapi.ActiveCurrency](t, w.do(t, w.ka, "PUT", base+"/active-currency", map[string]any{"currency": "EUR"}), 200)

	// Under a support action of the platform: the tenant's rows carry its ID, and the platform's log
	// has the open, the interventions and the close.
	plat := w.api.PlatformCaller()
	supportAction = apitest.OpenSupport(t, plat, w.a.ID, authz.ServiceAccountWrite, authz.ServiceAccountRead)
	for i := 0; i < 2; i++ {
		if r := plat.Do(t, "POST", base+"/service-accounts", map[string]any{"name": apitest.UniqueName("by-support"), "roles": []string{"inference"}}, nil); r.Status != 201 {
			t.Fatalf("an intervention: %d %s", r.Status, r.Body)
		}
	}
	// A failure of authentication on the administration listener (a wrong secret for a key of the
	// tenant: a key nobody knows is written to the platform's log), and two on the inference listener.
	badKey, badID, _, _ := authn.Generate(authn.KindTenant)
	_, _, otherHash, _ := authn.Generate(authn.KindTenant)
	if _, err := w.api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`,
		w.a.ID, w.saA.ID, badID, otherHash[:]); err != nil {
		t.Fatal(err)
	}
	if r := w.api.Do(t, "GET", base+"/audit-events", badKey.Reveal(), nil, nil); r.Status != 401 {
		t.Fatalf("a wrong secret on the administration listener: %d", r.Status)
	}
	gw := w.api.DB.Open(t, store.RoleGateway)
	for _, reason := range []string{"revoked", "unknown_key"} {
		if err := gw.WithTenant(ctx(t), w.idA, func(c context.Context, tx store.Tx) error {
			return queries.New(tx).RecordAuthFailure(c, queries.RecordAuthFailureParams{KeyID: "tabcdefghijklmnop", Reason: reason, RequestID: strings.Repeat("c", 32)})
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = owner
	return w, supportAction
}

// TestListAuditEvents is T-940: newest first by seq; each filter alone and combined; a cursor bound
// to its filters; the rows written under a support action; the failures of both listeners with
// their listener; every member of the schema present.
func TestListAuditEvents(t *testing.T) {
	w, support := auditWorld(t)
	path := auditPath(w.a)
	all := auditPages(t, w, w.ka, path, "")
	if len(all) < 20 {
		t.Fatalf("the world wrote %d events: the test proves nothing", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq != all[i-1].Seq-1 {
			t.Fatalf("the pages are not newest first without gap: seq %d after %d", all[i].Seq, all[i-1].Seq)
		}
	}
	if all[len(all)-1].Seq != 1 {
		t.Errorf("the oldest event has seq %d, want 1 (no retention ran)", all[len(all)-1].Seq)
	}

	// Every member of the schema is present in the JSON of an item.
	raw := w.do(t, w.ka, "GET", path+"?limit=1", nil)
	var generic struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw.Body, &generic); err != nil || len(generic.Items) != 1 {
		t.Fatalf("%v %s", err, raw.Body)
	}
	for _, k := range []string{"seq", "id", "occurred_at", "actor_type", "actor_id", "actor_name", "key_id", "action", "resource_type", "resource_id", "outcome",
		"request_id", "reason", "support_action_id", "detail", "detail_withheld", "detail_digest", "prev_hash", "hash"} {
		if _, ok := generic.Items[0][k]; !ok {
			t.Errorf("an event has no member %s: %s", k, raw.Body)
		}
	}
	for _, e := range all {
		if len(e.Hash) != 64 || len(e.PrevHash) != 64 || e.Hash != strings.ToLower(e.Hash) {
			t.Errorf("seq %d: hash %q prev_hash %q: lower-case hex of 32 bytes", e.Seq, e.Hash, e.PrevHash)
		}
	}

	filter := func(keep func(adminapi.AuditEvent) bool) []int {
		var seqs []int
		for _, e := range all {
			if keep(e) {
				seqs = append(seqs, e.Seq)
			}
		}
		return seqs
	}
	seqsOf := func(es []adminapi.AuditEvent) []int {
		var out []int
		for _, e := range es {
			out = append(out, e.Seq)
		}
		return out
	}
	check := func(name, query string, want []int) {
		t.Helper()
		got := seqsOf(auditPages(t, w, w.ka, path, query))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s (%s): seq %v, want %v", name, query, got, want)
		}
		if len(want) == 0 {
			t.Errorf("%s: the filter matches nothing in the world: the case proves nothing", name)
		}
	}
	pivot := all[len(all)/2]
	check("action, exact", "action=service_account.create", filter(func(e adminapi.AuditEvent) bool { return e.Action == "service_account.create" }))
	check("action, prefix", "action="+url.QueryEscape("service_account."), filter(func(e adminapi.AuditEvent) bool { return strings.HasPrefix(e.Action, "service_account.") }))
	check("resource_type", "resource_type=role", filter(func(e adminapi.AuditEvent) bool { return e.ResourceType == "role" }))
	check("resource_id", "resource_id="+url.QueryEscape(pivot.ResourceID), filter(func(e adminapi.AuditEvent) bool { return e.ResourceID == pivot.ResourceID }))
	check("actor_type", "actor_type=platform", filter(func(e adminapi.AuditEvent) bool { return e.ActorType == "platform" }))
	check("actor_id", "actor_id="+url.QueryEscape(pivot.ActorID), filter(func(e adminapi.AuditEvent) bool { return e.ActorID == pivot.ActorID }))
	check("outcome", "outcome=failure", filter(func(e adminapi.AuditEvent) bool { return e.Outcome == "failure" }))
	check("request_id", "request_id="+pivot.RequestID, filter(func(e adminapi.AuditEvent) bool { return e.RequestID == pivot.RequestID }))
	// The window is on occurred_at: from is inclusive, to is exclusive.
	from, to := pivot.OccurredAt, all[0].OccurredAt
	check("a window", "from="+url.QueryEscape(from.Format(time.RFC3339Nano))+"&to="+url.QueryEscape(to.Format(time.RFC3339Nano)),
		filter(func(e adminapi.AuditEvent) bool { return !e.OccurredAt.Before(from) && e.OccurredAt.Before(to) }))
	check("combined", "action="+url.QueryEscape("service_account.")+"&actor_type=service_account&outcome=success",
		filter(func(e adminapi.AuditEvent) bool {
			return strings.HasPrefix(e.Action, "service_account.") && e.ActorType == "service_account" && e.Outcome == "success"
		}))

	t.Run("a cursor is bound to its filters", func(t *testing.T) {
		page := decodeInto[adminapi.AuditEventList](t, w.do(t, w.ka, "GET", path+"?limit=2&action="+url.QueryEscape("service_account."), nil), 200)
		if page.NextCursor == "" {
			t.Fatal("no next page: the test proves nothing")
		}
		for name, q := range map[string]string{
			"another action": "action=role.create",
			"no filter":      "",
			"another filter": "outcome=failure&action=" + url.QueryEscape("service_account."),
		} {
			r := w.do(t, w.ka, "GET", path+"?limit=2&cursor="+url.QueryEscape(page.NextCursor)+"&"+q, nil)
			p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
			if len(p.Errors) == 0 || p.Errors[0].Field != "cursor" {
				t.Errorf("%s: the error is %+v, want the field cursor", name, p.Errors)
			}
		}
		next := decodeInto[adminapi.AuditEventList](t, w.do(t, w.ka, "GET", path+"?limit=2&action="+url.QueryEscape("service_account.")+"&cursor="+url.QueryEscape(page.NextCursor), nil), 200)
		if len(next.Items) == 0 || next.Items[0].Seq >= page.Items[len(page.Items)-1].Seq {
			t.Errorf("the next page %v does not follow the first %v", seqsOf(next.Items), seqsOf(page.Items))
		}
	})

	t.Run("the rows written under a support action", func(t *testing.T) {
		got := auditPages(t, w, w.ka, path, "support_action_id="+support)
		want := filter(func(e adminapi.AuditEvent) bool { return e.SupportActionID == support })
		if !reflect.DeepEqual(seqsOf(got), want) || len(want) != 3 {
			t.Errorf("support_action_id: seq %v, want the opening and the two interventions %v", seqsOf(got), want)
		}
		for _, e := range got {
			// The opening of the action is written under it too (support.go).
			if (e.Action != "service_account.create" && e.Action != "support_action.open") || e.ActorType != "platform" {
				t.Errorf("an intervention row: %+v", e)
			}
		}
		for _, e := range all {
			if e.SupportActionID != "" && e.SupportActionID != support {
				t.Errorf("a row carries the support action %s", e.SupportActionID)
			}
		}
	})

	t.Run("the failures of both listeners", func(t *testing.T) {
		failures := auditPages(t, w, w.ka, path, "action=auth.failure")
		listeners := map[string]int{}
		for _, e := range failures {
			var d struct {
				Listener string `json:"listener"`
			}
			if err := json.Unmarshal(e.Detail, &d); err != nil {
				t.Fatal(err)
			}
			listeners[d.Listener]++
			if e.Outcome != "failure" || e.ActorType != "anonymous" {
				t.Errorf("a failure row: %+v", e)
			}
		}
		if listeners["inference"] != 2 || listeners["admin"] != 1 {
			t.Errorf("the failures by listener: %v, want 2 inference and 1 admin", listeners)
		}
	})

	t.Run("another tenant's events are not listed", func(t *testing.T) {
		for _, e := range auditPages(t, w, w.kb, auditPath(w.b), "") {
			if strings.Contains(string(e.Detail), w.a.ID) || e.ResourceID == w.a.ID {
				t.Errorf("tenant B reads an event of tenant A: %+v", e)
			}
		}
		if r := w.do(t, w.kb, "GET", auditPath(w.a), nil); r.Status != 404 {
			t.Errorf("B's key on A's log: %d, want 404", r.Status)
		}
	})
}

// TestAuditDetailWithheld is T-941: the detail of the events that hold a price, a rate or the active
// currency is withheld from a reader without price_list:read, with its digest, so that the chain
// still verifies.
func TestAuditDetailWithheld(t *testing.T) {
	w, _ := auditWorld(t)
	path := auditPath(w.a)
	withheldActions := []string{"tenant_price.create", "tenant_price.delete", "tenant.active_currency_update"}

	// The list of the code is the three actions of the design (T-R75 checks it against the calls).
	got := make([]string, 0, len(admin.PriceDetailActions))
	for a := range admin.PriceDetailActions {
		got = append(got, a)
	}
	sort.Strings(got)
	want := append([]string(nil), withheldActions...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("admin.PriceDetailActions is %v, want %v", got, want)
	}

	auditor := w.api.AsRole(t, w.a.ID, "auditor")
	withPrices := w.api.AsRole(t, w.a.ID, w.api.CustomRole(t, w.a.ID, authz.AuditRead, authz.PriceListRead))
	owner := w.api.Owner(t, w.a.ID)

	ownerEvents := auditPages(t, w, owner.Key, path, "")
	byseq := map[int]adminapi.AuditEvent{}
	for _, e := range ownerEvents {
		byseq[e.Seq] = e
	}
	n := 0
	for _, who := range []struct {
		name string
		key  string
		see  bool
	}{{"auditor", auditor.Key, false}, {"a role with audit:read and price_list:read", withPrices.Key, true}} {
		for _, e := range auditPages(t, w, who.key, path, "") {
			o := byseq[e.Seq]
			if o.Seq == 0 {
				continue // written by the call itself (auth rows of a read)
			}
			if !admin.PriceDetailActions[e.Action] {
				if e.DetailWithheld || e.DetailDigest != "" || string(e.Detail) != string(o.Detail) {
					t.Errorf("%s: the detail of %s is withheld or changed: %+v", who.name, e.Action, e)
				}
				continue
			}
			n++
			if who.see {
				if e.DetailWithheld || e.DetailDigest != "" || string(e.Detail) != string(o.Detail) {
					t.Errorf("%s: %s is withheld from a reader with price_list:read: %+v", who.name, e.Action, e)
				}
				continue
			}
			digest, err := auditchain.DetailDigest(o.Detail)
			if err != nil {
				t.Fatal(err)
			}
			if !e.DetailWithheld || strings.TrimSpace(string(e.Detail)) != "{}" || e.DetailDigest != hex.EncodeToString(digest[:]) {
				t.Errorf("%s: %s: detail %s withheld %v digest %q, want {} true %x", who.name, e.Action, e.Detail, e.DetailWithheld, e.DetailDigest, digest)
			}
			if e.Hash != o.Hash || e.PrevHash != o.PrevHash {
				t.Errorf("%s: seq %d: the hashes differ from the owner's: the chain is the same for everybody", who.name, e.Seq)
			}
		}
	}
	if n < 6 {
		t.Errorf("only %d price events were compared, want create, delete and currency for both readers", n)
	}
	for _, e := range ownerEvents {
		if e.DetailWithheld || e.DetailDigest != "" {
			t.Errorf("the owner has withheld details: %+v", e)
		}
	}

	t.Run("the export withholds the same", func(t *testing.T) {
		export := func(key string) []adminapi.AuditEvent {
			r := w.do(t, key, "GET", path+"/export", nil)
			if r.Status != 200 {
				t.Fatalf("%d %s", r.Status, r.Body)
			}
			var out []adminapi.AuditEvent
			sc := bufio.NewScanner(bytes.NewReader(r.Body))
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var e adminapi.AuditEvent
				if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
					t.Fatalf("an export line: %v %q", err, sc.Text())
				}
				out = append(out, e)
			}
			return out
		}
		for _, e := range export(auditor.Key) {
			if admin.PriceDetailActions[e.Action] && (!e.DetailWithheld || e.DetailDigest == "" || strings.TrimSpace(string(e.Detail)) != "{}") {
				t.Errorf("the export holds the detail of %s for an auditor: %+v", e.Action, e)
			}
		}
		for _, e := range export(owner.Key) {
			if admin.PriceDetailActions[e.Action] && e.DetailWithheld {
				t.Errorf("the export withholds %s from the owner", e.Action)
			}
		}
	})
}

// TestAuditHead is T-946: the head equals the last row; after retention the anchor is the first row
// kept and the hash of the last row removed.
func TestAuditHead(t *testing.T) {
	w, _ := auditWorld(t)
	path := "/v1/tenants/" + w.a.ID + "/audit-head"
	head := decodeInto[adminapi.AuditHead](t, w.do(t, w.ka, "GET", path, nil), 200)
	all := auditPages(t, w, w.ka, auditPath(w.a), "")
	// The read itself writes no row: the head is the last row of the list.
	if head.Seq != all[0].Seq || head.Hash != all[0].Hash || head.AnchorSeq != 1 || head.AnchorHash != strings.Repeat("0", 64) {
		t.Errorf("head %+v, last row %d %s", head, all[0].Seq, all[0].Hash)
	}
	if r := w.do(t, w.kb, "GET", "/v1/tenants/"+w.a.ID+"/audit-head", nil); r.Status != 404 {
		t.Errorf("B's key on A's head: %d, want 404", r.Status)
	}
	if r := w.do(t, w.plat, "GET", path, nil); r.Status != 403 {
		t.Errorf("a platform key on a tenant head: %d, want 403", r.Status)
	}

	// Retention: rows of old months are sealed, the worker removes them.
	now := time.Now().UTC()
	old := func(back int) time.Time {
		return time.Date(now.Year(), now.Month()-time.Month(back), 10, 12, 0, 0, 0, time.UTC)
	}
	for _, back := range []int{4, 3} {
		w.api.DB.SealedRow(t, w.idA, old(back), "old.row")
	}
	// Sealed rows go to the end of the chain: the ones before them (the world's) are younger than the cut
	// only when they were written after it, so the prefix that goes is everything up to the last old row.
	before := auditPages(t, w, w.ka, auditPath(w.a), "")
	lastOld := 0
	for _, e := range before {
		if e.Action == "old.row" && e.Seq > lastOld {
			lastOld = e.Seq
		}
	}
	var lastOldHash string
	for _, e := range before {
		if e.Seq == lastOld {
			lastOldHash = e.Hash
		}
	}
	if err := w.api.DB.Open(t, store.RoleWorker).WithTenant(ctx(t), w.idA, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).TenantRetention(c, queries.TenantRetentionParams{UsageMonths: 0, AuditMonths: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after := decodeInto[adminapi.AuditHead](t, w.do(t, w.ka, "GET", path, nil), 200)
	rows := auditPages(t, w, w.ka, auditPath(w.a), "")
	oldest := rows[len(rows)-1]
	if after.AnchorSeq != lastOld+1 || after.AnchorHash != lastOldHash || oldest.Seq != after.AnchorSeq {
		t.Errorf("after retention: head %+v; the oldest row kept is %d; the last row removed was %d %s", after, oldest.Seq, lastOld, lastOldHash)
	}
	last := rows[0]
	if last.Action != "audit.retention" || after.Seq != last.Seq || after.Hash != last.Hash {
		t.Errorf("the head %+v is not the audit.retention row %+v", after, last)
	}
}
