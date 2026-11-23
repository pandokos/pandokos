# 0054. Usage data is the tenant's: what the platform does not see

- Status: accepted
- Date: 2026-11-18

Refines [ADR 0013](0013-ledger-and-audit.md), [ADR 0015](0015-observability.md) and
[ADR 0007](0007-authorization-rbac.md). Requirements USE-06, USE-07, AZ-06.

## Context

The usage ledger, the limit events and now the price lists describe what a tenant consumes and
what it pays. The first requirements gave platform roles aggregates per tenant and a usage
viewer, and the request log and the traces carried cost and token counts of every request. The
maintainer's review of 2026-11-18 decided that this data is the tenant's.

## Decision

1. Through the product, only the tenant reads its usage records, limit events and price lists.
   No platform permission, route, policy or `SECURITY DEFINER` function returns them, and no
   platform role is a usage viewer. Background jobs read usage only in the session of the tenant
   they work for.
2. The platform sees operational indicators of the installation: requests, errors, latency,
   saturation, without a tenant dimension on anything that is a cost or a token count, and
   without costs per tenant. It still sets ceilings, and knows the ceilings it set, not what was
   counted against them.
3. The request log and the traces carry no cost, currency, token count, reserved amount or
   budget figure of a request, and the lines of the ledger writer and of the limit event writer
   carry no row, metric, scope, subject or percent: only identifiers, the outcome, durations, sizes,
   the event kind and the rule ID. `telemetry.tenant_usage_details`, off by default, brings them
   back, with a warning at start: for single-tenant installations and for debugging.
4. This is held by tests: negative tests on platform sessions in the database, on every platform
   operation of the API, on the result types of definer functions, and a repository test over
   the handlers.
5. Confidentiality against whoever administers the database, the hosts or the backups is out of
   scope (issue #33) and stated in the security model.

## Consequences

- Reports, rollups and exports (sprint 6.2) are built for the tenant; metrics (sprint 6.1) have
  no tenant label on cost and tokens.
- An operator troubleshoots a refusal with operational fields; the figures are in the tenant's
  own tools (capability check, usage list, rules).
- The tenant ID stays in logs and traces: that a tenant made a request is operational.

## Alternatives considered

**Platform aggregates per tenant, as first required.** The platform would read what tenants
spend; under BYOK it has no need to.

**Removing the fields from logs with no way back.** A single-tenant installation loses a useful
line for nothing.

**Encrypting usage per tenant.** Protects against the operator of the database, which is another
problem (issue #33) with other costs.

## History

- 2026-11-18: proposed with sprint 5.4, after the maintainer's review of the same day.
- 2026-11-20: point 3 and the consequence "the tenant ID stays in logs and traces" are superseded: logs and traces hold no tenant data at all, and `telemetry.tenant_usage_details` is removed ([ADR 0055](0055-operational-telemetry-holds-no-tenant-data.md)).
- 2026-11-22: sprint 4.3: no support action carries a permission that reads usage, limit events or prices; the strong form of this decision holds under a support action ([ADR 0061](0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md)).
- 2026-11-23: accepted by the maintainer after review.
- 2026-11-22: sprint 6.1: cost and token metrics are per installation, model and currency, with no tenant dimension ([ADR 0062](0062-metrics-instruments-attributes-cardinality-and-scrape.md)).
- 2026-11-23: sprint 6.2: reports, limit usage and exports are tenant operations; the retention functions return counts ([ADR 0064](0064-usage-rollup-reports-and-exports.md))
- 2026-11-23: sprint 6.3: the strong form holds in a tenant deletion too: the erasure functions return no usage figure, the completion record holds none, and the kept ledger has no reader ([ADR 0067](0067-tenant-deletion-stages-closed-tenants-and-kept-records.md)); the tenant exports its own data ([ADR 0069](0069-tenant-data-export.md)).
