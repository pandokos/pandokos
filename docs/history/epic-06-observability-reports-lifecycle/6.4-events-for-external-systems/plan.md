# Sprint 6.4 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it; T-numbers are its tests, D-numbers
  its decisions, M-numbers its instruments.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in order
  A, B, C1 … C10, D, E, F.
- Suggested grouping: **G1** = C1, C2, C3 (the secret, the envelope and signature, one delivery);
  **G2** = C4, C5 (the relay and the janitor); **G3** = C6, C7 (the API); **G4** = C8, C9 (command
  line, receiver example). After part B the groups do not depend on each other, except that G2 needs
  the types of C2 and C3 (stubbed in A7), and C8 the generated client of A3. C10 is one agent after
  them. A, B, D, E and F are one agent each. Because the risk class is high, A1 (the migration), C1
  with C3 (secrets and egress) and C4 (claiming) are reviewed by an agent that did not write them,
  with §5.1, §5.6, §5.7, §5.9 and §6 as the checklist.

## Rules for the implementer

1. Work on the branch the orchestrator names; do not create one. Sign off every commit
   (`git commit -s`) in the maintainer's name. No co-author or attribution lines, no tool footers and
   no session links in any commit message, pull request text, comment or file. One commit for A, one
   for B (tests, failing), one per step of C (message given with the step), one for D, one for E, one
   for F if something is found.
2. **One migration (A1), copied byte for byte**: its SHA-256 as a file is given with it; a different
   digest means a wrong copy. A merged migration is immutable (ADR 0026); until the pull request
   merges, a correction changes the file and its line in `migrations.sum` together. If a statement of
   A1 or A2 does not work as written, stop and ask.
3. **No new error code and no new module.** The only new permissions are the five of §6; the only
   new keys the nine of §5.15; the only new definer functions the eight of A1; the only new
   dependency, if the maintainer accepts O-11, is test-only in `test/sdk/go.mod`.
4. **The database writes events; Go does not.** No Go code and no statement of
   `internal/store/queries/sql` inserts into, updates or deletes from `outbox_event`,
   `outbox_stream` or `platform_outbox_event`, or sets `pandokos.retention` (T-R81).
   `internal/ledger`, `internal/limits`, `internal/gateway` and `internal/resolver` are not edited
   and never import `internal/outbox`. Do not weaken a trigger, a policy, a grant or a check to make
   a test pass.
5. **Events of a tenant are the tenant's** (ADR 0054): nothing of an event, and no count of one
   tenant's events, deliveries or dead letters, leaves that tenant's session except to its own
   destination, its own API caller, and the installation-wide totals of §5.15.
6. **Signing secrets**: a `secret.Secret` from generation to the HMAC; every type that holds one
   prints the redaction marker on every formatting path; plaintext and unwrapped data keys are
   cleared (`clear`) when the round or the request that used them ends; no error, audit detail, log
   field or metric attribute holds a secret, a URL, a host of a URL (the audit detail excepted, §5.11)
   or a destination ID. Only `internal/vault` calls `KeyProvider.Wrap` or `Unwrap`.
7. **Egress**: `internal/outbox` builds no `http.Transport`, dialer or TLS configuration; it takes
   `egress.Guard.Transport` and chooses the private one only for a host of
   `events.webhook.private_hosts`. Redirects are never followed. No test contacts a host outside the
   machine.
8. Log lines and their fields are exactly those of §5.15; every one is added to
   `docs/reference/logs.md` in the same commit as the code that writes it.

## Environment

`make db-up` gives the test PostgreSQL (16.x) and PgBouncer; `make check` is what CI runs. The
`sdk` module needs the database too (6.3). Go and tool versions are those of the repository
(`go.mod`, `Makefile`); nothing is upgraded in this sprint.

## Part A — Scaffolding (commit: `chore: sprint 6.4 scaffolding: migration 0017, queries, API specification, permissions, configuration keys`)

### A1. Migration — `internal/store/migrations/sql/0017_events.sql`

The text is appendix A1, byte for byte, with one trailing newline. Check, then add the line to
`migrations.sum` with `make migrations-sum`:

```
sha256sum internal/store/migrations/sql/0017_events.sql
# ce557ccfd5c0e9e0197f6e1bc3184d9957bdcb025ba90b15bdd13049fb59224e
```

Verification: `go test ./internal/store/migrations/ -run 'TestChecksums|TestMigrate'`; apply it to a
scratch database at 0016 with `pandokos migrate`.

### A2. Queries

`internal/store/queries/sql/tenants.sql`: every statement that lists the columns of `tenant` gains
`external_reference` **at the end of its list** (so `sqlc` keeps returning `PandokosTenant`);
`CreateTenant` and `UpdateTenant` gain the parameter (`UpdateTenant` with the existing
`COALESCE(sqlc.narg(…), column)` form). `lifecycle.sql`: the statements that read `tenant_deletion`
for the API gain `external_reference`.

New file `internal/store/queries/sql/outbox.sql`. Statements of the worker (W) run in the tenant's
session unless marked platform; those of the administration (A) in the tenant's session unless
marked platform.

```sql
-- name: ClaimOutboxDestinations :many            (W, platform scope)
SELECT c.tenant_id::uuid AS tenant_id, c.destination_id::uuid AS destination_id
  FROM pandokos.outbox_relay_claim(sqlc.arg(token)::uuid, sqlc.arg(lease_seconds)::integer, sqlc.arg(max)::integer) c;

-- name: GetOutboxRelayState :one                 (W, platform scope)
SELECT s.destinations::bigint, s.failing::bigint, s.disabled::bigint, s.dead_letters::bigint,
       s.oldest_pending_seconds::double precision
  FROM pandokos.outbox_relay_state() s;

-- name: GetEventDestinationForRelay :one         (W) the first statement of a round: it reads the head
SELECT d.id, d.name, d.url, d.event_types, d.enabled, d.secret, d.secret_key_version, d.previous_secret,
       d.previous_secret_key_version, d.previous_secret_expires_at, d.cursor_seq, d.failures, d.next_attempt_at,
       d.test_event_id, (d.test_requested_at IS NOT NULL
                         AND (d.test_completed_at IS NULL OR d.test_completed_at < d.test_requested_at))::boolean AS test_pending,
       (SELECT t.external_reference FROM pandokos.tenant t WHERE t.id = d.tenant_id) AS external_reference,
       COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = d.tenant_id), 0)::bigint AS head_seq
  FROM pandokos.event_destination d
 WHERE d.id = sqlc.arg(id)::uuid AND d.lease_token = sqlc.arg(token)::uuid;

-- name: ListOutboxEventsAfter :many              (W, A) after GetEventDestinationForRelay, never before
SELECT o.seq, o.id, o.type, o.subject, o.occurred_at, o.recorded_at, o.data
  FROM pandokos.outbox_event o
 WHERE o.seq > sqlc.arg(after)::bigint
   AND (cardinality(sqlc.arg(types)::text[]) = 0 OR o.type = ANY (sqlc.arg(types)::text[]))
 ORDER BY o.seq
 LIMIT sqlc.arg(max)::integer;

-- name: ListReplayDeadLetters :many              (W)
SELECT l.seq, l.attempts, o.id, o.type, o.subject, o.occurred_at, o.data
  FROM pandokos.outbox_dead_letter l
  JOIN pandokos.outbox_event o ON o.tenant_id = l.tenant_id AND o.seq = l.seq
 WHERE l.destination_id = sqlc.arg(destination_id)::uuid AND l.state = 'replay'
 ORDER BY l.seq
 LIMIT sqlc.arg(max)::integer;

-- name: SettleEventDestination :execrows         (W) 0 rows: the lease is not ours any more
UPDATE pandokos.event_destination
   SET cursor_seq = GREATEST(cursor_seq, sqlc.arg(cursor_seq)::bigint),
       failures = sqlc.arg(failures)::integer,
       failing_since = CASE WHEN sqlc.arg(failures)::integer = 0 THEN NULL ELSE COALESCE(failing_since, now()) END,
       next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz,
       last_success_at = COALESCE(sqlc.narg(last_success_at)::timestamptz, last_success_at),
       last_failure_at = COALESCE(sqlc.narg(last_failure_at)::timestamptz, last_failure_at),
       last_error = sqlc.arg(last_error)::text,
       last_status = sqlc.narg(last_status)::integer,
       test_completed_at = CASE WHEN sqlc.arg(test_outcome)::text <> '' THEN now() ELSE test_completed_at END,
       test_outcome = CASE WHEN sqlc.arg(test_outcome)::text <> '' THEN sqlc.arg(test_outcome)::text ELSE test_outcome END,
       test_status = CASE WHEN sqlc.arg(test_outcome)::text <> '' THEN sqlc.narg(test_status)::integer ELSE test_status END,
       lease_token = NULL, lease_until = NULL
 WHERE id = sqlc.arg(id)::uuid AND lease_token = sqlc.arg(token)::uuid;

-- name: ParkDeadLetter :exec                     (W) in the transaction of the settle, after it changed a row
INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason, status)
VALUES (pandokos.current_tenant_id(), sqlc.arg(destination_id)::uuid, sqlc.arg(seq)::bigint, sqlc.arg(event_id)::uuid,
        sqlc.arg(type)::text, sqlc.arg(attempts)::integer, sqlc.arg(reason)::text, sqlc.narg(status)::integer)
ON CONFLICT (tenant_id, destination_id, seq)
DO UPDATE SET state = 'dead', attempts = EXCLUDED.attempts, reason = EXCLUDED.reason, status = EXCLUDED.status, parked_at = now();

-- name: ReturnDeadLetter :exec                   (W) a replay that failed
UPDATE pandokos.outbox_dead_letter
   SET state = 'dead', attempts = attempts + 1, reason = sqlc.arg(reason)::text, status = sqlc.narg(status)::integer, parked_at = now()
 WHERE destination_id = sqlc.arg(destination_id)::uuid AND seq = sqlc.arg(seq)::bigint AND state = 'replay';

-- name: DeleteDeadLetter :exec                   (W) a replay that succeeded
DELETE FROM pandokos.outbox_dead_letter
 WHERE destination_id = sqlc.arg(destination_id)::uuid AND seq = sqlc.arg(seq)::bigint AND state = 'replay';

-- name: TenantOutboxRetention :one               (W)
SELECT r.events::bigint, r.expired::bigint, r.limit_events::bigint
  FROM pandokos.tenant_outbox_retention(sqlc.arg(event_hours)::integer, sqlc.arg(usage_months)::integer, sqlc.arg(batch)::integer) r;

-- name: CreateEventDestination :one              (A)
INSERT INTO pandokos.event_destination (tenant_id, id, name, description, url, event_types, enabled, secret, secret_key_version)
VALUES (pandokos.current_tenant_id(), sqlc.arg(id)::uuid, sqlc.arg(name), sqlc.arg(description), sqlc.arg(url), sqlc.arg(event_types)::text[],
        sqlc.arg(enabled), sqlc.arg(secret), sqlc.arg(secret_key_version))
RETURNING id;

-- name: GetEventDestination :one                 (A) also ListEventDestinations :many with the keyset of the other lists
SELECT d.id, d.tenant_id, d.name, d.description, d.url, d.event_types, d.enabled, d.secret_created_at,
       d.previous_secret_expires_at, d.created_at, d.updated_at, d.cursor_seq, d.failures, d.failing_since,
       d.next_attempt_at, d.last_success_at, d.last_failure_at, d.last_error, d.last_status, d.expired_events,
       d.test_requested_at, d.test_event_id, d.test_completed_at, d.test_outcome, d.test_status,
       COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = d.tenant_id), 0)::bigint AS head_seq,
       (SELECT count(*) FROM pandokos.outbox_dead_letter l WHERE l.destination_id = d.id AND l.state = 'dead')::bigint AS dead_letters
  FROM pandokos.event_destination d
 WHERE d.id = sqlc.arg(id)::uuid;

-- name: UpdateEventDestination :execrows         (A) enabling (false to true) also clears the failure state
UPDATE pandokos.event_destination d
   SET name = COALESCE(sqlc.narg(name), d.name), description = COALESCE(sqlc.narg(description), d.description),
       url = COALESCE(sqlc.narg(url), d.url), event_types = COALESCE(sqlc.narg(event_types)::text[], d.event_types),
       enabled = COALESCE(sqlc.narg(enabled), d.enabled), updated_at = now(),
       failures = CASE WHEN sqlc.narg(enabled)::boolean AND NOT d.enabled THEN 0 ELSE d.failures END,
       failing_since = CASE WHEN sqlc.narg(enabled)::boolean AND NOT d.enabled THEN NULL ELSE d.failing_since END,
       next_attempt_at = CASE WHEN sqlc.narg(enabled)::boolean AND NOT d.enabled THEN now() ELSE d.next_attempt_at END
 WHERE d.id = sqlc.arg(id)::uuid;

-- name: RotateEventDestinationSecret :execrows   (A) the trigger keeps the replaced secret when an expiry is given
UPDATE pandokos.event_destination
   SET secret = sqlc.arg(secret), secret_key_version = sqlc.arg(secret_key_version), updated_at = now(),
       previous_secret_expires_at = CASE WHEN sqlc.arg(previous_seconds)::integer > 0
                                         THEN now() + make_interval(secs => sqlc.arg(previous_seconds)::integer) END
 WHERE id = sqlc.arg(id)::uuid;

-- name: RequestEventDestinationTest :execrows    (A)
UPDATE pandokos.event_destination
   SET test_requested_at = now(), test_event_id = sqlc.arg(event_id)::uuid
 WHERE id = sqlc.arg(id)::uuid;

-- name: RewindEventDestination :one              (A) since is NULL or after is NULL, never both
UPDATE pandokos.event_destination d
   SET cursor_seq = LEAST(d.cursor_seq, COALESCE(sqlc.narg(after)::bigint,
           (SELECT o.seq FROM pandokos.outbox_event o WHERE o.recorded_at < sqlc.narg(since)::timestamptz
             ORDER BY o.seq DESC LIMIT 1), 0)),
       failures = 0, failing_since = NULL, next_attempt_at = now(), lease_token = NULL, lease_until = NULL
 WHERE d.id = sqlc.arg(id)::uuid
RETURNING d.cursor_seq;

-- name: DeleteEventDestination :execrows         (A)
DELETE FROM pandokos.event_destination WHERE id = sqlc.arg(id)::uuid;

-- name: ListEventDeadLetters :many               (A) keyset on seq
SELECT l.seq, l.event_id, l.type, l.state, l.attempts, l.reason, l.status, l.parked_at, l.replay_requested_at
  FROM pandokos.outbox_dead_letter l
 WHERE l.destination_id = sqlc.arg(destination_id)::uuid AND l.seq > sqlc.arg(after)::bigint
 ORDER BY l.seq LIMIT sqlc.arg(max)::integer;

-- name: ReplayEventDeadLetters :execrows         (A) an empty list: all of the destination
UPDATE pandokos.outbox_dead_letter
   SET state = 'replay', replay_requested_at = now()
 WHERE destination_id = sqlc.arg(destination_id)::uuid AND state = 'dead'
   AND (cardinality(sqlc.arg(seqs)::bigint[]) = 0 OR seq = ANY (sqlc.arg(seqs)::bigint[]));

-- name: ListPlatformOutboxEventsAfter :many      (A, platform scope)
SELECT e.seq, e.id, e.type, e.subject, e.occurred_at, e.tenant_id, e.data,
       COALESCE((SELECT t.external_reference FROM pandokos.tenant t WHERE t.id = e.tenant_id), '') AS external_reference
  FROM pandokos.platform_outbox_event e
 WHERE e.seq > sqlc.arg(after)::bigint
   AND (cardinality(sqlc.arg(types)::text[]) = 0 OR e.type = ANY (sqlc.arg(types)::text[]))
 ORDER BY e.seq LIMIT sqlc.arg(max)::integer;
```

