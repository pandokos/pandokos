# 0026. Schema migrations: in-house, forward-only, exact match at start

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0004](0004-postgresql-and-rls.md); does not supersede it.

## Context

ADR 0004 says migrations are SQL files embedded in the binary and applied by `pandokos migrate`;
requirement OPS-09 adds that an instance refuses to start against a schema it does not match. The
migrator runs with the only credentials that can change the schema, must put every object under
one owner whatever user logs in, and must work when the only path to the database is a
transaction-mode connection pooler.

## Decision

1. Pandokos has its own migrator, not a library.
2. Migrations are numbered SQL files, forward-only, each applied in one transaction that also
   writes a ledger row with the file's SHA-256.
3. Inside that transaction the migrator switches to `pandokos_owner` with `SET LOCAL ROLE` and
   serializes with a transaction-level advisory lock; it uses no session state.
4. A merged migration is immutable. A checksum file in the repository makes an edit visible in
   review, and a ledger row that differs from the binary stops both `migrate` and `serve`.
5. `serve` starts only when the ledger equals the migrations of the binary, and turns not-ready
   if that stops being true.
6. Statements that cannot run in a transaction (`CREATE INDEX CONCURRENTLY`, explicit
   `COMMIT`) are not supported until a design exists for them.

## Consequences

A wrong migration is fixed by a new one or by a restore. Upgrades are "stop, migrate, start":
rolling upgrades need a compatibility window that this ADR does not provide (OPS-13). The
migrator is about two hundred lines that the project owns and tests. Data migrations must cope
with forced row-level security, which hides rows from the owner too.

## Alternatives considered

- A migration library (golang-migrate, goose, tern): a second driver abstraction, session locks,
  down migrations and a dependency in the most privileged code path, for a need this small.
- Down migrations: untested code that runs with owner rights on the worst day.
- Accepting a newer schema at start: needs rules for writing backward-compatible migrations
  first.
- Applying migrations at `serve` start: gives every instance owner credentials, against
  [ADR 0003](0003-single-binary-with-roles.md).
