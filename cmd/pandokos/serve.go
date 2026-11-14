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
	"sync"
	"syscall"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/config"
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

	gatewayDB, adminDB := dbs[0], dbs[1]
	kp, err := vault.NewLocalProvider(cfg.Vault.KEK.Local.Key)
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	vctx, vcancel := context.WithTimeout(ctx, 30*time.Second)
	kekVersion, err := vault.Check(vctx, adminDB, kp)
	vcancel()
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	logger.Info("vault ready", "kek_provider", kp.Name(), "kek_version", kekVersion)
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
	gatewayAuth, err := authn.New(authn.Options{DB: gatewayDB, Logger: logger, Kinds: []authn.Kind{authn.KindTenant}, CacheTTL: time.Duration(cfg.Cache.PrincipalTTL)})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	adminAuth, err := authn.New(authn.Options{DB: adminDB, Logger: logger, Kinds: []authn.Kind{authn.KindTenant, authn.KindPlatform}})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	listenConn := connOptions(cfg, cfg.Database.Gateway.User, cfg.Database.Gateway.Password)
	listenConn.Host, listenConn.Port = cfg.Database.DirectHost, cfg.Database.DirectPort
	listener, err := store.NewListener(store.ListenerOptions{
		Conn: listenConn, Role: store.RoleGateway, Logger: logger, Direct: cfg.Database.DirectAddress != "",
		OnNotify: func(p string) { gatewayAuth.Invalidate(p); opener.Invalidate(p) },
		OnReset:  func() { gatewayAuth.Flush(); opener.Flush() },
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	adminHandler, err := admin.New(admin.Options{
		Logger: logger, DB: adminDB, Authenticator: adminAuth, Sealer: sealer,
		Validators: map[string]vault.Validator{"openai": vault.NewOpenAIValidator(cfg.Skeleton.OpenAI.ParsedBaseURL, 10*time.Second)},
	})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	srv, err := server.New(server.Options{Config: cfg, Logger: logger, Authenticator: gatewayAuth, Admin: adminHandler})
	if err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}
	for _, db := range dbs {
		srv.Health().AddCheck("database-"+string(db.Role()), db.Ready)
	}
	if err := srv.Listen(); err != nil {
		logger.Error("startup failed", "error", err.Error())
		return 1
	}

	background, stopBackground := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { stopBackground(); wg.Wait() }()
	wg.Add(2)
	go func() { defer wg.Done(); listener.Run(background) }()
	go func() { defer wg.Done(); warnBootstrap(background, logger, adminDB) }()

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		stop() // a second signal terminates the process with the Go default behaviour
	}()
	if err := srv.Serve(sigCtx); err != nil {
		logger.Error("startup failed", "error", fmt.Sprint(err))
		return 1
	}
	return 0
}
