package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pandokos/pandokos/internal/vault"
)

// runKEK runs the kek command group; the only sub-command is generate.
func runKEK(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "generate" {
		fmt.Fprintln(stderr, "pandokos: kek needs a sub-command: generate")
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