No statement of the administration names the columns `secret` or `previous_secret` except to
assign `secret`: the admin role cannot read them. The settle of a round is one transaction:
`SettleEventDestination`, and only when it changed one row, `ParkDeadLetter`, `ReturnDeadLetter` and `DeleteDeadLetter` for the round's outcomes. A
statement refused with `tenant_open` (23001) or a foreign-key violation (the destination is gone)
ends the round silently. `make generate`; the package compiles.

### A3. API specification — `api/openapi/admin.v1.yaml`

Appendix A3. `make generate` regenerates `internal/adminapi`.

### A4. Isolation harness — `test/isolation/{spec,lint}.go`

- `Spec.Global`: `event_destination_directory` and `platform_outbox_stream` (no privilege for any
  runtime role), `platform_outbox_event` (`Restricted: true`).
- `Spec.Grants`: the matrix of §6, column by column as appendix A1 grants it; `tenant` and
  `tenant_deletion` gain `external_reference`.
- `Spec.SecurityDefiner`: the eight functions of §6.
- `Spec.Fixtures`: one row each for `outbox_stream`, `outbox_event` (inserted by the superuser with
  the triggers bypassed, as the ledger's fixture), `event_destination`, `outbox_dead_letter`.
- `Spec.AppendOnly` (L-17): `outbox_event`, `platform_outbox_event`.
- L-23 (`lintOutboxTriggers`): `pg_trigger` has `usage_record_outbox` on `usage_record`
  (statement-level, after insert, `tgenabled = 'O'`) and `limit_event_outbox` on `limit_event`
  (row-level, after insert, enabled).
- The table groups of T-979 and the definer calls of `TestTenantOpenDefinerFunctions` (§9, changed
  tests).

### A5. Permissions — `internal/authz/{authz,roles}.go`, `testdata/roles.golden`

The five constants and catalogue rows of §6 (descriptions: the "Covers" column; `Support` true for
`event_destination:read` only; `platform_event:read` is `ScopePlatform`, not `InTenant`). Roles:
`auditor` gains `event_destination:read`; `usage-viewer` gains `event:read`; `platform-auditor`
gains `platform_event:read`; `owner`, `tenant-admin`, `read-only` and `platform-admin` follow their
rules. `make generate` rewrites `docs/reference/permissions.md`.

### A6. Configuration — `internal/config/{config,keys,validate}.go`

The nine keys of §5.15 with `serveDef`, a struct `Events` (`Source`, `Retention`, `Relay{Interval,
Workers, BatchSize}`, `Delivery{Timeout, MaxAttempts, BackoffMax}`, `Webhook{PrivateHosts}`); the
host list is validated by the function that validates `tokens.private_hosts`.

### A7. Types and stubs (no behaviour)

```go
// internal/vault/named.go
type SecretKind string
const KindWebhookSecret SecretKind = "pandokos/webhook-secret/v1"
// Associated data: kind, 0x00, tenant (16 bytes), object ID (16 bytes), data key version (4 bytes, big endian).
func (s *Sealer) SealNamed(ctx context.Context, tx store.Tx, id tenant.ID, kind SecretKind, objectID string, plaintext secret.Secret) (Sealed, error)
func OpenNamed(ctx context.Context, tx store.Tx, p KeyProvider, id tenant.ID, kind SecretKind, objectID string, keyVersion int32, sealed []byte) (secret.Secret, error)

// internal/outbox
type Event struct { Seq int64; ID, Type, Subject string; Time time.Time; Data json.RawMessage }
type Envelope struct { Source string; TenantID tenant.ID; TenantRef string } // zero TenantID: no tenant attributes
func Render(e Event, env Envelope) []byte                                    // §5.3; Seq 0: no sequence
type Key struct{ /* 32 bytes, unexported */ }
func NewKey() (Key, error)                    // crypto/rand
func ParseKey(text secret.Secret) (Key, error) // "whsec_" + base64 (standard, padded) of 32 bytes
func (k Key) Text() secret.Secret
func Sign(id string, at time.Time, body []byte, keys ...Key) (timestamp, signature string) // §5.9
type Outcome string // delivered, status, redirect, timeout, connection, tls, blocked
type Result struct { Outcome Outcome; Status int; Duration time.Duration }
func ValidateURL(raw string, privateHosts []string) error
type Deliverer struct { Transport func(private bool) http.RoundTripper; PrivateHosts []string; Timeout time.Duration; UserAgent string; Now func() time.Time }
func (d *Deliverer) Send(ctx context.Context, url string, e Event, env Envelope, keys ...Key) Result // the relay is its only caller
type Relay struct { DB *store.DB; Provider vault.KeyProvider; Deliverer *Deliverer; Source string
	Interval time.Duration; Workers, BatchSize, MaxAttempts int; BackoffMax time.Duration; Logger *slog.Logger; Now func() time.Time }
func (r *Relay) Run(ctx context.Context); func (r *Relay) Pass(ctx context.Context) (rounds int, err error); func (r *Relay) Stats() RelayStats
type Janitor struct { DB *store.DB; Retention time.Duration; UsageMonths int; Logger *slog.Logger }
func (j *Janitor) Run(ctx context.Context); func (j *Janitor) Pass(ctx context.Context) error; func (j *Janitor) Stats() JanitorStats
func Types() []string; func Schema(typ string) ([]byte, bool) // embedded
```

`RelayStats` and `JanitorStats` hold the counters behind M52 to M58 and the two kinds of M44;
nothing in them names a tenant or a destination. Verification of part A: `make generate-check`,
`go build ./...`, `go vet ./...`.

## Part B — Tests first (commit: `test: sprint 6.4 tests, failing`)

### B1. Helpers and fixtures

- `test/dbtest`: `SeedEventDestination`, `OutboxEvents`, `AgeOutbox`, and `PopulatedTenant` extended
  (§7); `SeedUsageRecords(t, tenant, n)` if none exists, through the gateway role and the writer's
  statement.
- `test/e2e`: the `events` options of `startBinary`; `faultReceiver` (§7) on a listener of
  `127.0.0.1` reached as `localhost`, listed in `events.webhook.private_hosts`.
- O-11, only if accepted: in `test/sdk`, `go list -m -versions` of the module the Standard Webhooks
  project publishes for Go; take the newest tagged version, record it in the commit message, and
  add to T-1051 a verification of one delivered request with it. If no tagged version exists, skip
  and say so in the pull request.

### B2. Where each test lives

| Tests | File |
|---|---|
| T-1013 to T-1023, T-1025 | `test/isolation/outbox_test.go` (T-1021 beside the erasure tests) |
| T-1024 | `internal/store/migrations/upgrade_0017_db_test.go` |
| T-1026 to T-1029 | `internal/outbox/{event,sign,deliver}_test.go` |
| T-1030 to T-1038 | `internal/outbox/{relay,janitor}_db_test.go` |
| T-1039 | `internal/vault/named_db_test.go` |
| T-1040 to T-1045 | `internal/admin/{event_destinations,events,tenants}_db_test.go` |
| T-1046 to T-1048 | `internal/cli/events_test.go`, `internal/config`, `internal/telemetry` |
| T-1049 | `examples/webhook-receiver/receiver/receiver_test.go` |
| T-1050 | `internal/ledger/outbox_cost_db_test.go` (with `BenchmarkLedgerBatchOutbox`) |
| T-1051 | `test/sdk/events_test.go` |
| T-1052 to T-1056 | `test/e2e/events_test.go` |
| T-R81 to T-R84 | `test/repo/events_test.go` |

The changed tests of §9 are changed in this commit. The golden files of T-1014, T-1015 and T-1026
are written from §5.3 and from the events shown in §12, not from the implementation's output.

Verification: `go vet ./...`; `go test ./... 2>&1 | grep -c '^--- FAIL'` is not zero and every new
test fails for the missing behaviour, not for a compile error. The database tests T-1013 to T-1025
pass already, except what needs `Spec` (they test A1): say so in the commit message.

## Part C — Implementation

### C1. Named secrets — `feat: the vault seals and opens a named secret under a tenant's data key`

Files: `internal/vault/{named,envelope}.go`. `SealNamed` follows `Seal` (ensure and unwrap the data
key, seal, clear) with the associated data of A7; `Sealed.Fingerprint` and `Hint` stay empty.
`OpenNamed` reads the data key of `keyVersion` in the session of `tx`, unwraps it, opens, clears
the key. Errors are the vault's (`ErrUnavailable`, `ErrUndecryptable`) with the causes the vault
lines already use. Verification: T-1039; `go test ./internal/vault/`.

### C2. Envelope, signature, schemas — `feat: CloudEvents envelope, webhook signature and the event schemas`

Files: `internal/outbox/{doc,event,sign,schema}.go`, `internal/outbox/schema/*.schema.json` (nine
files, named by type without the `io.pandokos.` prefix), `Makefile` (`generate` copies them to
`docs/reference/events/`). Before writing `sign.go`, read the published Standard Webhooks
specification and check the three header names, the signed text `id.timestamp.body`, the `v1,`
prefix, the space between signatures and the `whsec_` form against §5.9; a difference is raised as
a question. Each schema is JSON Schema 2020-12 for the whole document: the envelope members of §5.3
(`specversion` const `1.0`, `type` const, `id` a UUID, `time` a date-time, the extensions) and
`data` with the members of the type's row, their types as migration 0017 writes them, `required`
for those never absent, `additionalProperties: false` in `data` and `true` in the envelope.
`internal/telemetry/redact.go` gains the shape `whsec_[A-Za-z0-9+/]{43}=`. Verification: T-1026,
T-1027, T-1048 (the shape), `make generate-check`.

### C3. One delivery — `feat: signed webhook requests through the egress guard`

Files: `internal/outbox/deliver.go`. `ValidateURL` (§5.5). `Send` renders, signs with the keys
given, and makes one request with one context deadline (`Timeout`): a client whose transport is
`Transport(private)` where `private` is true only for a listed host, `CheckRedirect` returning
`http.ErrUseLastResponse`; the body is read through `io.LimitReader` (64 KiB) and closed. The
mapping to outcomes of §5.7 uses `errors.As` on the guard's refusal (its `BlockedReason`
interface), on `net.Error` timeouts and on TLS errors; anything else is `connection`. No header
other than those of §5.9. Verification: T-1028 (backoff lives here too: `func Backoff(failures int,
max time.Duration, rnd func() float64) time.Duration`), T-1029, T-R83.

