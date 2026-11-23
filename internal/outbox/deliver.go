package outbox

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// errNotBuilt is what the stubs of part A of the plan answer until parts C2 to C5 replace them.
var errNotBuilt = errors.New("outbox: not implemented yet")

// Outcome is how one delivery request ended; the values are the label of the delivery metric and
// the reason of a dead letter.
type Outcome string

// The outcomes of a delivery request.
const (
	OutcomeDelivered  Outcome = "delivered"
	OutcomeStatus     Outcome = "status"
	OutcomeRedirect   Outcome = "redirect"
	OutcomeTimeout    Outcome = "timeout"
	OutcomeConnection Outcome = "connection"
	OutcomeTLS        Outcome = "tls"
	OutcomeBlocked    Outcome = "blocked"
)

// Result is the outcome of one request, with the status the receiver answered, if any, and the
// time the request took.
type Result struct {
	Outcome  Outcome
	Status   int
	Duration time.Duration
}

// ValidateURL checks the URL of a destination: https, a host, no user information, no fragment,
// at most 2048 bytes; a host that is a literal address or that is not on privateHosts must resolve
// to a public address when it is used, not when it is registered.
func ValidateURL(raw string, privateHosts []string) error {
	_, _ = raw, privateHosts
	return errNotBuilt
}

// Deliverer sends one signed request to a webhook URL. The relay is its only caller. It builds no
// transport of its own: Transport returns the one of the egress guard for the class of the host.
type Deliverer struct {
	Transport    func(private bool) http.RoundTripper
	PrivateHosts []string
	Timeout      time.Duration
	UserAgent    string
	Now          func() time.Time
}

// Send posts the rendering of e to url, signed with keys, and never follows a redirect.
func (d *Deliverer) Send(ctx context.Context, url string, e Event, env Envelope, keys ...Key) Result {
	_, _, _, _, _ = ctx, url, e, env, keys
	return Result{}
}

// Backoff returns the wait after the failures-th consecutive failure of the event at the head of a
// destination's queue (sprint 6.4, section 5.7): min(5 s * 2^(failures-1), max), less a part of up
// to 20 % taken with rnd, a function that returns a number in [0, 1). The stub of part A answers
// zero until plan step C3 writes it.
func Backoff(failures int, max time.Duration, rnd func() float64) time.Duration {
	_, _, _ = failures, max, rnd
	return 0
}
