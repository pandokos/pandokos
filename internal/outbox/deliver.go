package outbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
)

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

// maxURL is the longest URL of a destination, in bytes.
const maxURL = 2048

// maxAnswer is how much of a response body is read before it is dropped.
const maxAnswer = 64 << 10

var errURL = errors.New("outbox: the URL of a destination is not valid")

// isPrivateHost reports whether host is listed, ignoring case.
func isPrivateHost(host string, list []string) bool {
	return slices.ContainsFunc(list, func(h string) bool { return strings.EqualFold(h, host) })
}

// ValidateURL checks the URL of a destination: https, a host, no user information, no fragment,
// at most 2048 bytes; http only for a host on privateHosts. A host that is a literal address that
// the egress guard would refuse is refused too; any other host must resolve to a public address
// when it is used, not when it is registered. The error never quotes the URL.
func ValidateURL(raw string, privateHosts []string) error {
	if raw == "" || len(raw) > maxURL {
		return errURL
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] <= ' ' || raw[i] == 0x7f {
			return errURL
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.Hostname() == "" {
		return errURL
	}
	if strings.Contains(raw, "#") {
		return errURL
	}
	private := isPrivateHost(u.Hostname(), privateHosts)
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && private:
	default:
		return errURL
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil {
		switch c := egress.Classify(a.Unmap().WithZone("")); {
		case c == egress.AddrForbidden, c == egress.AddrPrivate && !private:
			return errURL
		}
	}
	return nil
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

// Send posts the rendering of e to url, signed with keys, and never follows a redirect. The
// request has one deadline, Timeout. The result holds the class of the outcome and the status the
// receiver answered; nothing of the answer is kept and the error of the transport, which can name
// the host, is dropped.
func (d *Deliverer) Send(ctx context.Context, rawURL string, e Event, env Envelope, keys ...Key) Result {
	start := time.Now()
	done := func(o Outcome, status int) Result {
		return Result{Outcome: o, Status: status, Duration: max(time.Since(start), time.Nanosecond)}
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return done(OutcomeBlocked, 0)
	}
	body := Render(e, env)
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	timestamp, signature := Sign(e.ID, now(), body, keys...)

	ctx, cancel := context.WithTimeout(ctx, d.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return done(OutcomeBlocked, 0)
	}
	req.Header.Set("Content-Type", "application/cloudevents+json; charset=utf-8")
	req.Header.Set("User-Agent", d.UserAgent)
	req.Header.Set("webhook-id", e.ID)
	req.Header.Set("webhook-timestamp", timestamp)
	req.Header.Set("webhook-signature", signature)

	client := &http.Client{
		Transport:     d.Transport(isPrivateHost(u.Hostname(), d.PrivateHosts)),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return done(classify(ctx, err), 0)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	_ = resp.Body.Close()
	switch c := resp.StatusCode; {
	case c >= 200 && c <= 299:
		return done(OutcomeDelivered, c)
	case c >= 300 && c <= 399:
		return done(OutcomeRedirect, c)
	default:
		return done(OutcomeStatus, c)
	}
}

// blockedReason is what the refusal of the egress guard implements.
type blockedReason interface{ BlockedReason() string }

// classify maps the error of a request to an outcome (section 5.7).
func classify(ctx context.Context, err error) Outcome {
	var blocked blockedReason
	if errors.As(err, &blocked) {
		return OutcomeBlocked
	}
	var (
		verify  *tls.CertificateVerificationError
		record  tls.RecordHeaderError
		alert   tls.AlertError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		invalid x509.CertificateInvalidError
		ne      net.Error
	)
	switch {
	case errors.As(err, &verify), errors.As(err, &record), errors.As(err, &alert), errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &invalid):
		return OutcomeTLS
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil, errors.As(err, &ne) && ne.Timeout():
		return OutcomeTimeout
	default:
		return OutcomeConnection
	}
}

// Backoff returns the wait after the failures-th consecutive failure of the event at the head of a
// destination's queue (sprint 6.4, section 5.7): min(5 s * 2^(failures-1), max), less a part of up
// to 20 % taken with rnd, a function that returns a number in [0, 1). A nil rnd draws at random.
func Backoff(failures int, max time.Duration, rnd func() float64) time.Duration {
	const base = 5 * time.Second
	wait := base
	for i := 1; i < failures && wait < max; i++ {
		wait *= 2
	}
	if wait > max {
		wait = max
	}
	if rnd == nil {
		rnd = rand.Float64
	}
	r := min(max0(rnd()), 0.999999999)
	return wait - time.Duration(float64(wait)*0.2*r)
}

func max0(f float64) float64 { return math.Max(f, 0) }
