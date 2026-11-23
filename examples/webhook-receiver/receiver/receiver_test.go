package receiver_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/examples/webhook-receiver/receiver"
)

// store keeps the events by ID and counts what it was given twice.
type store struct {
	mu   sync.Mutex
	got  map[string]receiver.Event
	dups int
	err  error
}

func newStore() *store { return &store{got: map[string]receiver.Event{}} }

func (s *store) Put(id string, e receiver.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if _, seen := s.got[id]; seen {
		s.dups++
		return false, nil
	}
	s.got[id] = e
	return true, nil
}

var now = time.Date(2026, 11, 23, 17, 20, 4, 0, time.UTC)

func secretOf(fill byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

// sign is the signature of Standard Webhooks, written here from the specification.
func sign(secret, id string, ts int64, body []byte) string {
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	m := hmac.New(sha256.New, raw)
	m.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "."))
	m.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

const doc = `{"specversion":"1.0","id":"01a2096c-074d-750b-9e85-c37222e3b9c9","source":"urn:pandokos","type":"io.pandokos.usage.recorded.v1","time":"2026-11-23T17:20:04.426849Z","subject":"eccbc87e4b5ce2fe28308fd9f2a7baf3","datacontenttype":"application/json","tenantid":"01a2096c-0745-7db9-b65e-3f2f525e04af","tenantref":"cust-42","sequence":"1","data":{"model":"gpt-4o-mini"}}`

const eventID = "01a2096c-074d-750b-9e85-c37222e3b9c9"

type req struct {
	id, ts, sig string
	body        string
	method      string
}

func good(secrets ...string) req {
	r := req{id: eventID, ts: strconv.FormatInt(now.Unix(), 10), body: doc, method: "POST"}
	var sigs []string
	for _, s := range secrets {
		sigs = append(sigs, sign(s, r.id, now.Unix(), []byte(doc)))
	}
	r.sig = strings.Join(sigs, " ")
	return r
}

func do(t *testing.T, h *receiver.Handler, r req) *httptest.ResponseRecorder {
	t.Helper()
	hr := httptest.NewRequest(r.method, "/hook", strings.NewReader(r.body))
	if r.id != "" {
		hr.Header.Set("webhook-id", r.id)
	}
	if r.ts != "" {
		hr.Header.Set("webhook-timestamp", r.ts)
	}
	if r.sig != "" {
		hr.Header.Set("webhook-signature", r.sig)
	}
	hr.Header.Set("Content-Type", "application/cloudevents+json; charset=utf-8")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, hr)
	return w
}

