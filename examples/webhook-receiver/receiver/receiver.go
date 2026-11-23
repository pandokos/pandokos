// Package receiver is the example of a webhook receiver of Pandokos events (sprint 6.4, section
// 5.16): it checks the signature of a delivery against one or two secrets and the timestamp
// against a tolerance, reads the CloudEvents document and hands it to a Store that tells whether
// the ID was new. It uses the standard library only and does not import internal/outbox: its
// signature check is written independently, so that the two agreeing is the interoperability
// test.
//
// This file is the stub of the tests of part B of the plan; plan step C9 writes the behaviour.
package receiver

import (
	"encoding/json"
	"net/http"
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

// ServeHTTP answers 204 to a valid delivery, new or repeated, and 401 to one whose signature or
// timestamp is not valid.
func (h *Handler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "receiver: not implemented yet", http.StatusInternalServerError)
}
