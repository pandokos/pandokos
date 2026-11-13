package main

import (
	"context"
	"io"

	"github.com/pandokos/pandokos/internal/cli"
)

// runClient runs an API command: a client of the administration API.
func runClient(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	return cli.Run(ctx, args, stdout, stderr, environ)
}
