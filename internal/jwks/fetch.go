package jwks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const (
	maxDiscoveryBytes = 64 << 10
	maxKeySetBytes    = 256 << 10
)

// The classes of a failed fetch (section 5.5). A failure says nothing else: not the URL's query,
// not a header, not a byte of the answer.
const (
	classBlocked         = "blocked"
	classTimeout         = "timeout"
	classStatus          = "status"
	classTooLarge        = "too_large"
	classInvalidDocument = "invalid_document"
	classIssuerMismatch  = "issuer_mismatch"
	classNoUsableKey     = "no_usable_key"
	classNetwork         = "network"
)

// FetchClasses lists every class of a failed fetch, in a fixed order (the values of the
// pandokos.result attribute of the metrics besides ok).
var FetchClasses = []string{classBlocked, classTimeout, classStatus, classTooLarge, classInvalidDocument, classIssuerMismatch, classNoUsableKey, classNetwork}

// fetchError is a failed fetch: its class, and for a refusal by the egress guard its reason.
type fetchError struct{ class, reason string }

func (e *fetchError) Error() string { return "jwks: fetch failed: " + e.class }

// blocked is implemented by the error of the egress guard.
type blocked interface{ BlockedReason() string }

// parseHTTPS returns u when it is an https URL with a host and no user information.
func parseHTTPS(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil, false
	}
	return u, true
}

// private reports whether the host of u is one that the operator listed.
func (c *Cache) private(u *url.URL) bool {
	return c.hosts[strings.ToLower(u.Hostname())]
}

// get fetches one document: a GET through the transport of the class of u, at most limit bytes,
// status 200 only, no redirect.
func (c *Cache) get(ctx context.Context, u *url.URL, limit int64) ([]byte, error) {
	client := c.public
	if c.private(u) {
		client = c.priv
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &fetchError{class: classInvalidDocument}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, classify(ctx, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode != http.StatusOK:
		return nil, &fetchError{class: classStatus}
	case resp.ContentLength > limit:
		return nil, &fetchError{class: classTooLarge}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, classify(ctx, err)
	}
	if int64(len(body)) > limit {
		return nil, &fetchError{class: classTooLarge}
	}
	if err := ctx.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	return body, nil
}

func classify(ctx context.Context, err error) *fetchError {
	var b blocked
	if errors.As(err, &b) {
		return &fetchError{class: classBlocked, reason: b.BlockedReason()}
	}
	var ne net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return &fetchError{class: classTimeout}
	}
	return &fetchError{class: classNetwork}
}

// discover returns the address of the key set of an issuer: its jwks_uri when given, else the
// one named by the discovery document, whose issuer must be the registered one.
func (c *Cache) discover(ctx context.Context, src Source) (*url.URL, error) {
	if src.JWKSURI != "" {
		u, ok := parseHTTPS(src.JWKSURI)
		if !ok {
			return nil, &fetchError{class: classInvalidDocument}
		}
		return u, nil
	}
	iss, ok := parseHTTPS(src.Issuer)
	if !ok {
		return nil, &fetchError{class: classInvalidDocument}
	}
	d := *iss
	d.RawQuery, d.Fragment, d.RawFragment, d.RawPath = "", "", "", ""
	d.Path = strings.TrimSuffix(iss.Path, "/") + "/.well-known/openid-configuration"
	body, err := c.get(ctx, &d, maxDiscoveryBytes)
	if err != nil {
		return nil, err
	}
	return parseDiscovery(body, src.Issuer)
}

// parseDiscovery reads the body of a discovery document asked for the issuer given and returns
// the address of its key set: the pure part of discover.
func parseDiscovery(body []byte, issuer string) (*url.URL, error) {
	var doc struct {
		Issuer  *string `json:"issuer"`
		JWKSURI *string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Issuer == nil || doc.JWKSURI == nil {
		return nil, &fetchError{class: classInvalidDocument}
	}
	if *doc.Issuer != issuer {
		return nil, &fetchError{class: classIssuerMismatch}
	}
	u, ok := parseHTTPS(*doc.JWKSURI)
	if !ok {
		return nil, &fetchError{class: classInvalidDocument}
	}
	return u, nil
}

// fetch retrieves the usable keys of a source: discovery when needed, then the key set. Each
// document fetched is counted by its result.
func (c *Cache) fetch(ctx context.Context, src Source) ([]usableKey, error) {
	u, err := c.discover(ctx, src)
	if src.JWKSURI == "" {
		c.countFetch("metadata", err)
	}
	if err != nil {
		if src.JWKSURI != "" {
			c.countFetch("keys", err)
		}
		return nil, err
	}
	keys, err := c.fetchKeys(ctx, u)
	c.countFetch("keys", err)
	return keys, err
}

func (c *Cache) fetchKeys(ctx context.Context, u *url.URL) ([]usableKey, error) {
	body, err := c.get(ctx, u, maxKeySetBytes)
	if err != nil {
		return nil, err
	}
	keys, perr := parseKeySet(body)
	switch {
	case perr != nil:
		return nil, &fetchError{class: classInvalidDocument}
	case len(keys) == 0:
		return nil, &fetchError{class: classNoUsableKey}
	}
	return keys, nil
}
