package vault

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
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

// Validator presents a secret to a provider and reports what the provider made of it. It
// returns the verdict and the HTTP status the provider answered (0 when there was none). It
// never returns, logs or stores the body of the response or the text of a client error:
// providers echo parts of the key in them.
type Validator interface {
	Validate(ctx context.Context, s secret.Secret) (v Verdict, upstreamStatus int)
}

const (
	defaultValidationTimeout = 10 * time.Second
	maxValidationBody        = 64 * 1024
)

type openAIValidator struct {
	url    string
	client *http.Client
}

// NewOpenAIValidator returns a Validator that asks `GET <base>/models` with the secret as a
// bearer token. The destination is operator configuration, never caller input. The call uses
// its own transport: no proxy from the environment, certificates verified, TLS 1.2 or later,
// redirects not followed, no connection reuse; timeout bounds the whole exchange (10 s when not
// positive) and at most 64 KiB of the response is read and discarded.
func NewOpenAIValidator(base *url.URL, timeout time.Duration) Validator {
	if timeout <= 0 {
		timeout = defaultValidationTimeout
	}
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + "/models"
	u.RawPath, u.RawQuery, u.Fragment, u.User = "", "", "", nil
	tr := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: maxValidationBody,
	}
	return &openAIValidator{url: u.String(), client: &http.Client{
		Timeout:       timeout,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Validate implements Validator.
func (v *openAIValidator) Validate(ctx context.Context, s secret.Secret) (Verdict, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return Indeterminate, 0
	}
	req.Header.Set("Authorization", "Bearer "+s.Reveal())
	resp, err := v.client.Do(req) // the error text may quote the request: it is dropped
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
