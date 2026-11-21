package main

import (
	"context"
	"fmt"
	"io"
)

// runUser is the host commands on users: reset-password and unlock (sprint 4.2). Part A registers
// them; step C6 gives them their behaviour.
func runUser(_ context.Context, args []string, _, stderr io.Writer, _ []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "reset-password", "unlock":
			fmt.Fprintf(stderr, "pandokos user %s: not implemented\n", args[0])
			return 1
		}
	}
	fmt.Fprintln(stderr, "usage: pandokos user reset-password|unlock USERNAME")
	return 2
}
