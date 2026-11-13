# Contributing to Pandokos

Thank you for your interest. The project is in an early design phase, so the most useful contributions right now are feedback on requirements, architecture and security assumptions.

## Before you start

- For anything beyond a small fix, open an issue first so the approach can be discussed.
- Security problems must **not** be reported in public issues; follow [SECURITY.md](SECURITY.md).

## Architecture decisions

Significant design choices are recorded as short Architecture Decision Records in [`docs/adr/`](docs/adr). To propose a decision, copy [`0000-template.md`](docs/adr/0000-template.md) to the next free number and open a pull request.

## Pull requests

- Keep each pull request focused on a single change.
- Include tests. Any change touching tenant data must include negative tests showing that one tenant cannot read, modify or delete another tenant's data.
- Never include secrets, credentials, prompts or completions in code, tests, logs or fixtures.
- Tests that need PostgreSQL fail when it is missing. `make db-up` starts a throwaway PostgreSQL and PgBouncer in Docker; `make test` uses them; `make test-nodb` runs the rest.
- `internal/adminapi/adminapi.gen.go` and `internal/store/queries/*.go` are generated (`make generate`: the API generator and `sqlc`). Never edit a generated file; change `api/openapi/admin.v1.yaml` or a query file under `internal/store/queries/sql/` and run `make generate`. CI fails when the committed code is not current.
- Describe what changed and why.

## Branches and releases

- **Development happens on `main`.** Every change, including each sprint of the plan, is a short-lived branch created from `main` (`feat/...`, `fix/...`, `docs/...`) and merged back with a pull request, normally squashed. Nobody pushes to `main` directly.
- **A release is a tag, not a branch.** When `main` meets the acceptance criteria of a release, the commit that was verified gets a signed, annotated tag `vMAJOR.MINOR.PATCH` (for example `v0.1.0`), following [Semantic Versioning](https://semver.org/). Release candidates are tagged `v0.1.0-rc.1` on `main` and the final tag goes on the same commit that passed. Tags are never moved or deleted; a bad release is replaced by a new version. Before 1.0, minor versions may break compatibility, and only the latest minor version is supported.
- **Maintenance branches.** When a minor version is released, `release/0.1` (and so on) is created from its `v0.1.0` tag. A fix for a released version is merged into `main` first, then cherry-picked onto the maintenance branch through a pull request, and tagged `v0.1.1`.
- **Protection.** `main`, `release/*` and `v*` tags cannot be pushed to, force-pushed or deleted; changes enter only through pull requests.

## Sign-off

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE). Please sign off your commits (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).
