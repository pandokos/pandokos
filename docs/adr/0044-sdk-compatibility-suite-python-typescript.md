# 0044. The SDK compatibility suite in Python and TypeScript

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0018](0018-supply-chain.md) and [ADR 0023](0023-test-dependencies-in-nested-modules.md).

## Context

GW-02 promises that stock SDKs work. Most callers use the Python and TypeScript SDKs of OpenAI and
Anthropic, which a Go test cannot import. Running them brings two package ecosystems into CI.

## Decision

- The suites live in `test/sdk/python` and `test/sdk/ts` and are run as subprocesses by Go tests of the
  `test/sdk` module behind the build tag `sdkexternal`, against the in-process server and the fake
  provider, with a loopback control server for assertions. Without the tag the module's tests are Go only.
- Dependencies are exact pins, locked with hashes (`requirements.txt` from `pip-compile
  --generate-hashes`; `package-lock.json`), installed with `--require-hashes --no-deps --only-binary`
  and `npm ci --ignore-scripts`, updated by Dependabot, used by CI only and never shipped.
- Tests are plain Python and ES modules; no TypeScript compiler.
- Network is needed only to install packages; the tests reach nothing but loopback.

## Consequences

- Each SDK update runs against Pandokos before it is merged.
- Two more CI jobs and two lock files to review.

## Alternatives considered

**Go SDKs only.** Misses the clients most callers use and their header behaviour (the Anthropic SDKs'
switch to Bearer was found this way).
**A container per language.** More to maintain than a pinned interpreter on the runner.