### C4. The relay — `feat: the worker delivers events by destination, with leases, backoff and dead letters`

Files: `internal/outbox/relay.go`. `Pass` as §5.6: one claim (limit `Workers × 4`), then the claimed
destinations on `Workers` goroutines; `Run` loops at `Interval`, at once after a pass with a full
batch, and refreshes the gauges from `GetOutboxRelayState` once a minute. A round: the read
transaction in the order of A2; keys opened with `vault.OpenNamed` (the previous one only while its
expiry is in the future); a pending test request first (one `io.pandokos.destination.test.v1` event
with the ID the administration chose; its outcome goes to the settle and touches neither the cursor
nor the failure state); then, only if the destination is enabled and due: replays, and the batch (4
at a time) or the head alone; the settle. No request starts after the first failure in sequence
order or after half the lease. The cursor
rule: `k` leading successes in sequence order give `cursor = seq of the k-th`; with no failure and
fewer events than the batch, `cursor = max(head_seq, last seq)`. The failure rule is §5.7:
`failures = previous + 1` when the head is the event that was failing, 1 for a new head; at
`MaxAttempts` park it, `failures = 0`, `next_attempt_at = now`. A context that ends between request
and settle settles nothing (that is the crash case). Hooks for tests are unexported fields
(`afterSend`, `beforeSettle`). Log lines and counters of §5.15. Verification: T-1030 to T-1037.

### C5. The janitor — `feat: retention of events and of limit events`

Files: `internal/outbox/janitor.go`. Hourly; tenants from `ListTenantsForWorker`; per tenant the
function with a batch of 5000 until `events < 5000`, at most 30 s per tenant and pass; hours from
`events.retention`, months from `retention.usage_months`. Totals to the stats (`outbox_event`,
`limit_event` for M44; `expired` for M55) and to `event retention applied`. Verification: T-1038,
T-1019.

### C6. Destinations in the API — `feat: event destinations: create, change, rotate, test, rewind, dead letters`

Files: `internal/admin/{event_destinations,convert,problem,endpoints}.go`. Ten operations (§5.11).
Create: `ValidateURL`, the types against `outbox.Types()` (tenant types only), `NewKey`, the ID
generated by the handler so that the secret is sealed for it (`SealNamed`) before the insert, the
audit row, the secret in the response; the idempotency store keeps the response without `secret`
(ADR 0059). Rotate: seal the new key, one update (the trigger moves the old one). Test: one update
with a new event ID, answer `202`; the handler sends nothing and opens nothing. Database refusals: `event_destination_name_key` and `event_destination_limit`
are `409 conflict`; the table checks are `400 invalid_request` (the handler validates first, so
they are a second line of defence). `delivery.state` as §5.8. Verification: T-1040, T-1041, T-1043,
T-1045.

### C7. Exports and the external reference — `feat: event export for tenants and for the platform; a tenant's external reference`

Files: `internal/admin/{events,tenants,tenant_deletion}.go`. `exportEvents` streams pages of
`ListOutboxEventsAfter` (1000 per statement) through `outbox.Render` until `limit` or the end;
`exportPlatformEvents` the same in platform scope (`tenantid` and `tenantref` when the row has a
tenant). Both count in `pandokos.exports` (M46) with the kinds `event` and `platform_event`.
`external_reference` in the tenant handlers, with its validation and its audit detail.
Verification: T-1042, T-1044; T-R71 still passes.

### C8. Command line — `feat: CLI for event destinations, event export and schemas`

Files: `internal/cli/{events,commands,cli}.go`, `cmd/pandokos/main.go` (`event schema` runs without
a connection). Verification: T-1046, T-219.

### C9. Receiver example — `feat: a webhook receiver example with signature check and deduplication`

Files: `examples/webhook-receiver/` (§5.16), with a `README.md` that shows the configuration (the
secret from an environment variable, never an argument), the three checks, and why the ID is the
key. Standard library only. Verification: T-1049; `go vet ./examples/...`.

### C10. Wiring, telemetry, end to end — `feat: the event relay and janitor in serve; instruments and log lines`

Files: `cmd/pandokos/{serve,metrics}.go`, `internal/telemetry/{instruments,sources}.go`. The relay
and the janitor start with the worker role, after the partitioner, and stop before the pools close;
the admin process sends no webhook request. The seven instruments in the catalogue.
Verification: T-1047, T-1048, T-1050 to T-1056; `make check`.

## Part D — Repository tests and CI (commit: `ci: sprint 6.4 repository tests; schemas in generate; the outbox benchmark`)

- `test/repo/events_test.go`: T-R81 to T-R84; `TestConfigurationReference`, `TestLogReference`,
  `TestMetricsReference` and T-R71 updated.
- `Makefile`: `generate` copies `internal/outbox/schema/*.schema.json` to `docs/reference/events/`;
  `bench` gains `go test ./internal/ledger/ -run '^$$' -bench BenchmarkLedgerBatchOutbox -benchmem`
  (it needs `make db-up`; the help text says so).
- `.github/workflows/ci.yml`: no new job, action or pin.

Verification: `make check`; `cd test/sdk && go vet ./... && go mod tidy -diff && go test -race -count=1 ./...`.

## Part E — Documentation and decision records (commit: `docs: sprint 6.4 events, destinations, the event reference and three decision records`)

### E1. Guides and references

- `docs/operations/events.md` (new): what is an event and when it is written; destinations and
  their limits; the request and how to verify it (with the example); retries, dead letters, replay
  and rewind; the pull export; retention and the bound of EVT-03; what a deletion does; the
  platform's events; the keys; the instruments and what to alert on (M57, M54, M55); the measured
  cost of the event on the ledger (plan F4).
- `docs/reference/events.md` (new): the envelope, every type with its `data` members, the schemas,
  the versioning rule.
- `docs/reference/{admin-api,permissions,logs,metrics,failure-behaviour}.md`,
  `docs/operations/{configuration,tenant-deletion,backup-and-restore,usage,limits,roles}.md`: the
  operations, permissions, lines, instruments, keys; two rows of the failure reference ("event
  destination", "worker crash in a delivery") citing T-1053 and T-1032; the backup guide's list of
  completed deletions from `pandokos platform-event export --type io.pandokos.tenant.deleted.v1`.
- `docs/architecture/data-model.md`: the "Outbox" section and the table list rewritten for the six
  tables; the erasure groups; `docs/architecture/security-model.md`: webhooks as an asset and a
  threat (SSRF, secret leak, redirection of a tenant's events); `overview.md`: the worker row and
  the failure table.
- `docs/requirements/README.md`: a note under EVT-03 on its bound, if O-3 is confirmed.
- `README.md`: the feature line and the link to the guide; `docs/releases/v0.3.0-draft.md` (§11).

### E2. Decision records

- **`docs/adr/0070-the-outbox-as-a-log-relay-by-destination-and-retention.md`** — refines ADR 0014
  ("Relay", "Guarantee"). Decision: (1) events are rows of an append-only log per tenant, written by
  triggers of the tables that hold the facts; (2) the sequence follows commit order by a row lock
  per tenant, and a transaction that also writes the audit chain locks the chain first; (3) the
  relay claims destinations with a lease, each with a cursor, not events; (4) the head of a queue is
  retried with backoff and parked after a number of attempts; replay and rewind; (5) at least once,
  no order guaranteed; (6) events live for a configured time, delivered or not, and a deleted
  tenant's undelivered events go with it. Alternatives: a delivery row per event and destination
  (ADR 0014 as written: a row written and updated per delivery, and an outage that writes);
  transaction IDs instead of a lock (break on dump and restore); events written by the Go writers
  (a second statement per batch, and a writer that can forget).
- **`docs/adr/0071-webhook-destinations-signature-secrets-and-egress.md`** — Decision: the request
  and signature of §5.9; server-generated secrets shown once, two valid during a rotation; sealed
  under the tenant's data key with their own associated data (amends ADR 0033: a second sealed
  object); the egress guard's public class, named private hosts, no redirects (extends ADR 0038 to a
  fourth caller); the test request from the admin process. Alternatives: asymmetric signatures (a
  key registry for receivers); mutual TLS (per-destination certificates); a secret supplied by the
  caller.
- **`docs/adr/0072-event-types-the-envelope-the-external-reference-and-platform-events.md`** —
  Decision: CloudEvents 1.0 structured JSON, the attributes and extensions of §5.3, versioned types
  with published schemas; the content of the usage event; what is not an event; the external
  reference; the platform's log, pull only. Alternatives: binary content mode; a platform webhook
  with a secret in the configuration; the reference set by the tenant.
- History lines: 0014, 0033, 0038, 0039, 0052, 0054, 0062, 0063, 0065, 0066, 0067 (one line each,
  dated, naming the sprint and the new record); `docs/adr/README.md` lists the three.

