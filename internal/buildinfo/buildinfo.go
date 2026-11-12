// Package buildinfo holds the version and commit set at link time.
package buildinfo

import (
	"fmt"
	"runtime"
)

// Set with -ldflags "-X github.com/pandokos/pandokos/internal/buildinfo.Version=... -X ...Commit=...".
var (
	// Version is the release version, "dev" for a local build.
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "unknown"
)

// String returns "pandokos <Version> (commit <Commit>, <runtime.Version()>, <GOOS>/<GOARCH>)".
func String() string {
	return fmt.Sprintf("pandokos %s (commit %s, %s, %s/%s)", Version, Commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
