---
id: EPIC-1
title: Foundations
status: done
release: v0.1
depends_on: []
---

# Epic 1 — Foundations

## Outcome

A running, signed container with a database whose tenant isolation is proven by tests, in which a
platform admin creates tenants and a tenant admin creates service accounts and virtual keys, every
change being audited. Nothing is proxied through the full pipeline yet, but the three assumptions
the product rests on have been tested: the proxy primitives, the isolation model and the build
pipeline.

## Why first

Everything else writes tenant data and ships through this pipeline. The isolation harness and the
canary tests must exist before the first table that holds something valuable.

## Decisions it implements

ADR 0002, 0003, 0004, 0005 (virtual keys), 0007 (core), 0013 (audit), 0017, 0018, 0019.

## Sprints

### 1.1 Walking skeleton

The thinnest end-to-end slice, built to find out early whether the proxy design holds.

- Repository layout, Go module, lint, tests, `govulncheck` in CI.
- `pandokos serve` with the inference listener; one hard-wired OpenAI route that forwards a
  request with a provider key read from a file, streaming, with cancellation on client disconnect.
- A first usage reader for the OpenAI chat protocol, streaming and non-streaming, tested against
  recorded responses; usage is written to the log.
- Static configuration: file, environment, secret files, validation (`config check`).
- Health endpoints, graceful shutdown.
- Container image: distroless, non-root, multi-architecture; image scan; SBOM; signed image and
  provenance on a tag.
- A test with the official OpenAI SDK pointed at the skeleton.

Requirements: SEC-06, SEC-07, OPS-01, OPS-08 (static part), OBS-08, GW-03 and GW-04 (first
evidence).

Exit: the SDK test passes with streaming; cancelling the client closes the upstream connection;
a tagged build yields a verifiable signed image.

### 1.2 Persistence and tenant isolation

- `pandokos migrate`; embedded SQL migrations; start-up schema check.
- Database roles: owner, gateway, admin, worker; the transaction helper that sets the tenant.
- `tenant` table and lifecycle states; first tenant-scoped tables.
- Isolation harness: schema lint, SQL-level negative tests per role, running against PostgreSQL in
  CI.
- Tested behind a transaction-mode connection pooler.

Requirements: TEN-01, TEN-03, TEN-04, TEN-05, TEN-06, TEN-07, TEN-09 (model), OPS-09.

Exit: a table without a policy fails the build; with tenant A's session or with none, every
runtime role is shown unable to read or change tenant B's rows.

### 1.3 Service accounts, virtual keys, authorization core, audit

- OpenAPI document for the first resources; generated server interfaces and client; the admin
  listener, separate from the inference listener.
- `pandokos init`: first platform admin credential, expiring.
- Tenants: create, suspend, reactivate. Service accounts. Virtual keys: format, hashing, lookup
  function, scopes, expiry, several active keys, revocation.
- Principal and permission catalogue; built-in roles `platform-admin`, `tenant-admin`,
  `inference`; every route declares a permission.
- Principal cache with invalidation by notification; documented revocation bound, tested.
- Audit records written in the same transaction as each change.
- CLI commands for all of the above.
- API-level isolation tests: a resource of another tenant is indistinguishable from a missing one.

Requirements: TEN-02, TEN-08, TEN-09, TEN-10, IAM-01 to IAM-05, IAM-13, IAM-14, AZ-01, AZ-02,
AZ-03 (first roles), AZ-06 and AZ-07 (platform admin), AUD-01 to AUD-03, AUD-04 (append-only),
AUD-07, API-01 to API-04, API-06, SEC-01 (native TLS on listeners, from sprint 1.1 O-3), SEC-03,
SEC-04.

Exit: from an empty database, the CLI creates two tenants, each with a service account and a key;
a key of one tenant cannot address the other; a revoked key stops working within the bound; each
step appears in the audit log.

## Risks

- The standard reverse proxy may need more control than expected for streams and cancellation.
  Sprint 1.1 exists to find out.
- Three database roles in one process add configuration. If it proves confusing in the
  single-container setup, the Compose reference must hide it behind sensible defaults.