### E3. Planning files and traceability

`planning/roadmap.md` and the epic file are updated by the orchestrator at close; the requirement
table of `docs/requirements` gets the delivered marks of EVT-01 to EVT-04 and LIM-13.

## Part F — Verification (no commit unless something is found)

1. `make check`; the `sdk` job's commands of part D; `make image` and the container tests if the
   image job is run locally.
2. **Upgrade**: a database of v0.2.0 with data (tenants, usage, limit events, a completed deletion),
   `pandokos migrate` to 0017, `serve`: both audit chains verify, no event exists for the past, a
   call then produces one, and the suite of the v0.2.0 tag still passes its e2e upgrade test against
   0017 if run (0017 revokes nothing). Record the commands and outputs in the pull request.
3. **The exit criteria by hand**, once: T-1052's scenario with `examples/webhook-receiver` as a
   separate process and `kill -9` of the binary; T-1053's with the receiver stopped for ten minutes.
4. **The cost**: `make bench` (the outbox benchmark) and T-1050's logged figures on the CI machine;
   write them in `docs/operations/events.md` and the pull request. `EXPLAIN (ANALYZE, BUFFERS)` of
   `ListOutboxEventsAfter` with and without a type filter on a log of 10⁶ events, of one batch of
   `tenant_outbox_retention`, and of `outbox_relay_claim` over 10³ destinations; record them.
5. Read every new log line and metric of a run of T-1052 and T-1055 for a tenant ID, a destination,
   a URL or a secret.
6. Review by an agent that did not write them (the grouping at the top), with the checklist: no
   path writes a usage record without its event; a reader of `seq > n` misses nothing; a destination
   has one holder; a crash delivers again and loses nothing; no request leaves but through the
   guard; no secret outlives its round; no event of a tenant reaches the platform.

## What the implementer does not do

- No edit of `internal/ledger`, `internal/limits`, `internal/gateway` or `internal/resolver` (the
  benchmark file in `internal/ledger` excepted); no statement that writes an event.
- No platform webhook, no destination kind other than a webhook, no batching, no automatic
  disabling, no event for audit rows, unlimited traffic or currency changes (D-5, D-7).
- No destination or reference in `export`/`apply` or in the tenant data export (D-22).
- No change to migrations 0001 to 0016; no widening of a grant beyond §6 without asking.

## Appendix A3 — additions to `api/openapi/admin.v1.yaml`

Every operation has the responses `401`, `403`, `404`, `500`, `503` of its neighbours; operations
with a body add `400`, `413`, `415`; the two with `Idempotency-Key` add `422`
(`IdempotencyKeyReused`); `409` (`Conflict`) where noted. Tag `events`.

```yaml
paths:
  /v1/tenants/{tenant}/event-destinations:
    parameters: [{$ref: '#/components/parameters/Tenant'}]
    get:
      operationId: listEventDestinations
      summary: List the event destinations of a tenant, with the state of their delivery
      x-pandokos-permission: event_destination:read
      parameters: [{$ref: '#/components/parameters/Limit'}, {$ref: '#/components/parameters/Cursor'}]
      responses: {"200": {$ref: '#/components/responses/EventDestinationList'}}
    post:
      operationId: createEventDestination
      summary: Register a webhook destination; the signing secret is returned once
      description: >-
        The destination receives the events recorded after it is created. A tenant has at most 8
        (409 conflict), and names are unique in the tenant (409 conflict).
      x-pandokos-permission: event_destination:write
      parameters: [{$ref: '#/components/parameters/IdempotencyKey'}]
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/EventDestinationCreate'}}}}
      responses: {"201": {$ref: '#/components/responses/EventDestinationCreated'}, "409": {$ref: '#/components/responses/Conflict'}}
  /v1/tenants/{tenant}/event-destinations/{destination}:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/EventDestination'}]
    get:
      operationId: getEventDestination
      summary: Read an event destination and the state of its delivery
      x-pandokos-permission: event_destination:read
      responses: {"200": {$ref: '#/components/responses/EventDestination'}}
    patch:
      operationId: updateEventDestination
      summary: Change an event destination; enable or disable it
      description: Enabling a disabled destination clears its failure state; it resumes at its cursor.
      x-pandokos-permission: event_destination:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/EventDestinationUpdate'}}}}
      responses: {"200": {$ref: '#/components/responses/EventDestination'}, "409": {$ref: '#/components/responses/Conflict'}}
    delete:
      operationId: deleteEventDestination
      summary: Delete an event destination with its dead letters
      x-pandokos-permission: event_destination:delete
      responses: {"204": {description: Deleted.}}
  /v1/tenants/{tenant}/event-destinations/{destination}/rotate-secret:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/EventDestination'}]
    post:
      operationId: rotateEventDestinationSecret
      summary: Replace the signing secret; the new one is returned once
      description: The previous secret keeps signing, beside the new one, for previous_valid_for_seconds.
      x-pandokos-permission: event_destination:write
      parameters: [{$ref: '#/components/parameters/IdempotencyKey'}]
      requestBody: {required: false, content: {application/json: {schema: {$ref: '#/components/schemas/EventDestinationSecretRotate'}}}}
      responses: {"200": {$ref: '#/components/responses/EventDestinationCreated'}}
  /v1/tenants/{tenant}/event-destinations/{destination}/test:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/EventDestination'}]
    post:
      operationId: testEventDestination
      summary: Ask the worker to send one signed test event to the destination
      description: >-
        The event has the type io.pandokos.destination.test.v1 and the ID of the answer; it is not kept
        and moves no cursor. It is sent within seconds, to a disabled destination too; the outcome is
        the member test of the destination. A new request replaces one that is still waiting.
      x-pandokos-permission: event_destination:write
      responses: {"202": {$ref: '#/components/responses/EventDestinationTest'}}
  /v1/tenants/{tenant}/event-destinations/{destination}/rewind:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/EventDestination'}]
    post:
      operationId: rewindEventDestination
      summary: Deliver again the events after a sequence number or a time
      description: Exactly one of after and since. Events that retention removed are not delivered again. The cursor never moves forward.
      x-pandokos-permission: event_destination:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/EventDestinationRewind'}}}}
      responses: {"200": {$ref: '#/components/responses/EventDestination'}}
  /v1/tenants/{tenant}/event-destinations/{destination}/dead-letters:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/EventDestination'}]
    get:
      operationId: listEventDeadLetters
      summary: List the events the destination refused, oldest first
      x-pandokos-permission: event_destination:read
      parameters: [{$ref: '#/components/parameters/Limit'}, {$ref: '#/components/parameters/Cursor'}]
      responses: {"200": {$ref: '#/components/responses/EventDeadLetterList'}}
  /v1/tenants/{tenant}/event-destinations/{destination}/dead-letters/replay:
    parameters: [{$ref: '#/components/parameters/Tenant'}, {$ref: '#/components/parameters/EventDestination'}]
    post:
      operationId: replayEventDeadLetters
      summary: Deliver dead letters again, all of them or the given sequence numbers
      x-pandokos-permission: event_destination:write
      requestBody: {required: false, content: {application/json: {schema: {$ref: '#/components/schemas/EventDeadLetterReplay'}}}}
      responses: {"200": {$ref: '#/components/responses/EventDeadLetterReplayed'}}
  /v1/tenants/{tenant}/events/export:
    parameters: [{$ref: '#/components/parameters/Tenant'}]
    get:
      operationId: exportEvents
      summary: Export the tenant's events after a sequence number, oldest first, as NDJSON
      description: >-
        One CloudEvents 1.0 document per line. Ask again with the sequence of the last line: no
        event is missed and none is repeated. Events older than the retention are gone.
      x-pandokos-permission: event:read
      parameters: [{$ref: '#/components/parameters/EventAfter'}, {$ref: '#/components/parameters/EventExportLimit'}, {$ref: '#/components/parameters/EventTypeFilter'}]
      responses: {"200": {$ref: '#/components/responses/EventExport'}, "400": {$ref: '#/components/responses/InvalidRequest'}}
  /v1/platform-events/export:
    get:
      operationId: exportPlatformEvents
      summary: Export the platform's events after a sequence number, oldest first, as NDJSON
      x-pandokos-permission: platform_event:read
      parameters: [{$ref: '#/components/parameters/EventAfter'}, {$ref: '#/components/parameters/EventExportLimit'}, {$ref: '#/components/parameters/PlatformEventTypeFilter'}]
      responses: {"200": {$ref: '#/components/responses/EventExport'}, "400": {$ref: '#/components/responses/InvalidRequest'}}
components:
  parameters:
    EventDestination: {name: destination, in: path, required: true, description: Event destination ID., schema: {$ref: '#/components/schemas/ID'}}
    EventAfter: {name: after, in: query, description: Only events with a greater sequence number., schema: {type: integer, format: int64, minimum: 0, default: 0}}
    EventExportLimit: {name: limit, in: query, description: Maximum number of events., schema: {type: integer, minimum: 1, maximum: 10000, default: 1000}}
    EventTypeFilter: {name: type, in: query, description: Only events of these types., explode: true, schema: {type: array, maxItems: 16, uniqueItems: true, items: {$ref: '#/components/schemas/EventType'}}}
    PlatformEventTypeFilter: {name: type, in: query, description: Only events of these types., explode: true, schema: {type: array, maxItems: 16, uniqueItems: true, items: {$ref: '#/components/schemas/PlatformEventType'}}}
  responses:
    EventDestination: {description: The destination., content: {application/json: {schema: {$ref: '#/components/schemas/EventDestination'}}}}
    EventDestinationList: {description: One page of destinations., content: {application/json: {schema: {$ref: '#/components/schemas/EventDestinationList'}}}}
    EventDestinationCreated: {description: The destination and its signing secret., content: {application/json: {schema: {$ref: '#/components/schemas/EventDestinationCreated'}}}}
    EventDestinationTest: {description: The test request as recorded., content: {application/json: {schema: {$ref: '#/components/schemas/EventDestinationTest'}}}}
    EventDeadLetterList: {description: One page of dead letters., content: {application/json: {schema: {$ref: '#/components/schemas/EventDeadLetterList'}}}}
    EventDeadLetterReplayed: {description: How many dead letters will be delivered again., content: {application/json: {schema: {$ref: '#/components/schemas/EventDeadLetterReplayed'}}}}
    EventExport: {description: 'The events, oldest first, one CloudEvents document per line.', content: {application/x-ndjson: {schema: {$ref: '#/components/schemas/CloudEvent'}}}}
  schemas:
    EventType:
      type: string
      enum: [io.pandokos.usage.recorded.v1, io.pandokos.limit.threshold_reached.v1, io.pandokos.limit.exhausted.v1]
    PlatformEventType:
      type: string
      enum: [io.pandokos.tenant.deletion_requested.v1, io.pandokos.tenant.deletion_cancelled.v1, io.pandokos.tenant.deleted.v1,
             io.pandokos.kek.version_activated.v1, io.pandokos.kek.version_retired.v1]
    DeliveryOutcome: {type: string, enum: [delivered, status, redirect, timeout, connection, tls, blocked]}
    ExternalReference: {type: string, maxLength: 255, pattern: '^[^\x00-\x1f\x7f]*$', description: What the operator's other systems call the tenant; carried by every event.}
    WebhookURL: {type: string, minLength: 9, maxLength: 2048, description: 'An https URL without user information or fragment. It is readable by every holder of event_destination:read: put no secret in it.'}
    CloudEvent:
      type: object
      description: A CloudEvents 1.0 document; docs/reference/events.md has the types and their schemas.
      additionalProperties: true
      required: [specversion, id, source, type, time, data]
      properties:
        specversion: {type: string, enum: ["1.0"]}
        id: {type: string}
        source: {type: string}
        type: {type: string}
        time: {type: string, format: date-time}
        subject: {type: string}
        datacontenttype: {type: string}
        tenantid: {type: string}
        tenantref: {type: string}
        sequence: {type: string, pattern: '^[0-9]+$'}
        data: {type: object, additionalProperties: true}
    EventDelivery:
      type: object
      additionalProperties: false
      required: [state, cursor, head, failures, next_attempt_at, last_error, dead_letters, expired_events]
      properties:
        state: {type: string, enum: [idle, delivering, failing, disabled]}
        cursor: {type: integer, format: int64, description: Every event up to this sequence number is settled for the destination.}
        head: {type: integer, format: int64, description: The newest sequence number of the tenant's log.}
        failures: {type: integer, description: Consecutive failed attempts of the event at the head of the queue.}
        failing_since: {type: string, format: date-time}
        next_attempt_at: {type: string, format: date-time}
        last_success_at: {type: string, format: date-time}
        last_failure_at: {type: string, format: date-time}
        last_error: {type: string, description: 'The outcome of the last failed request; empty when none.'}
        last_status: {type: integer}
        dead_letters: {type: integer, format: int64}
        expired_events: {type: integer, format: int64, description: Events that retention removed before the destination received them.}
    EventDestination:
      type: object
      additionalProperties: false
      required: [id, tenant_id, name, description, url, event_types, enabled, secret_created_at, created_at, updated_at, delivery]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        tenant_id: {$ref: '#/components/schemas/ID'}
        name: {$ref: '#/components/schemas/Name'}
        description: {type: string}
        url: {$ref: '#/components/schemas/WebhookURL'}
        event_types: {type: array, items: {$ref: '#/components/schemas/EventType'}, description: Empty means every type.}
        enabled: {type: boolean}
        secret_created_at: {type: string, format: date-time}
        previous_secret_expires_at: {type: string, format: date-time, description: Present while a replaced secret still signs.}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
        delivery: {$ref: '#/components/schemas/EventDelivery'}
        test: {$ref: '#/components/schemas/EventDestinationTest', description: The last test request; absent when none was made.}
    EventDestinationCreate:
      type: object
      additionalProperties: false
      required: [name, url]
      properties:
        name: {$ref: '#/components/schemas/Name'}
        description: {type: string, maxLength: 500}
        url: {$ref: '#/components/schemas/WebhookURL'}
        event_types: {type: array, maxItems: 16, uniqueItems: true, items: {$ref: '#/components/schemas/EventType'}}
        enabled: {type: boolean, default: true}
    EventDestinationUpdate:
      type: object
      additionalProperties: false
      minProperties: 1
      properties:
        name: {$ref: '#/components/schemas/Name'}
        description: {type: string, maxLength: 500}
        url: {$ref: '#/components/schemas/WebhookURL'}
        event_types: {type: array, maxItems: 16, uniqueItems: true, items: {$ref: '#/components/schemas/EventType'}}
        enabled: {type: boolean}
    EventDestinationCreated:
      type: object
      additionalProperties: false
      required: [destination]
      properties:
        destination: {$ref: '#/components/schemas/EventDestination'}
        secret: {type: string, description: 'The signing secret (whsec_…). It is returned by the first response only; absent on an idempotent replay.'}
    EventDestinationSecretRotate:
      type: object
      additionalProperties: false
      properties:
        previous_valid_for_seconds: {type: integer, minimum: 0, maximum: 604800, default: 86400}
    EventDestinationRewind:
      type: object
      additionalProperties: false
      minProperties: 1
      maxProperties: 1
      properties:
        after: {type: integer, format: int64, minimum: 0}
        since: {type: string, format: date-time}
    EventDestinationTest:
      type: object
      additionalProperties: false
      required: [event_id, requested_at]
      properties:
        event_id: {type: string, description: The ID the test event carries.}
        requested_at: {type: string, format: date-time}
        completed_at: {type: string, format: date-time, description: Absent while the request waits for the worker.}
        outcome: {$ref: '#/components/schemas/DeliveryOutcome'}
        status: {type: integer}
    EventDestinationList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items: {type: array, items: {$ref: '#/components/schemas/EventDestination'}}
        next_cursor: {type: string, description: Absent on the last page.}
    EventDeadLetter:
      type: object
      additionalProperties: false
      required: [seq, event_id, type, state, attempts, reason, parked_at]
      properties:
        seq: {type: integer, format: int64}
        event_id: {type: string}
        type: {$ref: '#/components/schemas/EventType'}
        state: {type: string, enum: [dead, replay]}
        attempts: {type: integer}
        reason: {$ref: '#/components/schemas/DeliveryOutcome'}
        status: {type: integer}
        parked_at: {type: string, format: date-time}
        replay_requested_at: {type: string, format: date-time}
    EventDeadLetterList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items: {type: array, items: {$ref: '#/components/schemas/EventDeadLetter'}}
        next_cursor: {type: string, description: Absent on the last page.}
    EventDeadLetterReplay:
      type: object
      additionalProperties: false
      properties:
        seqs: {type: array, maxItems: 1000, uniqueItems: true, items: {type: integer, format: int64, minimum: 1}, description: Absent or empty means every dead letter of the destination.}
    EventDeadLetterReplayed:
      type: object
      additionalProperties: false
      required: [replayed]
      properties:
        replayed: {type: integer, format: int64}
```

