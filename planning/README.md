# Planning

`planning/` is the work; `docs/` is the product. Documents under `docs/` describe what Pandokos
is and must be. Files here describe what is being built, in which order, and where it stands.

## How work is organized

- A **release** is a version with a theme and acceptance criteria
  ([requirements §4 and §7](../docs/requirements/README.md)).
- An **epic** is a coherent body of work with one outcome. Each has a file in
  [`epics/`](epics/) with its scope, the requirements it delivers, its sprints and its risks.
- A **sprint** is a self-contained increment inside an epic: it ends with something that runs, is
  tested and is documented. Sprints are sized by outcome, not by calendar.

Within an epic the riskiest sprint comes first, so that a wrong assumption surfaces early.

## Sprint lifecycle

1. **Design.** Before a sprint starts, its design is written and frozen in
   `epics/<epic>/<sprint>/sprint.md`, with a file-by-file implementation plan next to it. The
   commit that adds them is the baseline for review.
2. **Implementation** follows the frozen plan. A gap in the plan is raised as a question, not
   filled silently.
3. **Review** checks the result against the frozen design, the requirement IDs the sprint claims
   and the definition of done.
4. **Close.** The sprint's status is updated, and the epic is adjusted if the sprint changed what
   comes next.

Only the next sprint is designed in detail. The sprint descriptions in the epic files are the
plan of record until then.

## Definition of done

A sprint is done when:

- the requirement IDs it claims are met and each is covered by an automated test;
- every new tenant-scoped table has a policy and isolation tests, and every new route declares a
  permission and has a cross-tenant negative test;
- no secret or content appears in any output (the canary tests pass);
- lint, vulnerability check and the full test suite pass in CI;
- from v0.3 on, a feature with an administrative surface has its console page, gated by the same
  permission as its API operation;
- user-facing behaviour is documented, the OpenAPI specification is current, and any decision
  taken along the way has its ADR.

## Status

Each epic file starts with a `status:` field: `todo`, `in-progress`, `done`. The
[roadmap](roadmap.md) is the overview.
