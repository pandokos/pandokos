# 0019. Configuration: static settings in a file, dynamic state in the database

- Status: accepted
- Date: 2026-11-11

## Context

Some settings describe the installation and are needed before anything else works (listeners,
database, keys). Others describe tenants and policy and change while the system runs. Mixing the
two leads either to restarts for routine changes or to a database that must be reachable before
the process knows how to reach it (OPS-08).

## Decision

**Static configuration** describes the process and is read once at start:

- listeners and TLS; roles to run; database connections per role; counter store; key-encryption
  key provider; telemetry; limits on request size and duration; cache lifetimes; the pinned
  tenant of a silo cell.
- Sources, in order of precedence: environment variables (`PANDOKOS_…`), then one YAML file.
- Secrets are never values in the file. They are given as paths to files
  (`database.password_file`, `kek.file`), which fits Docker and Kubernetes secrets.
- At start, the configuration is validated as a whole. Pandokos refuses to start on an unsafe
  combination (for example, no key-encryption key, or an admin listener on a public address
  without TLS unless explicitly acknowledged) and logs warnings for weak ones (SEC-04).
  `pandokos config check` runs the same validation without starting.

**Dynamic state** lives in the database and is changed only through the administration API:
tenants, identities, roles, issuers, catalogue, prices, credentials, limits, funding modes, event
destinations.

**Declarative management.** `pandokos export` writes the dynamic state of the platform or of one
tenant, without secrets, to a YAML document; `pandokos apply` reconciles the installation with
such a document through the API. This gives repeatable, reviewable configuration without making
files a second source of truth.

## Consequences

- Routine administration never needs a restart, and instances pick up changes through cache
  invalidation.
- The process cannot start without its file or environment, and cannot be misconfigured into an
  unsafe state silently.
- `apply` must be idempotent and must define how it treats resources absent from the document
  (by default it leaves them alone and reports them).

## Alternatives considered

**Everything in a file**, as many single-organization gateways do. Tenants and keys in a file do
not work for a multi-tenant product administered by its tenants.

**Everything in the database.** The bootstrap problem, and no safe place for the settings that
protect the database itself.
