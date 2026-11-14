package vault

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// Verdict is what presenting a credential to its provider showed.
type Verdict int

// The verdicts of a validation.
const (
	// Accepted: the provider answered 2xx.
	Accepted Verdict = iota + 1
	// Rejected: the provider answered 401 or 403.
	Rejected
	// Indeterminate: anything else, including a redirect, a timeout and a network error.
	Indeterminate
)

// String names the verdict.
func (v Verdict) String() string {
	switch v {
	case Accepted:
		return "accepted"
	case Rejected:
		return "rejected"
	case Indeterminate:
		return "indeterminate"
	}
	return "unknown"
}

// ValidationTarget is the endpoint a credential is presented to. It comes from the catalogue.
type ValidationTarget struct {
	Family  string // "openai", "anthropic"
	BaseURL string // the endpoint's base URL
	Private bool
}

// Validator presents a secret to a provider and reports what the provider made of it. It
// returns the verdict and the HTTP status the provider answered (0 when there was none). It
// never returns, logs or stores the body of the response or the text of a client error:
// providers echo parts of the key in them.
type Validator interface {
	Validate(ctx context.Context, t ValidationTarget, s secret.Secret) (v Verdict, upstreamStatus int)
}

const (
	defaultValidationTimeout = 10 * time.Second
	maxValidationBody        = 64 * 1024
)

// setCredential sets the headers that present a secret to a family's provider.
type setCredential func(h http.Header, s secret.Secret)

var validators = map[string]setCredential{
	"openai": func(h http.Header, s secret.Secret) { h.Set("Authorization", "Bearer "+s.Reveal()) },
	"anthropic": func(h http.Header, s secret.Secret) {
		h.Set("x-api-key", s.Reveal())
		h.Set("anthropic-version", "2023-06-01")
	},
}

// ValidatorFamilies lists the API families a Validator knows, sorted.
func ValidatorFamilies() []string {
	out := make([]string, 0, len(validators))
	for f := range validators {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

type validator struct {
	public, private http.RoundTripper
	timeout         time.Duration
}

// NewValidator returns the validator of every family in ValidatorFamilies. public and private
// are the round trippers of the two endpoint classes; timeout bounds one validation (10 s when
// not positive). Redirects are never followed and at most 64 KiB of the response is read and
// discarded.
func NewValidator(public, private http.RoundTripper, timeout time.Duration) Validator {
	if timeout <= 0 {
		timeout = defaultValidationTimeout
	}
	return &validator{public: public, private: private, timeout: timeout}
}

// Validate implements Validator.
func (v *validator) Validate(ctx context.Context, t ValidationTarget, s secret.Secret) (Verdict, int) {
	set, ok := validators[t.Family]
	if !ok {
		return Indeterminate, 0
	}
	u, err := url.Parse(strings.TrimRight(t.BaseURL, "/") + "/v1/models")
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Indeterminate, 0
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Indeterminate, 0
	}
	set(req.Header, s)
	rt := v.public
	if t.Private {
		rt = v.private
	}
	resp, err := rt.RoundTrip(req) // the error text may quote the request: it is dropped
	if err != nil {
		return Indeterminate, 0
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, maxValidationBody)
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return Accepted, code
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return Rejected, code
	default:
		return Indeterminate, code
	}
}
