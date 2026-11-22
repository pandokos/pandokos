# 0018. Supply chain: few dependencies, minimal image, signed releases

- Status: accepted
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

**Repository.** Protected main branch, required reviews and status checks, GitHub secret scanning
with its default patterns, a gitleaks scan of the whole history in CI with the `pdk_` key rule
(`make secrets`, `.gitleaks.toml`), private vulnerability reporting.

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

## History

- 2026-11: sprint 3.2 adds the OpenTelemetry SDK, the OTLP/HTTP exporter and logr as direct dependencies ([ADR 0040](0040-traces.md)).
- 2026-11: sprint 3.3 adds hash-locked Python and npm packages for the SDK suite, CI only ([ADR 0044](0044-sdk-compatibility-suite-python-typescript.md)).
- 2026-11: sprint 4.1 adds `github.com/go-jose/go-jose/v4` as the one new direct dependency, for compact JWS and JWK, imported by `internal/authn`, `internal/jwks` and `test/fakeidp` only and only its root package ([ADR 0046](0046-issuer-key-retrieval-and-caching.md)); the identity-provider images of the tests are pinned by digest ([ADR 0047](0047-identity-providers-in-the-test-suite.md)).
- 2026-11-22: sprint 4.2 adds `golang.org/x/crypto` as the one new direct dependency, for `argon2`, imported by `internal/password` only; no OpenID Connect or OAuth library is added ([ADR 0057](0057-local-accounts-and-sessions-formats-and-numbers.md), [ADR 0058](0058-openid-connect-relying-party.md)).
- 2026-11-22: sprint 6.1 adds `go.opentelemetry.io/otel/sdk/metric` and `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp` (v1.47.0) as direct dependencies, and writes the Prometheus format instead of adding a Prometheus client ([ADR 0062](0062-metrics-instruments-attributes-cardinality-and-scrape.md)).