// TestHandler is T-1049: a valid request is stored once; the same ID again answers 204 and stores
// nothing; a bad signature, a missing header and a timestamp six minutes old are 401; either of two
// secrets verifies.
func TestHandler(t *testing.T) {
	s1, s2 := secretOf(1), secretOf(2)
	newHandler := func(secrets ...string) (*receiver.Handler, *store) {
		st := newStore()
		return &receiver.Handler{Secrets: secrets, Store: st, Now: func() time.Time { return now }}, st
	}

	t.Run("a valid request is stored once, a repeat is acknowledged and stores nothing", func(t *testing.T) {
		h, st := newHandler(s1)
		if w := do(t, h, good(s1)); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("a valid delivery: %d %q", w.Code, w.Body)
		}
		e, ok := st.got[eventID]
		if !ok || len(st.got) != 1 || e.ID != eventID || e.Type != "io.pandokos.usage.recorded.v1" || e.Subject != "eccbc87e4b5ce2fe28308fd9f2a7baf3" || e.TenantRef != "cust-42" ||
			e.TenantID != "01a2096c-0745-7db9-b65e-3f2f525e04af" || e.Sequence != "1" || !strings.Contains(string(e.Data), "gpt-4o-mini") {
			t.Errorf("the stored event: %+v", e)
		}
		if w := do(t, h, good(s1)); w.Code != http.StatusNoContent {
			t.Errorf("the same ID again: %d", w.Code)
		}
		if len(st.got) != 1 || st.dups != 1 {
			t.Errorf("the store holds %d events and saw %d duplicates, want 1 and 1", len(st.got), st.dups)
		}
	})

	t.Run("a bad signature, a missing header and a timestamp out of tolerance are 401", func(t *testing.T) {
		h, st := newHandler(s1)
		cases := map[string]func(r *req){
			"a signature of another secret":       func(r *req) { r.sig = sign(s2, r.id, now.Unix(), []byte(r.body)) },
			"a signature of another body":         func(r *req) { r.sig = sign(s1, r.id, now.Unix(), []byte(r.body+" ")) },
			"a signature of another ID":           func(r *req) { r.sig = sign(s1, "another", now.Unix(), []byte(r.body)) },
			"a signature of another timestamp":    func(r *req) { r.sig = sign(s1, r.id, now.Unix()-1, []byte(r.body)) },
			"a signature without v1":              func(r *req) { r.sig = strings.TrimPrefix(r.sig, "v1,") },
			"a signature that is not base64":      func(r *req) { r.sig = "v1,!!!!" },
			"an empty signature":                  func(r *req) { r.sig = "" },
			"no id":                               func(r *req) { r.id = "" },
			"no timestamp":                        func(r *req) { r.ts = "" },
			"a timestamp that is not a number":    func(r *req) { r.ts = "yesterday" },
			"a timestamp six minutes old":         func(r *req) { setTime(r, s1, now.Add(-6*time.Minute)) },
			"a timestamp six minutes ahead":       func(r *req) { setTime(r, s1, now.Add(6*time.Minute)) },
			"a timestamp of the epoch":            func(r *req) { setTime(r, s1, time.Unix(0, 0)) },
			"a signature for a body that changed": func(r *req) { r.body = strings.Replace(r.body, "cust-42", "cust-43", 1) },
		}
		for what, mutate := range cases {
			r := good(s1)
			mutate(&r)
			if w := do(t, h, r); w.Code != http.StatusUnauthorized {
				t.Errorf("%s: %d, want 401", what, w.Code)
			}
		}
		if len(st.got) != 0 {
			t.Errorf("%d events stored from requests that were refused", len(st.got))
		}
		// Within the tolerance: four minutes and fifty-nine seconds either way.
		for _, d := range []time.Duration{-299 * time.Second, 299 * time.Second} {
			r := good(s1)
			setTime(&r, s1, now.Add(d))
			if w := do(t, h, r); w.Code != http.StatusNoContent {
				t.Errorf("a timestamp %v away: %d, want 204", d, w.Code)
			}
		}
	})

	t.Run("either of two secrets verifies, and a header with two signatures too", func(t *testing.T) {
		h, st := newHandler(s1, s2)
		for i, r := range []req{good(s1), func() req {
			r := good(s2)
			r.id = "second"
			r.sig = sign(s2, "second", now.Unix(), []byte(r.body))
			return r
		}()} {
			if w := do(t, h, r); w.Code != http.StatusNoContent {
				t.Errorf("request %d: %d", i, w.Code)
			}
		}
		if len(st.got) != 2 {
			t.Errorf("%d events stored for two secrets", len(st.got))
		}
		// A delivery signed with both (a rotation) is accepted by a receiver that knows either.
		both := good(s1, s2)
		both.id = "both"
		both.sig = sign(s1, "both", now.Unix(), []byte(both.body)) + " " + sign(s2, "both", now.Unix(), []byte(both.body))
		for _, secrets := range [][]string{{s1}, {s2}, {s1, s2}} {
			hh, _ := newHandler(secrets...)
			if w := do(t, hh, both); w.Code != http.StatusNoContent {
				t.Errorf("a receiver with %d secret(s) and a delivery with two signatures: %d", len(secrets), w.Code)
			}
		}
		// A receiver that knows neither refuses.
		hh, _ := newHandler(secretOf(3))
		if w := do(t, hh, both); w.Code != http.StatusUnauthorized {
			t.Errorf("a receiver that knows neither secret: %d", w.Code)
		}
		// A receiver with no secret refuses everything: it never accepts what it cannot check.
		none, _ := newHandler()
		if w := do(t, none, good(s1)); w.Code != http.StatusUnauthorized {
			t.Errorf("a receiver with no secret: %d", w.Code)
		}
	})

	t.Run("a valid signature on a document that is not an event is 400 and stores nothing", func(t *testing.T) {
		h, st := newHandler(s1)
		for what, body := range map[string]string{"not JSON": "not json", "no ID": `{"specversion":"1.0","type":"x"}`, "an ID that differs from the header": strings.Replace(doc, eventID, "another-id", 1), "an empty body": ""} {
			r := good(s1)
			r.body = body
			r.sig = sign(s1, r.id, now.Unix(), []byte(body))
			if w := do(t, h, r); w.Code != http.StatusBadRequest {
				t.Errorf("%s: %d, want 400", what, w.Code)
			}
		}
		if len(st.got) != 0 {
			t.Errorf("%d events stored", len(st.got))
		}
	})

	t.Run("a store that fails is 500 and the sender retries", func(t *testing.T) {
		h, st := newHandler(s1)
		st.err = errors.New("disk full")
		if w := do(t, h, good(s1)); w.Code != http.StatusInternalServerError {
			t.Errorf("a failing store: %d", w.Code)
		}
	})

	t.Run("only POST, and no secret in an answer", func(t *testing.T) {
		h, _ := newHandler(s1)
		r := good(s1)
		r.method = "GET"
		if w := do(t, h, r); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET: %d", w.Code)
		}
		bad := good(s2)
		w := do(t, h, bad)
		if strings.Contains(w.Body.String(), s1) || strings.Contains(w.Body.String(), strings.TrimPrefix(s1, "whsec_")) {
			t.Errorf("the answer quotes the secret: %s", w.Body)
		}
	})
}

func setTime(r *req, secret string, at time.Time) {
	r.ts = strconv.FormatInt(at.Unix(), 10)
	r.sig = sign(secret, r.id, at.Unix(), []byte(r.body))
}
