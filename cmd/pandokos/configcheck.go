package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/pandokos/pandokos/internal/config"
)

func runConfig(args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, "pandokos: config needs the sub-command check")
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "configuration file")
	purposeName := fs.String("for", "serve", "which command the configuration is for: serve, migrate or init")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	var purpose config.Purpose
	switch *purposeName {
	case "serve":
		purpose = config.PurposeServe
	case "migrate":
		purpose = config.PurposeMigrate
	case "init":
		purpose = config.PurposeInit
	default:
		fmt.Fprintln(stderr, "pandokos: --for must be serve, migrate or init")
		fmt.Fprint(stderr, usage)
		return 2
	}
	path := *file
	if path == "" {
		path = config.FileFromEnviron(environ)
	}
	_, warnings, err := config.Load(config.Source{File: path, Environ: environ, Purpose: purpose})
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
	fmt.Fprintln(stdout, "configuration OK")
	return 0
}