`Tenant` gains the required property `external_reference: {$ref: '#/components/schemas/ExternalReference'}`;
`TenantCreate` and `TenantUpdate` gain it as optional; `TenantDeletion` gains it as required and
read-only. If the generator does not accept a compact form used above (a flow mapping, `explode`,
`minProperties` with `maxProperties`), write the same content in the form its neighbours use and
validate the pair `after`/`since` in the handler.

## Appendix A1 — `internal/store/migrations/sql/0017_events.sql`

```sql
-- 0017_events: the external reference of a tenant; the event log of a tenant (outbox), written in
-- the transaction that records the fact; webhook destinations with the state of their delivery;
-- dead letters; the claim of the relay and its state; retention of events and of limit events;
-- the event log of the platform; and the erasure of all of it with the tenant (ADR 0070, ADR 0071,
-- ADR 0072).
-- Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- ---------------------------------------------------------------------------------------------
-- 1. The external reference of a tenant: what the operator's other systems call it. Set by the
--    platform, carried by every event, copied into the deletion request.
-- ---------------------------------------------------------------------------------------------

ALTER TABLE pandokos.tenant
    ADD COLUMN external_reference text NOT NULL DEFAULT '',
    ADD CONSTRAINT tenant_external_reference_format
        CHECK (char_length(external_reference) <= 255 AND external_reference !~ '[[:cntrl:]]');
GRANT INSERT (external_reference), UPDATE (external_reference) ON pandokos.tenant TO pandokos_admin;

-- The reference is the platform's: the administration role may update the row of the tenant of
-- its session (time zone, active currency), but not this column.
CREATE FUNCTION pandokos.tenant_external_reference_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.external_reference IS DISTINCT FROM OLD.external_reference AND NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'the external reference of a tenant changes in the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_external_reference_guard() FROM PUBLIC;
CREATE TRIGGER tenant_external_reference_guard BEFORE UPDATE OF external_reference ON pandokos.tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_external_reference_guard();

ALTER TABLE pandokos.tenant_deletion ADD COLUMN external_reference text NOT NULL DEFAULT '';
GRANT SELECT (external_reference) ON pandokos.tenant_deletion TO pandokos_admin;

-- The request keeps the reference the tenant has when it is made: the event "tenant deleted" is
-- written when the tenant's configuration is gone.
CREATE OR REPLACE FUNCTION pandokos.tenant_deletion_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_ref text;
BEGIN
    SELECT x.external_reference INTO v_ref FROM pandokos.tenant x WHERE x.id = NEW.tenant_id AND x.state = 'suspended';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'a deletion is requested for a suspended tenant' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_deletion_suspended';
    END IF;
    NEW.state := 'scheduled';
    NEW.step := 'begin';
    NEW.requested_at := pg_catalog.now();
    NEW.external_reference := v_ref;
    RETURN NEW;
END
$$;

-- A timestamp as the events carry it: RFC 3339, UTC, microseconds.
CREATE FUNCTION pandokos.rfc3339(p timestamptz) RETURNS text
    LANGUAGE sql STABLE PARALLEL SAFE
    SET search_path = pg_catalog, pg_temp
    RETURN pg_catalog.to_char(pg_catalog.timezone('UTC', p), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"');
REVOKE ALL ON FUNCTION pandokos.rfc3339(timestamptz) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------
-- 2. The event log of a tenant. A row is an event: appended in the transaction that records the
--    fact, by the triggers of section 3 and by nothing else; never changed; removed by retention
--    and with the tenant. seq grows in the order of the commits (the row of outbox_stream is
--    locked from the first event of a transaction to its commit), so a reader that asks for
--    "seq > n" never misses a row. It is not gapless.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.outbox_stream (
    tenant_id uuid   NOT NULL,
    last_seq  bigint NOT NULL DEFAULT 0,
    CONSTRAINT outbox_stream_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT outbox_stream_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT outbox_stream_last_seq_valid CHECK (last_seq >= 0)
);

ALTER TABLE pandokos.outbox_stream ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.outbox_stream FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.outbox_stream
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE TRIGGER outbox_stream_tenant_open BEFORE INSERT ON pandokos.outbox_stream
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
GRANT SELECT ON pandokos.outbox_stream TO pandokos_admin, pandokos_worker;

CREATE TABLE pandokos.outbox_event (
    tenant_id   uuid        NOT NULL,
    seq         bigint      NOT NULL,
    id          uuid        NOT NULL,
    type        text        NOT NULL,
    subject     text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    data        jsonb       NOT NULL,
    CONSTRAINT outbox_event_pkey PRIMARY KEY (tenant_id, seq),
    CONSTRAINT outbox_event_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT outbox_event_seq_valid CHECK (seq >= 1),
    CONSTRAINT outbox_event_type_format CHECK (type ~ '^io\.pandokos\.[a-z][a-z_]*(\.[a-z][a-z_]*)+\.v[1-9][0-9]?$'),
    CONSTRAINT outbox_event_subject_length CHECK (char_length(subject) <= 255),
    CONSTRAINT outbox_event_data_object CHECK (jsonb_typeof(data) = 'object')
);

CREATE TRIGGER outbox_event_append_only BEFORE UPDATE OR DELETE ON pandokos.outbox_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER outbox_event_no_truncate BEFORE TRUNCATE ON pandokos.outbox_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER outbox_event_tenant_open BEFORE INSERT ON pandokos.outbox_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

ALTER TABLE pandokos.outbox_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.outbox_event FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.outbox_event
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The admin role reads (the export); the worker reads (the relay). No runtime role writes.
GRANT SELECT ON pandokos.outbox_event TO pandokos_admin, pandokos_worker;

-- Reserves p_count sequence numbers of a tenant and returns the one before the first. The row
-- stays locked until the transaction ends: that is what orders the log.
CREATE FUNCTION pandokos.outbox_seq_reserve(p_tenant uuid, p_count bigint) RETURNS bigint
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_last bigint;
BEGIN
    INSERT INTO pandokos.outbox_stream AS s (tenant_id, last_seq)
    VALUES (p_tenant, p_count)
    ON CONFLICT (tenant_id) DO UPDATE SET last_seq = s.last_seq + EXCLUDED.last_seq
    RETURNING s.last_seq INTO v_last;
    RETURN v_last - p_count;
END
$$;
REVOKE ALL ON FUNCTION pandokos.outbox_seq_reserve(uuid, bigint) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------
-- 3. The facts that are events of a tenant: a usage record and a limit event. The event is
--    written by a trigger of the table that holds the fact, so no writer can record one without
--    the other, and a row that a conflict left out has no event.
-- ---------------------------------------------------------------------------------------------

-- One event per usage record, in the statement that inserts the records (the batch of the
-- ledger writer): one reservation and one insert per statement. The data of
-- io.pandokos.usage.recorded.v1 is the record without its provider-specific usage object; it
-- holds no content, because the ledger has none. The expression is written here and not in a
-- function of its own: a function with a search_path is called once per row.
CREATE FUNCTION pandokos.usage_record_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    v_tenant   uuid;
    v_count    bigint;
    v_base     bigint;
BEGIN
    -- The rows of a statement of a runtime role are all of the tenant of its session (the policy
    -- of usage_record). A superuser, whom no policy binds, may insert without a session or for
    -- several tenants: the log of each tenant is then written in a session of that tenant.
    FOR v_tenant, v_count IN SELECT r.tenant_id, pg_catalog.count(*) FROM inserted r GROUP BY r.tenant_id LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', v_tenant::text, true);
        v_base := pandokos.outbox_seq_reserve(v_tenant, v_count);
        INSERT INTO pandokos.outbox_event (tenant_id, seq, id, type, subject, occurred_at, data)
        SELECT r.tenant_id, v_base + pg_catalog.row_number() OVER (ORDER BY r.started_at, r.id), r.id,
               'io.pandokos.usage.recorded.v1', r.request_id, r.recorded_at,
               pg_catalog.jsonb_strip_nulls(pg_catalog.jsonb_build_object(
                   'record_id', r.id,
                   'request_id', r.request_id,
                   'trace_id', NULLIF(r.trace_id, ''),
                   'started_at', pg_catalog.to_char(pg_catalog.timezone('UTC', r.started_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
                   'duration_ms', r.duration_ms,
                   'principal', pg_catalog.jsonb_build_object('kind', r.principal_kind, 'id', r.principal_id),
                   'key_id', NULLIF(r.key_id, ''),
                   'provider', r.provider,
                   'api_family', r.api_family,
                   'operation', r.operation,
                   'model', NULLIF(r.model, ''),
                   'upstream_model', NULLIF(r.upstream_model, ''),
                   'stream', r.stream,
                   'outcome', r.outcome,
                   'error_code', NULLIF(r.error_code, ''),
                   'usage', pg_catalog.jsonb_build_object(
                       'input_tokens', r.input_tokens, 'output_tokens', r.output_tokens,
                       'cache_read_tokens', r.cache_read_tokens, 'cache_write_tokens', r.cache_write_tokens,
                       'reasoning_tokens', r.reasoning_tokens,
                       'input_source', r.input_source, 'output_source', r.output_source,
                       'other_units', NULLIF(r.other_units, '{}'::jsonb)),
                   'cost', pg_catalog.jsonb_build_object(
                       'status', r.cost_status, 'reason', NULLIF(r.cost_reason, ''),
                       'amount', pg_catalog.trim_scale(r.cost)::text, 'currency', r.currency,
                       'price_version_id', r.price_version_id, 'price_source', r.price_source,
                       'fallbacks', CASE WHEN r.cost_fallbacks <> '{}'::text[] THEN pg_catalog.to_jsonb(r.cost_fallbacks) END),
                   'limit_state', r.limit_state,
                   'metadata', NULLIF(r.metadata, '{}'::jsonb)))
          FROM inserted r
         WHERE r.tenant_id = v_tenant;
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.usage_record_outbox() FROM PUBLIC;
CREATE TRIGGER usage_record_outbox AFTER INSERT ON pandokos.usage_record
    REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.usage_record_outbox();

-- One event per limit event (5.2): io.pandokos.limit.threshold_reached.v1 or
-- io.pandokos.limit.exhausted.v1, with the ID of the limit_event row.
CREATE FUNCTION pandokos.limit_event_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    v_cost     boolean := NEW.metric IN ('cost_per_day', 'cost_per_month');
BEGIN
    PERFORM pg_catalog.set_config('app.tenant_id', NEW.tenant_id::text, true);
    INSERT INTO pandokos.outbox_event (tenant_id, seq, id, type, subject, occurred_at, data)
    VALUES (NEW.tenant_id, pandokos.outbox_seq_reserve(NEW.tenant_id, 1) + 1, NEW.id,
            'io.pandokos.limit.' || NEW.kind || '.v1', NEW.rule_id::text, NEW.occurred_at,
            pg_catalog.jsonb_strip_nulls(pg_catalog.jsonb_build_object(
                'limit_event_id', NEW.id,
                'rule_id', NEW.rule_id,
                'rule_level', NEW.rule_level,
                'subject', pg_catalog.jsonb_build_object('kind', NEW.subject_kind, 'id', NULLIF(NEW.subject_id, '')),
                'provider', NULLIF(NEW.provider, ''),
                'model', NULLIF(NEW.model, ''),
                'metric', NEW.metric,
                'currency', NULLIF(NEW.currency, ''),
                'period', pg_catalog.jsonb_build_object('start', pandokos.rfc3339(NEW.period_start), 'end', pandokos.rfc3339(NEW.period_end)),
                'percent', CASE WHEN NEW.kind = 'threshold_reached' THEN NEW.percent END,
                'limit', CASE WHEN NOT v_cost THEN NEW.limit_value END,
                'amount', CASE WHEN v_cost THEN pg_catalog.trim_scale(NEW.limit_value::numeric / 1000000)::text END,
                'observed', pg_catalog.trim_scale(NEW.observed)::text)));
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.limit_event_outbox() FROM PUBLIC;
CREATE TRIGGER limit_event_outbox AFTER INSERT ON pandokos.limit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.limit_event_outbox();

-- ---------------------------------------------------------------------------------------------
-- 4. Webhook destinations of a tenant, with the state of their delivery. The administration
--    writes the configuration, the worker the state; column grants keep them apart, and the
--    admin role cannot read a sealed secret (as for provider credentials). cursor_seq is the
--    event up to which everything is settled for the destination: delivered, parked as a dead
--    letter, not of its types, or removed by retention. A test request is asked by the
--    administration (test_requested_at, test_event_id) and sent by the worker.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.event_destination (
    tenant_id                   uuid        NOT NULL,
    id                          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name                        text        NOT NULL,
    description                 text        NOT NULL DEFAULT '',
    url                         text        NOT NULL,
    event_types                 text[]      NOT NULL DEFAULT '{}',
    enabled                     boolean     NOT NULL DEFAULT true,
    secret                      bytea       NOT NULL,
    secret_key_version          integer     NOT NULL,
    secret_created_at           timestamptz NOT NULL DEFAULT now(),
    previous_secret             bytea,
    previous_secret_key_version integer,
    previous_secret_expires_at  timestamptz,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    cursor_seq                  bigint      NOT NULL DEFAULT 0,
    failures                    integer     NOT NULL DEFAULT 0,
    failing_since               timestamptz,
    next_attempt_at             timestamptz NOT NULL DEFAULT now(),
    lease_token                 uuid,
    lease_until                 timestamptz,
    last_success_at             timestamptz,
    last_failure_at             timestamptz,
    last_error                  text        NOT NULL DEFAULT '',
    last_status                 integer,
    expired_events              bigint      NOT NULL DEFAULT 0,
    test_requested_at           timestamptz,
    test_event_id               uuid,
    test_completed_at           timestamptz,
    test_outcome                text        NOT NULL DEFAULT '',
    test_status                 integer,
    CONSTRAINT event_destination_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT event_destination_id_key UNIQUE (id),
    CONSTRAINT event_destination_name_key UNIQUE (tenant_id, name),
    CONSTRAINT event_destination_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT event_destination_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT event_destination_description_length CHECK (char_length(description) <= 500),
    CONSTRAINT event_destination_url_format CHECK (char_length(url) <= 2048 AND url ~ '^https?://[^[:space:][:cntrl:]]+$'),
    CONSTRAINT event_destination_event_types_valid CHECK (
        cardinality(event_types) <= 16
        AND event_types <@ ARRAY['io.pandokos.usage.recorded.v1', 'io.pandokos.limit.threshold_reached.v1',
                                 'io.pandokos.limit.exhausted.v1']::text[]),
    CONSTRAINT event_destination_secret_length CHECK (octet_length(secret) BETWEEN 30 AND 512),
    CONSTRAINT event_destination_previous_secret CHECK (
        (previous_secret IS NULL) = (previous_secret_key_version IS NULL)
        AND (previous_secret IS NULL) = (previous_secret_expires_at IS NULL)
        AND (previous_secret IS NULL OR octet_length(previous_secret) BETWEEN 30 AND 512)),
    CONSTRAINT event_destination_state_valid CHECK (
        cursor_seq >= 0 AND failures >= 0 AND expired_events >= 0
        AND (failures = 0) = (failing_since IS NULL)
        AND (lease_token IS NULL) = (lease_until IS NULL)
        AND last_error ~ '^[a-z_]{0,40}$'
        AND (last_status IS NULL OR last_status BETWEEN 100 AND 599)),
    CONSTRAINT event_destination_test_valid CHECK (
        (test_requested_at IS NULL) = (test_event_id IS NULL)
        AND test_outcome ~ '^[a-z_]{0,40}$'
        AND (test_status IS NULL OR test_status BETWEEN 100 AND 599))
);

ALTER TABLE pandokos.event_destination ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.event_destination FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.event_destination
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE TRIGGER event_destination_tenant_open BEFORE INSERT ON pandokos.event_destination
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

-- A destination starts at the head of the log: it receives the events recorded after it was
-- created. A tenant has at most 8. When the secret is replaced, the trigger keeps the replaced one
-- as the previous secret if the statement gives it an expiry, and drops it otherwise: the
-- administration, which cannot read the column, never copies a sealed secret itself.
CREATE FUNCTION pandokos.event_destination_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('pandokos.event_destination:' || NEW.tenant_id::text, 0));
        IF (SELECT pg_catalog.count(*) FROM pandokos.event_destination d WHERE d.tenant_id = NEW.tenant_id) >= 8 THEN
            RAISE EXCEPTION 'a tenant has at most 8 event destinations' USING ERRCODE = 'check_violation', CONSTRAINT = 'event_destination_limit';
        END IF;
        NEW.created_at := pg_catalog.now();
        NEW.updated_at := NEW.created_at;
        NEW.secret_created_at := NEW.created_at;
        NEW.cursor_seq := COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = NEW.tenant_id), 0);
        NEW.failures := 0;
        NEW.failing_since := NULL;
        NEW.next_attempt_at := NEW.created_at;
        NEW.lease_token := NULL;
        NEW.lease_until := NULL;
        NEW.expired_events := 0;
        RETURN NEW;
    END IF;
    IF NEW.secret IS DISTINCT FROM OLD.secret THEN
        NEW.secret_created_at := pg_catalog.now();
        IF NEW.previous_secret_expires_at IS NOT NULL THEN
            NEW.previous_secret := OLD.secret;
            NEW.previous_secret_key_version := OLD.secret_key_version;
        END IF;
    END IF;
    IF NEW.previous_secret_expires_at IS NULL THEN
        NEW.previous_secret := NULL;
        NEW.previous_secret_key_version := NULL;
    END IF;
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.event_destination_guard() FROM PUBLIC;
CREATE TRIGGER event_destination_guard BEFORE INSERT OR UPDATE ON pandokos.event_destination
    FOR EACH ROW EXECUTE FUNCTION pandokos.event_destination_guard();

-- The worker reads everything, the secrets included: it signs. The administration reads
-- everything but the two sealed secrets.
GRANT SELECT ON pandokos.event_destination TO pandokos_worker;
GRANT SELECT (tenant_id, id, name, description, url, event_types, enabled, secret_key_version, secret_created_at,
              previous_secret_key_version, previous_secret_expires_at, created_at, updated_at, cursor_seq, failures,
              failing_since, next_attempt_at, lease_token, lease_until, last_success_at, last_failure_at, last_error,
              last_status, expired_events, test_requested_at, test_event_id, test_completed_at, test_outcome, test_status)
    ON pandokos.event_destination TO pandokos_admin;
GRANT INSERT (tenant_id, id, name, description, url, event_types, enabled, secret, secret_key_version)
    ON pandokos.event_destination TO pandokos_admin;
-- The administration changes the configuration, replaces the secret, asks for a test, moves the
-- cursor back (replay) and, with a replay or an enabling, releases the lease and the back-off.
GRANT UPDATE (name, description, url, event_types, enabled, secret, secret_key_version, previous_secret_expires_at,
              updated_at, cursor_seq, failures, failing_since, next_attempt_at, lease_token, lease_until,
              test_requested_at, test_event_id)
    ON pandokos.event_destination TO pandokos_admin;
GRANT DELETE ON pandokos.event_destination TO pandokos_admin;
-- The worker settles a round of delivery and a test, and nothing else.
GRANT UPDATE (cursor_seq, failures, failing_since, next_attempt_at, lease_token, lease_until,
              last_success_at, last_failure_at, last_error, last_status, test_completed_at, test_outcome, test_status)
    ON pandokos.event_destination TO pandokos_worker;

-- Events that a destination refused max_attempts times while it was at the head of its queue:
-- parked, shown, and delivered again when a replay is asked ('replay'). The event itself stays
-- in the log until retention removes it, and the dead letter with it.
CREATE TABLE pandokos.outbox_dead_letter (
    tenant_id           uuid        NOT NULL,
    destination_id      uuid        NOT NULL,
    seq                 bigint      NOT NULL,
    event_id            uuid        NOT NULL,
    type                text        NOT NULL,
    state               text        NOT NULL DEFAULT 'dead',
    attempts            integer     NOT NULL,
    reason              text        NOT NULL,
    status              integer,
    parked_at           timestamptz NOT NULL DEFAULT now(),
    replay_requested_at timestamptz,
    CONSTRAINT outbox_dead_letter_pkey PRIMARY KEY (tenant_id, destination_id, seq),
    CONSTRAINT outbox_dead_letter_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT outbox_dead_letter_destination_fkey FOREIGN KEY (tenant_id, destination_id)
        REFERENCES pandokos.event_destination (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT outbox_dead_letter_state_valid CHECK (state IN ('dead', 'replay')),
    CONSTRAINT outbox_dead_letter_values_valid CHECK (
        seq >= 1 AND attempts >= 1 AND reason ~ '^[a-z_]{1,40}$' AND (status IS NULL OR status BETWEEN 100 AND 599))
);
CREATE INDEX outbox_dead_letter_replay_idx ON pandokos.outbox_dead_letter (tenant_id, destination_id) WHERE state = 'replay';

ALTER TABLE pandokos.outbox_dead_letter ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.outbox_dead_letter FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.outbox_dead_letter
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE TRIGGER outbox_dead_letter_tenant_open BEFORE INSERT ON pandokos.outbox_dead_letter
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

GRANT SELECT ON pandokos.outbox_dead_letter TO pandokos_admin, pandokos_worker;
GRANT UPDATE (state, replay_requested_at) ON pandokos.outbox_dead_letter TO pandokos_admin;
GRANT INSERT (tenant_id, destination_id, seq, event_id, type, attempts, reason, status) ON pandokos.outbox_dead_letter TO pandokos_worker;
GRANT UPDATE (state, attempts, reason, status, parked_at) ON pandokos.outbox_dead_letter TO pandokos_worker;
GRANT DELETE ON pandokos.outbox_dead_letter TO pandokos_worker;

-- Which tenants have destinations (global table, identifiers only, like the other directories):
-- what the claim of the relay walks. Written by the trigger below and by the erasure; no runtime
-- role holds a privilege on it.
CREATE TABLE pandokos.event_destination_directory (
    destination_id uuid NOT NULL,
    tenant_id      uuid NOT NULL,
    CONSTRAINT event_destination_directory_pkey PRIMARY KEY (destination_id),
    CONSTRAINT event_destination_directory_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);
CREATE INDEX event_destination_directory_tenant_idx ON pandokos.event_destination_directory (tenant_id);


CREATE FUNCTION pandokos.event_destination_directory_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM pandokos.event_destination_directory d WHERE d.destination_id = OLD.id;
    ELSE
        INSERT INTO pandokos.event_destination_directory (destination_id, tenant_id) VALUES (NEW.id, NEW.tenant_id);
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.event_destination_directory_sync() FROM PUBLIC;
CREATE TRIGGER event_destination_directory_sync AFTER INSERT OR DELETE ON pandokos.event_destination
    FOR EACH ROW EXECUTE FUNCTION pandokos.event_destination_directory_sync();

-- In platform scope: gives the caller, for p_lease_seconds, up to p_limit destinations that are
-- not leased and either have a test request waiting, or are enabled, due, and have an event
-- after their cursor or a dead letter to replay.
-- It answers identifiers only. A lease that is not settled expires by itself: another worker
-- then delivers the same events again (at least once). Workers never wait for each other
-- (SKIP LOCKED).
CREATE FUNCTION pandokos.outbox_relay_claim(p_token uuid, p_lease_seconds integer, p_limit integer)
    RETURNS TABLE (tenant_id uuid, destination_id uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    v_left     integer := p_limit;
    v_count    integer;
    t          uuid;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'outbox_relay_claim needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_token IS NULL OR p_lease_seconds IS NULL OR p_lease_seconds NOT BETWEEN 10 AND 600
       OR p_limit IS NULL OR p_limit NOT BETWEEN 1 AND 1000 THEN
        RAISE EXCEPTION 'invalid claim' USING ERRCODE = 'check_violation';
    END IF;
    FOR t IN SELECT x.tenant_id FROM (SELECT DISTINCT d.tenant_id FROM pandokos.event_destination_directory d) x
              ORDER BY pg_catalog.random() LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', t::text, true);
        RETURN QUERY
        UPDATE pandokos.event_destination u
           SET lease_token = p_token, lease_until = pg_catalog.now() + pg_catalog.make_interval(secs => p_lease_seconds)
         WHERE u.tenant_id = t
           AND u.id IN (SELECT y.id
                          FROM pandokos.event_destination y
                         WHERE y.tenant_id = t
                           AND (y.lease_until IS NULL OR y.lease_until <= pg_catalog.now())
                           AND ((y.test_requested_at IS NOT NULL
                                 AND (y.test_completed_at IS NULL OR y.test_completed_at < y.test_requested_at))
                                OR (y.enabled AND y.next_attempt_at <= pg_catalog.now()
                                    AND (y.cursor_seq < COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = t), 0)
                                         OR EXISTS (SELECT 1 FROM pandokos.outbox_dead_letter l
                                                     WHERE l.tenant_id = t AND l.destination_id = y.id AND l.state = 'replay'))))
                         ORDER BY y.next_attempt_at
                         LIMIT v_left
                           FOR UPDATE SKIP LOCKED)
        RETURNING u.tenant_id, u.id;
        GET DIAGNOSTICS v_count = ROW_COUNT;
        v_left := v_left - v_count;
        EXIT WHEN v_left <= 0;
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;
REVOKE ALL ON FUNCTION pandokos.outbox_relay_claim(uuid, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.outbox_relay_claim(uuid, integer, integer) TO pandokos_worker;

-- In platform scope: the state of delivery over the installation, for the metrics. It answers
-- counts and one age, never an identifier and nothing of one tenant.
CREATE FUNCTION pandokos.outbox_relay_state(OUT destinations bigint, OUT failing bigint, OUT disabled bigint,
                                           OUT dead_letters bigint, OUT oldest_pending_seconds double precision)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    t          uuid;
    r          record;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'outbox_relay_state needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    destinations := 0;
    failing := 0;
    disabled := 0;
    dead_letters := 0;
    oldest_pending_seconds := 0;
    FOR t IN SELECT DISTINCT d.tenant_id FROM pandokos.event_destination_directory d LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', t::text, true);
        SELECT pg_catalog.count(*) FILTER (WHERE d.enabled) AS enabled,
               pg_catalog.count(*) FILTER (WHERE d.enabled AND d.failures > 0) AS failing,
               pg_catalog.count(*) FILTER (WHERE NOT d.enabled) AS disabled,
               COALESCE(pg_catalog.max(EXTRACT(EPOCH FROM pg_catalog.now() - (
                   SELECT o.recorded_at FROM pandokos.outbox_event o
                    WHERE o.tenant_id = t AND o.seq > d.cursor_seq ORDER BY o.seq LIMIT 1))) FILTER (WHERE d.enabled), 0) AS oldest
          INTO r
          FROM pandokos.event_destination d
         WHERE d.tenant_id = t;
        destinations := destinations + r.enabled;
        failing := failing + r.failing;
        disabled := disabled + r.disabled;
        oldest_pending_seconds := GREATEST(oldest_pending_seconds, r.oldest::double precision);
        dead_letters := dead_letters + (SELECT pg_catalog.count(*) FROM pandokos.outbox_dead_letter l WHERE l.tenant_id = t AND l.state = 'dead');
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;
REVOKE ALL ON FUNCTION pandokos.outbox_relay_state() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.outbox_relay_state() TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 5. Retention. In the session of a tenant: removes one batch of the events recorded more than
--    p_event_hours ago, from the oldest; moves past them the cursor of every destination that had
--    not passed them, counting them as expired for the enabled ones; removes the dead letters of
--    removed events; and, with p_usage_months > 0, the limit events older than the retention of
--    usage (their rule and period are then long closed). The worker calls it until events < p_batch.
-- ---------------------------------------------------------------------------------------------

CREATE FUNCTION pandokos.tenant_outbox_retention(p_event_hours integer, p_usage_months integer, p_batch integer,
                                               OUT events bigint, OUT expired bigint, OUT limit_events bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    t      uuid := pandokos.current_tenant_id();
    v_cut  timestamptz := pg_catalog.now() - pg_catalog.make_interval(hours => p_event_hours);
    v_seqs bigint[];
    v_max  bigint;
    v_day  date;
BEGIN
    IF t IS NULL THEN
        RAISE EXCEPTION 'tenant_outbox_retention needs a tenant session' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_event_hours IS NULL OR p_event_hours NOT BETWEEN 1 AND 2160
       OR p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_batch IS NULL OR p_batch NOT BETWEEN 100 AND 50000 THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    expired := 0;
    limit_events := 0;
    PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
    WITH oldest AS (SELECT o.seq, o.recorded_at FROM pandokos.outbox_event o WHERE o.tenant_id = t ORDER BY o.seq LIMIT p_batch),
         gone AS (DELETE FROM pandokos.outbox_event e USING oldest x
                   WHERE e.tenant_id = t AND e.seq = x.seq AND x.recorded_at < v_cut
                  RETURNING e.seq)
    SELECT COALESCE(pg_catalog.array_agg(g.seq), '{}'), pg_catalog.max(g.seq) INTO v_seqs, v_max FROM gone g;
    events := pg_catalog.cardinality(v_seqs);
    IF events > 0 THEN
        WITH behind AS (
            SELECT d.id, d.enabled, (SELECT pg_catalog.count(*) FROM pg_catalog.unnest(v_seqs) s WHERE s > d.cursor_seq) AS n
              FROM pandokos.event_destination d
             WHERE d.tenant_id = t AND d.cursor_seq < v_max
               FOR UPDATE OF d),
             moved AS (
            UPDATE pandokos.event_destination d
               SET cursor_seq = v_max,
                   expired_events = d.expired_events + CASE WHEN b.enabled THEN b.n ELSE 0 END
              FROM behind b
             WHERE d.tenant_id = t AND d.id = b.id
            RETURNING CASE WHEN b.enabled THEN b.n ELSE 0 END AS n)
        SELECT COALESCE(pg_catalog.sum(m.n), 0) INTO expired FROM moved m;
        DELETE FROM pandokos.outbox_dead_letter l WHERE l.tenant_id = t AND l.seq = ANY (v_seqs);
    END IF;
    IF p_usage_months > 0 THEN
        v_day := (pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date
                  - pg_catalog.make_interval(months => p_usage_months))::date;
        DELETE FROM pandokos.limit_event e WHERE e.tenant_id = t AND e.occurred_at < (v_day::timestamp AT TIME ZONE 'UTC');
        GET DIAGNOSTICS limit_events = ROW_COUNT;
    END IF;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    IF limit_events > 0 THEN
        INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome, detail)
        VALUES (t, 'platform', 'retention', 'limit_event.retention', 'limit_event', 'success',
                pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_day, 'YYYY-MM-DD'),
                                              'months', p_usage_months, 'deleted', limit_events));
    END IF;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_outbox_retention(integer, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.tenant_outbox_retention(integer, integer, integer) TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 6. The event log of the platform (global table): what happened to tenants and to the
--    key-encryption key, without a figure of usage. Written by the functions and triggers below,
--    read by the administration in platform scope, never changed and not removed.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.platform_outbox_stream (
    id       smallint NOT NULL DEFAULT 1,
    last_seq bigint   NOT NULL DEFAULT 0,
    CONSTRAINT platform_outbox_stream_pkey PRIMARY KEY (id),
    CONSTRAINT platform_outbox_stream_one CHECK (id = 1 AND last_seq >= 0)
);
INSERT INTO pandokos.platform_outbox_stream (id) VALUES (1);

CREATE TABLE pandokos.platform_outbox_event (
    seq         bigint      NOT NULL,
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    type        text        NOT NULL,
    subject     text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    tenant_id   uuid,
    data        jsonb       NOT NULL,
    CONSTRAINT platform_outbox_event_pkey PRIMARY KEY (seq),
    CONSTRAINT platform_outbox_event_id_key UNIQUE (id),
    CONSTRAINT platform_outbox_event_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT platform_outbox_event_seq_valid CHECK (seq >= 1),
    CONSTRAINT platform_outbox_event_type_format CHECK (type ~ '^io\.pandokos\.[a-z][a-z_]*(\.[a-z][a-z_]*)+\.v[1-9][0-9]?$'),
    CONSTRAINT platform_outbox_event_subject_length CHECK (char_length(subject) <= 255),
    CONSTRAINT platform_outbox_event_data_object CHECK (jsonb_typeof(data) = 'object')
);
CREATE TRIGGER platform_outbox_event_append_only BEFORE UPDATE OR DELETE ON pandokos.platform_outbox_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER platform_outbox_event_no_truncate BEFORE TRUNCATE ON pandokos.platform_outbox_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.platform_outbox_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_outbox_event FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_outbox_event_owner ON pandokos.platform_outbox_event
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY platform_outbox_event_select ON pandokos.platform_outbox_event
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
GRANT SELECT ON pandokos.platform_outbox_event TO pandokos_admin;

-- Appends an event of the platform. The head of the platform's audit chain is locked first: a
-- transaction that writes both always takes the two locks in this order.
CREATE FUNCTION pandokos.platform_outbox_append(p_type text, p_tenant uuid, p_subject text, p_data jsonb) RETURNS void
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_seq bigint;
BEGIN
    PERFORM 1 FROM pandokos.platform_audit_chain c WHERE c.id = 1 FOR UPDATE;
    UPDATE pandokos.platform_outbox_stream SET last_seq = last_seq + 1 WHERE id = 1 RETURNING last_seq INTO v_seq;
    INSERT INTO pandokos.platform_outbox_event (seq, type, subject, tenant_id, data)
    VALUES (v_seq, p_type, p_subject, p_tenant, pg_catalog.jsonb_strip_nulls(p_data));
END
$$;
REVOKE ALL ON FUNCTION pandokos.platform_outbox_append(text, uuid, text, jsonb) FROM PUBLIC;

-- A deletion requested, cancelled (the scheduled row is removed) and completed (the statement of
-- tenant_erasure_step that writes the completion): each is an event in the same transaction.
CREATE FUNCTION pandokos.tenant_deletion_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.tenant.deletion_requested.v1', NEW.tenant_id, NEW.tenant_id::text,
            pg_catalog.jsonb_build_object('tenant_id', NEW.tenant_id, 'external_reference', NULLIF(NEW.external_reference, ''),
                                          'requested_at', pandokos.rfc3339(NEW.requested_at), 'not_before', pandokos.rfc3339(NEW.not_before)));
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.tenant.deletion_cancelled.v1', OLD.tenant_id, OLD.tenant_id::text,
            pg_catalog.jsonb_build_object('tenant_id', OLD.tenant_id, 'external_reference', NULLIF(OLD.external_reference, ''),
                                          'requested_at', pandokos.rfc3339(OLD.requested_at)));
    ELSIF NEW.state = 'completed' AND OLD.state <> 'completed' THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.tenant.deleted.v1', NEW.tenant_id, NEW.tenant_id::text,
            pg_catalog.jsonb_build_object('tenant_id', NEW.tenant_id, 'external_reference', NULLIF(NEW.external_reference, ''),
                                          'requested_at', pandokos.rfc3339(NEW.requested_at), 'started_at', pandokos.rfc3339(NEW.started_at),
                                          'completed_at', pandokos.rfc3339(NEW.completed_at),
                                          'kek_versions', pg_catalog.to_jsonb(NEW.kek_versions),
                                          'completion_seq', NEW.completion_seq));
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_deletion_outbox() FROM PUBLIC;
CREATE TRIGGER tenant_deletion_outbox AFTER INSERT OR DELETE OR UPDATE OF state ON pandokos.tenant_deletion
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_deletion_outbox();

-- A key-encryption key version that becomes active, and one that is retired (from then on the
-- backups taken under it cannot yield a deleted tenant's credentials once its file is destroyed).
CREATE FUNCTION pandokos.kek_version_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF NEW.state = 'active' AND (TG_OP = 'INSERT' OR OLD.state <> 'active') THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.kek.version_activated.v1', NULL, NEW.id,
            pg_catalog.jsonb_build_object('version', NEW.id, 'provider', NEW.provider));
    ELSIF TG_OP = 'UPDATE' AND NEW.state = 'retired' AND OLD.state <> 'retired' THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.kek.version_retired.v1', NULL, NEW.id,
            pg_catalog.jsonb_build_object('version', NEW.id, 'provider', NEW.provider));
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.kek_version_outbox() FROM PUBLIC;
CREATE TRIGGER kek_version_outbox AFTER INSERT OR UPDATE OF state ON pandokos.kek_version
    FOR EACH ROW EXECUTE FUNCTION pandokos.kek_version_outbox();

-- ---------------------------------------------------------------------------------------------
-- 7. The erasure of a tenant removes its destinations with the access (their secrets are sealed
--    under the data key, and a webhook is a way out for the tenant's data) and its event log
--    with the configuration. Events not delivered when the deletion begins are removed with it.
--    The bodies are those of migration 0016 with the new tables.
-- ---------------------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION pandokos.tenant_residue() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
    tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['limit_rule', 'subject_mapping', 'trusted_issuer', 'issuer_directory', 'platform_issuer_tenant',
                               'virtual_key', 'virtual_key_directory', 'role_assignment', 'idempotency_key', 'support_action',
                               'membership_role', 'membership', 'membership_directory', 'app_user', 'service_account',
                               'tenant_role', 'outbox_dead_letter', 'event_destination', 'event_destination_directory',
                               'tenant_data_key', 'provider_credential', 'platform_limit_rule',
                               'model_tenant_grant', 'tenant_model_restriction', 'metadata_key_policy', 'tenant_price_rate',
                               'tenant_price_version', 'limit_event', 'usage_rollup', 'usage_rollup_state',
                               'outbox_event', 'outbox_stream'] LOOP
        EXECUTE pg_catalog.format('SELECT pg_catalog.count(*) FROM pandokos.%I x WHERE x.tenant_id = $1', tab) INTO n USING t;
        IF n > 0 THEN
            out := out || pg_catalog.jsonb_build_object(tab, n);
        END IF;
    END LOOP;
    RETURN out;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.tenant_erasure_access() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
BEGIN
    DELETE FROM pandokos.limit_rule;                                        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('limit_rule', n);
    DELETE FROM pandokos.subject_mapping;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('subject_mapping', n);
    DELETE FROM pandokos.trusted_issuer;                                    GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('trusted_issuer', n);
    DELETE FROM pandokos.issuer_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.platform_issuer_tenant p WHERE p.tenant_id = t;    GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('platform_issuer_tenant', n);
    DELETE FROM pandokos.virtual_key;                                       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('virtual_key', n);
    DELETE FROM pandokos.virtual_key_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.role_assignment;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('role_assignment', n);
    DELETE FROM pandokos.idempotency_key k WHERE k.tenant_id = t;           GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('idempotency_key', n);
    DELETE FROM pandokos.support_action a WHERE a.tenant_id = t;            GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('support_action', n);
    DELETE FROM pandokos.membership_role;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('membership_role', n);
    DELETE FROM pandokos.membership;                                        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('membership', n);
    DELETE FROM pandokos.membership_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.user_session s USING pandokos.app_user u WHERE u.id = s.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.local_account a USING pandokos.app_user u WHERE u.id = a.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.user_identity i USING pandokos.app_user u WHERE u.id = i.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.app_user u WHERE u.tenant_id = t;                  GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('app_user', n);
    DELETE FROM pandokos.service_account;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('service_account', n);
    DELETE FROM pandokos.tenant_role;                                       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_role', n);
    DELETE FROM pandokos.outbox_dead_letter;
    DELETE FROM pandokos.event_destination;                                 GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('event_destination', n);
    DELETE FROM pandokos.event_destination_directory d WHERE d.tenant_id = t;
    RETURN out;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.tenant_erasure_configuration() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
BEGIN
    DELETE FROM pandokos.platform_limit_rule r WHERE r.tenant_id = t;       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('platform_limit_rule', n);
    DELETE FROM pandokos.model_tenant_grant g WHERE g.tenant_id = t;        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('model_tenant_grant', n);
    DELETE FROM pandokos.tenant_model_restriction;                          GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_model_restriction', n);
    DELETE FROM pandokos.metadata_key_policy;                               GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('metadata_key_policy', n);
    PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
    DELETE FROM pandokos.tenant_price_version;                              GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_price_version', n);
    DELETE FROM pandokos.limit_event;
    DELETE FROM pandokos.outbox_event;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    -- A transaction that began before the deletion and has not ended holds the row of the stream:
    -- the step does not wait for it; the row and the events of that transaction are found by
    -- tenant_residue, and the second pass removes them.
    DELETE FROM pandokos.outbox_stream s
     WHERE s.tenant_id IN (SELECT x.tenant_id FROM pandokos.outbox_stream x WHERE x.tenant_id = t FOR UPDATE SKIP LOCKED);
    DELETE FROM pandokos.usage_rollup;
    DELETE FROM pandokos.usage_rollup_state;
    RETURN out;
END
$$;
```
