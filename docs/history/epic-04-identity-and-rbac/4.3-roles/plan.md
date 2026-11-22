# Sprint 4.3 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) and D-n, T-n, O-n, R-n refer to it.
- Parts: **A** scaffolding, **B** tests (failing), **C** implementation in nine steps, **D** CI,
  **E** documentation and records, **F** verification. Each part and each step of C is
  self-contained: its files, what it must make pass, and the commands that prove it. A different
  implementer can take each one, in order.
- One commit per part A, B, D, E, F and per step of C, with the message given.

## Rules for the implementer

1. Follow the design. A gap, or a statement of the "not verified" list of §12 that does not hold,
   is raised as a question; it is not filled silently.
2. Tests first: part B is committed with every new test failing (or not compiling against the
   stubs of part A only where a stub cannot exist); a step of C ends when its tests pass and no
   other test broke.
3. Never write a version, a digest or a checksum that was not read from its source in this run.
   This sprint adds no module, no image and no GitHub action; `migrations.sum` is produced by
   `make migrations-sum`.
4. No secret in code, tests, logs or fixtures. Keys, passwords and session tokens of tests are
   generated when the test runs.
5. Operational logs and spans name no tenant, role, custom role, permission list of a role,
   reason, support action, user, username or group (ADR 0055); T-889 and T-R58 check it. A reason
   is written to the audit log and to `support_action` and nowhere else.
6. Authorization is decided by permissions. Outside `internal/authz` no code compares a role name
   to decide anything; the places that name a role constant are listed in §5.11 (T-R61).
7. The golden file `internal/authz/testdata/roles.golden` is written in part A from §5.2 and is
   not regenerated from the code afterwards: when a test disagrees with it, the code is wrong
   unless the design says otherwise.
8. Every commit is signed off in the maintainer's name (`git commit -s`) and carries no other
   trailer: no co-author line, no tool or session attribution and no link to a working session,
   and no mention of an assistant or of a model, in commit messages, pull requests, comments and
   documents alike. Whoever delegates a part passes this rule on with it.
9. Generated code (`internal/adminapi`, `internal/store/queries`) and the generated section of
   `docs/reference/permissions.md` are produced by `make generate` and never edited.

## Environment

```
make db-up            # PostgreSQL 16 and PgBouncer for D and E tests
make idp-up           # Keycloak for the one X test (the realm of 4.2, unchanged)
cat .go-version       # the release toolchain used by make vuln and the tagged tests
```

## Part A — Scaffolding (commit: `chore: sprint 4.3 scaffolding: migration 0014, API, permissions, roles, configuration`)

After part A the schema, the document, the permissions, the roles and the configuration keys
exist, and `createTenant` still behaves as before. Handlers of the new operations return
`errors.New("not implemented")` (a 500), so that the router tests pass their shape checks. The
changes of behaviour that existing tests would notice (the first owner, the setting split, the
required reasons, the guard) are made in part C, each with its tests.

**A1. Migration.** `internal/store/migrations/sql/0014_roles_support.sql` is appendix A1,
verbatim. Then `make migrations-sum`. Verify:

```
go test ./internal/store/... -run 'Migrat|Schema' -count=1
```

**A2. Queries.** `internal/store/queries/sql/roles.sql` and `support.sql`, and the changes to
`virtual_keys.sql`, `issuers.sql`, `users.sql` and `audit.sql`, as appendix A2. `make generate`.
If `sqlc` refuses the lateral `unnest` of the `granted` aggregate, stop and ask (§12): the
fallback is a SQL function without `SECURITY DEFINER`, added to this migration, not a statement
built in Go.

**A3. OpenAPI and generator.** Apply appendix A3 to `api/openapi/admin.v1.yaml`; extend
`internal/apigen` and `internal/adminapi/runtime.go`:

| Construct | Generator and runtime |
|---|---|
| `x-pandokos-reason: required` on an operation | accepted only with a required request body whose schema is an object with a required string member `reason`; any other value, a missing body, a `GET` or a `DELETE` is rejected at generation; `Operation.ReasonRequired = true`; the generated decoder returns `&RequestError{Code: invalid_request, Fields: [{Field: "reason", Message: "must not be empty"}]}` when `strings.TrimSpace(body.Reason) == ""` |

In part A only the three new operations carry the construct (`openSupportAction`,
`deactivateRole`, `activateRole`); C7 adds it to the six existing ones with their tests. `make generate`; the generated diff is committed. Verify:
`go test ./internal/apigen/... ./internal/adminapi/... ./test/repo/... -run 'OpenAPI|Generat|Subset|Router' -count=1`.

**A4. Permissions, roles, principal.** `internal/authz`:

- `authz.go`: the eight permissions with the descriptions and scopes of §5.1; the attributes
  `InTenant` and `Support` on `Permission`, set as the table of §5.1; `Principal.Granted` and
  `Principal.SupportActionID`; `TenantPermissions`, `SupportPermissions`, `CheckCustom`.
  `Effective` and `Decide` are **not** changed yet (C1), so `Granted` has no effect.
- `roles.go`: `Role.Members`; the seven new constants; `Roles()` returns the ten roles of §5.2.
  `owner` is built from `TenantPermissions()`, `read-only` from the tenant permissions whose name
  ends in `:read`, `tenant-admin` from `TenantPermissions()` minus the unexported list
  `ownerOnly = []string{SupportActionEnd}`; the others are literal lists. `platform-admin` is
  every platform permission. Every role also gets `identity:read`.
- `grant.go`: `CanGrant` returning false (stub). `Assignable` stays until C4 removes it with its
  callers.
- `testdata/roles.golden`: ten lines, `role<TAB>scope<TAB>members<TAB>permissions separated by
  one space`, permissions sorted, in the order of the table of §5.2. Written by hand from §5.2.

`tenant-admin` and `platform-admin` gain their new permissions in this part (five and two), so
that the tenant administrator of every existing test can use the new operations; the 1.3 and 4.2
matrices are updated for exactly these seven additions.

**A5. Configuration.** `internal/config`: `Support{Enabled bool; MaxDuration Duration}` under
`support`, `keys.go` entries, defaults `true` and `1h`, the environment names of the existing
derivation (`PANDOKOS_SUPPORT_ENABLED`, `PANDOKOS_SUPPORT_MAX_DURATION`), validation 1 min to
24 h, the warning of §5.10 in `serve`. A row for each in `docs/operations/configuration.md`.

**A6. Stubs so that part B compiles.** `store.DB.ActInTenant` and `audit.RecordIntervention`
returning an error; `audit.Event.SupportActionID`; `admin.Options.Support` (`Enabled bool`,
`MaxDuration time.Duration`) wired from the configuration in `cmd/pandokos/serve.go`; the
handler methods of the sixteen operations in `internal/admin/{roles,support}.go`; the problem
codes `escalation_refused`, `support_disabled`, `role_in_use` in the document and their sentinel
errors `errEscalation`, `errSupportDisabled`, `errRoleInUse` mapped in `server.fail`; package
`internal/permdoc` with `func Generate(perms []authz.Permission, roles []authz.Role, ops []adminapi.Operation) []byte`
returning nil and a `main` that writes it between the markers (A8).

**A7. Isolation spec.** `test/isolation/spec.go`:

- `Global["support_action"] = {Reason: "Support actions of the platform inside tenants; protected by its own policies", Restricted: true}`.
- `Grants`: `tenant_role`: gateway `SELECT`; admin `SELECT`, `INSERT (tenant_id, name,
  description, permissions)`, `UPDATE (description, permissions, state)`, `DELETE`.
  `support_action`: admin `SELECT`, `INSERT` and `UPDATE` with the columns of appendix A1.
  `audit_event`: the admin role's insert columns gain `support_action_id`.
- `Fixtures["tenant_role"] = {Insert: "INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, 'fixture-' || substr(md5(random()::text), 1, 12), ARRAY['member:read']) RETURNING id::text", Update: "description = 'changed'"}`.
- No entry in `SecurityDefiner`.

