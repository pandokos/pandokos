# 0002. Implementation language: Go

- Status: proposed
- Date: 2026-11-11

## Context

Pandokos is a network proxy that holds the provider credentials of every tenant and sits on the
path of every model call. The candidates are Python and Go. C++ is excluded: manual memory
management is the wrong risk for a process that keeps secrets in memory.

What the language has to serve, in order of weight:

1. **A small trusted code base.** A compromised dependency in this process exposes every tenant's
   keys. Supply-chain attacks on package registries are a recurring event, and they have already
   hit a widely used LLM proxy written in Python.
2. **Many long-lived streaming connections**, each mostly idle, with cancellation that propagates
   cleanly from caller to provider.
3. **Simple self-hosting**: one artefact, small image, low memory, fast start.
4. **Proxy primitives** that are mature and maintained by someone else.
5. **Contributors and maintenance.**

The workload itself does not discriminate: it waits on the network, and Python's `asyncio`
handles that well at the request rates most installations will see. Raw throughput is not the
argument.

## Decision

Pandokos is written in Go, on the current stable release.

- The standard library covers the core of the product: HTTP/1.1 and HTTP/2 server and client, a
  streaming reverse proxy, TLS, AES-GCM, HKDF, SHA-256, structured logging. The Go cryptography
  module is FIPS 140-3 validated, which matters to some adopters. The expected set of direct
  third-party dependencies is about ten modules (database driver, OIDC and JWT, OpenTelemetry,
  Valkey client, YAML).
- The build produces one static binary, shipped in a distroless image with no shell, no package
  manager and no interpreter.
- Goroutines and `context` make "one connection, one flow of control, cancelled as a unit" the
  natural way to write the forwarder, including when a caller disconnects in mid-stream.
- CPU-bound work on the hot path (JSON field extraction, parsing server-sent events, hashing,
  decryption) runs in parallel on all cores without process pools.

## Consequences

- The maintainer's deepest experience is in Python. Go is deliberately a small language and the
  code base is conventional (no framework, no reflection-heavy libraries), which keeps it
  reviewable, but review will be slower at first than it would be in Python.
- The Python ecosystem for LLM tooling is richer. The piece that matters here is reading usage
  from provider responses and pricing it: the Python library that does both has a Go package
  that covers pricing but not, as documented, extraction. Extraction is therefore written in this
  project, one small reader per API family ([ADR 0010](0010-usage-extraction.md)), and prices
  are consumed as data ([ADR 0011](0011-pricing-source.md)).
- Nothing from an existing Python platform can be reused as a library. Pandokos is integrated
  over HTTP, which is the intended boundary anyway.
- Most projects in this category that target infrastructure use (rather than application
  embedding) are written in Go or Rust, so contributors with gateway experience are likely to
  know the language.

## Alternatives considered

**Python (FastAPI or Starlette, `httpx`, `asyncio`).** Fastest to start for the maintainer, and
adequate for the load. Rejected for this component because of the dependency tree (an
installation pulls in dozens of transitive packages, each able to read process memory), the larger
image and memory footprint, the need for several worker processes to use several cores with
separate caches and counters in each, and the subtlety of cancellation and back-pressure in a
streaming proxy built on an application framework.

**Rust.** Memory safety without a garbage collector and excellent proxy libraries. Rejected
because the same result is reachable in Go at a clearly lower cost in development time and in the
barrier to contribution, and because garbage-collection pauses are irrelevant next to
model latencies.

**Extending an existing gateway** (as a plugin or fork). Rejected because the multi-tenant
custody of customer keys is a property of the data model and of the trust boundaries, not a
feature that can be added at the edge of a single-organization design.
