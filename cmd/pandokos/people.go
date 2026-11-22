package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/idem"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/store"
)

// people are the services of sprint 4.2 that the administration listener serves: sessions, local
// accounts with their password hasher, and idempotency keys. They run in the admin role.
type people struct {
	sessions    *session.Store
	accounts    *account.Service
	idempotency *idem.Store
}

func newPeople(cfg *config.Config, db *store.DB) (*people, error) {
	l := cfg.Auth.Local
	hasher, err := password.New(password.Params{
		MemoryKiB: uint32(l.Argon2.MemoryKiB), Iterations: uint32(l.Argon2.Iterations), Parallelism: uint8(l.Argon2.Parallelism), //nolint:gosec // bounded by the configuration
	}, l.MaxConcurrentHashes)
	if err != nil {
		return nil, err
	}
	sessions := session.NewStore(session.Options{
		DB: db, Idle: time.Duration(cfg.Auth.Session.IdleTimeout), Absolute: time.Duration(cfg.Auth.Session.AbsoluteLifetime), MaxPerUser: cfg.Auth.Session.MaxPerUser,
	})
	accounts := account.NewService(account.Options{
		DB: db, Hasher: hasher, Sessions: sessions, MinLength: l.PasswordMinLength, GeneratedPasswordTTL: time.Duration(l.GeneratedPasswordTTL),
		MaxFailures: l.MaxFailures, SourceMaxFailures: l.SourceMaxFailures, FailureWindow: time.Duration(l.FailureWindow), LockDuration: time.Duration(l.LockDuration),
	})
	return &people{
		sessions: sessions, accounts: accounts,
		idempotency: idem.NewStore(idem.Options{DB: db, Retention: time.Duration(cfg.Idempotency.Retention)}),
	}, nil
}

// newOIDC builds the relying party of the identity provider and its group rules, or nil when it is off.
// It has a key cache of its own (the timings of tokens.jwks.*, the hosts of auth.oidc.private_hosts)
// and a client for the token endpoint whose transport is the egress guard's: no redirect, 10 s.
func newOIDC(cfg *config.Config, guard *egress.Guard, logger *slog.Logger) (*oidc.Provider, []oidc.Rule, error) {
	o := cfg.Auth.OIDC
	if !o.Enabled {
		return nil, nil, nil
	}
	rules, err := oidc.ParseRules(o.GroupMappings)
	if err != nil && len(o.GroupMappings) > 0 {
		return nil, nil, err
	}
	hosts := make([]string, 0, len(o.PrivateHosts))
	private := map[string]bool{}
	for _, h := range o.PrivateHosts {
		h = strings.ToLower(h)
		hosts = append(hosts, h)
		private[h] = true
	}
	cache, err := jwks.New(jwks.Options{
		Transport: guard.Transport, PrivateHosts: hosts,
		FetchTimeout:       time.Duration(cfg.Tokens.JWKS.FetchTimeout),
		RefreshInterval:    time.Duration(cfg.Tokens.JWKS.RefreshInterval),
		MinRefetchInterval: time.Duration(cfg.Tokens.JWKS.MinRefetchInterval),
		MaxStale:           time.Duration(cfg.Tokens.JWKS.MaxStale),
		Logger:             logger,
	})
	if err != nil {
		return nil, nil, err
	}
	pub, priv := guard.Transport(false), guard.Transport(true)
	p, err := oidc.New(oidc.Options{
		Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: o.ClientSecret, ClientAuth: o.ClientAuth,
		RedirectURI: cfg.Auth.PublicOrigin + "/v1/auth/oidc/callback",
		Scopes:      o.Scopes, Algorithms: o.Algorithms,
		NameClaim: o.NameClaim, EmailClaim: o.EmailClaim, GroupsClaim: o.GroupsClaim,
		Cache: cache, Logger: logger,
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if private[strings.ToLower(r.URL.Hostname())] {
					return priv.RoundTrip(r)
				}
				return pub.RoundTrip(r)
			}),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       10 * time.Second,
		},
	})
	return p, rules, err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// adminOrigin gives the handler the facts about the administration listener that the origin rule of
// section 5.7 needs.
func adminOrigin(cfg *config.Config) admin.OriginOptions {
	a := cfg.Listeners.Admin
	o := admin.OriginOptions{Public: cfg.Auth.PublicOrigin, TLS: a.TLS.CertFile != "" && a.TLS.KeyFile != ""}
	if host, _, err := net.SplitHostPort(a.Address); err == nil {
		ip, perr := netip.ParseAddr(host)
		o.Loopback = host == "localhost" || (perr == nil && ip.IsLoopback())
	}
	for _, e := range a.TrustedProxies {
		if p, err := netip.ParsePrefix(e); err == nil { // validated at load
			o.TrustedProxies = append(o.TrustedProxies, p)
		}
	}
	return o
}

// adminAuthOptions gives the handler the sign-in settings it reads.
func adminAuthOptions(cfg *config.Config) admin.AuthOptions {
	return admin.AuthOptions{
		LocalEnabled: cfg.Auth.Local.Enabled, OIDCDisplayName: cfg.Auth.OIDC.DisplayName,
		PostSignInPath: cfg.Auth.OIDC.PostSignInPath, AutoProvision: cfg.Auth.OIDC.AutoProvision,
	}
}

// purgeEvery is the period of the purge of sessions, throttle rows, sign-in transactions and
// idempotency keys; each wait has up to 30 seconds of jitter either way.
const purgeEvery = 5 * time.Minute

// runPurge deletes what has expired, as the admin role, until ctx ends. A failure is logged with its
// class only and tried again at the next tick.
func (p *people) runPurge(ctx context.Context, logger *slog.Logger) {
	for {
		wait := purgeEvery + time.Duration(rand.Int64N(int64(time.Minute))) - 30*time.Second //nolint:gosec // jitter, not a secret
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		for name, purge := range map[string]func(context.Context) error{
			"sessions": p.sessions.Purge, "accounts": p.accounts.Purge, "idempotency": p.idempotency.Purge,
		} {
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := purge(pctx); err != nil && ctx.Err() == nil {
				if sqlstate := store.SQLState(err); sqlstate != "" {
					logger.Warn("purge failed", "target", name, "sqlstate", sqlstate)
				} else {
					logger.Warn("purge failed", "target", name)
				}
			}
			cancel()
		}
	}
}
