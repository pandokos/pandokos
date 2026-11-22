// Package oidc is the OpenID Connect relying party of the administration listener: the redirect
// to the identity provider, the exchange of the code and the check of the ID token, and the group
// rules (sprint 4.2). It does not import go-jose; the token is verified by internal/authn.
package oidc

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/secret"
)

// Failure is a refusal of a sign-in through the identity provider; Reason is the audit reason
// (state_mismatch, provider_error, code_rejected, nonce_mismatch, unmapped_subject, ...).
type Failure struct{ Reason string }

func (f *Failure) Error() string { return "oidc: sign-in refused: " + f.Reason }

// Options configure a Provider. Cache gives both the provider's metadata and the keys that verify
// the ID token; HTTP is the client of the token endpoint (the transport of the egress guard, no
// redirect, 10 s).
type Options struct {
	Issuer, ClientID                   string
	ClientSecret                       secret.Secret
	ClientAuth                         string // "basic" (default) or "post"
	RedirectURI                        string // the expected origin of the listener + /v1/auth/oidc/callback
	Scopes, Algorithms                 []string
	NameClaim, EmailClaim, GroupsClaim string
	Cache                              *jwks.Cache
	HTTP                               *http.Client
	Now                                func() time.Time
	Logger                             *slog.Logger
}

// Provider is the relying party of one identity provider.
type Provider struct{ o Options }

// New returns the relying party of the provider that o describes.
func New(o Options) (*Provider, error) {
	switch {
	case o.Issuer == "" || o.ClientID == "" || o.ClientSecret.IsZero() || o.RedirectURI == "":
		return nil, errors.New("oidc: issuer, client ID, client secret and redirect URI are required")
	case o.Cache == nil || o.HTTP == nil || o.Logger == nil:
		return nil, errors.New("oidc: Cache, HTTP and Logger are required")
	case len(o.Algorithms) == 0:
		return nil, errors.New("oidc: at least one algorithm is required")
	}
	if u, err := url.Parse(o.RedirectURI); err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("oidc: the redirect URI is not an absolute address")
	}
	switch o.ClientAuth {
	case "":
		o.ClientAuth = "basic"
	case "basic", "post":
	default:
		return nil, fmt.Errorf("oidc: unknown client authentication %q", o.ClientAuth)
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{"openid", "profile", "email"}
	}
	if o.NameClaim == "" {
		o.NameClaim = "name"
	}
	if o.EmailClaim == "" {
		o.EmailClaim = "email"
	}
	if o.GroupsClaim == "" {
		o.GroupsClaim = "groups"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Provider{o: o}, nil
}

// Issuer returns the issuer of the provider: the one every user_identity row of the installation
// names.
func (p *Provider) Issuer() string { return p.o.Issuer }

// SeedCookieName is the name of the cookie that binds a sign-in to the browser that started it;
// SeedCookie returns the cookie that carries seed.
const SeedCookieName = "__Host-pandokos_oidc"

// SeedCookie returns the cookie that binds the flow of seed to the browser.
func SeedCookie(seed secret.Secret) *http.Cookie {
	return &http.Cookie{Name: SeedCookieName, Value: seed.Reveal(), Path: "/", MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

// ClearSeedCookie returns the cookie that makes the browser forget the seed.
func ClearSeedCookie() *http.Cookie {
	return &http.Cookie{Name: SeedCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

// Identity is what is kept of an ID token.
type Identity struct {
	Subject, Name, Email string
	Groups               []string
}

// Rule is one group rule: members of Group get Role, in the platform or in the tenant.
type Rule struct {
	Platform bool
	Tenant   string // tenant name; empty for a platform rule
	Role     string
	Group    string
}
