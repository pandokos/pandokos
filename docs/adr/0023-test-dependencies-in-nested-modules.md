# 0023. Test-only dependencies live in nested modules

- Status: proposed
- Date: 2026-11-12

## Context

GW-02 and the release acceptance require tests with the official provider SDKs. The OpenAI Go SDK
brings five transitive modules and, at the version current when this was written, requires a newer
Go than the product's floor. [ADR 0018](0018-supply-chain.md) limits the product's dependencies to
what the product needs; a `require` in the product's `go.mod` enters its `go.sum`, its dependency
review, its vulnerability scope and source-based SBOMs, even if only tests import it.

## Decision

Tests that need a third-party module the product does not need live in a nested Go module under
`test/` (first: `test/sdk`), with its own `go.mod` and `go.sum`, linked to the product by a
`replace` directive to the repository root. The nested module may import the product's `internal`
packages, since its path is inside the product's. The product module's direct dependencies stay on
an allow-list checked by a test. The nested module may require a newer Go than the product. CI runs
it as a separate job and runs `govulncheck` on it; it is never part of a release artefact. No
`go.work` file is committed.

## Consequences

`go test ./...` at the root does not run the SDK suite: `make test-sdk` and the CI job do. There
are two `go.sum` files to keep current (Dependabot covers both). Refactoring an internal package
can break the nested module without the root build noticing; the CI job catches it.

## Alternatives considered

- Requiring the SDK in the product module: rejected, it pollutes the graph and raises the Go floor.
- Calling the SDK from another language's test suite: rejected, it needs a second toolchain in CI
  for no gain now; it may return for the Python SDKs in sprint 3.3 under the same rule.
- Hand-written HTTP requests instead of the SDK: rejected, it does not prove GW-02.
