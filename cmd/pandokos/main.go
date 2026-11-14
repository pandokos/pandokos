// Command pandokos is the Pandokos server and its command line.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pandokos/pandokos/internal/buildinfo"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
}

// run is the whole program, so that it can be tested. It returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr, environ)
	case "migrate":
		return runMigrate(ctx, args[1:], stdout, stderr, environ)
	case "init":
		return runInit(ctx, args[1:], stdout, stderr, environ)
	case "whoami", "tenant", "service-account", "key", "platform-key", "credential", "tenant-credential":
		return runClient(ctx, args, stdout, stderr, environ)
	case "kek":
		return runKEK(ctx, args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr, environ)
	case "version":
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "pandokos: unknown command %q\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}
