# Sprint 4.2 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) and D-n, T-n, O-n refer to it.
- Parts: **A** scaffolding, **B** tests (failing), **C** implementation in ten steps, **D**
  containers and CI, **E** documentation and records, **F** verification. Each part and each step
  of C is self-contained: its files, what it must make pass, and the commands that prove it. A
  different implementer can take each one, in order.
- One commit per part A, B, D, E, F and per step of C, with the message given.

## Rules for the implementer

1. Follow the design. A gap, or a statement of the "not verified" list of §12 that does not hold,
   is raised as a question; it is not filled silently.
2. Tests first: part B is committed with every new test failing (or not compiling against the
   stubs of part A only where a stub cannot exist); a step of C ends when its tests pass and no
   other test broke.
3. Never write a version, a digest or a checksum that was not read from its source in this run;
   each one below has its command.
4. No secret in code, tests, logs or fixtures. Test passwords, client secrets, session tokens and
   signing keys are generated when the test runs; canary values are random per run. Passwords and
   tokens travel as `secret.Secret` and are revealed only where they are hashed, compared or
   written to the one response that shows them.
5. Operational logs and spans name no user, username, subject, group, e-mail address or source
   address (ADR 0055); T-R58 checks it.
6. `golang.org/x/crypto/argon2` is imported by `internal/password` and by nothing else; go-jose
   stays where it is; outbound HTTP uses a transport of `internal/egress`.
7. Every commit is signed off in the maintainer's name (`git commit -s`) and carries no other
   trailer: no co-author line, no tool or session attribution and no link to a working session, in
   commit messages, pull requests, comments and documents alike.
8. Generated code (`internal/adminapi`, `internal/store/queries`) is produced by `make generate`
   and never edited.

## Environment

```
make db-up            # PostgreSQL 16 and PgBouncer for D and E tests
make idp-up           # Keycloak, Hydra, Authentik for X tests (part D changes the realm)
google-chrome --version   # B tests (part D)
cat .go-version       # the release toolchain used by make vuln and the tagged tests
```

## Part A — Scaffolding (commit: `chore: sprint 4.2 scaffolding: argon2 module, migration 0013, API, permissions, configuration`)

Nothing behaves differently after part A except that the schema, the document, the permissions
and the configuration keys exist. Handlers of the new operations return `errors.New("not
implemented")` (a 500), so that the router test of every route passes its shape checks.

**A1. Dependency.**

```
go list -m -versions golang.org/x/crypto | tr ' ' '\n' | tail -1     # design time: v0.59.0
go get golang.org/x/crypto@<that version>
go mod tidy && go mod verify
grep -n 'golang.org/x/' go.mod      # x/crypto direct; x/sys, x/net, x/text indirect, nothing else new
make vuln
```

Stop if the latest version requires a Go version above the `go` line of `go.mod`, or if `go mod
tidy` adds a module other than `golang.org/x/crypto` and moves others than `golang.org/x/sys`,
`golang.org/x/text`, `golang.org/x/net`. Add `golang.org/x/crypto` to the depguard allow-list of
`.golangci.yml` with a rule `argon2-only-in-password` (deny `golang.org/x/crypto` outside
`**/internal/password/**`; in that package only the sub-package `argon2` is imported, checked by
T-R53). Create `internal/password/doc.go` importing nothing yet.

**A2. Migration.** `internal/store/migrations/sql/0013_users_sessions.sql` is appendix A1,
verbatim. Then `make migrations-sum`. Verify:

```
go test ./internal/store/... -run 'Migrat|Schema' -count=1
```

**A3. Queries.** `internal/store/queries/sql/users.sql`, `sessions.sql`, `idempotency.sql` as
appendix A2; the token queries of `issuers.sql` are unchanged. `make generate`. Stop if `sqlc`
refuses a statement; do not rewrite it as a string in Go (T-R "no SQL from strings").

**A4. OpenAPI and generator.** Apply appendix A3 to `api/openapi/admin.v1.yaml`; extend
`internal/apigen` and `internal/adminapi/runtime.go`:

| Construct | Generator and runtime |
|---|---|
| `x-pandokos-public: true` | exactly one of it and `x-pandokos-permission`; `Operation.Public bool`; the operation has `security: []` |
| a `"303"` response with `headers: {Location}` and no content, as the only non-problem response, on a public `GET` | the server method returns `(*adminapi.Redirect, error)`, `type Redirect struct{ Location string }`; the router writes `303` with `Location` and no body; `Operation.Status` is 303; the client method returns the `Location` and does not follow it |
| `{$ref: '#/components/parameters/IdempotencyKey'}` (`in: header`) | allowed only on operations whose success status is 201; `Operation.Idempotent bool`; the input struct gains `IdempotencyKey string`, which the client sends when not empty; the router does not validate it (the hook does) |
| `x-pandokos-lenient-query: true` | only with `x-pandokos-public`; `parseQuery` ignores undeclared parameters for it and still refuses a repeated declared one |
| `headers` on a response; a second security scheme `session` (`apiKey`, `in: cookie`); `security` with two entries | accepted and ignored |
| a request member named `password`, `current_password` or `new_password` | `carriesSecret` treats it as `secret` does |

`Hooks.Before` already receives the `*http.Request`; no hook changes. `make generate`; the
generated diff is committed. Verify: `go test ./internal/apigen/... ./internal/adminapi/...
./test/repo/... -run 'OpenAPI|Generat|Subset|Router' -count=1` (T-822 fails until part B adds
its cases; the existing cases pass).

**A5. Permissions and principal.** `internal/authz/authz.go`: the five permissions with these
descriptions and scopes, the role changes of §5.3, `KindUser`, the two principal members,
`Role.Scope` replacing `Role.Kind` (update every reader: `internal/admin`, the reference
generator, tests), `Assignable` returning false (stub).

| Permission | Scope | Description |
|---|---|---|
| `user:read` | platform | Reading and listing the users that the platform manages, with their platform roles |
| `user:write` | platform | Creating and changing such users; disabling and enabling them; resetting their password; unlocking them; ending their sessions; assigning platform roles |
| `member:read` | tenant | Reading and listing the members of the tenant and their roles |
| `member:write` | tenant | Adding and removing members and setting their roles; creating local accounts that the tenant manages, and for those: disabling, enabling, resetting the password, unlocking, ending sessions |
| `session:self` | self | Signing out, changing one's own password, listing and ending one's own sessions |

**A6. Configuration.** `internal/config`: a file `auth.go` with the structures and `keys.go`
entries for every key of §5.15 (names, defaults and rules exactly as the table; environment names
follow the existing derivation, `PANDOKOS_AUTH_LOCAL_ENABLED`, …). List keys
(`auth.oidc.scopes`, `algorithms`, `group_mappings`, `private_hosts`,
`listeners.admin.trusted_proxies`) use the list syntax of `tokens.private_hosts`.
`auth.oidc.client_secret_file` is read with `secret.ReadFile` when the purpose is serve and
`auth.oidc.enabled`. Validation and the three warnings are written in C8 and C1; part A adds the
keys and defaults only, and the configuration reference row for each.

**A7. Stubs so that part B compiles.** Packages `internal/password`, `internal/session`,
`internal/account`, `internal/oidc`, `internal/idem` with the exported names of §5.16 and bodies
that return an error; `authn.VerifyJWT` and `jwks.Cache.Metadata` likewise; the handler methods
of the 27 operations in `internal/admin/{auth,users,members}.go`; `cmd/pandokos/user.go` with the
two commands registered and returning exit code 1.

**A8. Isolation spec.** `test/isolation/spec.go`: the nine global tables with reasons, the grant
matrix read from appendix A1 (admin role only, columns as granted), the five definer functions,
and fixtures for the two tenant-scoped tables. A fixture needs a user: its `Insert` is one
statement with a data-modifying CTE as far as the role running fixtures can insert into
`app_user` for the tenant (`tenant_id = $1`, `sign_in = 'local'`); if the harness runs fixtures as
a role that cannot, stop and ask: do not weaken the policy.

Verification of part A:

```
make generate-check fmt-check vet lint tidy
go test ./... -count=1        # with make db-up: everything that passed before passes
```

### Appendix A1 — `internal/store/migrations/sql/0013_users_sessions.sql`

