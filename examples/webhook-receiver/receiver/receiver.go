// Package receiver is the example of a webhook receiver of Pandokos events (sprint 6.4, section
// 5.16): it checks the signature of a delivery against one or two secrets and the timestamp
// against a tolerance, reads the CloudEvents document and hands it to a Store that tells whether
// the ID was new. It uses the standard library only and does not import internal/outbox: its
// signature check is written independently, so that the two agreeing is the interoperability
// test.
package receiver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Event is the part of a CloudEvents document that the receiver reads.
type Event struct {
	ID        string          // the idempotency key
	Type      string          // io.pandokos.usage.recorded.v1 ...
	Subject   string          // the request ID of a usage event
	TenantID  string          // the tenant's ID
	TenantRef string          // the tenant's external reference, absent when empty
	Sequence  string          // the sequence in the tenant's log, absent on a test event
	Data      json.RawMessage // the data of the type, as sent
}

// Store keeps the events a receiver has seen. Put reports whether the ID was new: a delivery that
// repeats an ID is acknowledged and stores nothing.
type Store interface {
	Put(id string, e Event) (isNew bool, err error)
}

// Handler is the http.Handler of the receiver. Secrets are signing secrets in their text form
// ("whsec_" and the base64 of 32 bytes): during a rotation a delivery is accepted if it matches
// either. A delivery whose timestamp is more than Tolerance (five minutes when zero) from Now (the
// clock when nil) is refused.
type Handler struct {
	Secrets   []string
	Store     Store
	Now       func() time.Time
	Tolerance time.Duration
}

const (
	defaultTolerance = 5 * time.Minute
	maxBody          = 1 << 20
)

// ServeHTTP answers 204 to a valid delivery, new or repeated, and 401 to one whose signature or
// timestamp is not valid. A document that is signed but is not an event is 400; a store that fails
// is 500, so that the sender retries. No answer quotes a secret.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "cannot read the body", http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	id := r.Header.Get("webhook-id")
	if !h.verify(id, r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	// The signature is valid: the sender is the one the secret was given to. The document is read
	// only now.
	e, ok := parseEvent(body)
	if !ok || e.ID != id {
		http.Error(w, "not an event", http.StatusBadRequest)
		return
	}
	// The ID is the key: a delivery is at least once, and the same event comes again with the same
	// ID, whatever the attempt.
	if _, err := h.Store.Put(e.ID, e); err != nil {
		http.Error(w, "cannot store the event", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verify checks the timestamp against the tolerance and the signature header against every secret:
// HMAC-SHA256 of "id.timestamp.body", base64, in the entries "v1,<signature>" of the header.
func (h *Handler) verify(id, ts, header string, body []byte) bool {
	if id == "" || ts == "" || header == "" {
		return false
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	tolerance := h.Tolerance
	if tolerance == 0 {
		tolerance = defaultTolerance
	}
	if d := now().Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return false
	}
	var given [][]byte
	for _, entry := range strings.Fields(header) {
		version, sig, ok := strings.Cut(entry, ",")
		if !ok || version != "v1" {
			continue
		}
		if raw, err := base64.StdEncoding.DecodeString(sig); err == nil {
			given = append(given, raw)
		}
	}
	if len(given) == 0 {
		return false
	}
	matched := false
	for _, secret := range h.Secrets {
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err != nil || len(key) == 0 {
			continue
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(body)
		want := mac.Sum(nil)
		for _, g := range given {
			if hmac.Equal(g, want) {
				matched = true
			}
		}
	}
	return matched
}

// parseEvent reads the members the receiver uses from a CloudEvents document.
func parseEvent(body []byte) (Event, bool) {
	var doc struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Subject   string          `json:"subject"`
		TenantID  string          `json:"tenantid"`
		TenantRef string          `json:"tenantref"`
		Sequence  json.Number     `json:"sequence"`
		Data      json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.ID == "" {
		return Event{}, false
	}
	return Event{ID: doc.ID, Type: doc.Type, Subject: doc.Subject, TenantID: doc.TenantID, TenantRef: doc.TenantRef,
		Sequence: doc.Sequence.String(), Data: doc.Data}, true
}
