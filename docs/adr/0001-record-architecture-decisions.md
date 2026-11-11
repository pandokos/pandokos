# 0001. Record architecture decisions

- Status: accepted
- Date: 2026-11-11

## Context

Pandokos has many open design questions (language and framework, row-level security strategy, vault and key management, authentication models, deployment topologies). Contributors need to understand not only what was decided but why.

## Decision

Significant architecture decisions are recorded as short Architecture Decision Records in `docs/adr/`, numbered sequentially and written from the [template](0000-template.md). A decision is changed by adding a new record that supersedes the old one, not by rewriting history.

## Consequences

Decisions stay visible and reviewable through pull requests. A small amount of writing is required for each significant choice.

## Alternatives considered

Keeping decisions only in issues or chat threads: rejected because they are hard to find and are not versioned with the code.