**A8. Reference generator and Makefile.** `docs/reference/permissions.md` gets the two markers
`<!-- generated by internal/permdoc: begin -->` and `<!-- generated by internal/permdoc: end -->`
around its permission table and its role table (left as they are until C9). `Makefile`:
`generate` gains `go run ./internal/permdoc -out docs/reference/permissions.md` after the
`apigen` line; `GENERATED` gains `docs/reference/permissions.md`. With the stub, `permdoc`
leaves the file unchanged when `Generate` returns nil.

Verification of part A:

```
make generate-check fmt-check vet lint tidy
go test ./... -count=1        # with make db-up: everything that passed before passes
```

### Appendix A1 — `internal/store/migrations/sql/0014_roles_support.sql`

Applied and exercised while designing (§12). Verbatim:

```sql
-- 0014_roles_support: custom roles of a tenant, support actions of the platform inside a tenant,
-- the support action on audit rows, and the role owner for the identities that held every tenant
-- permission before it existed. Runs in one transaction as pandokos_owner. Every name is
-- schema-qualified. No function of this migration is SECURITY DEFINER.

-- ---------------------------------------------------------------------------------------------
-- Custom roles (tenant-scoped table). A row is a named set of catalogue permissions; the
-- catalogue and the built-in roles are in code (ADR 0007). role_assignment.role and
-- membership_role.role hold the name of a built-in role or of a row of this table.
CREATE TABLE pandokos.tenant_role (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    permissions text[]      NOT NULL,
    state       text        NOT NULL DEFAULT 'active',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_role_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT tenant_role_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_role_name_key UNIQUE (tenant_id, name),
    CONSTRAINT tenant_role_name_format CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    -- The names of the built-in roles, and two prefixes kept for roles that the product adds later.
    CONSTRAINT tenant_role_name_not_builtin CHECK (
        name NOT IN ('owner', 'tenant-admin', 'credential-manager', 'usage-viewer', 'auditor', 'developer',
                     'inference', 'read-only', 'platform-admin', 'platform-auditor')
        AND name !~ '^(platform|pandokos)-'),
    CONSTRAINT tenant_role_description_format CHECK (char_length(description) <= 500 AND description !~ '[[:cntrl:]]'),
    CONSTRAINT tenant_role_permissions_format CHECK (
        cardinality(permissions) BETWEEN 1 AND 64
        AND array_position(permissions, NULL) IS NULL
        AND array_to_string(permissions, ' ') ~ '^[a-z_]+:[a-z_]+( [a-z_]+:[a-z_]+)*$'),
    CONSTRAINT tenant_role_state_valid CHECK (state IN ('active', 'inactive'))
);

-- The name of a role never changes: assignments hold it. A role that is assigned to a member or
-- to a service account is not deleted (AZ-05). The handler checks first, with the row locked;
-- this trigger is the backstop.
CREATE FUNCTION pandokos.tenant_role_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.name <> OLD.name THEN
            RAISE EXCEPTION 'the name of a role is immutable' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM pandokos.membership_role r WHERE r.tenant_id = OLD.tenant_id AND r.role = OLD.name)
       OR EXISTS (SELECT 1 FROM pandokos.role_assignment r WHERE r.tenant_id = OLD.tenant_id AND r.role = OLD.name) THEN
        RAISE EXCEPTION 'the role is assigned' USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'tenant_role_in_use';
    END IF;
    RETURN OLD;
END
$$;

CREATE TRIGGER tenant_role_row_guard BEFORE INSERT OR UPDATE ON pandokos.tenant_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER tenant_role_guard BEFORE UPDATE OR DELETE ON pandokos.tenant_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_role_guard();
-- The gateways cache principals with the permissions of their roles (ADR 0030).
CREATE TRIGGER tenant_role_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.tenant_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('scoped');

CREATE INDEX role_assignment_role_idx ON pandokos.role_assignment (tenant_id, role);
CREATE INDEX membership_role_role_idx ON pandokos.membership_role (tenant_id, role);

ALTER TABLE pandokos.tenant_role ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_role FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.tenant_role
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway reads the permissions of the roles of a service account; it writes nothing.
GRANT SELECT ON pandokos.tenant_role TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, name, description, permissions) ON pandokos.tenant_role TO pandokos_admin;
GRANT UPDATE (description, permissions, state) ON pandokos.tenant_role TO pandokos_admin;
GRANT DELETE ON pandokos.tenant_role TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- Support actions (global table with explicit policies). A row says that a platform principal
-- may act inside one tenant with the listed permissions until it expires or is closed, and why.
-- The platform scope sees every row; a tenant's session sees and ends the rows of its tenant.
-- A row is never deleted here (tenant deletion removes it, sprint 6.3).
CREATE TABLE pandokos.support_action (
    id           uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    tenant_id    uuid        NOT NULL,
    actor_type   text        NOT NULL,
    actor_id     text        NOT NULL,
    actor_name   text        NOT NULL DEFAULT '',
    key_id       text        NOT NULL DEFAULT '',
    reason       text        NOT NULL,
    permissions  text[]      NOT NULL,
    request_id   text        NOT NULL DEFAULT '',
    opened_at    timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    closed_at    timestamptz,
    closed_by    text        NOT NULL DEFAULT '',
    closed_by_id text        NOT NULL DEFAULT '',
    close_reason text        NOT NULL DEFAULT '',
    CONSTRAINT support_action_pkey PRIMARY KEY (id),
    CONSTRAINT support_action_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT support_action_actor_type_valid CHECK (actor_type IN ('platform', 'user')),
    CONSTRAINT support_action_actor_id_format CHECK (actor_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    CONSTRAINT support_action_actor_name_format CHECK (char_length(actor_name) <= 200 AND actor_name !~ '[[:cntrl:]]'),
    CONSTRAINT support_action_key_id_format CHECK (key_id ~ '^[A-Za-z0-9_-]{0,64}$'),
    CONSTRAINT support_action_reason_format CHECK (char_length(reason) BETWEEN 1 AND 500 AND reason ~ '[^[:space:]]'),
    CONSTRAINT support_action_permissions_format CHECK (
        cardinality(permissions) BETWEEN 1 AND 64
        AND array_position(permissions, NULL) IS NULL
        AND array_to_string(permissions, ' ') ~ '^[a-z_]+:[a-z_]+( [a-z_]+:[a-z_]+)*$'),
    CONSTRAINT support_action_request_id_format CHECK (request_id ~ '^([0-9a-f]{32})?$'),
    CONSTRAINT support_action_lifetime CHECK (expires_at > opened_at AND expires_at <= opened_at + interval '24 hours'),
    CONSTRAINT support_action_closed_by_valid CHECK (closed_by IN ('', 'platform', 'tenant', 'expiry')),
    CONSTRAINT support_action_close_complete CHECK ((closed_at IS NULL) = (closed_by = '')),
    CONSTRAINT support_action_closed_by_id_format CHECK (closed_by_id ~ '^[A-Za-z0-9_-]{0,64}$'),
    CONSTRAINT support_action_close_reason_length CHECK (char_length(close_reason) <= 500)
);
-- One open action for an actor in a tenant: opening another closes the first.
CREATE UNIQUE INDEX support_action_open_key ON pandokos.support_action (tenant_id, actor_type, actor_id)
    WHERE closed_at IS NULL;
CREATE INDEX support_action_tenant_idx ON pandokos.support_action (tenant_id, opened_at DESC);
CREATE INDEX support_action_actor_idx ON pandokos.support_action (actor_type, actor_id) WHERE closed_at IS NULL;

-- The time of opening is the database's (the handler writes expires_at as now() plus the duration,
-- in the same transaction). A row changes once:
-- when it is closed, at the time of the database, or at its expiry when it is closed as expired.
CREATE FUNCTION pandokos.support_action_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.opened_at := now();
        IF NEW.closed_at IS NOT NULL THEN
            RAISE EXCEPTION 'a support action is opened open' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.closed_at IS NOT NULL THEN
        RAISE EXCEPTION 'a closed support action cannot be changed' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.actor_type <> OLD.actor_type OR NEW.actor_id <> OLD.actor_id
       OR NEW.actor_name <> OLD.actor_name OR NEW.key_id <> OLD.key_id OR NEW.reason <> OLD.reason
       OR NEW.permissions <> OLD.permissions OR NEW.request_id <> OLD.request_id
       OR NEW.opened_at <> OLD.opened_at OR NEW.expires_at <> OLD.expires_at THEN
        RAISE EXCEPTION 'a support action is immutable except for its closing' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.closed_by = 'expiry' AND now() < OLD.expires_at THEN
        RAISE EXCEPTION 'a support action that has not expired is not closed as expired' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.closed_at IS NOT NULL THEN
        NEW.closed_at := CASE WHEN NEW.closed_by = 'expiry' THEN OLD.expires_at ELSE LEAST(now(), OLD.expires_at) END;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER support_action_guard BEFORE INSERT OR UPDATE ON pandokos.support_action
    FOR EACH ROW EXECUTE FUNCTION pandokos.support_action_guard();

ALTER TABLE pandokos.support_action ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.support_action FORCE ROW LEVEL SECURITY;

CREATE POLICY support_action_platform ON pandokos.support_action
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY support_action_tenant_read ON pandokos.support_action
    FOR SELECT TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY support_action_tenant_end ON pandokos.support_action
    FOR UPDATE TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.support_action TO pandokos_admin;
GRANT INSERT (tenant_id, actor_type, actor_id, actor_name, key_id, reason, permissions, request_id, expires_at)
    ON pandokos.support_action TO pandokos_admin;
GRANT UPDATE (closed_at, closed_by, closed_by_id, close_reason) ON pandokos.support_action TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- What a platform principal changes in a tenant under a support action names the action. Adding
-- a nullable column rewrites no row and fires no row trigger of the append-only table.
ALTER TABLE pandokos.audit_event ADD COLUMN support_action_id uuid;
GRANT INSERT (support_action_id) ON pandokos.audit_event TO pandokos_admin;
CREATE INDEX audit_event_support_action_idx ON pandokos.audit_event (tenant_id, support_action_id)
    WHERE support_action_id IS NOT NULL;

-- ---------------------------------------------------------------------------------------------
-- Until this migration tenant-admin held every tenant permission. The role owner now does, and
-- tenant-admin every one but those kept for owners. An identity that holds tenant-admin by an
-- administrator's hand becomes an owner as well, so that every existing tenant has one and nobody
-- holds less than before. Rows kept by the group rules (source idp) are left to the rules.
-- The owner of the tables is not subject to their policies while they are not forced (as in
-- migration 0011); both tables are forced again before the migration ends.
ALTER TABLE pandokos.role_assignment NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership_role NO FORCE ROW LEVEL SECURITY;

INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role)
SELECT r.tenant_id, r.service_account_id, 'owner'
  FROM pandokos.role_assignment r
 WHERE r.role = 'tenant-admin'
ON CONFLICT ON CONSTRAINT role_assignment_key DO NOTHING;

INSERT INTO pandokos.membership_role (tenant_id, user_id, role, source)
SELECT r.tenant_id, r.user_id, 'owner', 'manual'
  FROM pandokos.membership_role r
 WHERE r.role = 'tenant-admin' AND r.source = 'manual'
ON CONFLICT ON CONSTRAINT membership_role_key DO NOTHING;

ALTER TABLE pandokos.role_assignment FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership_role FORCE ROW LEVEL SECURITY;
```

