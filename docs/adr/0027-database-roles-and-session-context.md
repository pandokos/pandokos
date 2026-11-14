# 0027. Database roles, session context and pooler safety

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0004](0004-postgresql-and-rls.md); does not supersede it.

## Context

ADR 0004 fixes forced row-level security keyed on `app.tenant_id`, one database role per process
role and compatibility with transaction-mode poolers. Building it showed four things the ADR
leaves open: how role names relate to login users; that a transaction-local setting reads as an
empty string, not as absent, on a reused connection; that the tenant list itself needs
protection although it is global; and what "works behind a pooler" requires of the driver.

## Decision

1. Four privilege roles with fixed names and no login, created by an operator script. Database
   users are members of exactly one of them. `serve` verifies each of its users at start and
   refuses a superuser, `BYPASSRLS`, the owner role, a user of two roles or an owner of objects.
2. All objects live in schema `pandokos`, are owned by `pandokos_owner`, and every statement
   qualifies names; `search_path` is not used.
3. Policies read the tenant through `pandokos.current_tenant_id()`, which maps an empty setting
   to NULL and fails on a malformed one; every tenant-scoped table has exactly one policy of one
   canonical form.
4. A second transaction-local setting, `app.scope = 'platform'`, read through
   `pandokos.platform_scope()`, marks platform sessions; global tables that describe tenants
   have policies in terms of it, granted per role. A transaction without tenant and without scope
   sees no protected row.
5. Pandokos keeps no session state on database connections: transaction-local settings, no
   prepared statements (`QueryExecModeExec`), transaction-level locks, one start-up parameter.
   The behaviour is the same with and without a pooler; there is no switch.
6. Functions in the schema are not executable by `PUBLIC`; `SECURITY DEFINER` functions and views
   are allow-listed in the schema lint.

## Consequences

Users and passwords rotate without migrations. Statements are parsed on every execution; a
statement cache for direct connections can be added later. Parameters travel in text form: SQL
should cast explicitly (`$1::uuid`). The application, not the database, names the tenant: the
model defends against missing filters, not against arbitrary SQL on a runtime connection. Role
names are cluster-wide: one cluster per installation, or care with `CONNECT`. `LISTEN` needs a
direct connection.

## Alternatives considered

- Configurable role names: migrations become templates.
- Roles created by a migration: the owner needs `CREATEROLE`, and concurrent databases race.
- The raw `current_setting(...)::uuid` in each policy: errors on reused connections.
- A tenant list without policies: any role in any session could list tenants.
- A configuration switch for pooler mode: two behaviours to test, one more way to misconfigure.
- Session-level `SET` with a reset query: unsafe behind transaction pooling.

## History

- 2026-11: sprint 1.3 adds the transaction with neither setting (Unscoped), the provisioning transaction with both (ADR 0031), and one LISTEN connection per gateway (ADR 0030).
- 2026-11: sprint 2.1 adds lint rule L-19 (views run with the caller's rights) and the first view.
- 2026-11: sprint 2.2 adds restricted global catalogue tables read in any session, and insert grants for the worker on prices and the platform audit log ([ADR 0035](0035-catalogue-and-model-access.md)).
- 2026-11: sprint 3.1 adds the definer function record_auth_failure, executable by the gateway role only; the role still holds no privilege on the audit tables.
- 2026-11: sprint 3.2 adds the definer function ensure_usage_partitions, executable by the worker role only, and lint rule L-20 for partitions.
