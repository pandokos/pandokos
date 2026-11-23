package apitest

import (
	"crypto/x509"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeidp"
)

// PublicOrigin is auth.public_origin of an API made by NewPeopleOIDC: the redirect URI that the
// provider is given is this origin and /v1/auth/oidc/callback. The handler is reached at its own
// address; only the redirect URI and the expected origin use this one.
const PublicOrigin = "https://admin.example.test"

// OIDCConfig configures the relying party of NewPeopleOIDC.
type OIDCConfig struct {
	// IDP is the fake provider; one is started with Users when nil.
	IDP   *fakeidp.Server
	Users map[string]fakeidp.Claims
	// Mappings are auth.oidc.group_mappings.
	Mappings      []string
	AutoProvision bool
	// PostSignInPath is auth.oidc.post_sign_in_path; "/console" when empty.
	PostSignInPath string
	ClientAuth     string
}

// OIDC is the relying party and its provider.
type OIDC struct {
	IDP      *fakeidp.Server
	Provider *oidc.Provider
	Post     string
	Rules    []oidc.Rule
}

// NewPeopleOIDC is NewPeople with the identity provider on.
func NewPeopleOIDC(tb testing.TB, cfg PeopleConfig, oc OIDCConfig) (*API, *OIDC) {
	tb.Helper()
	o := &OIDC{IDP: oc.IDP, Post: oc.PostSignInPath}
	if o.IDP == nil {
		o.IDP = fakeidp.Start(tb, fakeidp.Options{Users: oc.Users})
	}
	o.IDP.RegisterRedirectURI(PublicOrigin + "/v1/auth/oidc/callback")
	if o.Post == "" {
		o.Post = "/console"
	}
	pem, err := os.ReadFile(o.IDP.CAFile())
	if err != nil {
		tb.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	guard, err := egress.New(egress.Options{ConnectTimeout: 2 * time.Second, RootCAs: pool})
	if err != nil {
		tb.Fatal(err)
	}
	logger := telemetry.NewLogger(&discard{}, "error")
	cache, err := jwks.New(jwks.Options{
		Transport: guard.Transport, PrivateHosts: []string{"localhost"}, FetchTimeout: 2 * time.Second,
		RefreshInterval: 15 * time.Minute, MinRefetchInterval: time.Second, MaxStale: 6 * time.Hour, Logger: logger,
	})
	if err != nil {
		tb.Fatal(err)
	}
	clientAuth := oc.ClientAuth
	if clientAuth == "" {
		clientAuth = "basic"
	}
	o.Provider, err = oidc.New(oidc.Options{
		Issuer: o.IDP.Issuer(), ClientID: o.IDP.ClientID(), ClientSecret: secret.New(o.IDP.ClientSecret()), ClientAuth: clientAuth,
		RedirectURI: PublicOrigin + "/v1/auth/oidc/callback", Scopes: []string{"openid", "profile", "email"}, Algorithms: []string{"RS256", "ES256"},
		NameClaim: "name", EmailClaim: "email", GroupsClaim: "groups", Cache: cache, Logger: logger,
		HTTP: &http.Client{Transport: guard.Transport(true), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second},
	})
	if err != nil {
		tb.Fatalf("oidc.New: %v", err)
	}
	if len(oc.Mappings) > 0 {
		if o.Rules, err = oidc.ParseRules(oc.Mappings); err != nil {
			tb.Fatalf("oidc.ParseRules: %v", err)
		}
	}
	inner := cfg.Admin
	cfg.Admin = func(a *admin.Options) {
		a.OIDC = o.Provider
		a.Origin.Public = PublicOrigin
		a.Auth.OIDCDisplayName = "Acme single sign-on"
		a.Auth.PostSignInPath = o.Post
		a.Auth.GroupRules = o.Rules
		a.Auth.AutoProvision = oc.AutoProvision
		if inner != nil {
			inner(a)
		}
	}
	api := NewPeople(tb, cfg)
	api.PublicOrigin = PublicOrigin
	return api, o
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }

// Start does the first step of a sign-in as a browser does: it navigates to the start operation and
// returns the hop, whose Location is the authorization endpoint of the provider when it worked.
func (o *OIDC) Start(tb testing.TB, b *Browser) Hop {
	tb.Helper()
	hops := b.Navigate(tb, "/v1/auth/oidc/start")
	return hops[len(hops)-1]
}

// Authorize plays the provider's side for the Location of a start: the query that the callback gets.
func (o *OIDC) Authorize(tb testing.TB, location string) url.Values {
	tb.Helper()
	q, _ := o.IDP.Authorize(location)
	return q
}

// Callback navigates to the callback with the query, as the provider's redirect makes the browser
// do, and returns every hop.
func (o *OIDC) Callback(tb testing.TB, b *Browser, q url.Values) []Hop {
	tb.Helper()
	return b.Navigate(tb, "/v1/auth/oidc/callback?"+q.Encode())
}

// SignIn runs a whole sign-in through the provider and returns the hops from the start to the end.
func (o *OIDC) SignIn(tb testing.TB, b *Browser) []Hop {
	tb.Helper()
	start := o.Start(tb, b)
	if start.Status != http.StatusSeeOther || !strings.HasPrefix(start.Location, o.IDP.AuthorizeURL()) {
		return []Hop{start}
	}
	return append([]Hop{start}, o.Callback(tb, b, o.Authorize(tb, start.Location))...)
}
