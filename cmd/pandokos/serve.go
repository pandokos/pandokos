package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/server"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
	"github.com/pandokos/pandokos/internal/telemetry"
)

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

	srv, err := server.New(cfg, logger)
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
