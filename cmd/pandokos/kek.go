package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/vault"
)

// runKEK runs the kek command group: generate and check.
func runKEK(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) > 0 && args[0] == "check" {
		return runKEKCheck(ctx, args[1:], stdout, stderr, environ)
	}
	if len(args) == 0 || args[0] != "generate" {
		fmt.Fprintln(stderr, "pandokos: kek needs a sub-command: generate or check")
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("kek generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write the key to this new file (mode 0600)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 || *out == "" {
		fmt.Fprintln(stderr, "pandokos: kek generate takes --out FILE and no argument")
		fmt.Fprint(stderr, usage)
		return 2
	}
	key, err := vault.GenerateLocalKey()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	p, err := vault.NewLocalProvider(key)
	if err != nil {
		fmt.Fprintln(stderr, "error: the generated key was refused by the local provider")
		return 1
	}
	version, _, err := p.Wrap(ctx, make([]byte, 32), nil)
	if err != nil {
		fmt.Fprintln(stderr, "error: the generated key cannot be used")
		return 1
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is the operator's choice
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot create the key file: %v\n", err)
		return 1
	}
	_, werr := io.WriteString(f, key.Reveal()+"\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(*out)
		fmt.Fprintln(stderr, "error: cannot write the key file")
		return 1
	}
	fmt.Fprintf(stdout, "key-encryption key written to %s; version %s\n", *out, version)
	return 0
}

// runKEKCheck is pandokos kek check (sprint 12.1, section 5.12): it loads the configuration as
// serve does, authenticates to the key service and runs the probe of vault.Check that needs no
// database. It writes nothing anywhere and prints no secret.
func runKEKCheck(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("kek check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "pandokos: kek check takes no argument")
		fmt.Fprint(stderr, usage)
		return 2
	}
	path := *file
	if path == "" {
		path = config.FileFromEnviron(environ)
	}
	cfg, _, err := config.Load(config.Source{File: path, Environ: environ})
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
	logger := telemetry.NewLogger(io.Discard, "error")
	guard, err := newGuard(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer guard.CloseIdleConnections()
	providers, err := buildKeyProviders(cfg, guard, logger, nil)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	// The check never waits: a service that does not answer is the answer.
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if tp := providers.Transit; tp != nil {
		if err := tp.Start(cctx); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}
	version, err := vault.ProbeAll(cctx, providers.Provider)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "provider: %s\n", providers.Provider.Name())
	fmt.Fprintf(stdout, "version: %s\n", version)
	if tp := providers.Transit; tp != nil {
		info := tp.Info()
		fmt.Fprintf(stdout, "unwraps: v%d to v%d\n", info.Oldest, info.Newest)
		if len(info.Weak) > 0 {
			fmt.Fprintf(stdout, "weak: %s\n", strings.Join(info.Weak, ", "))
		}
	}
	return 0
}
