# 0025. Go toolchain: language floor and release toolchain

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0002](0002-implementation-language-go.md) ("current stable release").

## Context

The Go project supports the two most recent minor releases. A binary built with an unsupported
release carries known standard-library vulnerabilities in exactly the packages this product is
made of (`net/http`, `crypto/tls`); `govulncheck` reports them and SEC-06 makes that a build
failure. Contributors and development environments may have an older toolchain installed.

## Decision

Two separate pins.

1. The `go` directive in `go.mod` is the language floor: the oldest Go that can compile the
   product. It is raised deliberately, by a pull request, when a newer language or library feature
   is wanted. `go.mod` has no `toolchain` line.
2. The *release toolchain* is named in `.go-version` and is always a supported Go release, normally
   the current stable one. The container build, the vulnerability gate and the SDK suite use it;
   the Dockerfile's builder image must match it (a test checks). CI runs the test suite with both
   the floor and the release toolchain. A security release of Go is adopted by a pull request that
   changes `.go-version` and the Dockerfile.

## Consequences

Behaviour differences between Go versions surface in CI. `make vuln` and the SDK suite need the
release toolchain locally; Go downloads it on demand (`GOTOOLCHAIN=go<version>`) where the network
allows. Dependabot updates the Dockerfile but not `.go-version`; the test that compares them makes
the pull request fail until both agree.

## Alternatives considered

- One pin at the floor: it cannot pass the vulnerability gate once the floor is out of support.
- One pin at the current release with a `toolchain` line: every developer command silently
  downloads a toolchain, and offline environments stop working.
- Raising the floor to the current release at once: correct in the long run, deferred because the
  baseline development environment has go1.24.7 (sprint 1.1, open point O-1).

## History

- 2026-11: the language floor is raised to Go 1.25.0, required by the PostgreSQL driver (sprint 1.2, decision D-1).
- 2026-11: the language floor is raised to Go 1.26.0: the OpenTelemetry exporter of sprint 3.2 brings `golang.org/x/net`, whose fixed release needs Go 1.26, and Go 1.25 is out of support (sprint 3.2, O-1).