```sql
-- 0013_users_sessions: people (users, local accounts, federated identities, sessions), memberships
-- and their roles, platform roles of users, sign-in throttling, OIDC sign-in transactions and
-- idempotency keys; tokens of trusted issuers for the admin role. Runs in one transaction as
-- pandokos_owner. Every name is schema-qualified. The gateway and worker roles receive no
-- privilege on any table of this migration (IAM-22).

-- Timestamps of rows whose key is user_id; the key never changes.
CREATE FUNCTION pandokos.user_row_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_at := now();
        NEW.updated_at := now();
        RETURN NEW;
    END IF;
    IF NEW.user_id <> OLD.user_id THEN
        RAISE EXCEPTION 'user_id is immutable' USING ERRCODE = 'check_violation';
    END IF;
    NEW.created_at := OLD.created_at;
    NEW.updated_at := now();
    RETURN NEW;
END
$$;

-- Users (global table with explicit policies). tenant_id names the tenant that manages the
-- account; NULL: the platform does. A federated user is always managed by the platform.
CREATE TABLE pandokos.app_user (
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    sign_in          text        NOT NULL,
    tenant_id        uuid,
    display_name     text        NOT NULL DEFAULT '',
    email            text        NOT NULL DEFAULT '',
    state            text        NOT NULL DEFAULT 'active',
    state_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT app_user_pkey PRIMARY KEY (id),
    CONSTRAINT app_user_id_sign_in_key UNIQUE (id, sign_in),
    CONSTRAINT app_user_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT app_user_sign_in_valid CHECK (sign_in IN ('local', 'oidc')),
    CONSTRAINT app_user_federated_is_platform CHECK (sign_in = 'local' OR tenant_id IS NULL),
    CONSTRAINT app_user_display_name_format CHECK (char_length(display_name) <= 200 AND display_name !~ '[[:cntrl:]]'),
    CONSTRAINT app_user_email_format CHECK (char_length(email) <= 254 AND email !~ '[[:cntrl:][:space:]]'),
    CONSTRAINT app_user_state_valid CHECK (state IN ('active', 'disabled'))
);
CREATE INDEX app_user_tenant_idx ON pandokos.app_user (tenant_id) WHERE tenant_id IS NOT NULL;

CREATE FUNCTION pandokos.app_user_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.sign_in <> OLD.sign_in OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id THEN
        RAISE EXCEPTION 'sign_in and tenant_id of a user are immutable' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.state <> OLD.state THEN
        NEW.state_changed_at := now();
    ELSE
        NEW.state_changed_at := OLD.state_changed_at;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER app_user_row_guard BEFORE INSERT OR UPDATE ON pandokos.app_user
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER app_user_guard BEFORE UPDATE ON pandokos.app_user
    FOR EACH ROW EXECUTE FUNCTION pandokos.app_user_guard();

ALTER TABLE pandokos.app_user ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.app_user FORCE ROW LEVEL SECURITY;

GRANT SELECT ON pandokos.app_user TO pandokos_admin;
GRANT INSERT (sign_in, tenant_id, display_name, email) ON pandokos.app_user TO pandokos_admin;
GRANT UPDATE (display_name, email, state) ON pandokos.app_user TO pandokos_admin;

-- Memberships (tenant-scoped table): a user is a member of a tenant. source says who keeps the
-- row: an administrator ('manual') or the group mapping at sign-in ('idp').
CREATE TABLE pandokos.membership (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    user_id    uuid        NOT NULL,
    source     text        NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT membership_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT membership_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT membership_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT membership_user_key UNIQUE (tenant_id, user_id),
    CONSTRAINT membership_source_valid CHECK (source IN ('manual', 'idp'))
);
CREATE INDEX membership_user_idx ON pandokos.membership (user_id);

-- A user that a tenant manages is a member of that tenant only (sprint 4.2, D-1): otherwise the
-- administrators of one tenant could reset the password of a member of another.
CREATE FUNCTION pandokos.membership_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    home uuid;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.user_id <> OLD.user_id THEN
            RAISE EXCEPTION 'user_id of a membership is immutable' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    SELECT u.tenant_id INTO home FROM pandokos.app_user u WHERE u.id = NEW.user_id;
    IF home IS NOT NULL AND home <> NEW.tenant_id THEN
        RAISE EXCEPTION 'a user managed by a tenant is a member of that tenant only' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER membership_row_guard BEFORE INSERT OR UPDATE ON pandokos.membership
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER membership_guard BEFORE INSERT OR UPDATE ON pandokos.membership
    FOR EACH ROW EXECUTE FUNCTION pandokos.membership_guard();

ALTER TABLE pandokos.membership ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.membership
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.membership TO pandokos_admin;
GRANT INSERT (tenant_id, user_id, source) ON pandokos.membership TO pandokos_admin;
GRANT UPDATE (source) ON pandokos.membership TO pandokos_admin;
GRANT DELETE ON pandokos.membership TO pandokos_admin;

-- Roles of a member (tenant-scoped table). The role is the name of a built-in role; role_assignment
-- keeps the roles of service accounts, and the gateway role reads that table only.
CREATE TABLE pandokos.membership_role (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    user_id    uuid        NOT NULL,
    role       text        NOT NULL,
    source     text        NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT membership_role_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT membership_role_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT membership_role_membership_fkey FOREIGN KEY (tenant_id, user_id)
        REFERENCES pandokos.membership (tenant_id, user_id) ON DELETE CASCADE,
    CONSTRAINT membership_role_key UNIQUE (tenant_id, user_id, role),
    CONSTRAINT membership_role_role_format CHECK (role ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT membership_role_source_valid CHECK (source IN ('manual', 'idp'))
);

CREATE TRIGGER membership_role_row_guard BEFORE INSERT OR UPDATE ON pandokos.membership_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();

ALTER TABLE pandokos.membership_role ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership_role FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.membership_role
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.membership_role TO pandokos_admin;
GRANT INSERT (tenant_id, user_id, role, source) ON pandokos.membership_role TO pandokos_admin;
GRANT DELETE ON pandokos.membership_role TO pandokos_admin;

-- Which tenants a user is a member of (global table, no grant to any runtime role), kept by a
-- trigger and read only through user_tenants.
CREATE TABLE pandokos.membership_directory (
    user_id   uuid NOT NULL,
    tenant_id uuid NOT NULL,
    CONSTRAINT membership_directory_pkey PRIMARY KEY (user_id, tenant_id),
    CONSTRAINT membership_directory_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT membership_directory_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);

CREATE FUNCTION pandokos.membership_directory_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM pandokos.membership_directory d WHERE d.user_id = OLD.user_id AND d.tenant_id = OLD.tenant_id;
    ELSE
        INSERT INTO pandokos.membership_directory (user_id, tenant_id) VALUES (NEW.user_id, NEW.tenant_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER membership_directory_sync AFTER INSERT OR DELETE ON pandokos.membership
    FOR EACH ROW EXECUTE FUNCTION pandokos.membership_directory_sync();

-- The tenants of one user. It crosses the tenant policy for one fact, for one user, and only in
-- the platform scope (sign-in and GET /v1/me); roles are then read in each tenant's session.
CREATE FUNCTION pandokos.user_tenants(p_user_id uuid) RETURNS SETOF uuid
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'user_tenants needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT d.tenant_id FROM pandokos.membership_directory d WHERE d.user_id = p_user_id ORDER BY d.tenant_id;
END
$$;

REVOKE ALL ON FUNCTION pandokos.user_tenants(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.user_tenants(uuid) TO pandokos_admin;

-- Policies of app_user. The platform scope sees and writes every user (sign-in happens there); a
-- tenant's session sees and writes the users it manages and sees its members.
CREATE POLICY app_user_platform ON pandokos.app_user
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY app_user_managed ON pandokos.app_user
    FOR ALL TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY app_user_member ON pandokos.app_user
    FOR SELECT TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.membership m
                    WHERE m.tenant_id = (SELECT pandokos.current_tenant_id()) AND m.user_id = app_user.id));
-- The definer functions of this migration run as the owner, which the forced policies would
-- otherwise leave without any row.
CREATE POLICY app_user_owner ON pandokos.app_user FOR SELECT TO pandokos_owner USING (true);

-- Local accounts (global table with explicit policies): username and password hash of a user.
-- The hash is an Argon2id PHC string that carries its parameters and salt.
CREATE TABLE pandokos.local_account (
    user_id             uuid        NOT NULL,
    sign_in             text        NOT NULL DEFAULT 'local',
    username            text        NOT NULL,
    password_hash       text        NOT NULL,
    must_change         boolean     NOT NULL DEFAULT true,
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    password_expires_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT local_account_pkey PRIMARY KEY (user_id),
    CONSTRAINT local_account_user_fkey FOREIGN KEY (user_id, sign_in) REFERENCES pandokos.app_user (id, sign_in),
    CONSTRAINT local_account_sign_in_valid CHECK (sign_in = 'local'),
    CONSTRAINT local_account_username_key UNIQUE (username),
    CONSTRAINT local_account_username_format CHECK (username ~ '^[a-z0-9][a-z0-9._@+-]{1,62}[a-z0-9]$'),
    CONSTRAINT local_account_password_hash_format CHECK (
        password_hash ~ '^\$argon2id\$v=19\$m=[0-9]{1,8},t=[0-9]{1,3},p=[0-9]{1,3}\$[A-Za-z0-9+/]{22,86}\$[A-Za-z0-9+/]{43,86}$'),
    CONSTRAINT local_account_expiry_only_generated CHECK (must_change OR password_expires_at IS NULL)
);

CREATE TRIGGER local_account_guard BEFORE INSERT OR UPDATE ON pandokos.local_account
    FOR EACH ROW EXECUTE FUNCTION pandokos.user_row_guard();

ALTER TABLE pandokos.local_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.local_account FORCE ROW LEVEL SECURITY;

CREATE POLICY local_account_platform ON pandokos.local_account
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY local_account_managed ON pandokos.local_account
    FOR ALL TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.app_user u
                    WHERE u.id = local_account.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())))
    WITH CHECK (EXISTS (SELECT 1 FROM pandokos.app_user u
                         WHERE u.id = local_account.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY local_account_member ON pandokos.local_account
    FOR SELECT TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.membership m
                    WHERE m.tenant_id = (SELECT pandokos.current_tenant_id()) AND m.user_id = local_account.user_id));
CREATE POLICY local_account_owner ON pandokos.local_account FOR SELECT TO pandokos_owner USING (true);

GRANT SELECT ON pandokos.local_account TO pandokos_admin;
GRANT INSERT (user_id, username, password_hash, must_change, password_expires_at) ON pandokos.local_account TO pandokos_admin;
GRANT UPDATE (password_hash, must_change, password_changed_at, password_expires_at) ON pandokos.local_account TO pandokos_admin;

-- Federated identities (global table with explicit policies): the issuer and subject of the
-- identity provider that a user signs in with. Platform scope only.
CREATE TABLE pandokos.user_identity (
    user_id    uuid        NOT NULL,
    sign_in    text        NOT NULL DEFAULT 'oidc',
    issuer     text        NOT NULL,
    subject    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_identity_pkey PRIMARY KEY (user_id),
    CONSTRAINT user_identity_user_fkey FOREIGN KEY (user_id, sign_in) REFERENCES pandokos.app_user (id, sign_in),
    CONSTRAINT user_identity_sign_in_valid CHECK (sign_in = 'oidc'),
    CONSTRAINT user_identity_subject_key UNIQUE (issuer, subject),
    CONSTRAINT user_identity_issuer_format CHECK (issuer ~ '^https://[^[:space:][:cntrl:]?#@]+$' AND octet_length(issuer) <= 255),
    CONSTRAINT user_identity_subject_format CHECK (char_length(subject) BETWEEN 1 AND 255 AND subject !~ '[[:cntrl:]]')
);

CREATE TRIGGER user_identity_guard BEFORE INSERT OR UPDATE ON pandokos.user_identity
    FOR EACH ROW EXECUTE FUNCTION pandokos.user_row_guard();

ALTER TABLE pandokos.user_identity ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.user_identity FORCE ROW LEVEL SECURITY;

CREATE POLICY user_identity_platform ON pandokos.user_identity
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.user_identity TO pandokos_admin;
GRANT INSERT (user_id, issuer, subject) ON pandokos.user_identity TO pandokos_admin;

-- Sessions (global table with explicit policies). Only the SHA-256 of the token is stored. A row
-- changes in last_used_at only; a session ends by deletion.
CREATE TABLE pandokos.user_session (
    id           uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    token_hash   bytea       NOT NULL,
    user_id      uuid        NOT NULL,
    method       text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    CONSTRAINT user_session_pkey PRIMARY KEY (id),
    CONSTRAINT user_session_token_hash_key UNIQUE (token_hash),
    CONSTRAINT user_session_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT user_session_token_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT user_session_method_valid CHECK (method IN ('local', 'oidc')),
    CONSTRAINT user_session_lifetime CHECK (expires_at > created_at AND expires_at <= created_at + interval '7 days')
);
CREATE INDEX user_session_user_idx ON pandokos.user_session (user_id);
CREATE INDEX user_session_expires_idx ON pandokos.user_session (expires_at);

CREATE FUNCTION pandokos.user_session_stamp() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    NEW.id := pandokos.uuid_v7();
    NEW.created_at := now();
    NEW.last_used_at := now();
    RETURN NEW;
END
$$;

CREATE TRIGGER user_session_stamp BEFORE INSERT ON pandokos.user_session
    FOR EACH ROW EXECUTE FUNCTION pandokos.user_session_stamp();

ALTER TABLE pandokos.user_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.user_session FORCE ROW LEVEL SECURITY;

CREATE POLICY user_session_platform ON pandokos.user_session
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY user_session_managed_select ON pandokos.user_session
    FOR SELECT TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.app_user u
                    WHERE u.id = user_session.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY user_session_managed_delete ON pandokos.user_session
    FOR DELETE TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.app_user u
                    WHERE u.id = user_session.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));

GRANT SELECT ON pandokos.user_session TO pandokos_admin;
GRANT INSERT (token_hash, user_id, method, expires_at) ON pandokos.user_session TO pandokos_admin;
GRANT UPDATE (last_used_at) ON pandokos.user_session TO pandokos_admin;
GRANT DELETE ON pandokos.user_session TO pandokos_admin;

-- Platform roles of users (global table with explicit policies). Only a user that the platform
-- manages can hold one.
CREATE TABLE pandokos.platform_role_assignment (
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    user_id    uuid        NOT NULL,
    role       text        NOT NULL,
    source     text        NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_role_assignment_pkey PRIMARY KEY (id),
    CONSTRAINT platform_role_assignment_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT platform_role_assignment_key UNIQUE (user_id, role),
    CONSTRAINT platform_role_assignment_role_format CHECK (role ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT platform_role_assignment_source_valid CHECK (source IN ('manual', 'idp'))
);

CREATE FUNCTION pandokos.platform_role_assignment_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF (SELECT u.tenant_id FROM pandokos.app_user u WHERE u.id = NEW.user_id) IS NOT NULL THEN
        RAISE EXCEPTION 'a user managed by a tenant holds no platform role' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER platform_role_assignment_row_guard BEFORE INSERT OR UPDATE ON pandokos.platform_role_assignment
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER platform_role_assignment_guard BEFORE INSERT ON pandokos.platform_role_assignment
    FOR EACH ROW EXECUTE FUNCTION pandokos.platform_role_assignment_guard();

ALTER TABLE pandokos.platform_role_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_role_assignment FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_role_assignment_platform ON pandokos.platform_role_assignment
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_role_assignment TO pandokos_admin;
GRANT INSERT (user_id, role, source) ON pandokos.platform_role_assignment TO pandokos_admin;
GRANT DELETE ON pandokos.platform_role_assignment TO pandokos_admin;

-- The ID of a user that the platform manages, by ID or by username, or NULL: what a tenant's
-- session needs to add such a user as a member, and cannot see. It takes one ID or one username,
-- returns one ID, cannot list, and answers in a tenant's session only.
CREATE FUNCTION pandokos.platform_user_lookup(p_user_id uuid, p_username text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    RETURN (SELECT u.id
              FROM pandokos.app_user u
              LEFT JOIN pandokos.local_account a ON a.user_id = u.id
             WHERE u.tenant_id IS NULL
               AND pandokos.current_tenant_id() IS NOT NULL
               AND pg_catalog.num_nonnulls(p_user_id, p_username) = 1
               AND (u.id = p_user_id OR a.username = p_username));

REVOKE ALL ON FUNCTION pandokos.platform_user_lookup(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.platform_user_lookup(uuid, text) TO pandokos_admin;

-- Sign-in throttling (global table with explicit policies, platform scope only). The key is the
-- SHA-256 of the kind and of the lower-case username or of the source address prefix: a row
-- exists for a username whether or not an account has it.
CREATE TABLE pandokos.signin_throttle (
    key               bytea       NOT NULL,
    kind              text        NOT NULL,
    failures          integer     NOT NULL DEFAULT 0,
    window_started_at timestamptz NOT NULL DEFAULT now(),
    last_failure_at   timestamptz NOT NULL DEFAULT now(),
    locked_until      timestamptz,
    CONSTRAINT signin_throttle_pkey PRIMARY KEY (key),
    CONSTRAINT signin_throttle_key_length CHECK (octet_length(key) = 32),
    CONSTRAINT signin_throttle_kind_valid CHECK (kind IN ('account', 'source')),
    CONSTRAINT signin_throttle_failures_range CHECK (failures BETWEEN 0 AND 1000000)
);
CREATE INDEX signin_throttle_last_failure_idx ON pandokos.signin_throttle (last_failure_at);

ALTER TABLE pandokos.signin_throttle ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.signin_throttle FORCE ROW LEVEL SECURITY;

CREATE POLICY signin_throttle_platform ON pandokos.signin_throttle
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT, DELETE ON pandokos.signin_throttle TO pandokos_admin;
GRANT INSERT (key, kind, failures, window_started_at, last_failure_at, locked_until) ON pandokos.signin_throttle TO pandokos_admin;
GRANT UPDATE (failures, window_started_at, last_failure_at, locked_until) ON pandokos.signin_throttle TO pandokos_admin;

-- Sign-in transactions of the OIDC relying party (global table with explicit policies, platform
-- scope only). The row holds the SHA-256 of the state and nothing else: it makes a state usable
-- once and for a bounded time. Nonce and code verifier are never stored.
CREATE TABLE pandokos.oidc_login (
    state_hash bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CONSTRAINT oidc_login_pkey PRIMARY KEY (state_hash),
    CONSTRAINT oidc_login_state_hash_length CHECK (octet_length(state_hash) = 32),
    CONSTRAINT oidc_login_lifetime CHECK (expires_at > created_at AND expires_at <= created_at + interval '1 hour')
);
CREATE INDEX oidc_login_expires_idx ON pandokos.oidc_login (expires_at);

ALTER TABLE pandokos.oidc_login ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.oidc_login FORCE ROW LEVEL SECURITY;

CREATE POLICY oidc_login_platform ON pandokos.oidc_login
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT, DELETE ON pandokos.oidc_login TO pandokos_admin;
GRANT INSERT (state_hash, expires_at) ON pandokos.oidc_login TO pandokos_admin;

-- Idempotency keys of create operations (global table with explicit policies). A row belongs to
-- a tenant (operations under /v1/tenants/{tenant}) or to the platform (tenant_id NULL), and to one
-- principal. request_hash is SHA-256(salt || operation || path || body). The stored response never
-- holds a member that is shown once.
CREATE TABLE pandokos.idempotency_key (
    id              uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    tenant_id       uuid,
    principal_kind  text        NOT NULL,
    principal_id    text        NOT NULL,
    key             text        NOT NULL,
    operation       text        NOT NULL,
    salt            bytea       NOT NULL,
    request_hash    bytea       NOT NULL,
    attempt         integer     NOT NULL DEFAULT 1,
    locked_until    timestamptz NOT NULL,
    committed_at    timestamptz,
    resource_type   text        NOT NULL DEFAULT '',
    resource_id     text        NOT NULL DEFAULT '',
    response_status integer,
    response_body   jsonb,
    completed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    CONSTRAINT idempotency_key_pkey PRIMARY KEY (id),
    CONSTRAINT idempotency_key_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT idempotency_key_principal_kind_valid CHECK (principal_kind IN ('platform', 'service_account', 'user')),
    CONSTRAINT idempotency_key_principal_id_format CHECK (principal_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    CONSTRAINT idempotency_key_key_format CHECK (key ~ '^[\x21-\x7e]{1,128}$'),
    CONSTRAINT idempotency_key_operation_format CHECK (operation ~ '^[A-Za-z][A-Za-z0-9]{0,63}$'),
    CONSTRAINT idempotency_key_salt_length CHECK (octet_length(salt) = 16),
    CONSTRAINT idempotency_key_request_hash_length CHECK (octet_length(request_hash) = 32),
    CONSTRAINT idempotency_key_attempt_positive CHECK (attempt >= 1),
    CONSTRAINT idempotency_key_response_complete CHECK (
        (completed_at IS NULL AND response_status IS NULL AND response_body IS NULL)
        OR (completed_at IS NOT NULL AND committed_at IS NOT NULL AND response_status BETWEEN 200 AND 299
            AND jsonb_typeof(response_body) = 'object' AND pg_column_size(response_body) <= 262144)),
    CONSTRAINT idempotency_key_resource_format CHECK (resource_type ~ '^[a-z_]{0,64}$' AND char_length(resource_id) <= 64)
);
CREATE UNIQUE INDEX idempotency_key_key ON pandokos.idempotency_key
    ((COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid)), principal_kind, principal_id, key);
CREATE INDEX idempotency_key_expires_idx ON pandokos.idempotency_key (expires_at);

ALTER TABLE pandokos.idempotency_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.idempotency_key FORCE ROW LEVEL SECURITY;

CREATE POLICY idempotency_key_tenant ON pandokos.idempotency_key
    FOR ALL TO pandokos_admin
    USING (tenant_id IS NOT NULL AND tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id IS NOT NULL AND tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY idempotency_key_platform ON pandokos.idempotency_key
    FOR ALL TO pandokos_admin
    USING (tenant_id IS NULL AND (SELECT pandokos.platform_scope()))
    WITH CHECK (tenant_id IS NULL AND (SELECT pandokos.platform_scope()));

GRANT SELECT, DELETE ON pandokos.idempotency_key TO pandokos_admin;
GRANT INSERT (tenant_id, principal_kind, principal_id, key, operation, salt, request_hash, locked_until, expires_at)
    ON pandokos.idempotency_key TO pandokos_admin;
GRANT UPDATE (attempt, locked_until, committed_at, resource_type, resource_id, response_status, response_body, completed_at)
    ON pandokos.idempotency_key TO pandokos_admin;

-- The host commands (pandokos user reset-password, unlock) record what they did to a user that a
-- tenant manages in that tenant's log too.
ALTER TABLE pandokos.audit_event DROP CONSTRAINT audit_event_actor_type_valid;
ALTER TABLE pandokos.audit_event ADD CONSTRAINT audit_event_actor_type_valid
    CHECK (actor_type IN ('platform', 'service_account', 'user', 'anonymous', 'host'));

-- Tokens of trusted issuers on the administration listener (tokens.admin_listener): the admin role
-- selects a registration and the tenant of a platform-level token as the gateway role does.
GRANT EXECUTE ON FUNCTION pandokos.issuer_lookup(text, text[]) TO pandokos_admin;
GRANT EXECUTE ON FUNCTION pandokos.platform_issuer_tenant_lookup(uuid, text) TO pandokos_admin;
```

