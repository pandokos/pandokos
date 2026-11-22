package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
)

const userUsage = "usage: pandokos user reset-password USERNAME [--config FILE] [--password-out FILE]\n" +
	"       pandokos user unlock USERNAME [--config FILE]\n"

// runUser is the host commands on users (sprint 4.2, section 5.13): reset-password and unlock work
// through the database with the admin role, like init, and are audited with the actor host.
func runUser(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) == 0 || (args[0] != "reset-password" && args[0] != "unlock") {
		fmt.Fprint(stderr, userUsage)
		return 2
	}
	command := args[0]
	fs := flag.NewFlagSet("user "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "configuration file")
	passwordOut := new(string)
	if command == "reset-password" {
		passwordOut = fs.String("password-out", "", "write the new password to this new file (mode 0600) instead of printing it")
	}
	// Flags may come before and after the username.
	var names []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		names = append(names, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(names) != 1 {
		fmt.Fprint(stderr, userUsage)
		return 2
	}
	username := names[0]

	path := *file
	if path == "" {
		path = config.FileFromEnviron(environ)
	}
	cfg, warnings, err := config.Load(config.Source{File: path, Environ: environ, Purpose: config.PurposeInit})
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			for _, p := range ve.Problems {
				fmt.Fprintf(stderr, "error: %s: %s\n", p.Key, p.Message)
			}
		} else {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return 1
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "warning: %s: %s\n", w.Key, w.Message)
	}
	ms, err := migrations.Load()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(store.Options{Conn: connOptions(cfg, cfg.Database.Admin.User, cfg.Database.Admin.Password), Role: store.RoleAdmin, MaxConns: 2, Migrations: ms})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	cctx, cancel := context.WithTimeout(sigCtx, 30*time.Second)
	err = db.Check(cctx)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	p, err := newPeople(cfg, db)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if command == "unlock" {
		if err := p.accounts.HostUnlock(sigCtx, username); err != nil {
			userFailure(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "unlocked")
		return 0
	}
	written := false
	pw, err := p.accounts.HostReset(sigCtx, username, func(pw secret.Secret) error {
		if *passwordOut == "" {
			return nil
		}
		if err := createSecretFile(*passwordOut, "password", pw.Reveal()); err != nil {
			return err
		}
		written = true
		return nil
	})
	code := 0
	if errors.Is(err, account.ErrAuditIncomplete) {
		fmt.Fprintf(stderr, "error: %v\n", err) // the password is changed: it is shown all the same
		code, err = 1, nil
	}
	if err != nil {
		if written {
			_ = os.Remove(*passwordOut)
		}
		userFailure(stderr, err)
		return 1
	}
	if *passwordOut != "" {
		fmt.Fprintf(stdout, "password written to %s\n", *passwordOut)
	} else {
		fmt.Fprintf(stdout, "password: %s\n", pw.Reveal()) // shown once, here
	}
	fmt.Fprintln(stderr, "note: the password must be changed at the first sign-in and stops working when auth.local.generated_password_ttl has passed")
	return code
}

// userFailure reports a failed host command; an unknown username says no more than that.
func userFailure(stderr io.Writer, err error) {
	if errors.Is(err, account.ErrNoUser) {
		fmt.Fprintln(stderr, "error: no such account")
		return
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
}
