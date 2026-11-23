# 0063. The log: redaction by key and by shape, and the log reference

- Status: proposed
- Date: 2026-11-22

Refines [ADR 0015](0015-observability.md) and [ADR 0055](0055-operational-telemetry-holds-no-tenant-data.md).
Requirements OBS-01, OBS-04.

## Context

ADR 0015 asks for a handler that redacts known sensitive keys; ADR 0055 decides what a line may
say about a tenant. Keys catch a secret under a known name, not a secret inside an error text, and
nothing listed what lines and fields exist, so a new field could carry tenant data unnoticed.

## Decision

1. Every logger of the product is built on one handler that redacts the values of sensitive keys
   and, in the message and every value, the shapes of Pandokos secrets: virtual and platform keys,
   session tokens, bearer values, compact JWS. Each redaction by shape is counted in a metric.
2. `docs/reference/logs.md` lists every message with its level and fields. A test over the code
   fails on a message or a field that is not in it, and on tenant-identifying keys outside the
   lines it marks as platform identities; every end-to-end process has its output checked against
   it, with the fixtures' tenant identifiers as canaries.
3. The process-wide error line of the OpenTelemetry SDK is `telemetry error`; export failures
   are `trace export failing` and `metric export failing`.

## Consequences

Adding a log line means adding a row to the reference. A secret that reaches a logger by mistake is
redacted and shows up in the redaction counter. The shapes are checked against the generators, so a
change of a key format fails a test.

## Alternatives considered

**Key-based redaction only.** Misses secrets inside error texts. **A field allow-list in the
handler.** Couples the handler to every component; the reference with tests gives the same guarantee
without a run-time cost. **Removing lines instead of documenting them.** The lines are needed to run
the installation.

## History

- 2026-11-22: proposed with sprint 6.1.
- 2026-11-23: sprint 6.3: the eight lines are added or changed (`tenant deletion ready`, `started`, `completed`, `failing`, `recovered`, `key rewrap failing`, `key rotation complete`, and `vault ready` with two fields), none with a tenant or a count of one.