### Appendix A2 — queries

Conventions of `internal/store/queries/sql` apply (sqlc annotations, every name schema-qualified,
`$n` parameters, `::text` for UUIDs returned). The statements whose exact form matters are given;
the others are plain single-table statements named in the list.

`sessions.sql`:

```sql
-- name: GetSessionPrincipal :one
-- Platform scope. One statement per request with a session cookie (§5.6).
SELECT s.id::text, s.user_id::text, s.method, s.created_at, s.last_used_at, s.expires_at,
       u.sign_in, u.state, u.display_name, u.tenant_id::text AS managing_tenant_id,
       COALESCE(a.username, '') AS username, COALESCE(a.must_change, false) AS must_change,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.platform_role_assignment r
                  WHERE r.user_id = u.id), '{}')::text[] AS platform_roles
  FROM pandokos.user_session s
  JOIN pandokos.app_user u ON u.id = s.user_id
  LEFT JOIN pandokos.local_account a ON a.user_id = u.id
 WHERE s.token_hash = $1;

-- name: TouchSession :exec
UPDATE pandokos.user_session SET last_used_at = now()
 WHERE id = $1 AND last_used_at < now() - interval '1 minute';

-- name: GetMemberPrincipal :one
-- The tenant's session. No row: the user is not a member (404).
SELECT t.name AS tenant_name, t.state AS tenant_state,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.membership_role r
                  WHERE r.user_id = m.user_id), '{}')::text[] AS roles
  FROM pandokos.membership m
  JOIN pandokos.tenant t ON t.id = m.tenant_id
 WHERE m.user_id = $1;

-- name: DeleteOldestSessions :exec
-- Keeps the $2 most recent sessions of the user.
DELETE FROM pandokos.user_session
 WHERE id IN (SELECT id FROM pandokos.user_session WHERE user_id = $1
               ORDER BY created_at DESC, id DESC OFFSET $2);

-- name: PurgeSessions :execrows
DELETE FROM pandokos.user_session
 WHERE expires_at <= now() OR last_used_at <= now() - sqlc.arg(idle)::interval;
```