### Appendix A2 — queries

Conventions as in the existing files: `sqlc.arg(name)::type`, every statement in the session its
comment names, no statement names a tenant that the session does not already fix.

**Changed: the three principal statements.** `GetKeyPrincipal` (`virtual_keys.sql`),
`GetTokenPrincipal` (`issuers.sql`) and `GetMemberPrincipal` (`sessions.sql`) gain one column
after `roles`. For the first (the others differ only in the assignment table and its key:
`role_assignment` by `m.service_account_id`, `membership_role` by `r.user_id = m.user_id`):

```sql
       COALESCE((SELECT array_agg(DISTINCT p ORDER BY p)
                   FROM pandokos.role_assignment r
                   JOIN pandokos.tenant_role c
                     ON c.tenant_id = r.tenant_id AND c.name = r.role AND c.state = 'active'
                  CROSS JOIN LATERAL unnest(c.permissions) AS p
                  WHERE r.service_account_id = k.service_account_id), '{}')::text[] AS granted
```

**Changed: `InsertAuditEvent`** (`audit.sql`) gains the column `support_action_id` with the value
`sqlc.narg(support_action_id)::uuid`. `InsertPlatformAuditEvent` is unchanged.

**New: `roles.sql`** (every statement in a tenant's session).

| Statement | Kind | What |
|---|---|---|
| `CreateTenantRole` | `:one` | insert `tenant_id`, `name`, `description`, `permissions`; returns the row |
| `GetTenantRole` | `:one` | by `name`; the row and two counts: `(SELECT count(*) FROM pandokos.membership_role r WHERE r.role = t.name)::bigint AS members`, the same on `role_assignment` AS `service_accounts` |
| `ListTenantRoles` | `:many` | filter `state`; sort and cursor exactly as `ListServiceAccounts` (`created_at`, `-created_at`, `name`, `-name`) |
| `CountTenantRoles` | `:one` | `count(*)` |
| `LockTenantRole` | `:one` | `SELECT id, name, description, permissions, state FROM pandokos.tenant_role WHERE name = sqlc.arg(name)::text FOR UPDATE` |
| `LockTenantRolesForAssignment` | `:many` | `SELECT name, permissions, state FROM pandokos.tenant_role WHERE name = ANY (sqlc.arg(names)::text[]) ORDER BY name FOR SHARE` |
| `UpdateTenantRole` | `:execrows` | `description`, `permissions` by `id` |
| `SetTenantRoleState` | `:execrows` | `state` by `id` |
| `DeleteTenantRole` | `:execrows` | by `id` |
| `CountRoleHolders` | `:one` | the two counts of `GetTenantRole`, by name; run after `LockTenantRole` |
| `GetServiceAccountGrant` | `:one` | for `createVirtualKey` and `createSubjectMapping`: by service account ID, `roles` and `granted` as in `GetKeyPrincipal` |
| `GetMemberGrant` | `:one` | for the member operations: by user ID, `roles` and `granted` as in `GetMemberPrincipal` |

**New: `support.sql`.**

| Statement | Session | What |
|---|---|---|
| `OpenSupportAction` | `ActInTenant` | insert `tenant_id`, `actor_type`, `actor_id`, `actor_name`, `key_id`, `reason`, `permissions`, `request_id`, `expires_at = now() + make_interval(secs => sqlc.arg(seconds)::integer)`; returns the row |
| `CloseOpenSupportActions` | `ActInTenant` | for (tenant, actor type, actor ID) with `closed_at IS NULL`: set `closed_at = now()`, `closed_by = CASE WHEN expires_at <= now() THEN 'expiry' ELSE 'platform' END`, `closed_by_id` (empty for an expiry); returns the IDs. Run before `OpenSupportAction` |
| `GetOpenSupportAction` | platform | the hook's look-up: `SELECT a.id::text, a.permissions, a.expires_at, t.name AS tenant_name, t.state AS tenant_state FROM pandokos.support_action a JOIN pandokos.tenant t ON t.id = a.tenant_id WHERE a.tenant_id = $1::uuid AND a.actor_type = $2 AND a.actor_id = $3 AND a.closed_at IS NULL AND a.expires_at > now()` |
| `ListOpenSupportActionsOfActor` | platform | for `getMe`: the same without the tenant condition, with `a.tenant_id`, ordered by `t.name` |
| `GetSupportAction` | platform or tenant | by `id` and `tenant_id`; every column |
| `ListSupportActions` | platform or tenant | by `tenant_id`; filter `state` (`open`: `closed_at IS NULL AND expires_at > now()`; `closed`: the rest); sort `-opened_at` (default) or `opened_at`, cursor on `(opened_at, id)` |
| `LockSupportAction` | `ActInTenant` or tenant | by `id` and `tenant_id`, `FOR UPDATE` |
| `CloseSupportAction` | `ActInTenant` or tenant | set `closed_at = now()`, `closed_by`, `closed_by_id`, `close_reason` by `id` where `closed_at IS NULL`; `:execrows` |
| `TenantExists` | platform | `SELECT EXISTS (SELECT 1 FROM pandokos.tenant WHERE id = $1::uuid)`; use the existing statement if `tenants.sql` has one |

The state an API response shows is computed in Go from `closed_at`, `closed_by` and `expires_at`:
`open`, `expired` (not closed, expiry passed, or closed by `expiry`), `closed` (by the platform),
`ended` (by the tenant).

### Appendix A3 — OpenAPI changes (`api/openapi/admin.v1.yaml`)

Responses of every operation follow the neighbours' lists (`400` where there is a body or a
query, `401`, `403`, `404` where there is a path parameter, `409` where named below, `413` and
`415` with a body, `422` with `IdempotencyKey`, `500`, `503`); they are not repeated here.

