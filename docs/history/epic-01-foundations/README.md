# Epic 1 — Foundations: record

Completed 2026-11-13. Epic file as closed: [epic.md](epic.md).

## Outcome

A signed, non-root container image with a PostgreSQL database whose tenant isolation is proven by
tests (forced row-level security, three unprivileged runtime roles, checked directly and through a
transaction-mode pooler). A platform admin bootstraps with `pandokos init`, creates tenants through
the administration API or the CLI; a tenant admin creates service accounts and virtual keys; every
change is audited in the same transaction. Inference calls on the OpenAI route authenticate with
virtual keys. The provider key is still the operator's single key until epic 2 (vault).

## Sprints

| Sprint | Design and plan | Delivered | Review |
|---|---|---|---|
| 1.1 Walking skeleton | [sprint](1.1-walking-skeleton/sprint.md), [plan](1.1-walking-skeleton/plan.md) | PR #4 | 2 rounds (4 findings, then none) |
| 1.2 Persistence and tenant isolation | [sprint](1.2-persistence-and-tenant-isolation/sprint.md), [plan](1.2-persistence-and-tenant-isolation/plan.md) | PR #5 | 2 rounds (6 findings, then none) |
| 1.3 Service accounts, virtual keys, authorization core, audit | [sprint](1.3-service-accounts-keys-authz-audit/sprint.md), [plan](1.3-service-accounts-keys-authz-audit/plan.md) | PR #6 | 2 rounds (5 findings, then none) |

Each sprint followed the same cycle: a frozen design, tests committed failing before the
implementation, a review against the design repeated until it reported no findings, a pull request
with green CI. Open points raised by each design and the maintainer's decisions on them are at the
end of section 12 of each `sprint.md`.

## Decisions recorded

ADR 0023–0025 (sprint 1.1: nested test modules, OpenAI stream usage option, Go toolchain),
ADR 0026–0027 (sprint 1.2: schema migrations, database roles and session context),
ADR 0028–0032 (sprint 1.3: admin API generator, static SQL with sqlc, key format/storage/lookup,
authorization core and provisioning, first audit log). See the [ADR index](../../adr/README.md).

## Carried forward

- Sprint 3.1: `HTTPS_PROXY` for provider traffic with the egress guard; ADR 0020 amendment for a
  `bad_request`-like code; OPS-12 and GW-16 completion; auditing of inference authentication
  failures.
- Sprint 4.3: recovery of a tenant that lost every `tenant-admin` key (support actions, AZ-09).
- Sprint 8.2: listener certificate reload; `serve --role`.
- Not scheduled: `--key-stdin` and proxy support for the CLI; prefixed database role names.
- Maintainer actions outside the repository: verify a tagged release (signature, SBOM,
  provenance).
- Secret scanning for `pdk_` keys is covered by gitleaks in CI (`make secrets`), added after the
  epic closed because GitHub custom patterns require a paid plan.