Also: `CreateSession :one` (returns the row), `DeleteSession :execrows` (by ID),
`DeleteSessionOfUser :execrows` (by ID and user), `DeleteSessionsOfUser :execrows` (by user, with
an optional ID to keep), `ListSessionsOfUser :many`.

`users.sql`:

```sql
-- name: GetAccountForSignIn :one
-- Platform scope.
SELECT u.id::text, u.state, u.tenant_id::text AS managing_tenant_id, u.display_name,
       a.username, a.password_hash, a.must_change, a.password_expires_at
  FROM pandokos.local_account a JOIN pandokos.app_user u ON u.id = a.user_id
 WHERE a.username = $1;

-- name: GetThrottle :many
SELECT key, kind, failures, window_started_at, last_failure_at, locked_until, now() AS now
  FROM pandokos.signin_throttle WHERE key = ANY (sqlc.arg(keys)::bytea[]);

-- name: CountFailure :one
-- One failure for one key; the window restarts when it has passed; the lock is set when the
-- count reaches the limit. Returns the count and whether this call locked the key.
INSERT INTO pandokos.signin_throttle AS t (key, kind, failures, window_started_at, last_failure_at)
VALUES (sqlc.arg(key), sqlc.arg(kind), 1, now(), now())
ON CONFLICT (key) DO UPDATE SET
    failures          = CASE WHEN t.window_started_at <= now() - sqlc.arg(failure_window)::interval THEN 1 ELSE t.failures + 1 END,
    window_started_at = CASE WHEN t.window_started_at <= now() - sqlc.arg(failure_window)::interval THEN now() ELSE t.window_started_at END,
    last_failure_at   = now(),
    locked_until      = CASE WHEN t.window_started_at > now() - sqlc.arg(failure_window)::interval
                                  AND t.failures + 1 >= sqlc.arg(max_failures)::integer
                                  AND (t.locked_until IS NULL OR t.locked_until <= now())
                             THEN now() + sqlc.arg(lock_duration)::interval ELSE t.locked_until END
RETURNING failures, (locked_until IS NOT NULL AND locked_until = now() + sqlc.arg(lock_duration)::interval) AS locked_now;

-- name: PurgeThrottle :execrows
DELETE FROM pandokos.signin_throttle
 WHERE last_failure_at <= now() - sqlc.arg(keep)::interval AND (locked_until IS NULL OR locked_until <= now());

-- name: UserTenants :many
SELECT pandokos.user_tenants($1)::text AS tenant_id;

-- name: LookupPlatformUser :one
SELECT COALESCE(pandokos.platform_user_lookup(sqlc.narg(user_id), sqlc.narg(username))::text, '') AS user_id;

-- name: TakeOidcLogin :execrows
DELETE FROM pandokos.oidc_login WHERE state_hash = $1 AND expires_at > now();
```

Also, each a single statement on one table (two for the lists that join `local_account`):
`CreateUser`, `GetUser`, `ListUsers` (platform-managed only: `tenant_id IS NULL`; filters `state`,
`username`; the cursor and sort of the other collections: `created_at`, `username`),
`UpdateUser`, `SetUserState`, `CreateLocalAccount`, `SetPassword` (hash, `must_change`, expiry,
`password_changed_at = now()`), `UpgradePasswordHash` (hash only, `WHERE password_hash =
old`), `CreateUserIdentity`, `GetUserByIdentity`, `ListPlatformRoles`, `AddPlatformRole`,
`RemovePlatformRole`, `CountPlatformRoleHolders` (active users holding a role), `CreateMembership`,
`GetMember`, `ListMembers`, `DeleteMembership`, `SetMembershipSource`, `AddMembershipRole`,
`RemoveMembershipRole`, `ListMembershipRoles`, `DeleteThrottle` (by key), `CreateOidcLogin`,
`CountOidcLogins` (live), `PurgeOidcLogins`, `TenantIDsByName`.

`idempotency.sql`:

```sql
-- name: ClaimIdempotencyKey :one
-- In the scope of the operation. No row returned: the key exists (read it with GetIdempotencyKey).
INSERT INTO pandokos.idempotency_key (tenant_id, principal_kind, principal_id, key, operation, salt, request_hash, locked_until, expires_at)
VALUES (sqlc.narg(tenant_id), $1, $2, $3, $4, $5, $6, now() + interval '60 seconds', now() + sqlc.arg(retention)::interval)
ON CONFLICT DO NOTHING
RETURNING id::text, attempt;

-- name: GetIdempotencyKey :one
SELECT id::text, operation, salt, request_hash, attempt, locked_until > now() AS locked,
       committed_at IS NOT NULL AS committed, completed_at IS NOT NULL AS completed,
       response_status, response_body, expires_at <= now() AS expired
  FROM pandokos.idempotency_key
 WHERE COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid)
       = COALESCE(sqlc.narg(tenant_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
   AND principal_kind = $1 AND principal_id = $2 AND key = $3
   FOR UPDATE;

-- name: TakeOverIdempotencyKey :one
UPDATE pandokos.idempotency_key SET attempt = attempt + 1, locked_until = now() + interval '60 seconds'
 WHERE id = $1 AND committed_at IS NULL AND locked_until <= now()
RETURNING attempt;

-- name: MarkIdempotencyKeyCommitted :execrows
-- In the transaction of the create. Zero rows: the attempt was superseded; the caller fails.
UPDATE pandokos.idempotency_key SET committed_at = now(), resource_type = $3, resource_id = $4
 WHERE id = $1 AND attempt = $2 AND committed_at IS NULL;

-- name: CompleteIdempotencyKey :exec
UPDATE pandokos.idempotency_key SET response_status = $2, response_body = $3, completed_at = now()
 WHERE id = $1 AND committed_at IS NOT NULL AND completed_at IS NULL;

-- name: ReleaseIdempotencyKey :exec
DELETE FROM pandokos.idempotency_key WHERE id = $1 AND attempt = $2 AND committed_at IS NULL;

-- name: PurgeIdempotencyKeys :execrows
DELETE FROM pandokos.idempotency_key WHERE expires_at <= now();
```

