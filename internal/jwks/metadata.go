package jwks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Metadata is what the OpenID Connect relying party needs of a discovery document.
type Metadata struct{ Issuer, JWKSURI, AuthorizationEndpoint, TokenEndpoint string }

// metaEntry is the state of one issuer's document, kept as keys are (section 5.5): mu guards every
// field; a fetch in flight runs without holding it.
type metaEntry struct {
	mu          sync.Mutex
	doc         Metadata
	have        bool
	fetchedAt   time.Time
	attemptedAt time.Time
	inflight    chan struct{}
}

func (c *Cache) metaFor(issuer string) *metaEntry {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	if c.meta == nil {
		c.meta = map[string]*metaEntry{}
	}
	if e, ok := c.meta[issuer]; ok {
		return e
	}
	if len(c.meta) >= maxSources { // the documents are few; over the bound the oldest is not worth tracking
		for k := range c.meta {
			delete(c.meta, k)
			break
		}
	}
	e := &metaEntry{}
	c.meta[issuer] = e
	return e
}

// Metadata returns the discovery document of an issuer, whose issuer member must be the one asked
// for (sprint 4.2). It is cached as keys are: answered from memory, refreshed in the background
// after RefreshInterval, dropped after MaxStale; the error wraps authz.ErrUnavailable.
func (c *Cache) Metadata(ctx context.Context, issuer string) (Metadata, error) {
	e := c.metaFor(issuer)
	fetched := false
	for {
		e.mu.Lock()
		now := c.o.Now()
		if e.have && now.Sub(e.fetchedAt) > c.o.MaxStale {
			e.doc, e.have = Metadata{}, false
		}
		if e.have {
			m := e.doc
			if e.inflight == nil && now.Sub(e.fetchedAt) >= c.o.RefreshInterval && c.metaMayAttempt(e, now) {
				c.metaStart(e, issuer, now)
			}
			e.mu.Unlock()
			return m, nil
		}
		var wait chan struct{}
		switch {
		case e.inflight != nil && !fetched:
			wait = e.inflight
		case e.inflight == nil && !fetched && c.metaMayAttempt(e, now):
			wait = c.metaStart(e, issuer, now)
		default:
			e.mu.Unlock()
			return Metadata{}, unavailable("no discovery document obtained")
		}
		e.mu.Unlock()
		fetched = true
		select {
		case <-wait:
		case <-ctx.Done():
			return Metadata{}, unavailable("gave up waiting")
		}
	}
}

func (c *Cache) metaMayAttempt(e *metaEntry, now time.Time) bool {
	return e.attemptedAt.IsZero() || now.Sub(e.attemptedAt) >= c.o.MinRefetchInterval
}

// metaStart begins a fetch detached from every caller. e.mu is held and no fetch is in flight.
func (c *Cache) metaStart(e *metaEntry, issuer string, now time.Time) chan struct{} {
	done := make(chan struct{})
	e.inflight = done
	e.attemptedAt = now
	go func() {
		defer func() {
			e.mu.Lock()
			e.inflight = nil
			e.mu.Unlock()
			close(done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), c.o.FetchTimeout)
		defer cancel()
		doc, err := c.fetchMetadata(ctx, issuer)
		c.countFetch("metadata", err)
		if err != nil {
			attrs := []any{"issuer", logIssuer(issuer)}
			var fe *fetchError
			if errors.As(err, &fe) {
				attrs = append(attrs, "class", fe.class)
				if fe.reason != "" {
					attrs = append(attrs, "reason", fe.reason)
				}
			}
			c.o.Logger.Warn("issuer metadata not fetched", attrs...)
			return
		}
		e.mu.Lock()
		e.doc, e.have, e.fetchedAt = doc, true, now
		e.mu.Unlock()
	}()
	return done
}

func (c *Cache) fetchMetadata(ctx context.Context, issuer string) (Metadata, error) {
	iss, ok := parseHTTPS(issuer)
	if !ok {
		return Metadata{}, &fetchError{class: classInvalidDocument}
	}
	d := *iss
	d.RawQuery, d.Fragment, d.RawFragment, d.RawPath = "", "", "", ""
	d.Path = strings.TrimSuffix(iss.Path, "/") + "/.well-known/openid-configuration"
	body, err := c.get(ctx, &d, maxDiscoveryBytes)
	if err != nil {
		return Metadata{}, err
	}
	var doc struct {
		Issuer                *string `json:"issuer"`
		JWKSURI               *string `json:"jwks_uri"`
		AuthorizationEndpoint *string `json:"authorization_endpoint"`
		TokenEndpoint         *string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Issuer == nil || doc.JWKSURI == nil || doc.AuthorizationEndpoint == nil || doc.TokenEndpoint == nil {
		return Metadata{}, &fetchError{class: classInvalidDocument}
	}
	if *doc.Issuer != issuer {
		return Metadata{}, &fetchError{class: classIssuerMismatch}
	}
	for _, raw := range []string{*doc.JWKSURI, *doc.AuthorizationEndpoint, *doc.TokenEndpoint} {
		if _, ok := parseHTTPS(raw); !ok {
			return Metadata{}, &fetchError{class: classInvalidDocument}
		}
	}
	return Metadata{Issuer: *doc.Issuer, JWKSURI: *doc.JWKSURI, AuthorizationEndpoint: *doc.AuthorizationEndpoint, TokenEndpoint: *doc.TokenEndpoint}, nil
}
