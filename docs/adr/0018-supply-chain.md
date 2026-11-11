# 0018. Supply chain: few dependencies, minimal image, signed releases

- Status: proposed
- Date: 2026-11-11

## Context

A compromised dependency or build would expose every credential the proxy handles. This risk is
one of the reasons for the language choice ([ADR 0002](0002-implementation-language-go.md)) and
needs rules of its own (SEC-06).

## Decision

**Dependencies.**

- The standard library first. A third-party module is added only when writing the equivalent
  would be riskier than depending on it (database driver, OIDC and JWT verification,
  OpenTelemetry, Valkey client, YAML).
- Every new direct dependency is justified in the pull request that adds it.
- Versions are pinned with checksums (`go.sum`); builds use the module proxy and checksum
  database; updates arrive as reviewed pull requests from an automated tool.
- `govulncheck` and a linter set run on every pull request; the build fails on a reachable known
  vulnerability.

**Build and image.**

- A static binary built with CGO disabled and reproducible flags.
- A distroless base image: no shell, no package manager; runs as a non-root user with a read-only
  root file system.
- Multi-architecture images (amd64, arm64).
- The image is scanned in CI.

**Release.**

- Releases are built only by the CI pipeline from a tagged commit.
- Each release publishes an SBOM, signs images and binaries with keyless signatures, and attaches
  build provenance. Verification instructions are part of the installation guide.
- GitHub Actions are pinned to commit hashes, and workflow permissions are minimal.

**Repository.** Protected main branch, required reviews and status checks, secret scanning with
the `pdk_` key pattern registered, private vulnerability reporting.

## Consequences

- Some conveniences are written by hand instead of imported (routing, configuration loading, the
  CLI).
- Releases cannot be cut from a laptop.
- Operators can verify what they run.

## Alternatives considered

**A full-featured base image.** Easier debugging, larger attack surface. A separate debug image
can be offered without changing the default.

**Vendoring dependencies.** Checksums and the module proxy give the same integrity guarantee with
less noise in reviews.
