package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/ledger"
	"github.com/pandokos/pandokos/internal/lifecycle"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
	"github.com/pandokos/pandokos/internal/reports"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/server"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/vault"
)

// warnBootstrap logs "bootstrap key active" at once and then every hour while a bootstrap key is
// usable (IAM-14). A failure of the query is logged at debug and tried again at the next hour.
func warnBootstrap(ctx context.Context, logger *slog.Logger, db *store.DB) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		keys, err := admin.ActiveBootstrapKeys(qctx, db)
		cancel()
		if err != nil {
			logger.Debug("bootstrap key check failed", "error", err.Error())
		}
		for _, k := range keys {
			logger.Warn("bootstrap key active", "key_id", k.KeyID, "expires_at", k.ExpiresAt.Format(time.RFC3339))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// warnNoAdministrator logs, once at start, that local sign-in is on and no active user holds a platform
// role (section 5.15). The query failing is logged at debug: the warning is a hint, not a gate.
func warnNoAdministrator(ctx context.Context, logger *slog.Logger, cfg *config.Config, db *store.DB) {
	if !cfg.Auth.Local.Enabled {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	has, err := account.HasAdministrator(qctx, db)
	if err != nil {
		logger.Debug("administrator check failed", "error", err.Error())
		return
	}
	if !has {
		logger.Warn("no user with a platform role exists", "hint", `create the first administrator with "pandokos init --admin-username NAME"`)
	}
}

// ensurePartitions creates the partitions at start; a variable so that a test can make it fail.
var ensurePartitions = func(ctx context.Context, p *ledger.Partitioner) error { return p.Ensure(ctx) }

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	path := *file
	if path == "" {
		path = config.FileFromEnviron(environ)
	}

	cfg, warnings, err := config.Load(config.Source{File: path, Environ: environ})
	if err != nil {
		boot := telemetry.NewLogger(stdout, "info")
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			for _, p := range ve.Problems {
				boot.Error("configuration error", "key", p.Key, "problem", p.Message)
			}
		} else {
			boot.Error("startup failed", "error", err.Error())
		}
		return 1
	}

	logger := telemetry.NewLogger(stdout, cfg.Log.Level)
	logger.Info("starting", "version", buildinfo.Version, "commit", buildinfo.Commit, "go_version", runtime.Version())
	for _, w := range warnings {
		logger.Warn("configuration warning", "key", w.Key, "warning", w.Message)
	}

	ms, err := migrations.Load()
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	var dbs []*store.DB
	defer func() {
		for _, db := range dbs {
			db.Close()
		}
	}()
	for _, r := range []struct {
		role store.Role
		c    config.DatabaseRole
	}{
		{store.RoleGateway, cfg.Database.Gateway},
		{store.RoleAdmin, cfg.Database.Admin},
		{store.RoleWorker, cfg.Database.Worker},
	} {
		db, err := store.Open(store.Options{
			Conn: connOptions(cfg, r.c.User, r.c.Password), Role: r.role,
			MaxConns: r.c.MaxConnections, Migrations: ms,
		})
		if err != nil {
			logger.Error("startup failed", "error", err.Error())
			return 1
		}
		dbs = append(dbs, db)
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = db.Check(cctx)
		cancel()
		if err != nil {
			logger.Error("startup failed", "error", err.Error())
			return 1
		}
		logger.Info("database ready", "role", string(r.role), "schema_version", len(ms))
	}

	gatewayDB, adminDB, workerDB := dbs[0], dbs[1], dbs[2]
	kp, err := vault.NewLocalProvider(cfg.Vault.KEK.Local.Key, cfg.Vault.KEK.Local.UnwrapKeys...)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	vctx, vcancel := context.WithTimeout(ctx, 30*time.Second)
	kekVersion, kekActivation, err := vault.Activate(vctx, adminDB, workerDB, kp)
	vcancel()
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	logger.Info("vault ready", "kek_provider", kp.Name(), "kek_version", kekVersion, "kek_activation", kekActivation,
		"kek_unwrap_versions", len(kp.Versions())-1)
	sealer, err := vault.NewSealer(kp)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	opener, err := vault.NewOpener(vault.OpenerOptions{
		DB: gatewayDB, Provider: kp, Logger: logger,
		CredentialTTL: time.Duration(cfg.Cache.CredentialTTL), DataKeyTTL: time.Duration(cfg.Cache.DataKeyTTL),
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	guard, err := egress.New(egress.Options{
		ConnectTimeout: time.Duration(cfg.Upstream.ConnectTimeout),
		RootCAs:        cfg.Upstream.TLS.RootCAs,
		Proxy:          cfg.Upstream.Proxy.ParsedURL,
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	defer guard.CloseIdleConnections()
	var keyCache *jwks.Cache
	gatewayAuthOpts := authn.Options{DB: gatewayDB, Logger: logger, Kinds: []authn.Kind{authn.KindTenant}, CacheTTL: time.Duration(cfg.Cache.PrincipalTTL)}
	if cfg.Tokens.Enabled {
		// Keys of token issuers are fetched here and nowhere else, through the egress guard.
		keys, err := jwks.New(jwks.Options{
			Transport:          guard.Transport,
			PrivateHosts:       cfg.Tokens.PrivateHosts,
			FetchTimeout:       time.Duration(cfg.Tokens.JWKS.FetchTimeout),
			RefreshInterval:    time.Duration(cfg.Tokens.JWKS.RefreshInterval),
			MinRefetchInterval: time.Duration(cfg.Tokens.JWKS.MinRefetchInterval),
			MaxStale:           time.Duration(cfg.Tokens.JWKS.MaxStale),
			Logger:             logger,
		})
		if err != nil {
			logger.Error("startup failed", "error", err.Error())
			return 1
		}
		keyCache = keys
		gatewayAuthOpts.Kinds = append(gatewayAuthOpts.Kinds, authn.KindToken)
		gatewayAuthOpts.Tokens = authn.TokenOptions{
			Keys: keys, MaxBytes: cfg.Tokens.MaxTokenBytes,
			ClockSkew: time.Duration(cfg.Tokens.ClockSkew), MaxLifetime: time.Duration(cfg.Tokens.MaxLifetime),
		}
	}
	gatewayAuth, err := authn.New(gatewayAuthOpts)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	adminAuthOpts := authn.Options{DB: adminDB, Logger: logger, Kinds: []authn.Kind{authn.KindTenant, authn.KindPlatform}}
	if cfg.Tokens.Enabled && cfg.Tokens.AdminListener {
		// The key cache is the gateway's; the administration listener keeps no principal cache.
		adminAuthOpts.Kinds = append(adminAuthOpts.Kinds, authn.KindToken)
		adminAuthOpts.Tokens = gatewayAuthOpts.Tokens
	}
	adminAuth, err := authn.New(adminAuthOpts)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	catalogCache, err := catalog.NewCache(catalog.CacheOptions{DB: gatewayDB, Logger: logger, TTL: time.Duration(cfg.Cache.CatalogTTL), DefaultCurrency: cfg.Pricing.DefaultCurrency})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	zones, err := limits.NewZones(cfg.Limits.Location)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	ruleCache, err := limits.NewRuleCache(limits.RuleCacheOptions{DB: gatewayDB, Logger: logger, TTL: time.Duration(cfg.Cache.LimitsTTL), Zones: zones, DefaultCurrency: cfg.Pricing.DefaultCurrency})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	events, err := limits.NewEventWriter(limits.EventWriterOptions{DB: gatewayDB, Logger: logger})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	// The horizon is longer than any request lives from its start to the hand-over of its record.
	horizon := time.Duration(cfg.Limits.RequestBodyReadTimeout) + time.Duration(cfg.Limits.MaxRequestDuration) + 2*time.Minute
	memory, err := limits.NewMemory(limits.MemoryOptions{Logger: logger, Events: events, Horizon: horizon})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	listenConn := connOptions(cfg, cfg.Database.Gateway.User, cfg.Database.Gateway.Password)
	listenConn.Host, listenConn.Port = cfg.Database.DirectHost, cfg.Database.DirectPort
	listener, err := store.NewListener(store.ListenerOptions{
		Conn: listenConn, Role: store.RoleGateway, Logger: logger, Direct: cfg.Database.DirectAddress != "",
		OnNotify: func(p string) {
			gatewayAuth.Invalidate(p)
			opener.Invalidate(p)
			catalogCache.Invalidate(p)
			ruleCache.Invalidate(p)
		},
		OnReset: func() { gatewayAuth.Flush(); opener.Flush(); catalogCache.Flush(); ruleCache.Flush() },
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	fetchPrices := func(ctx context.Context, u string) ([]byte, error) {
		return catalog.Fetch(ctx, u, catalog.FetchOptions{Transport: guard.Transport(false)})
	}
	refresh := admin.RefreshOptions{}
	if cfg.Pricing.Refresh.Enabled {
		refresh = admin.RefreshOptions{Enabled: true, URL: cfg.Pricing.Refresh.ParsedURL.String(), Fetch: fetchPrices}
	}
	adminOpts := admin.Options{
		Logger: logger, DB: adminDB, Authenticator: adminAuth, Sealer: sealer,
		Validator: vault.NewValidator(guard.Transport(false), guard.Transport(true), 10*time.Second),
		Snapshot:  genaiprices.Snapshot(),
		Refresh:   refresh,
	}
	adminOpts.Limits.Zones, adminOpts.Limits.OnUnknownPrice = zones, cfg.Limits.OnUnknownPrice
	adminOpts.DefaultCurrency = cfg.Pricing.DefaultCurrency
	adminOpts.Support.Enabled, adminOpts.Support.MaxDuration = cfg.Support.Enabled, time.Duration(cfg.Support.MaxDuration)
	adminOpts.Limits.Ledger = limits.NewLedger(workerDB)
	people, err := newPeople(cfg, adminDB)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	adminOpts.Sessions, adminOpts.Accounts, adminOpts.Idempotency = people.sessions, people.accounts, people.idempotency
	adminOpts.Origin, adminOpts.Auth = adminOrigin(cfg), adminAuthOptions(cfg)
	if adminOpts.OIDC, adminOpts.Auth.GroupRules, err = newOIDC(cfg, guard, logger); err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	adminHandler, err := admin.New(adminOpts)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	// One resource for every signal of this process (one service.instance.id).
	resource, err := telemetry.NewResource(cfg.Telemetry.ResourceAttributes, buildinfo.Version)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	// Traces: the collector's headers are secrets, revealed here and nowhere else.
	tracesOpts := telemetry.TracesOptions{
		Endpoint:    cfg.Telemetry.OTLP.ParsedEndpoint,
		Timeout:     time.Duration(cfg.Telemetry.OTLP.Timeout),
		SampleRatio: cfg.Telemetry.Traces.SampleRatio,
		Resource:    resource,
		Logger:      logger,
	}
	if cfg.Telemetry.OTLP.ParsedEndpoint != nil {
		tracesOpts.Transport = guard.Transport(true)
		tracesOpts.Headers = make(map[string]string, len(cfg.Telemetry.OTLP.Headers))
		for name, v := range cfg.Telemetry.OTLP.Headers {
			tracesOpts.Headers[name] = v.Reveal()
		}
	}
	traces, err := telemetry.NewTraces(tracesOpts)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	writer, err := ledger.New(ledger.Options{
		DB: gatewayDB, Logger: logger, BufferSize: cfg.Ledger.BufferSize, BatchSize: cfg.Ledger.BatchSize,
		FlushInterval: time.Duration(cfg.Ledger.FlushInterval),
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	reconciler, err := limits.NewReconciler(limits.ReconcilerOptions{
		Store: memory, Ledger: limits.NewLedger(workerDB),
		Pending: writer.OldestPendingStart, Horizon: horizon,
		Interval: time.Duration(cfg.Limits.ReconcileInterval), Logger: logger,
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	unlimited := resolver.NewUnlimitedAuditor(gatewayDB, logger, nil)
	limitsPort, err := resolver.NewLimits(resolver.LimitsOptions{
		Rules: ruleCache, Store: memory, Logger: logger,
		TTL:            time.Duration(cfg.Limits.MaxRequestDuration) + time.Minute,
		Reconciler:     reconciler,
		Unlimited:      unlimited,
		OnUnknownPrice: cfg.Limits.OnUnknownPrice, OnStoreFailure: cfg.Limits.OnStoreFailure,
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	logger.Info("limits ready", "time_zone", cfg.Limits.TimeZone,
		"on_unknown_price", cfg.Limits.OnUnknownPrice, "on_store_failure", cfg.Limits.OnStoreFailure)
	partitioner := &ledger.Partitioner{DB: workerDB, Logger: logger}
	rollup := &reports.Rollup{DB: workerDB, Interval: time.Duration(cfg.Reports.RollupInterval), Logger: logger}
	rewrapper := &vault.Rewrapper{DB: workerDB, Provider: kp, Logger: logger}
	deleter := &lifecycle.Deleter{DB: workerDB, Interval: time.Duration(cfg.TenantDeletion.Interval), Logger: logger}
	retention := &reports.Retention{DB: workerDB, UsageMonths: cfg.Retention.UsageMonths, AuditMonths: cfg.Retention.AuditMonths, Logger: logger}
	// Ensure bounds itself. A failure is a warning: the keeper retries, and rows whose month has
	// no partition land in the default one.
	if err := ensurePartitions(ctx, partitioner); err != nil {
		partitioner.Warn(err)
	}
	// Metrics: the sources read the components' snapshots at each collection. The collector's
	// headers are secrets, revealed here and nowhere else.
	sources := telemetry.Sources{
		Ledger: ledgerSource(writer), LedgerLimit: cfg.Ledger.BufferSize,
		Egress: egressSource(guard),
		Authn: map[string]func() telemetry.AuthnStats{
			"inference": authnSource(gatewayAuth), "admin": authnSource(adminAuth),
		},
		SignIn:      signInSource(people.accounts, adminHandler),
		Sessions:    people.sessionsOpen,
		Hashes:      hashSource(people.hasher),
		Idempotency: idempotencySource(people.idempotency),
		Admin:       adminSource(adminHandler),
		Limits:      limitsSource(memory, reconciler, events, ruleCache),
		Resolver:    resolverSource(limitsPort, unlimited),
		Reports:     reportsSource(rollup, retention, adminHandler),
	}
	if keyCache != nil {
		sources.JWKS = jwksSource(keyCache)
	}
	metricsOpts := telemetry.MetricsOptions{
		Resource:       resource,
		Endpoint:       cfg.Telemetry.OTLP.ParsedEndpoint,
		OTLP:           cfg.Telemetry.Metrics.OTLP,
		Timeout:        time.Duration(cfg.Telemetry.OTLP.Timeout),
		Interval:       time.Duration(cfg.Telemetry.Metrics.ExportInterval),
		Prometheus:     cfg.Telemetry.Metrics.Prometheus,
		ServiceVersion: buildinfo.Version,
		Logger:         logger,
		Sources:        sources,
	}
	if cfg.Telemetry.OTLP.ParsedEndpoint != nil {
		metricsOpts.Transport = guard.Transport(true)
		metricsOpts.Headers = make(map[string]string, len(cfg.Telemetry.OTLP.Headers))
		for name, v := range cfg.Telemetry.OTLP.Headers {
			metricsOpts.Headers[name] = v.Reveal()
		}
	}
	metrics, err := telemetry.NewMetrics(metricsOpts)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	srv, err := server.New(server.Options{
		Config: cfg, Logger: logger, Authenticator: gatewayAuth, Admin: adminHandler,
		Catalogue:   resolver.NewCatalogue(catalogCache, logger),
		Credentials: resolver.NewCredentials(opener),
		Failures:    resolver.NewFailureAuditor(gatewayDB, logger, nil),
		Egress:      guard,
		Recorder:    writer,
		Tracer:      traces.Provider(),
		Limits:      limitsPort,
		Metrics:     gatewayMetrics{metrics}, MetricsHandler: metrics.Handler(), MetricsAuth: adminAuth,
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	for _, db := range dbs {
		srv.Health().AddCheck("database-"+string(db.Role()), db.Ready)
	}
	srv.Health().AddCheck("ledger", writer.Ready)
	if cfg.Pricing.ImportAtStart {
		ictx, icancel := context.WithTimeout(ctx, 60*time.Second)
		catalog.ImportAtStart(ictx, workerDB, logger)
		icancel()
	}
	if err := srv.Listen(); err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}

	warnNoAdministrator(ctx, logger, cfg, adminDB)
	background, stopBackground := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { stopBackground(); wg.Wait() }()
	wg.Add(10)
	go func() { defer wg.Done(); people.runPurge(background, logger) }()
	go func() { defer wg.Done(); listener.Run(background) }()
	go func() { defer wg.Done(); warnBootstrap(background, logger, adminDB) }()
	go func() { defer wg.Done(); partitioner.Run(background) }()
	go func() { defer wg.Done(); rollup.Run(background) }()
	go func() { defer wg.Done(); retention.Run(background) }()
	go func() { defer wg.Done(); rewrapper.Run(background) }()
	logger.Info("tenant deletion ready", "grace_period", time.Duration(cfg.TenantDeletion.GracePeriod).String(), "interval", time.Duration(cfg.TenantDeletion.Interval).String())
	go func() { defer wg.Done(); deleter.Run(background) }()
	go func() { defer wg.Done(); memory.Run(background) }()
	go func() { defer wg.Done(); reconciler.Run(background) }()
	// The writer is not stopped by the signal: it runs until Close has returned.
	writerCtx, stopWriter := context.WithCancel(context.WithoutCancel(ctx))
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() { defer writerDone.Done(); writer.Run(writerCtx) }()
	defer func() { stopWriter(); writerDone.Wait() }()
	// The event writer likewise runs until the flush has stopped it, so that Close finds the queue.
	eventsCtx, stopEvents := context.WithCancel(context.WithoutCancel(ctx))
	var eventsDone sync.WaitGroup
	eventsDone.Add(1)
	go func() { defer eventsDone.Done(); events.Run(eventsCtx) }()
	defer func() { stopEvents(); eventsDone.Wait() }()
	// The unlimited-traffic recorder runs with the background context and is closed with the event writer.
	unlimitedCtx, stopUnlimited := context.WithCancel(context.WithoutCancel(ctx))
	var unlimitedDone sync.WaitGroup
	unlimitedDone.Add(1)
	go func() { defer unlimitedDone.Done(); unlimited.Run(unlimitedCtx) }()
	defer func() { stopUnlimited(); unlimitedDone.Wait() }()
	if cfg.Pricing.Refresh.Enabled {
		refresher := &catalog.Refresher{
			DB: workerDB, Logger: logger, URL: cfg.Pricing.Refresh.ParsedURL.String(),
			Interval: time.Duration(cfg.Pricing.Refresh.Interval), FirstDelay: refreshStartDelay(environ),
			Fetch: fetchPrices,
		}
		wg.Add(1)
		go func() { defer wg.Done(); refresher.Run(background) }()
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		stop() // a second signal terminates the process with the Go default behaviour
	}()
	serveErr := srv.Serve(sigCtx)

	// Every handler has recorded: write what is queued and flush the spans, together, within
	// ledger.shutdown_flush_timeout (OPS-12).
	flushDeadline := time.Now().Add(time.Duration(cfg.Ledger.ShutdownFlushTimeout))
	flushCtx, flushCancel := context.WithDeadline(context.WithoutCancel(ctx), flushDeadline)
	var unwritten int
	var flush sync.WaitGroup
	flush.Add(3)
	go func() { defer flush.Done(); unwritten = writer.Close(flushCtx) }()
	go func() { defer flush.Done(); _ = traces.Shutdown(flushCtx) }()
	go func() { defer flush.Done(); _ = metrics.Shutdown(flushCtx) }()
	flush.Wait()
	// Then the limit events, with what is left of the flush deadline (at least one second).
	left := max(time.Until(flushDeadline), time.Second)
	eventsFlush, eventsCancel := context.WithTimeout(context.WithoutCancel(ctx), left)
	stopEvents()
	eventsDone.Wait()
	if n := events.Close(eventsFlush); n > 0 {
		logger.Warn("limit events not recorded", "unwritten", n)
	}
	stopUnlimited()
	unlimitedDone.Wait()
	unlimited.Close(eventsFlush)
	eventsCancel()
	flushCancel()
	logger.Info("ledger flushed", "unwritten", unwritten)

	if serveErr != nil {
		logger.Error("startup failed", "error", fmt.Sprint(serveErr))
		return 1
	}
	return 0
}

// refreshStartDelay is the wait before the first price refresh: one minute, or the value of the
// test-only variable PANDOKOS_TEST_REFRESH_DELAY (a Go duration). The variable is not a
// configuration key; it shortens that one wait and nothing else.
func refreshStartDelay(environ []string) time.Duration {
	for i := len(environ) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(environ[i], "PANDOKOS_TEST_REFRESH_DELAY="); ok {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				return d
			}
			break
		}
	}
	return time.Minute
}