An expired row found by `GetIdempotencyKey` is deleted and the claim repeated once.
`PurgeIdempotencyKeys` runs in the platform scope and in each tenant's session reached by the
purge (it lists tenants in the platform scope, as the worker's jobs do, one transaction each).

### Appendix A3 — OpenAPI changes (`api/openapi/admin.v1.yaml`)

`info.description` names the three credentials. `security: [{key: []}, {session: []}]`;
`securitySchemes.session: {type: apiKey, in: cookie, name: __Host-pandokos_session}`. Tags
`sign-in`, `users`, `members`. `PrincipalType` gains `user`. `ProblemCode` gains the seven codes
of §5.14.

**Parameters**

```yaml
    User:
      {name: user, in: path, required: true, description: User ID., schema: {$ref: '#/components/schemas/ID'}}
    Session:
      {name: session, in: path, required: true, description: Session ID., schema: {$ref: '#/components/schemas/ID'}}
    IdempotencyKey:
      name: Idempotency-Key
      in: header
      description: >-
        Makes a retry of this request safe. A second request with the same key, by the same
        principal, on the same operation and body returns the first response (header
        Idempotency-Replayed) without the members that are shown once.
      schema: {type: string, pattern: '^[\x21-\x7e]{1,128}$'}
```

**Responses** (problem responses use the schema `Problem`, as the existing ones):
`TooManyAttempts` (429, codes `too_many_attempts`; header `Retry-After`),
`IdempotencyKeyReused` (422). The description of `Forbidden` adds `password_change_required`,
`csrf_rejected`, `sign_in_disabled`; of `Conflict` adds `idempotency_in_progress`,
`idempotency_response_lost`. One JSON response per new schema that an operation returns, named
after it. Every operation with `IdempotencyKey` gains `"422": IdempotencyKeyReused`. Operations
reachable with a session list 403 already.

**Schemas** (all objects `additionalProperties: false`; `ID`, `Name`, `TenantRef`, `Reason` exist)

```yaml
    SignInMethod: {type: string, enum: [local, oidc]}
    UserState: {type: string, enum: [active, disabled]}
    RoleSource: {type: string, enum: [manual, idp]}
    Username: {type: string, pattern: '^[a-z0-9][a-z0-9._@+-]{1,62}[a-z0-9]$'}
    DisplayName: {type: string, maxLength: 200}
    Email: {type: string, maxLength: 254}
    RoleName: {type: string, pattern: '^[a-z][a-z0-9-]{0,62}$'}
    AuthMethods:
      type: object
      required: [local, oidc]
      properties:
        local: {type: object, additionalProperties: false, required: [enabled], properties: {enabled: {type: boolean}}}
        oidc:
          type: object
          additionalProperties: false
          required: [enabled]
          properties:
            enabled: {type: boolean}
            display_name: {type: string}
            start_path: {type: string, description: 'Navigate the browser here: /v1/auth/oidc/start.'}
    SignInRequest:
      type: object
      required: [username, password]
      properties:
        username: {type: string, minLength: 1, maxLength: 64, description: Compared without regard to case.}
        password: {type: string, minLength: 1, maxLength: 1024}
    PasswordChange:
      type: object
      required: [current_password, new_password]
      properties:
        current_password: {type: string, minLength: 1, maxLength: 1024}
        new_password: {type: string, minLength: 1, maxLength: 1024}
    SessionUser:
      type: object
      required: [id, sign_in, display_name]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        sign_in: {$ref: '#/components/schemas/SignInMethod'}
        username: {$ref: '#/components/schemas/Username'}
        display_name: {$ref: '#/components/schemas/DisplayName'}
    Session:
      type: object
      required: [id, user, method, must_change_password, current, created_at, last_used_at, expires_at, idle_timeout_seconds]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        user: {$ref: '#/components/schemas/SessionUser'}
        method: {$ref: '#/components/schemas/SignInMethod'}
        must_change_password: {type: boolean}
        current: {type: boolean, description: True for the session of the request.}
        created_at: {type: string, format: date-time}
        last_used_at: {type: string, format: date-time}
        expires_at: {type: string, format: date-time}
        idle_timeout_seconds: {type: integer}
    SessionList:
      type: object
      required: [items]
      properties:
        items: {type: array, items: {$ref: '#/components/schemas/Session'}}
    SignedOut: {type: object, required: [signed_out], properties: {signed_out: {type: boolean}}}
    SessionsRevoked: {type: object, required: [revoked], properties: {revoked: {type: integer}}}
    User:
      type: object
      required: [id, sign_in, display_name, email, state, state_changed_at, platform_roles, created_at, updated_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        sign_in: {$ref: '#/components/schemas/SignInMethod'}
        username: {$ref: '#/components/schemas/Username'}
        subject: {type: string, description: Subject at the identity provider; federated users only.}
        display_name: {$ref: '#/components/schemas/DisplayName'}
        email: {$ref: '#/components/schemas/Email'}
        state: {$ref: '#/components/schemas/UserState'}
        state_changed_at: {type: string, format: date-time}
        must_change_password: {type: boolean}
        locked_until: {type: string, format: date-time, description: Present while sign-in is locked.}
        platform_roles: {type: array, items: {$ref: '#/components/schemas/RoleName'}}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    UserCreate:
      type: object
      required: [sign_in]
      properties:
        sign_in: {$ref: '#/components/schemas/SignInMethod'}
        username: {$ref: '#/components/schemas/Username'}
        subject: {type: string, minLength: 1, maxLength: 255}
        display_name: {$ref: '#/components/schemas/DisplayName'}
        email: {$ref: '#/components/schemas/Email'}
        platform_roles: {type: array, maxItems: 8, uniqueItems: true, items: {$ref: '#/components/schemas/RoleName'}}
    UserCreated:
      type: object
      required: [user]
      properties:
        user: {$ref: '#/components/schemas/User'}
        initial_password: {type: string, description: 'Shown once, for a local account; absent on an idempotent replay.'}
    UserUpdate:
      type: object
      properties:
        display_name: {$ref: '#/components/schemas/DisplayName'}
        email: {$ref: '#/components/schemas/Email'}
        platform_roles: {type: array, maxItems: 8, uniqueItems: true, items: {$ref: '#/components/schemas/RoleName'}}
    PasswordReset:
      type: object
      required: [user_id, password, expires_at]
      properties:
        user_id: {$ref: '#/components/schemas/ID'}
        password: {type: string, description: Shown once. It must be changed at the next sign-in.}
        expires_at: {type: string, format: date-time}
    UserList: {type: object, required: [items], properties: {items: {type: array, items: {$ref: '#/components/schemas/User'}}, next_cursor: {type: string}}}
    Member:
      type: object
      required: [user_id, sign_in, display_name, email, state, managed_by, roles, source, created_at, updated_at]
      properties:
        user_id: {$ref: '#/components/schemas/ID'}
        sign_in: {$ref: '#/components/schemas/SignInMethod'}
        username: {$ref: '#/components/schemas/Username'}
        display_name: {$ref: '#/components/schemas/DisplayName'}
        email: {$ref: '#/components/schemas/Email'}
        state: {$ref: '#/components/schemas/UserState'}
        managed_by: {type: string, enum: [tenant, platform]}
        must_change_password: {type: boolean, description: Members the tenant manages only.}
        locked_until: {type: string, format: date-time, description: Members the tenant manages only.}
        roles: {type: array, items: {$ref: '#/components/schemas/RoleName'}}
        source: {$ref: '#/components/schemas/RoleSource'}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    MemberCreate:
      type: object
      required: [account, roles]
      properties:
        account: {type: string, enum: [new, existing]}
        username: {$ref: '#/components/schemas/Username'}
        user_id: {$ref: '#/components/schemas/ID'}
        display_name: {$ref: '#/components/schemas/DisplayName'}
        email: {$ref: '#/components/schemas/Email'}
        roles: {type: array, minItems: 1, maxItems: 8, uniqueItems: true, items: {$ref: '#/components/schemas/RoleName'}}
    MemberCreated:
      type: object
      required: [member]
      properties:
        member: {$ref: '#/components/schemas/Member'}
        initial_password: {type: string, description: 'Shown once, for a new account; absent on an idempotent replay.'}
    MemberUpdate:
      type: object
      properties:
        roles: {type: array, minItems: 1, maxItems: 8, uniqueItems: true, items: {$ref: '#/components/schemas/RoleName'}}
        display_name: {$ref: '#/components/schemas/DisplayName'}
        email: {$ref: '#/components/schemas/Email'}
    MemberList: {type: object, required: [items], properties: {items: {type: array, items: {$ref: '#/components/schemas/Member'}}, next_cursor: {type: string}}}
    Membership:
      type: object
      required: [tenant, roles]
      properties:
        tenant: {$ref: '#/components/schemas/TenantRef'}
        roles: {type: array, items: {$ref: '#/components/schemas/RoleName'}}
```

`Me` gains the optional members `must_change_password` (boolean), `session` (`Session`) and
`memberships` (array of `Membership`), present for `principal_type: user`; `key_id` is empty and
`bootstrap` false for a user. In `TenantCreated`, `VirtualKeyCreated` and `PlatformKeyCreated` the
member that carries the key leaves `required` (D-16), with the description "absent on an
idempotent replay".

**Operations.** Standard problem responses as the neighbouring operations (400, 401, 403, 404,
409, 413, 415, 500, 503 where they can occur). `K` marks the `IdempotencyKey` parameter.

| operationId | Method and path | Permission | Request | Success |
|---|---|---|---|---|
| `getAuthMethods` | `GET /v1/auth/methods` | public | — | 200 `AuthMethods` |
| `signIn` | `POST /v1/auth/sign-in` | public | `SignInRequest` | 200 `Session` (header `Set-Cookie`); 401, 403, 429, 503 |
| `startOidcSignIn` | `GET /v1/auth/oidc/start` | public | — | 303; 403, 429, 503 |
| `completeOidcSignIn` | `GET /v1/auth/oidc/callback` | public, lenient query; declared: `code`, `state`, `error`, `iss` (strings, at most 4096) | — | 303; 403 |
| `signOut` | `POST /v1/auth/sign-out` | `session:self` | — | 200 `SignedOut` |
| `changePassword` | `POST /v1/auth/password` | `session:self` | `PasswordChange` | 200 `Session`; 429 |
| `listOwnSessions` | `GET /v1/auth/sessions` | `session:self` | — | 200 `SessionList` |
| `deleteOwnSession` | `DELETE /v1/auth/sessions/{session}` | `session:self` | — | 200 `Session` |
| `listUsers` | `GET /v1/users` | `user:read` | `limit`, `cursor`, `sort` (`created_at`, `username`), `state`, `username` | 200 `UserList` |
| `createUser` | `POST /v1/users` K | `user:write` | `UserCreate` | 201 `UserCreated` |
| `getUser` | `GET /v1/users/{user}` | `user:read` | — | 200 `User` |
| `updateUser` | `PATCH /v1/users/{user}` | `user:write` | `UserUpdate` | 200 `User` |
| `disableUser`, `enableUser`, `unlockUser` | `POST /v1/users/{user}/disable`, `/enable`, `/unlock` | `user:write` | `Reason` (optional) | 200 `User` |
| `resetUserPassword` | `POST /v1/users/{user}/reset-password` | `user:write` | `Reason` (optional) | 200 `PasswordReset` |
| `revokeUserSessions` | `POST /v1/users/{user}/revoke-sessions` | `user:write` | `Reason` (optional) | 200 `SessionsRevoked` |
| `listMembers` | `GET /v1/tenants/{tenant}/members` | `member:read` | `limit`, `cursor`, `sort` (`created_at`), `username` | 200 `MemberList` |
| `createMember` | `POST /v1/tenants/{tenant}/members` K | `member:write` | `MemberCreate` | 201 `MemberCreated` |
| `getMember` | `GET /v1/tenants/{tenant}/members/{user}` | `member:read` | — | 200 `Member` |
| `updateMember` | `PATCH /v1/tenants/{tenant}/members/{user}` | `member:write` | `MemberUpdate` | 200 `Member` |
| `deleteMember` | `DELETE /v1/tenants/{tenant}/members/{user}` | `member:write` | — | 200 `Member` |
| `disableMember`, `enableMember`, `unlockMember` | `POST …/members/{user}/disable`, `/enable`, `/unlock` | `member:write` | `Reason` (optional) | 200 `Member` |
| `resetMemberPassword` | `POST …/members/{user}/reset-password` | `member:write` | `Reason` (optional) | 200 `PasswordReset` |
| `revokeMemberSessions` | `POST …/members/{user}/revoke-sessions` | `member:write` | `Reason` (optional) | 200 `SessionsRevoked` |

Every existing operation whose success status is 201 gains the parameter `IdempotencyKey` and the
422 response; list them with `grep -n '"201"' api/openapi/admin.v1.yaml`.

## Part B — Tests first (commit: `test: sprint 4.2 tests, failing`)

All tests of §9, under the stated names, failing for the right reason (an assertion, not a
compile error, wherever the stubs of A7 allow). The changed tests of §9 are changed here too.

### B1. Helpers

- **`test/fakeidp`** (§7): `Options.Users` (subject → claims), `Server.AuthorizeURL()`,
  `TokenURL()`, discovery with `authorization_endpoint` and `token_endpoint`; `/authorize`
  validates `client_id`, `redirect_uri`, `response_type=code`, `code_challenge_method=S256` and
  redirects with `code` and the received `state` for `Server.SignInAs(subject)`; `/token`
  validates client authentication (basic and post), the code (one use, 60 s), `redirect_uri` and
  the verifier, and returns `id_token`, `access_token`, `refresh_token`, `token_type`. Faults:
  `IDTokenClaims(func(Claims) Claims)`, `IDTokenAlg`, `SignIDTokenWith(key)`, `TokenStatus`,
  `TokenBody`, `TokenDelay`, `TokenRedirect`, `CallbackError`, `CallbackIss`. Counters
  `TokenRequests()`. Client ID and secret are generated by `Start` and read with accessors.
- **`test/apitest`**: `Browser` (cookie jar that keeps `Secure` cookies for the test origin, the
  CSRF header, `Origin`, no automatic redirects) with `SignIn`, `Do`, `Navigate` (follows 303 by
  hand, returning each hop); `NewPassword()` (random, 20 characters); `Canary(kind)`.
- **`test/dbtest`**: seeds `PlatformUser`, `TenantUser`, `Member` that insert rows as the admin
  role with a hash made by `internal/password` at the floor parameters (`m=19456,t=2,p=1`), so
  that suites stay fast; `FailureRows`, `AuditRows` readers that run as the owner with the tenant
  set.
- A fake clock is passed through the `Now` options that already exist; `internal/account`,
  `internal/session` and `internal/idem` take `Now func() time.Time`. Where SQL uses `now()`, the
  tests move rows' timestamps as the owner instead of the clock (the helper `dbtest.Age`).

### B2. Where each test lives

| Package | Tests |
|---|---|
| `internal/password` | T-766 to T-770 |
| `internal/session` | T-771, T-772, T-797 |
| `internal/store`, `internal/store/migrations` | T-773 to T-776 |
| `test/isolation` | T-777 to T-780, T-825, T-826 |
| `internal/account` | T-781 to T-784, T-786, T-787 |
| `internal/admin` | T-785, T-788 to T-796, T-798 to T-804, T-814 to T-817, T-819 to T-821, T-823, T-837 (I) |
| `internal/authz` | T-805, T-806 |
| `internal/oidc`, `internal/jwks` | T-807 to T-813 |
| `internal/idem` | T-818 |
| `internal/adminapi`, `internal/apigen` | T-822 |
| `internal/authn` | T-824 |
| `internal/cli` | T-827 |
| `cmd/pandokos` | T-828, T-829 |
| `internal/config` | T-830 |
| `test/e2e` | T-831 to T-839 |
| `test/idp` (tag `idp`) | T-840 to T-843 (they fail until part D changes the realm) |
| `test/browser` (tag `browser`) | T-844, T-845 |
| `test/repo` | T-R53 to T-R58 |

Notes that the table of §9 does not carry:

- T-782 counts hash computations through an unexported hook of `internal/password` set by
  `export_test.go`; it does not measure time.
- T-788 and T-798 iterate `adminapi.Operations`; a new operation is covered without an edit.
- T-819 builds the request of each 201 operation from the call table of `test/apitest/calls.go`,
  extended with the two new creates; an operation missing from the table fails the test.
- T-837 plants: the typed password, a wrong password, the generated and the new password, the
  PHC string read from the database, the session token, the seed, the code, the provider's three
  tokens, the client secret and a username that is itself a canary; it searches the process
  output at `debug`, every response except the documented one, all audit rows (as owner), the
  rows of `idempotency_key`, `signin_throttle` and `oidc_login`, exported spans and CLI output.
- T-844 and T-845: `test/browser/page/index.html` is the minimal page (one script, `fetch` only,
  credentials from the URL fragment, result written to `<pre id="result">`);
  `test/browser/page/attacker.html` is the other site's page. The test starts the binary with a
  loopback admin listener, a `httputil.ReverseProxy` on `127.0.0.1` serving the page at `/` and
  the API at `/v1/` (`auth.public_origin` = that origin), a second server on `localhost` for the
  attacker page, and runs `google-chrome --headless --disable-gpu --no-sandbox
  --virtual-time-budget=10000 --dump-dom URL`. Without the binary `google-chrome` (or
  `PANDOKOS_TEST_CHROME`) the test fails.

Verification of part B:

```
go vet ./... && go vet -tags idp ./test/idp/... && go vet -tags browser ./test/browser/...
go test ./... -count=1 2>&1 | grep -c '^--- FAIL'     # only tests of §9 fail; record the list in the commit message
```

## Part C — Implementation

Order: local accounts and sessions end to end (C1 to C6), then OIDC (C7, C8), tokens (C9),
idempotency (C10). After C6 the first four exit conditions pass.

### C1. Passwords — `feat: Argon2id password hashing, policy and generator`

Files: `internal/password/{password,phc,policy,generate}.go`, `internal/config/{auth,validate}.go`
(the `auth.local.argon2.*`, `password_min_length`, `max_concurrent_hashes` rules).

- `argon2.IDKey(password, salt, t, m, p, 32)`; PHC encode and a strict parser (exact field order,
  `v=19`, unpadded standard base64, bounds before any allocation). `Verify` compares with
  `subtle.ConstantTimeCompare`. A buffered channel of `maxConcurrent` slots is the bound; waiting
  ends with the context (`ErrBusy`); callers give a 2 s context.
- `Dummy` verifies against a hash computed once in `New` from a random password.
- `Check`: the rules of §5.4 in this order: too short, too long, equals the username; sentences
  `must have at least N characters`, `must have at most 256 characters`, `must not be the
  username`.

Passes: T-766 to T-770, T-R53, the `auth.local` part of T-830. Verify:
`go test ./internal/password/... ./test/repo/... -run 'Password|Argon2|Dependencies|Cryptography' -count=1 && make fuzz FUZZTIME=20s`.

### C2. Principal model — `feat: users as principals: roles by scope, self permissions`

Files: `internal/authz/authz.go`, `internal/audit/audit.go` (`ActorUser`; `ActorOf` for
`KindUser`), `internal/admin/me.go`, the permission reference generator and `docs/reference/permissions.md`.

- `Effective` and `Decide` as §5.3; `Assignable(p, role, scope)`: the role exists with that
  scope, is one a user can hold (`tenant-admin`, `platform-admin`), and every permission of it is
  in `p.Effective()`.

Passes: T-805, T-806 and the unchanged authorization matrix of 1.3.
Verify: `go test ./internal/authz/... ./internal/audit/... ./test/isolation/... -run 'Authz|Permission|Decide|TenantAdmin' -count=1`.

### C3. Sessions and the hook — `feat: sessions on the administration listener; origin and forgery checks`

Files: `internal/session/*`, `internal/admin/{admin,origin,problem,authfail}.go`,
`internal/config/validate.go` (`auth.public_origin`, `auth.session.*`,
`listeners.admin.trusted_proxies`), `cmd/pandokos/serve.go` (options, the purge ticker).

- `admin.Options` gains `Sessions *session.Store`, `Accounts *account.Service`, `OIDC
  *oidc.Provider` (nil when off), `Idempotency *idem.Store`, `Origin OriginOptions{Public string;
  TLS, Loopback bool; TrustedProxies []netip.Prefix}`.
- `reqState` gains `header http.Header` (headers and cookies a handler or the hook adds; the
  status recorder copies them before the status line) and `session *session.Session`. Handlers
  use `setCookie(ctx, c)`.
- The outer handler adds the three security headers of §5.6.
- `before` follows §5.1: public operations return after the origin rule (and, for `signIn`, the
  checks); the `Authorization` path is the existing code; the session path runs the checks of
  §5.7, `Sessions.Lookup`, clears the cookie on `ErrNoSession`, builds the principal by scope
  (`GetMemberPrincipal` in the tenant's session; no row is `errNotFound`), applies the
  must-change gate, then `Decide`. `TouchSession` runs after a successful `Decide`, in its own
  short transaction; its failure is logged and does not fail the request.
- `expectedOrigin(r)` and `sourceAddr(r)` in `origin.go`, pure functions of the request and the
  options.
- `fail` maps the new errors to the codes of §5.14 and writes `Retry-After` for
  `*retryError{code, after}`.
- The purge: `serve.go` starts, with the admin role, a ticker of 5 minutes with ±30 s of jitter
  that calls `Sessions.Purge`, `Accounts.Purge` (throttle, sign-in transactions) and
  `Idempotency.Purge`; errors are logged with their class only; it stops at shutdown.

Passes: T-771, T-772, T-785, T-791 to T-794 (with seeded sessions), T-797 to T-801.
Verify: `go test ./internal/session/... ./internal/admin/... -run 'Session|CSRF|Origin|Transport|SecurityHeaders|Precedence|SourceAddress|Purge' -count=1`.

### C4. Local sign-in — `feat: sign-in with a local account: throttling, lock, forced change`

Files: `internal/account/{account,signin,throttle,reset}.go`, `internal/admin/auth.go` (`signIn`,
`signOut`, `changePassword`, `listOwnSessions`, `deleteOwnSession`, `getAuthMethods`).

- `SignIn` is §5.5, steps 2 to 6, in three transactions of the platform scope: read (throttle
  rows, account); after the hash, either `CountFailure` for both keys with the audit rows, or the
  success writes (throttle row deleted, hash upgrade, sessions, `auth.sign_in`). The hash is
  never computed inside a transaction.
- Audit of a failure goes through the existing `failureLimiter`; `user.lock` does not. A row of a
  tenant-managed user is written with `WithTenant(managing tenant)`.
- `ChangePassword` is §5.4; its throttle key is the account key of the user's username.
- The handler turns `*account.Refusal` into `401`, `ErrThrottled` into `429` with `Retry-After`
  rounded up to a second and at most the lock duration, `password.ErrBusy` into `503` with
  `Retry-After: 1`.

Passes: T-781 to T-784, T-786, T-787, T-788, T-790, T-793 to T-796 (complete), T-804.
Verify: `go test ./internal/account/... ./internal/admin/... -run 'SignIn|Throttle|Password|MustChange|SignOut|Lifetimes|Disable|Revoke' -count=1`.

### C5. Users and members — `feat: platform users and tenant members in the administration API`

Files: `internal/admin/{users,members}.go`, `internal/admin/convert.go`, `internal/cli/users.go`,
`internal/cli/commands.go`, `docs/reference/admin-api.md` (command table).

- One transaction per operation, in the platform scope for users and in the tenant's session for
  members, with the audit row of §5.14 inside it. `createMember` with `account: existing` calls
  `LookupPlatformUser` first.
- `locked_until` of a response is read from the account's throttle row; in a tenant's session
  that table is not visible, so `Member.locked_until` is filled by a read in the platform scope
  after the transaction, for members the tenant manages only.
- `unlock` and a password reset delete the throttle row in a transaction of the platform scope
  (a tenant's session cannot see that table) and audit where the user is managed.
- CLI: commands of §5.12; `--password-out FILE` on the commands that return a password, written
  like `init --key-out`.

Passes: T-789, T-795, T-796, T-802, T-803, T-826, T-827.
Verify: `go test ./internal/admin/... ./internal/cli/... ./test/isolation/... -run 'Users|Members|PeopleAudit|Reset|PeopleCommands|MembersAPI' -count=1`.

### C6. Host commands and exit — `feat: init creates the first administrator; user reset-password and unlock on the host`

Files: `cmd/pandokos/{init,user,main}.go`, `internal/admin/bootstrap.go`
(`BootstrapOptions.AdminUsername`, `PasswordOut`; `account.BootstrapAdmin` in the same
transaction, before `BeforeCommit`), `cmd/pandokos/serve.go` (the start-up warning of §5.15),
`deploy/compose` and its guide (the quick-start passes `--admin-username`).

- `user reset-password` and `user unlock` load the configuration with `PurposeInit`, open the
  admin pool as `init` does, and call `account.Service` with the actor `host`.

Passes: T-828, T-829, T-831 to T-835 (T-835 needs C8 for its OIDC half: it is finished there),
T-839, T-R55. Verify: `go test ./cmd/... ./test/e2e/... -run 'Init|UserHost|LocalAccount|Lock|NotOnInference|DisableExit|Upgrade' -count=1`.

### C7. Relying party — `feat: OpenID Connect relying party: metadata, code exchange, ID token`

Files: `internal/jwks/{jwks,fetch}.go` (`Metadata`), `internal/authn/token.go` (`VerifyJWT`
extracted from `validate`, which now calls it; checks 4 to 9 of 4.1 §5.4 in the same order),
`internal/oidc/{provider,flow,idtoken,groups}.go`.

- `Provider` is built with the configuration, a `*jwks.Cache` of its own (transport of the egress
  guard, `auth.oidc.private_hosts`, the `tokens.jwks.*` timings) and an `*http.Client` with that
  transport, `CheckRedirect` refusing, a 10 s timeout. The token response is read through
  `io.LimitReader` of 64 KiB + 1.
- Derivations use `crypto/hmac` with SHA-256 and the labels of §5.8; comparisons use
  `hmac.Equal`.
- `Complete` returns `*oidc.Failure{Reason}` for refusals and an error wrapping
  `authz.ErrUnavailable` for outages; it never returns or logs a token.

Passes: T-807, T-809 to T-813 (U and I with the provider alone), T-811, T-824; T-569 to T-573
unchanged. Verify: `go test ./internal/oidc/... ./internal/jwks/... ./internal/authn/... -count=1`.

### C8. OIDC sign-in — `feat: sign-in through the identity provider; group rules`

Files: `internal/admin/auth.go` (`startOidcSignIn`, `completeOidcSignIn`), `internal/account/federated.go`
(find or create the user, display attributes, reconciliation), `internal/config/validate.go`
(`auth.oidc.*`, the rule that group mappings name roles a user can hold), `cmd/pandokos/serve.go`
(the provider; tenants named by rules are resolved at start and re-resolved at each sign-in).

- Start: `CountOidcLogins`, `CreateOidcLogin`, the Lax cookie, 303.
- Callback: §5.8 steps 1 to 7. Reconciliation: `UserTenants` and `ListPlatformRoles` in the
  platform scope, then one `WithTenant` transaction per tenant to change, then the session.
  A rule whose tenant no longer exists is skipped with a log line that names the rule's index.
- Every failure writes `auth.failure` (cap applies) and redirects; nothing of the provider's
  answer reaches the response.

Passes: T-808, T-812, T-814 to T-817, T-835, T-836, the OIDC part of T-837, the rest of T-830.
Verify: `go test ./internal/admin/... ./test/e2e/... -run 'Oidc|Group|AutoProvision|LocalDisabled|Canary' -count=1`.

### C9. Tokens on the administration listener — `feat: tokens of trusted issuers on the administration listener`

Files: `internal/authn/tokens.go` (`checkTokens` accepts the admin pool), `cmd/pandokos/serve.go`
(with `tokens.admin_listener`, the admin authenticator gets `KindToken` and the gateway's key
cache), `internal/admin/admin.go` (`recordFailure` for token failures: `resource_type`
`token_issuer`, `resource_id` the registration, detail `reason`, `listener: admin`,
`issuer_scope`, `subject` when set, in the tenant's log when the failure names one, else
logged only as on the gateway), `internal/config/validate.go`.

Passes: T-823, T-838 (its idempotency half after C10). Verify:
`go test ./internal/authn/... ./internal/admin/... -run 'Token' -count=1`.

### C10. Idempotency keys — `feat: idempotency keys on create operations`

Files: `internal/idem/*`, `internal/audit/audit.go` (`RecordTenant` and `RecordPlatform` call
`idem.Mark(ctx, tx, e.ResourceType, e.ResourceID)` for the first event of outcome `success`),
`internal/admin/admin.go`.

- In `before`, after `Decide`, for `op.Idempotent` with the header present: validate the key,
  read the body (at most `maxBodyBytes`) and put it back on the request, `Begin`. A replay is
  returned as `*replayError{status, body}`, which `fail` writes with `Idempotency-Replayed: true`.
- The status recorder buffers the body of a claimed request; the outer handler calls `Finish`
  after the handler returned, with `context.WithoutCancel` and a 2 s timeout. A `Finish` that
  fails is logged; the row keeps its commit mark.
- Redaction of the stored body walks the JSON and drops members for which the sensitive-member
  test of `internal/audit` is true.

Passes: T-818 to T-821, T-838. Verify:
`go test ./internal/idem/... ./internal/admin/... ./test/e2e/... -run 'Idempoten|TokenAdministers' -count=1`.

After C10: `make check` passes except the tagged suites of part D.

## Part D — Containers and CI (commit: `ci: Keycloak people and groups; headless Chrome job`)

### D1. Keycloak realm (`test/idp/keycloak-realm.json.tmpl`, `test/idp/up.sh`)

Add to the realm, with every secret a placeholder that `up.sh` fills from values it generates
into `local/idp/env`:

- client `pandokos-admin`: confidential, `standardFlowEnabled: true`, direct grants off,
  `redirectUris` exactly the callback of the test origin (`up.sh` writes
  `PANDOKOS_TEST_IDP_ORIGIN`), `attributes: {"pkce.code.challenge.method": "S256"}`, a
  `oidc-group-membership-mapper` (`claim.name: groups`, `full.path: false`, in the ID token);
- groups `pandokos-admins` and `tenant-a-admins`; users `alice` (both groups) and `bob` (none),
  enabled, with `credentials: [{type: password, value: …, temporary: false}]`.

Check before writing the tests' form handling: `curl` the authorization URL and read the form's
`action`; if the realm import refuses users or the mapper differs, stop (§12).

### D2. Tests and jobs

- `make test-idp` runs T-840 to T-843 with the others. Authentik's blueprint gains nothing; T-842
  requests the `profile` scope with the existing service account and reads `groups`.
- `Makefile`: `test-browser` (`go test -tags browser -count=1 ./test/browser/...`, needs `make
  build` and `make db-up`), listed in `.PHONY` and in `help`.
- `.github/workflows/ci.yml`: job `browser`, built from the steps of the `test` job that start
  the database, then `google-chrome --version` and `make test-browser`. Actions pinned by commit
  as the others; no new action unless one is needed to install Chrome, in which case stop and
  ask.
- `Dockerfile`, the Compose reference: unchanged except the quick-start's `init` line (C6) and
  `auth.public_origin` in the Compose configuration (its listener serves TLS on loopback, so the
  derived origin already works; set nothing unless T-compose fails).

Verify: `make idp-up test-idp && make build test-browser && make image test-container test-compose`.

## Part E — Documentation and decision records (commit: `docs: sprint 4.2 documentation and ADRs`)

### E1. New page

`docs/operations/sign-in.md`: the two methods; the first administrator; who manages an account
(the managing rule, with the reason); passwords (policy, generated passwords, reset, the host
commands); throttling numbers and what a lock looks like; sessions (cookie, lifetimes, what ends
one, the bound of IAM-11); building a browser client (same origin, the header, `Origin`, the
codes, the minimal page as the example, what the client must not do); `auth.public_origin` and
reverse proxies, `trusted_proxies`, HSTS as the proxy's duty; the identity provider (client
registration, redirect URI, claims, group rules with examples for Keycloak and for a provider
with a nested claim, auto-provisioning, what happens when the provider removes a person);
tokens on the administration listener; idempotency keys; what is not built (ADR 0022) and the
recommendation to use an identity provider or a private administration network in production;
memory of the hasher.

### E2. Changed pages

`docs/architecture/security-model.md` (boundary 2; the rows on passwords, sessions and forgery
made exact; new rows: relying party, group rules, managing rule, idempotency; replay exposure of
a session; the residuals of §5.17), `docs/architecture/data-model.md` (the identity group, the
"People and sessions" section rewritten from §5.2 including D-2's departure from the earlier
text, database roles), `docs/architecture/overview.md` (the admin plane's authenticators; caches:
none for sessions), `docs/reference/admin-api.md` (authentication, the new codes, the subset, the
commands), `docs/reference/permissions.md` (generated), `docs/operations/configuration.md`,
`docs/operations/tokens.md` (the administration listener), `docs/operations/database.md` (the
tables and functions of 0013), `docs/reference/data-flows.md` (what the identity provider
receives and returns), the Compose guide, `README.md` status.

### E3. Decision records (status `proposed`, date of the commit; index in `docs/adr/README.md`)

- **0056 — Users, memberships, and how a session becomes a principal.** Context: ADR 0006 and
  0007 name users and assignments without a model. Decision: D-1 to D-4 with the reasons of §5.2,
  §5.3 and §5.9. Consequences: per-request membership read; tenant administrators cannot touch
  platform-managed members; platform administrators cannot touch a tenant's users before 4.3.
  Alternatives: one table of assignments for users and service accounts (gives the gateway role
  rows about people, and the harness allows one policy per tenant table); users per tenant
  (a person in two tenants would be two accounts and usernames would need the tenant at
  sign-in); a tenant chosen at sign-in and stored in the session (the tenant would stop being
  explicit in the request, against IAM-10); bearer tokens acting as users (ADR 0006).
- **0057 — Local accounts and sessions: formats and numbers.** Refines 0022. Decision: D-6 to
  D-11, D-20, D-22, the table of defaults of §5.15, the ASVS table of §5.17 with its unmet row.
  Alternatives: bcrypt or scrypt; a pepper; server-side sleeping instead of refusals; a signed
  double-submit token for forgery protection (needs a server secret shared by instances; the
  header and `Origin` need none); a cookie lifetime in the browser; a session cache.
- **0058 — The OpenID Connect relying party.** Refines 0006; amends 0046 (the admin role holds a
  key cache for the operator's provider). Decision: D-12, D-13. Alternatives: `go-oidc` and
  `x/oauth2` (two modules and their own HTTP client and cache for one request and one validation
  that the repository already has); state, nonce and verifier in a table (secrets at rest for ten
  minutes); a public client (no secret, but the provider cannot tell Pandokos from a copy);
  group rules as API resources; just-in-time users without a rule.
- **0059 — Idempotency keys.** Decision: D-16 with the state machine of §5.11. Consequences: one
  more statement in the audit helpers; create responses lose the one-time member on a replay.
  Alternatives: storing the response after the handler only (a crash between commit and store
  creates a duplicate on retry); storing full responses (secrets at rest); natural keys only
  (names are not unique for keys and mappings).
- History lines: 0005 and 0045 (tokens on the administration listener, D-5), 0006 (IAM-08 for
  users; the bound is the request, there is no cache), 0018 (the module and its confinement),
  0020 (seven codes), 0022 (O-5 as decided; usernames are lower case), 0028 (four constructs),
  0031 (five permissions, `KindUser`, roles by scope), 0032 (fifteen actions, the actor `host` in
  tenant logs, where sign-in events go), 0046 (the admin role's cache), 0047 (Keycloak people,
  headless Chrome), 0055 (D-21).

### E4. Planning files

`planning/epics/04-identity-and-rbac/epic.md` (4.2: decisions taken, records, guide; the 4.3
section gains the first item of §11), `planning/roadmap.md`, the front matter of `sprint.md`
(`status`). The maintainer's decisions on O-1 to O-12 are appended to §12 as a table when taken.

Verify: `go test ./test/repo/... -count=1` (T-R56 and the reference tests).

## Part F — Verification (no commit unless something is fixed)

1. `make check` on a clean tree; `make idp-up test-idp`; `make build test-browser`; `make image
   test-container test-compose`.
2. `make fuzz FUZZTIME=60s` for the three new targets; `make vuln`.
3. The definition of done of §10, item by item, with the test or the file that shows each; the
   five exit conditions by their tests.
4. The ASVS table of §5.17: for each row, open the code and the test named and note the file and
   line in the pull request. This is the reviewer's checklist; the implementer prepares it.
5. Grep the tree for leftovers: `grep -rn 'not implemented' internal/ cmd/` (none in the new
   handlers); `git grep -n -i -E 'password|secret' -- test/fixtures` (no value);
   `git log --format=%B <base>..HEAD | grep -i -E '^[a-z-]+-(by|session):' | grep -v -i '^signed-off-by:'` (nothing).
6. Record in the pull request: the binary size before and after, the memory of the admin role
   during T-831 (`/proc/<pid>/status` VmHWM), the version of `golang.org/x/crypto` and of Chrome
   used.

## What the implementer does not do

- Add a module other than `golang.org/x/crypto`, or a GitHub action, without asking.
- Give the gateway or worker role a privilege on a table of migration 0013, add a policy to a
  tenant-scoped table, or edit a merged migration.
- Cache sessions or users; store a provider token, a verifier, a nonce or a request body.
- Add roles, custom roles, support actions, reasons or effective permissions per tenant (4.3).
- Serve a page from the binary; add CORS headers; accept a session on the inference listener.
- Change the answer of a sign-in refusal so that the cases differ, or record a typed username.
- Decide an open point of §12: each is implemented as recommended until the maintainer says
  otherwise.

