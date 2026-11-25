package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/vault"
)

// startVaultOptions are what the start sequence of the vault (section 5.10 of sprint 12.1) needs.
type startVaultOptions struct {
	Transit       *vault.TransitProvider // started first; nil when no transit provider is configured
	Provider      vault.KeyProvider      // what activates and wraps: the provider or a router
	Admin, Worker *store.DB
	Logger        *slog.Logger
	StartTimeout  time.Duration // vault.kek.transit.start_timeout; 0 waits without limit
	Now           func() time.Time
	// Sleep waits d, or returns the error of ctx when it ends first; the tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// startVault runs the start sequence: start the transit provider, activate the version. A failure
// that is vault.ErrProviderUnavailable is logged and tried again after 1 s, 2 s, 4 s up to 15 s
// until it succeeds, ctx ends or the start timeout has passed; any other failure is returned at once.
func startVault(ctx context.Context, o startVaultOptions) (version, activation string, err error) {
	now, sleep := o.Now, o.Sleep
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepContext
	}
	begin := now()
	var lastLog, outageStart time.Time
	failures := 0
	for wait := time.Second; ; wait = min(2*wait, 15*time.Second) {
		version, activation, err = startAttempt(ctx, o)
		if err == nil {
			if failures > 0 {
				o.Logger.Info("key provider recovered", "kek_provider", providerTransit, "phase", "start",
					"outage_seconds", int64(now().Sub(outageStart)/time.Second))
			}
			return version, activation, nil
		}
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		if !errors.Is(err, vault.ErrProviderUnavailable) {
			return "", "", err
		}
		failures++
		t := now()
		if failures == 1 {
			outageStart = t
		}
		if lastLog.IsZero() || t.Sub(lastLog) >= 30*time.Second {
			lastLog = t
			operation, class := "unwrap", "connection"
			var te *vault.TransitError
			if errors.As(err, &te) {
				operation = te.Operation
			}
			if o.Transit != nil {
				if c := o.Transit.LastErrorClass(); c != "" {
					class = c
				}
			}
			o.Logger.Warn("key provider unavailable", "kek_provider", providerTransit, "phase", "start",
				"operation", operation, "error_class", class, "failures", failures)
		}
		if o.StartTimeout > 0 && t.Sub(begin) >= o.StartTimeout {
			return "", "", fmt.Errorf("the key service did not answer within vault.kek.transit.start_timeout: %w", err)
		}
		if err := sleep(ctx, wait); err != nil {
			return "", "", err
		}
	}
}

// startAttempt is one pass of the sequence: start the transit provider, activate.
func startAttempt(ctx context.Context, o startVaultOptions) (version, activation string, err error) {
	if o.Transit != nil {
		if err := o.Transit.Start(ctx); err != nil {
			return "", "", err
		}
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return vault.Activate(actx, o.Admin, o.Worker, o.Provider)
}

// sleepContext waits d, or returns the error of ctx when it ends first.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// keyProviders are the providers that serve and kek check build from the configuration.
type keyProviders struct {
	Provider vault.KeyProvider      // what wraps and unwraps: one provider or a router
	Transit  *vault.TransitProvider // nil without a transit provider
	Local    *vault.LocalProvider   // nil without a key file or unwrap key files
	// UnwrapOnly is the name of the provider that only unwraps ("local", "transit" or "").
	UnwrapOnly string
}

// buildKeyProviders builds the providers of the configuration (sprint 12.1, section 5.7). The
// transit provider reaches the service through the guard; onRequest records the time of a request
// (M60) and may be nil.
func buildKeyProviders(cfg *config.Config, guard *egress.Guard, logger *slog.Logger, onRequest func(string, time.Duration)) (*keyProviders, error) {
	k := cfg.Vault.KEK
	kp := &keyProviders{}
	var err error
	switch {
	case k.Provider == "local":
		kp.Local, err = vault.NewLocalProvider(k.Local.Key, k.Local.UnwrapKeys...)
	case len(k.Local.UnwrapKeys) > 0:
		kp.Local, err = vault.NewLocalProvider(k.Local.UnwrapKeys[0], k.Local.UnwrapKeys[1:]...)
	}
	if err != nil {
		return nil, err
	}
	if k.Transit.Configured() {
		t := k.Transit
		kp.Transit, err = vault.NewTransitProvider(vault.TransitOptions{
			Transport: guard.KeyService(t.TLS.RootCAs), Address: t.URL, Namespace: t.Namespace,
			Mount: t.Mount, KeyName: t.KeyName, AuthMethod: t.Auth.Method, TokenFile: t.Auth.TokenFile,
			AppRoleMount: t.Auth.AppRole.Mount, RoleIDFile: t.Auth.AppRole.RoleIDFile, SecretIDFile: t.Auth.AppRole.SecretIDFile,
			RequestTimeout: time.Duration(t.RequestTimeout), MaxConcurrent: t.MaxConcurrentRequests, AllowExportable: t.AllowExportableKey,
			UserAgent: "pandokos/" + buildinfo.Version, Logger: logger, OnRequest: onRequest,
		})
		if err != nil {
			return nil, err
		}
	}
	var active, other vault.KeyProvider
	if k.Provider == "transit" {
		active = kp.Transit
		if kp.Local != nil {
			other, kp.UnwrapOnly = kp.Local, providerLocal
		}
	} else {
		active = kp.Local
		if kp.Transit != nil {
			other, kp.UnwrapOnly = kp.Transit, providerTransit
		}
	}
	if other == nil {
		kp.Provider = active
		return kp, nil
	}
	kp.Provider, err = vault.NewRouter(active, other)
	if err != nil {
		return nil, err
	}
	return kp, nil
}

// keyProviderSource is the snapshot of the transit provider for the metrics.
func keyProviderSource(tp *vault.TransitProvider) func() telemetry.KeyProviderStats {
	return func() telemetry.KeyProviderStats {
		st := tp.Stats()
		out := telemetry.KeyProviderStats{Requests: make(map[[2]string]int64, len(st.Requests)), Up: st.Up, TokenExpires: st.TokenExpires, TokenTTL: st.TokenTTL}
		for c, n := range st.Requests {
			out.Requests[[2]string{c.Operation, c.Outcome}] = n
		}
		return out
	}
}

// The names of the providers, as the key versions record them.
const (
	providerLocal   = "local"
	providerTransit = "transit"
)

// newGuard builds the egress guard of the upstream settings.
func newGuard(cfg *config.Config) (*egress.Guard, error) {
	return egress.New(egress.Options{
		ConnectTimeout: time.Duration(cfg.Upstream.ConnectTimeout),
		RootCAs:        cfg.Upstream.TLS.RootCAs,
		Proxy:          cfg.Upstream.Proxy.ParsedURL,
	})
}
