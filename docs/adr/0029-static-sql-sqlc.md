# 0029. Static SQL: `sqlc`, text-mode rules, and a test against built SQL

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0004](0004-postgresql-and-rls.md) and [ADR 0027](0027-database-roles-and-session-context.md).

## Context

ADR 0004 prescribes `sqlc`; data model rule 3 forbids SQL built from strings; ADR 0027 fixes the driver mode (no prepared statements, parameters in text form).

## Decision

Every domain statement is written in a file under `internal/store/queries/sql/` and compiled by `sqlc` (version pinned in the Makefile, run without cgo with the release toolchain, never a requirement of `go.mod`) into the package `internal/store/queries`, under `internal/store` so that only the store imports the driver. Query files follow rules: every parameter is `sqlc.arg` with an explicit cast, every computed column has a cast and an alias, every object is schema-qualified, no `pgtype` in a signature, no `tenant_id` in a `WHERE` of a tenant-scoped table. The test T-R12 parses the code and fails on SQL that is not a literal or a constant of literals, and on any SQL call outside `internal/store`.

## Consequences

A parameter without a cast fails at run time, so every statement has a database test (T-230) directly and through the pooler; a list query with several orders is written with `CASE`. The `sqlc.yaml` override for `timestamptz` is named `timestamptz`.

## Alternatives considered

Hand-written query functions; a query builder (it builds strings).
