# 0066. Declarative export and apply

- Status: accepted
- Date: 2026-11-23

Refines [ADR 0019](0019-configuration.md). Requirement CAT-10.

## Context

ADR 0019 promises `pandokos export` and `pandokos apply`, idempotent, through the API, leaving
resources absent from the document alone and reporting them.

## Decision

1. A YAML document, `pandokos: v1`, of kind `platform` (providers with endpoints, models, model grants
   by tenant name, price overrides, platform ceilings) or `tenant` (time zone, active currency,
   metadata keys, model restriction, custom roles, service accounts with roles, limit rules, price
   lists). Resources are named by natural keys; no secret, key, credential, person or usage figure.
2. `export` writes it deterministically; `apply` plans by natural key, creates and updates through the
   API, reports conflicts on immutable fields and what it leaves alone, never deletes, and only adds
   price versions, never in the past: a version whose `valid_from` is before now is reported `skipped: in the past`,
   never created. A document cannot clear what the API can clear: an empty `metadata_keys` or
   `time_zone` leaves the setting alone. A rule on a subject mapping is left out of the export and
   reported. The plan of a document just exported is empty.
3. Platform-level and tenant token issuers and subject mappings join in sprint 12.3.

## Consequences

- Configuration can be reviewed and repeated without the database being a second source of truth.
- Removing a resource stays an explicit API call.

## Alternatives considered

**Apply in the server, as one transaction.** A second write path beside the API, with its own
authorization.

**Deleting what the document omits.** One wrong document would remove a tenant's configuration.

## History

- 2026-11-23: proposed with sprint 6.2.
- 2026-11-23: accepted by the maintainer after review.
- 2026-11-23: sprint 6.4 keeps event destinations and the external reference out of `export` and `apply`; they join in sprint 12.3 ([ADR 0072](0072-event-types-the-envelope-the-external-reference-and-platform-events.md)).