**Tags**: `roles` ("The permission catalogue, the built-in roles and the custom roles of a
tenant"), `tenant-support` ("Support actions of the platform inside a tenant"), `support-actions`
("What the platform was allowed to do in the tenant, seen by the tenant").

**Parameters**:

```yaml
    Role:
      name: role
      in: path
      required: true
      description: Name of a custom role of the tenant.
      schema: {$ref: '#/components/schemas/RoleName'}
    SupportAction:
      name: action
      in: path
      required: true
      description: Support action ID.
      schema: {$ref: '#/components/schemas/ID'}
```

**Paths** (abridged: a comment line stands for an operation that differs from the one it names
only as it says):

```yaml
  /v1/permissions:
    get:
      operationId: listPermissions
      tags: [roles]
      summary: The permission catalogue
      x-pandokos-permission: identity:read
      responses: {"200": {$ref: '#/components/responses/PermissionList'}}
  /v1/roles:
    get:
      operationId: listBuiltinRoles
      tags: [roles]
      summary: The built-in roles of tenants and of the platform
      x-pandokos-permission: identity:read
      responses: {"200": {$ref: '#/components/responses/BuiltinRoleList'}}
  /v1/tenants/{tenant}/roles:
    parameters: [{$ref: '#/components/parameters/Tenant'}]
    get:
      operationId: listRoles
      tags: [roles]
      summary: List the custom roles of a tenant
      x-pandokos-permission: role:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/NamedSort'}
        - {name: state, in: query, description: Only roles in this state., schema: {$ref: '#/components/schemas/RoleState'}}
      responses: {"200": {$ref: '#/components/responses/RoleList'}}
    post:
      operationId: createRole
      tags: [roles]
      summary: Define a custom role
      description: >-
        The caller must hold every permission of the role (escalation_refused otherwise). The name
        cannot be changed later, cannot be the name of a built-in role and cannot start with
        platform- or pandokos-.
      x-pandokos-permission: role:write
      parameters: [{$ref: '#/components/parameters/IdempotencyKey'}]
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/RoleCreate'}}}}
      responses: {"201": {$ref: '#/components/responses/RoleCreated'}, "409": {$ref: '#/components/responses/Conflict'}}
  /v1/tenants/{tenant}/roles/{role}:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/Role'}]
    get:
      operationId: getRole
      tags: [roles]
      summary: Read a custom role and how many identities hold it
      x-pandokos-permission: role:read
      responses: {"200": {$ref: '#/components/responses/Role'}}
    patch:
      operationId: updateRole
      tags: [roles]
      summary: Change the description or the permissions of a custom role
      description: >-
        The change holds for every holder: for members at their next request, for keys and tokens
        when the gateways drop their cached principals. The caller must hold every permission the
        role has and will have.
      x-pandokos-permission: role:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/RoleUpdate'}}}}
      responses: {"200": {$ref: '#/components/responses/Role'}}
    delete:
      operationId: deleteRole
      tags: [roles]
      summary: Delete a custom role that nobody holds
      x-pandokos-permission: role:delete
      responses: {"204": {description: Deleted.}, "409": {$ref: '#/components/responses/RoleInUse'}}
  /v1/tenants/{tenant}/roles/{role}/deactivate:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/Role'}]
    post:
      operationId: deactivateRole
      tags: [roles]
      summary: Deactivate a custom role
      description: Holders keep the assignment and lose the permissions; the role cannot be assigned. Deactivating an inactive role succeeds and changes nothing.
      x-pandokos-permission: role:write
      x-pandokos-reason: required
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/RequiredReason'}}}}
      responses: {"200": {$ref: '#/components/responses/Role'}}
  /v1/tenants/{tenant}/roles/{role}/activate:
    # as deactivate: operationId activateRole, summary "Activate a custom role", same permission, reason and responses
  /v1/tenant-support/{tenant}/actions:
    parameters: [{$ref: '#/components/parameters/Tenant'}]
    get:
      operationId: listTenantSupportActions
      tags: [tenant-support]
      summary: List the support actions of a tenant as a platform principal
      x-pandokos-permission: tenant_support:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {name: sort, in: query, schema: {$ref: '#/components/schemas/SupportActionSort'}}
        - {name: state, in: query, description: 'open, or closed for every other state.', schema: {type: string, enum: [open, closed]}}
      responses: {"200": {$ref: '#/components/responses/SupportActionList'}}
    post:
      operationId: openSupportAction
      tags: [tenant-support]
      summary: Open a support action in a tenant
      description: >-
        Lets the caller act in the tenant with the listed permissions until the action expires or
        is closed. The reason, the permissions and every change made under the action are recorded
        in the tenant's audit log; the tenant sees the action and its owner can end it. An open
        action of the same caller in the tenant is closed first. No permission that reads usage,
        prices or secrets can be listed. Refused with support_disabled when the installation
        switched support actions off.
      x-pandokos-permission: tenant_support:open
      x-pandokos-reason: required
      parameters: [{$ref: '#/components/parameters/IdempotencyKey'}]
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/SupportActionOpen'}}}}
      responses: {"201": {$ref: '#/components/responses/SupportActionCreated'}}
  /v1/tenant-support/{tenant}/actions/{action}:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/SupportAction'}]
    get:
      operationId: getTenantSupportAction
      tags: [tenant-support]
      summary: Read a support action as a platform principal
      x-pandokos-permission: tenant_support:read
      responses: {"200": {$ref: '#/components/responses/SupportAction'}}
  /v1/tenant-support/{tenant}/actions/{action}/close:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/SupportAction'}]
    post:
      operationId: closeSupportAction
      tags: [tenant-support]
      summary: Close a support action
      description: Closing an action that is closed or expired succeeds and changes nothing.
      x-pandokos-permission: tenant_support:open
      requestBody: {required: false, content: {application/json: {schema: {$ref: '#/components/schemas/Reason'}}}}
      responses: {"200": {$ref: '#/components/responses/SupportAction'}}
  /v1/tenants/{tenant}/support-actions:
    # get: operationId listSupportActions, tag support-actions, permission support_action:read,
    # parameters and response as listTenantSupportActions
  /v1/tenants/{tenant}/support-actions/{action}:
    # get: operationId getSupportAction, permission support_action:read, response SupportAction
  /v1/tenants/{tenant}/support-actions/{action}/end:
    # post: operationId endSupportAction, permission support_action:end, optional Reason body,
    # response SupportAction; "Ending an action that is closed or expired succeeds and changes nothing."
```

**Changed operations**:

| Operation | Change |
|---|---|
| `setTenantTimeZone`, `setActiveCurrency` | `x-pandokos-permission: tenant_setting:write` (C7) |
| `suspendTenant`, `reactivateTenant`, `resetUserPassword`, `resetMemberPassword` | `requestBody.required: true`, schema `RequiredReason`, `x-pandokos-reason: required` (C7) |
| `disableTenantCredential`, `revokeTenantCredential` | `x-pandokos-reason: required` (C7; the schema is already `RequiredReason`) |
| `createPlatformKey` | `PlatformKeyCreate` gains `role: {$ref: RoleName}`, optional; summary "Issue a platform key"; description names the default and the two roles |
| `createLimit`, `updateLimit`, `createPlatformLimit`, `updatePlatformLimit` | `LimitRuleCreate`, `LimitRuleUpdate` and `PlatformLimitRuleCreate` gain `reason: {type: string, maxLength: 500, description: Recorded in the audit log.}`, optional (C7) |
| `createTenant` | description: "a service account with the role owner" (C1) |
| the operations of §5.4's table | each gains the sentence "Refused with escalation_refused when it would give or take a permission the caller does not hold." |

**Schemas**:

```yaml
    PermissionName: {type: string, pattern: '^[a-z_]+:[a-z_]+$', maxLength: 64}
    RoleState: {type: string, enum: [active, inactive]}
    PermissionScope: {type: string, enum: [platform, tenant, self]}
    Permission:
      type: object
      additionalProperties: false
      required: [name, scope, description, in_tenant, support]
      properties:
        name: {$ref: '#/components/schemas/PermissionName'}
        scope: {$ref: '#/components/schemas/PermissionScope'}
        description: {type: string}
        in_tenant: {type: boolean, description: A platform permission whose operations read or change one tenant.}
        support: {type: boolean, description: A tenant permission that a support action may carry.}
    PermissionList:
      type: object
      additionalProperties: false
      required: [items]
      properties: {items: {type: array, items: {$ref: '#/components/schemas/Permission'}}}
    BuiltinRole:
      type: object
      additionalProperties: false
      required: [name, scope, members, description, permissions]
      properties:
        name: {$ref: '#/components/schemas/RoleName'}
        scope: {$ref: '#/components/schemas/PermissionScope'}
        members: {type: boolean, description: Whether a person can hold the role.}
        description: {type: string}
        permissions: {type: array, items: {$ref: '#/components/schemas/PermissionName'}}
    BuiltinRoleList:   # as PermissionList, items BuiltinRole
    Role:
      type: object
      additionalProperties: false
      required: [id, tenant_id, name, description, permissions, state, created_at, updated_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        tenant_id: {$ref: '#/components/schemas/ID'}
        name: {$ref: '#/components/schemas/RoleName'}
        description: {type: string, maxLength: 500}
        permissions: {type: array, items: {$ref: '#/components/schemas/PermissionName'}}
        state: {$ref: '#/components/schemas/RoleState'}
        assignments:
          type: object
          description: Present on a single read.
          additionalProperties: false
          required: [members, service_accounts]
          properties: {members: {type: integer}, service_accounts: {type: integer}}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    RoleList:          # items Role, next_cursor
    RoleCreate:
      type: object
      additionalProperties: false
      required: [name, permissions]
      properties:
        name: {$ref: '#/components/schemas/RoleName'}
        description: {type: string, maxLength: 500}
        permissions: {type: array, minItems: 1, maxItems: 64, items: {$ref: '#/components/schemas/PermissionName'}}
    RoleUpdate:
      type: object
      additionalProperties: false
      properties:
        description: {type: string, maxLength: 500}
        permissions: {type: array, minItems: 1, maxItems: 64, items: {$ref: '#/components/schemas/PermissionName'}}
        reason: {type: string, maxLength: 500, description: Recorded in the audit log.}
    SupportActionState: {type: string, enum: [open, expired, closed, ended]}
    SupportActionSort: {type: string, enum: [opened_at, -opened_at], default: -opened_at}
    SupportAction:
      type: object
      additionalProperties: false
      required: [id, tenant_id, actor, reason, permissions, state, opened_at, expires_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        tenant_id: {$ref: '#/components/schemas/ID'}
        actor:
          type: object
          additionalProperties: false
          required: [type, id, name]
          properties:
            type: {type: string, enum: [platform, user]}
            id: {type: string}
            name: {type: string}
        reason: {type: string}
        permissions: {type: array, items: {$ref: '#/components/schemas/PermissionName'}}
        state: {$ref: '#/components/schemas/SupportActionState'}
        opened_at: {type: string, format: date-time}
        expires_at: {type: string, format: date-time}
        closed_at: {type: string, format: date-time}
        closed_by: {type: string, enum: [platform, tenant, expiry]}
        close_reason: {type: string}
    SupportActionList: # items SupportAction, next_cursor
    SupportActionOpen:
      type: object
      additionalProperties: false
      required: [reason, permissions]
      properties:
        reason: {type: string, minLength: 1, maxLength: 500, description: Recorded in the audit log of the tenant and on the action.}
        permissions: {type: array, minItems: 1, maxItems: 11, uniqueItems: true, items: {$ref: '#/components/schemas/PermissionName'}}
        duration_seconds: {type: integer, minimum: 60, maximum: 86400, description: Default and upper bound are support.max_duration.}
    SupportGrant:
      type: object
      additionalProperties: false
      required: [id, tenant, permissions, expires_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        tenant: {$ref: '#/components/schemas/TenantRef'}
        permissions: {type: array, items: {$ref: '#/components/schemas/PermissionName'}}
        expires_at: {type: string, format: date-time}
```

`Membership`: `required: [tenant, roles, permissions]`, new member
`permissions: {type: array, description: Effective permissions of the caller in this tenant., items: {type: string}}`.
`Me`: new member `support_actions: {type: array, description: 'The open support actions of the caller; present for a platform principal.', items: {$ref: SupportGrant}}`.
`ProblemCode`: `escalation_refused`, `support_disabled`, `role_in_use` appended.
`RequiredReason.reason.description`: "Recorded in the audit log." Responses `PermissionList`,
`BuiltinRoleList`, `Role`, `RoleCreated`, `RoleList`, `SupportAction`, `SupportActionCreated`,
`SupportActionList` and `RoleInUse` ("The role is still assigned. Code: role_in_use.") are
added. The two new 403 codes are answered through the existing response `Forbidden`, whose
description gains them next to the codes it already lists.

## Part B — Tests first (commit: `test: sprint 4.3 tests, failing`)

All tests of §9, under the stated names, failing for the right reason (an assertion, not a
compile error, wherever the stubs of part A allow). The changed tests of §9 are changed here too,
and fail until the step of C that names them.

### B1. Helpers

- **`test/apitest`**: `AsRole(t, env, tenant, role) Client` creates a service account with the
  role (built-in or custom) and a key, through the API as the tenant's owner; `MemberAs(t, env,
  tenant, role) *Browser` creates a member with a new local account, signs in and changes the
  generated password; `PlatformAs(t, env, role) (Client, *Browser)` a platform key and a platform
  user with the role; `CustomRole(t, env, tenant, permissions...) string` returns the name of a
  new role; `OpenSupport(t, client, tenant, permissions...) string` returns the action ID, with a
  reason generated per call. `calls.go` gains a minimal valid request for each of the sixteen
  operations and a reason for the nine.
- **`test/apitest/matrix.go`**: `Golden(t) map[string][]string` reads
  `internal/authz/testdata/roles.golden`; `Refused(resp) bool` is true for `403` with the code
  `forbidden` and for nothing else; `Probe(t, client, op) *http.Response` sends the call table's
  request of the operation with the path parameters of a prepared tenant.
- **`test/dbtest`**: `CustomRole`, `SupportAction` (seeds as the admin role, the second through a
  transaction with both settings); `ExpireSupportAction(id)` moves `opened_at` and `expires_at`
  into the past as the owner, in a transaction that disables the guard trigger for itself
  (`ALTER TABLE … DISABLE TRIGGER support_action_guard` and enables it again before commit);
  `AuditRows(tenant)` (4.2) returns `support_action_id` as well.
- A gateway with a principal cache for T-857 and T-858 is the one `internal/gateway` tests
  already start; the notification is awaited with the helper those tests use for role changes
  of a service account.

### B2. Where each test lives

| Package | Tests |
|---|---|
| `internal/authz` | T-846 to T-851 |
| `internal/store`, `internal/store/migrations` | T-852, T-853, T-856 |
| `test/isolation` | T-854, T-855, T-888 |
| `internal/authn` | T-857 |
| `internal/gateway` | T-858 |
| `internal/admin` | T-859 to T-882, T-889, T-894 to T-896 |
| `internal/adminapi`, `internal/apigen` | T-883 |
| `internal/audit` | T-884 |
| `internal/cli` | T-885 |
| `internal/config` | T-886 |
| `internal/permdoc` | T-887 |
| `test/e2e` | T-890 to T-892 |
| `test/idp` (tag `idp`) | T-893 |
| `test/repo` | T-R59 to T-R63 |

Notes that the table of §9 does not carry:

- **T-847** parses the golden file itself (no helper of the package under test) and compares
  sets both ways, naming the role and the permission that differ.
- **T-850** and **T-851** build principals with `testing/quick`-style generators over the
  catalogue; the property of T-850 is stated for principals without a support action.
- **T-861**: two goroutines per round, one `updateServiceAccount` adding the role and one
  `deleteRole`; after both returned, either the role exists and is assigned, or it is gone and
  the assignment was refused with `400`; any other state fails. The check reads as the owner.
- **T-865** holds the classification as a Go map literal in the test file,
  `operation → confers | removes | contains | none`; `confers` and `removes` entries name a
  driver function that sets up a weaker caller and a stronger target. An operation of
  `adminapi.Operations` missing from the map, and a map entry without an operation, both fail.
- **T-867**: tenant roles are probed in one prepared tenant that has a resource of every kind, so
  that an allowed request is answered by its handler; "allowed" is any answer for which
  `Refused` is false. Each role is probed with a key, and with a session when `Members` is true.
  `session:self` operations are skipped for keys (4.2) and public operations for all. The test
  prints, on failure, the role, the operation, the permission and both expectations.
- **T-869**: the random sets draw from the eight tenant roles a member can hold and from three
  custom roles created per run with random permissions; the comparison is "permissions reported"
  against "permissions of the operations not refused", over the operations of scope tenant, plus
  the two self permissions.
- **T-871** compares the refusal bodies with those of a platform key and of a non-member user
  recorded in the same test before any action exists, ignoring the request ID.
- **T-872** derives its operation list from the catalogue (every operation whose permission is
  of scope tenant and lacks the attribute `Support`), so a later permission is covered.
- **T-874** revokes every key of the tenant as its owner and removes its members, then proves
  with one request that nobody of the tenant can administer it, before the platform acts.
- **T-878** exempts `closeSupportAction` in a map literal with a comment that cites §5.8.
- **T-889** searches the captured log and the exported spans for the tenant's ID and name, the
  names of the roles used, the reasons (random per run) and the action IDs.
- **T-892** uses the upgrade fixture of T-839 (a database of the previous release).
- **T-R61** parses the Go files of `internal/` and `cmd/` and lists identifiers
  `authz.Role[A-Z]\w*` outside `internal/authz`; the allowed positions are `internal/admin/tenants.go`
  (`RoleOwner`) and, for `RolePlatformAdmin`, `internal/admin/bootstrap.go`,
  `internal/account/host.go` and the default of `internal/admin/platform_keys.go` (the string
  literals there become the constant). Whether a person can hold a role is `Role.Members`, never
  a name.

Verification of part B:

```
go vet ./... && go vet -tags idp ./test/idp/...
go test ./... -count=1 2>&1 | grep -c '^--- FAIL'     # only tests of §9 fail; record the list in the commit message
```

## Part C — Implementation

Order: the decision core (C1), custom roles as data and through the API (C2, C3), the guard
(C4), `/v1/me` (C5), the support action (C6), reasons and the smaller contract changes (C7), CLI
(C8), the reference (C9). After C5 the first two exit conditions pass; after C6 the third.

### C1. Decision core — `feat: permissions from data, the support principal, the first owner`

Files: `internal/authz/{authz,roles,grant}.go`, `internal/admin/tenants.go`.

- `Effective` as the four steps of §5.3 and the replacement of §5.6 for a principal with
  `SupportActionID`. The catalogue look-ups use a package-level map built once.
- `Decide`: the two changed conditions of §5.11; the order of the five rules is not touched.
- `CanGrant(perms)`: for each name, look it up; scope self: skip; unknown: false; with
  `SupportActionID`: scope tenant true, scope platform false; otherwise membership in
  `Effective()`.
- `createTenant` assigns `authz.RoleOwner` to the first service account and reports it (three
  places of `tenants.go`); the audit detail says `roles: ["owner"]`.

Passes: T-846 to T-851, T-881, the changed matrices.
Verify: `go test ./internal/authz/... ./internal/admin/... -run 'Authz|Catalogue|Builtin|Effective|Decide|CanGrant|Provisioning|Tenant' -count=1 && go test ./internal/authz/ -run '^$' -fuzz '^FuzzEffective$' -fuzztime 20s`.

### C2. Custom roles as data — `feat: principals carry the permissions of custom roles`

Files: `internal/authn/{authn,tokens,cache}.go`, `internal/admin/admin.go` (the member branch
moves to `memberPrincipal` and fills `Granted`), `internal/admin/me.go` (calls it).

- The three principal builders copy the row's `granted` into `Principal.Granted`. The cache entry
  holds the row, so nothing else changes; the trigger of the migration sends the notification the
  cache already listens to.
- `memberPrincipal(ctx, user, sess, tid)`: `GetMemberPrincipal` in the tenant's session;
  `store.ErrNoRows` becomes `errNotFound`. The hook and `GetMe` both call it; no other code builds
  a member's principal.

Passes: T-857, T-858 (with rows seeded by `dbtest.CustomRole`).
Verify: `go test ./internal/authn/... ./internal/gateway/... -run 'CustomRole' -count=1`.

### C3. Role operations — `feat: custom roles in the administration API`

Files: `internal/admin/roles.go`, `internal/admin/convert.go`, `internal/admin/problem.go`.

- `ListPermissions`, `ListBuiltinRoles`: from `authz.Catalogue()` and `authz.Roles()`.
- `CreateRole`: `authz.CheckCustom` (offender: `invalid("permissions", "names a permission that is not a tenant permission: <name>")`);
  the name against `authz.Roles()` and the two prefixes (`invalid("name", …)`); `CountTenantRoles`
  ≥ 100: `errConflict`; unique violation `tenant_role_name_key`: `errConflict`; check violations
  of the table mapped to `invalid` on their member. Audit `role.create`.
- `UpdateRole`: a body with neither `description` nor `permissions` is refused as the other
  updates refuse an empty body; `LockTenantRole`; the new set normalised; audit `role.update` with
  `permissions: {added, removed}` and the description change; no row when nothing changes.
- `DeactivateRole`, `ActivateRole`: lock, set the state, audit with the reason; idempotent.
- `DeleteRole`: lock, `CountRoleHolders` > 0: `errRoleInUse`; delete; a `foreign_key_violation`
  with the constraint name `tenant_role_in_use` (the backstop) also maps to `errRoleInUse`. Audit
  `role.delete` with the last permissions.
- `resolveRoles(ctx, q, names, member)` in `grant.go`: built-in names from `authz`, scope tenant,
  `Members` when `member`; the rest through `LockTenantRolesForAssignment`; a name that is neither,
  or an inactive custom role: `invalid("roles", "must name built-in tenant roles or active custom roles of the tenant")`.
  `checkRoles` and `checkRolesIn` are replaced by it for tenant roles (the guard itself is C4:
  in this step a caller's permissions are not yet compared).

Passes: T-859 to T-861, T-866, T-894 (the role half), T-896.
Verify: `go test ./internal/admin/... -run 'Roles|RoleDe|Assignable|RoleChange|RolesIdem' -count=1`.

### C4. The guard — `feat: nobody grants or takes a permission they do not hold`

Files: `internal/admin/grant.go`, and the handlers of the table of §5.4:
`roles.go`, `service_accounts.go`, `members.go`, `users.go`, `platform_keys.go`,
`virtual_keys.go`, `subject_mappings.go`. `authz.Assignable` and its callers are removed.

- `guardGrant(p, sets...)`: `p.CanGrant` on each set; false: `errEscalation` and one log line
  `grant refused` (request ID and operation only).
- Role operations: the role before and after.
- Assignments: `added = want − have`, `removed = have − want`; the permissions of each role in
  either, from `resolveRoles` (custom) and `authz` (built-in); for a removed custom role that is
  inactive or gone, its stored permissions, or nothing when the row is gone. `deleteMember`:
  every role of the member. Platform roles: `users.go` keeps its `400` for an unknown or
  wrong-scope role and uses `guardGrant` for the rest.
- `createVirtualKey`: `GetServiceAccountGrant`; the set is the effective permissions of a
  principal `{Kind: service account, Roles, Granted, Scopes: the key's}`, computed with
  `Effective()`, so that the guard and the gateway can never disagree about what a key can do.
- `createSubjectMapping`: the same without scopes.
- `resetMemberPassword`: `GetMemberGrant` of the target, its `Effective()` as a member principal;
  `resetUserPassword`: the target's platform roles.
- Every guard runs inside the transaction that writes, after the rows it reads are locked as
  they already are, and before the first write.

Passes: T-862 to T-865, T-895, the changed T-802.
Verify: `go test ./internal/admin/... -run 'NoEscalation|GrantSurface|Separation|Users|Members' -count=1`.

### C5. `/v1/me` — `feat: effective permissions per tenant in /v1/me`

Files: `internal/admin/me.go`, `internal/admin/convert.go`.

- For each tenant of `user_tenants`: `memberPrincipal`, then `Roles` and `Effective()` into the
  member of `memberships`.
- `support_actions` for a principal without a tenant that holds `tenant_support:open` (empty
  until C6 fills it).

Passes: T-867, T-868 (its support-action case after C6), T-869, T-890.
Verify: `go test ./internal/admin/... -run 'RoleMatrix|MeMatrix|MeMatches' -count=1 && go test ./test/e2e/... -run 'RolesExit' -count=1`.

### C6. Support actions — `feat: support actions of the platform inside a tenant`

Files: `internal/store/tx.go`, `internal/audit/audit.go`, `internal/admin/{support,admin,me}.go`,
`cmd/pandokos/serve.go`.

- `store.DB.ActInTenant`: `inTx` with both settings; `ErrNotAdmin` for another role; a zero ID is
  `ErrNoTenant`.
- `audit.RecordTenant` sets `SupportActionID` from `authz.FromContext(ctx)` when the event has
  none; `RecordIntervention` calls `RecordTenant` and then `RecordPlatform` with a copy of the
  detail that has `tenant_id`.
- `OpenSupportAction`: `support.Enabled` false: `errSupportDisabled`. Each permission must be in
  `authz.SupportPermissions()` (`invalid("permissions", …)` naming the first that is not);
  duration default and bound from `support.MaxDuration`
  (`invalid("duration_seconds", "must be at most <n>")`). In `ActInTenant`: `TenantExists` (no:
  `errNotFound`); `CloseOpenSupportActions`; `OpenSupportAction`; `RecordIntervention`
  (`support_action.open`, resource `support_action`, the action's ID in `SupportActionID`,
  detail `permissions`, `expires_at`, `replaces`).
- `CloseSupportAction` (platform): `ActInTenant`, lock, already closed or expired: return it;
  else close as `platform` and `RecordIntervention` (`support_action.close`).
- `EndSupportAction` (tenant): `WithTenant`, lock, close as `tenant`, `RecordTenant`
  (`support_action.end`).
- The four reads: the platform ones in the platform scope after `TenantExists`, the tenant ones
  in the tenant's session.
- `supportPrincipal(ctx, p, tid)`: returns false at once unless `support.Enabled`, `p` has no
  tenant, and `p.Effective()` holds `tenant_support:open`. Actor type and ID as
  `audit.ActorOf(p)`. `GetOpenSupportAction` in the platform scope; no row: false. Otherwise a
  copy of `p` with `TenantID`, `TenantName`, `TenantState`, `Roles: nil`, `Granted`,
  `SupportActionID`.
- The hook: in `beforeKey`, after authentication, when the operation's permission is of scope
  tenant and `p.Kind` is platform; in `beforeSession`, where `memberPrincipal` returns
  `errNotFound`. In both, a path whose tenant ID does not parse keeps its old answer.
- `createMember` with `account: existing` refuses the caller's own user ID under a support
  action (`stateError` on `user_id`).
- `logRequest`: a principal with `SupportActionID` is logged as the platform principal it is,
  with `support: true`.
- `GetMe`: `ListOpenSupportActionsOfActor`.

Passes: T-849 (already), T-855, T-856, T-870 to T-876, T-884, T-888, T-889, T-891, T-894.
Verify: `go test ./internal/store/... ./internal/audit/... ./internal/admin/... ./test/isolation/... -run 'ActInTenant|Support|RolesAndSupport|RolesTelemetry' -count=1 && go test ./test/e2e/... -run 'SupportExit' -count=1`.

### C7. Reasons and contract changes — `feat: required reasons; tenant settings permission; platform auditor keys`

Files: `api/openapi/admin.v1.yaml` (the "changed operations" of appendix A3), generated code,
`internal/admin/{admin,tenants,users,members,tenant_credentials,time_zone,active_currency,platform_keys,limits,platform_limits,auth}.go`,
`internal/config/auth.go`, `internal/account/federated.go`.

- The six existing operations get the construct; their handlers read `Body.Reason` as before.
- `setTenantTimeZone`, `setActiveCurrency`: the permission in the document; no handler change.
- The four limit-rule operations pass `Body.Reason` to the audit event they already write; a
  body with `reason` alone changes nothing and is refused as an empty update is today.
- `createPlatformKey`: `role` (default `platform-admin`); a name that is not a platform role is
  `invalid("role", …)`; then `guardGrant`.
- `internal/config/auth.go` no longer compares role names: a platform target must name a
  platform role with `Members`; a tenant target is checked for the format of a role name only,
  because a custom role is known to the database, not to the configuration.
- `checkGroupRules` (start) and the sign-in reconciliation (§5.3): a rule's role is accepted when
  `authz` knows it in the scope with `Members`, or, for a tenant target, when the tenant has a
  custom role of that name (read with the tenants, in each tenant's session); at sign-in
  `resolveRoles` decides, and a refused name is skipped with the log line
  `group rule skipped` and the rule's index.

Passes: T-877 to T-880, T-882, T-883, T-892, T-893, the changed tests of the six operations.
Verify: `make generate-check && go test ./internal/admin/... ./internal/apigen/... ./internal/adminapi/... -run 'Reason|InTenant|SettingSplit|PlatformAuditor|GroupRules' -count=1`.

### C8. CLI — `feat: role, permission and support commands`

Files: `internal/cli/{roles,commands}.go`.

`permission list`; `role builtin`; `role list|create|get|update|deactivate|activate|delete`
(`--permission` repeatable, `--description`, `--reason`); `tenant-support open|list|get|close`
(`--permission` repeatable, `--duration`, `--reason`); `support-action list|get|end`;
`platform-key create --role`; `--reason` becomes required on `tenant suspend`, `tenant
reactivate`, `platform-user reset-password`, `member reset-password` (the two credential
oversight commands already require it).

Passes: T-885, the CLI coverage test.
Verify: `go test ./internal/cli/... -count=1`.

### C9. Reference — `feat: the permission reference is generated`

Files: `internal/permdoc/{permdoc,main}.go`, `docs/reference/permissions.md`.

`Generate` writes, in this order: the permission table (name, scope, attributes as "in tenant"
and "support", description, operations that need it, built-in roles that hold it); the role
table (name, scope, whether a person can hold it, description, permissions); the matrix (one
row per tenant permission, one column per tenant role; the same for the platform); the list of
operations that require a reason. Sorting is fixed (catalogue order, role order of `Roles()`,
operations by ID). The hand-written tables between the markers are replaced; the prose above
and below is edited in part E so that it does not repeat or contradict the tables.

Passes: T-887, T-R59.
Verify: `make generate-check && go test ./internal/permdoc/... ./test/repo/... -run 'Generate|PermissionReference' -count=1`.

## Part D — CI (commit: `ci: generated permission reference; fuzz target; limited-role test with Keycloak`)

- `Makefile`: `fuzz` gains `go test ./internal/authz/ -run '^$$' -fuzz '^FuzzEffective$$' -fuzztime $(FUZZTIME)`.
  `generate` and `GENERATED` were changed in A8; confirm that `make generate-check` fails when a
  role's permission is changed without regenerating (try it locally and revert).
- `.github/workflows/ci.yml`: no new job. The `idp` job runs T-893 with the others; its start-up
  configuration for that test adds one group rule `tenant:<name>:usage-viewer=<group>` using a
  group the realm of 4.2 already has. No realm change; if the realm has no second group that the
  test can use without disturbing T-841, stop and ask before changing the realm template.
- T-R57 and the job list tests of earlier sprints stay as they are.

Verify: `make check && make idp-up test-idp`.

## Part E — Documentation and decision records (commit: `docs: sprint 4.3 documentation and ADRs`)

### E1. New page

`docs/operations/roles.md`: the ten roles and what each is for; owner and administrator, and the
first owner after an upgrade; custom roles (defining, assigning to members and service accounts,
deactivating, deleting, reserved names, the bound of propagation to keys); three worked custom
roles: an identity manager, a limits manager (`limit:read`, `limit:write`, `limit:delete`), a
price reader (`price_list:read`); the rule against escalation with the table of §5.4 and what
`escalation_refused` means for a client; what `/v1/me` returns and how a client uses it, with the
sentence of AZ-11 that hiding is not the control; support actions: what they are for, the
permissions they can carry and the ones they never can, opening, using, closing, what the tenant
sees and how its owner ends one, the configuration keys, the recovery of a tenant step by step;
the nine operations that need a reason; group rules with roles.

### E2. Changed pages

`docs/architecture/security-model.md` (the row "a platform administrator abuses the role" made
exact: the residual path of §5.6, the switch; new rows from §5.12), `docs/architecture/data-model.md`
(`tenant_role`, `support_action`, the column of `audit_event`, the departure from "three
tables"), `docs/architecture/overview.md` (authorization: permissions from data; what the gateway
reads), `docs/reference/admin-api.md` (the three codes, the construct, the commands),
`docs/reference/permissions.md` (prose around the generated tables; the sentence "custom roles
are not yet possible" removed), `docs/operations/configuration.md`, `docs/operations/sign-in.md`
(platform administrators and tenant-managed users: the support action; group rules with roles),
`docs/operations/limits.md` and the price guide (the setting split), `docs/operations/tokens.md`
(mappings are guarded grants), `docs/operations/database.md` (migration 0014, the owner step),
the release notes of v0.2 (the three contract changes of R-9), `README.md` status.

### E3. Decision records (status `proposed`, date of the commit; index in `docs/adr/README.md`)

- **0060 — Built-in roles, custom roles and the rule against escalation.** Refines 0007, 0031,
  0056. Requirements AZ-03 to AZ-06, AZ-10, AZ-11. Context: ADR 0007 names roles, custom roles
  and "no escalation" without contents; 4.2 left two roles for people and a stub. Decision: D-1
  to D-7, D-13, D-15, D-16: the eight permissions and the setting split; the ten roles and the
  golden file; `owner` by construction, the first owner and the migration; custom roles as rows
  referenced by name, tenant permissions only, read by the gateway, deactivated by state, deleted
  when unassigned; `CanGrant` and the closed table of operations, with removal covered and
  containment free; one function for `/v1/me` and the hook; the generated reference.
  Consequences: a new permission must be placed in the golden file; a new built-in role needs a
  migration that handles a colliding custom role; an administrator cannot act on an owner beyond
  containment; keys scoped to `limit:write` or `price_list:write` lose the two settings.
  Alternatives: `owner` and `tenant-admin` with the same permissions (the requirement met by
  name); role rows for built-in roles in the database (two sources of truth for what a role
  holds, and a migration for every change); a foreign key from assignments to a roles table
  (built-in roles would have to be rows in every tenant); checking the whole role set on
  assignment instead of the difference (an administrator could not edit a member who also holds
  a role it lacks); scopes per subject mapping (a second place where a principal's power is
  defined); a guard on containment operations (a holder of `virtual_key:revoke` could not revoke
  a stronger key in an incident).
- **0061 — Support actions and reasons: how the platform acts inside a tenant.** Refines 0007,
  0034, 0054. Requirements AZ-06, AZ-09, SEC-08. Context: a platform administrator has no
  permission in a tenant (ADR 0031) and usage is the tenant's (ADR 0054), yet a tenant that lost
  its administrators must be recoverable and issues #31, #35 and #52 need the same ingredients.
  Decision: D-8 to D-12, D-17: the support action (permission, reason, closed set of eleven
  permissions, bounded time, one transaction for the row and both audit events, honoured at the
  "no membership" place, the switch, visible to the tenant, ended by its owner); grants with the
  tenant's authority under an action and the two limits; the residual path stated; the construct
  `x-pandokos-reason` and the nine operations with their three criteria; the attributes
  `InTenant` and `Support`, `ActInTenant`, `RecordIntervention` as the mechanism later work
  reuses, with the table of §5.8. Consequences: with `support.enabled: false` a tenant without
  administrators is recovered only from the host's database; whether a support action may read a
  tenant's audit log is decided with `audit:read` in sprint 6.2. Alternatives: a platform role
  with tenant permissions (AZ-06); a reason header on every request of a platform principal in a
  tenant (reads would need an audit write each, and nothing bounds the time); a dedicated
  recovery operation only (AZ-09 names users, roles and keys); a support action that can carry
  any tenant permission (usage and secrets would be one request away); making the platform user
  a temporary member (it would hold a role, and the tenant's member list would lie); reasons
  required on every state change (boilerplate reasons).
- History lines: 0007 (the contents, by 0060 and 0061), 0020 (three codes), 0027 (the
  transaction with both settings generalised from provisioning to `ActInTenant`), 0028 (one
  construct), 0030 (the cached principal holds the permissions of custom roles; `tenant_role`
  notifies), 0031 (`owner` holds every tenant permission and is the first identity's role;
  `tenant-admin` is a list; eight permissions; `Assignable` replaced by `CanGrant`), 0032 (eight
  actions, `support_action_id`), 0034 (the two oversight permissions carry `InTenant`; they are
  not behind the support switch), 0045 (4.1 O-5 closed: no scopes per mapping), 0052 (the time
  zone under `tenant_setting:write`), 0053 (the active currency under `tenant_setting:write`),
  0054 (no support action carries a permission that reads usage, limit events or prices), 0055
  (D-17), 0056 (roles of people: any tenant role but `inference`; custom roles are not for the
  platform; the support action at the "no membership" place).

### E4. Planning files

`planning/epics/04-identity-and-rbac/epic.md` (4.3: decisions taken, records, guide; status),
`planning/roadmap.md`, the front matter of `sprint.md` (`status`), the epics that §11 changes
(6: the `audit:read` note; the issue notes where the epics track #31, #35, #52). The
maintainer's decisions on O-1 to O-13 are appended to §12 as a table when taken.

Verify: `go test ./test/repo/... -count=1` (T-R63 and the reference tests).

## Part F — Verification (no commit unless something is fixed)

1. `make check` on a clean tree; `make idp-up test-idp`; `make build test-browser`; `make image
   test-container test-compose`.
2. `make fuzz FUZZTIME=60s` for `FuzzEffective`; `make vuln` (no module changed: the result must
   equal the base's).
3. The definition of done of §10, item by item, with the test or the file that shows each; the
   exit conditions by their tests.
4. The review checklists, prepared by the implementer for the reviewer: for each row of the
   table of §5.4, the handler, the line of its `guardGrant` call and its case in T-863 to T-865;
   for each row of §5.12, the code and the test; the golden file next to §5.2.
5. Grep the tree for leftovers: `grep -rn 'not implemented' internal/ cmd/` (none in the new
   handlers); `grep -rn 'Assignable' internal/ test/` (none); `git grep -n -E 'authz\.Role[A-Z]' -- internal cmd ':!internal/authz'`
   (the places of §5.11 only);
   `git log --format=%B <base>..HEAD | grep -i -E '^[a-z-]+-(by|session):' | grep -v -i '^signed-off-by:'` (nothing).
6. Record in the pull request: the number of statements of a key authentication on a cache miss
   before and after (it must be equal), and the result of applying migration 0014 to a copy of a
   database of the previous release with at least two tenants.

## What the implementer does not do

- Add a module, an image or a GitHub action.
- Give the gateway role more than `SELECT` on `tenant_role`, give the worker role anything of
  migration 0014, add a policy to a tenant-scoped table, add a `SECURITY DEFINER` function, or
  edit a merged migration.
- Give a platform permission, or the attribute `Support`, to anything that reads usage, limit
  events, prices or a secret; add `audit:read` (6.2).
- Decide by comparing role names; regenerate the golden file from the code; widen the list of
  operations that require a reason, or the classification of T-865, without the design saying so.
- Build budget exceptions, bulk containment or re-identification (issues #31, #35, #52), custom
  platform roles, or scopes per subject mapping.
