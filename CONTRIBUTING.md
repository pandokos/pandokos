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
- `make secrets` scans the repository and its whole history with gitleaks (default rules plus the Pandokos key format, `.gitleaks.toml`); CI runs it on every pull request. To run it before every push, add `make secrets` to a local `pre-push` hook.
- Tests that need PostgreSQL fail when it is missing. `make db-up` starts a throwaway PostgreSQL and PgBouncer in Docker; `make test` uses them; `make test-nodb` runs the rest.
- `make test-idp` runs the trusted issuer tests against Keycloak, Ory Hydra and Authentik in Docker (`make db-up` and `make idp-up` first, `make idp-down` after). It needs several more containers (Authentik takes a few minutes to start), so `make check` does not run it; CI has its own job, `idp`.
- `internal/adminapi/adminapi.gen.go` and `internal/store/queries/*.go` are generated (`make generate`: the API generator and `sqlc`). Never edit a generated file; change `api/openapi/admin.v1.yaml` or a query file under `internal/store/queries/sql/` and run `make generate`. CI fails when the committed code is not current.
- Describe what changed and why.

## Branches and releases

- **Development happens on `main`.** Every change, including each sprint of the plan, is a short-lived branch created from `main` (`feat/...`, `fix/...`, `docs/...`) and merged back with a pull request, normally squashed. Nobody pushes to `main` directly.
- **A release is a tag, not a branch.** When `main` meets the acceptance criteria of a release, the commit that was verified gets a signed, annotated tag `vMAJOR.MINOR.PATCH` (for example `v0.1.0`), following [Semantic Versioning](https://semver.org/). Release candidates are tagged `v0.1.0-rc.1` on `main` and the final tag goes on the same commit that passed. Tags are never moved or deleted; a bad release is replaced by a new version. Before 1.0, minor versions may break compatibility, and only the latest minor version is supported.
- **Maintenance branches.** When a minor version is released, `release/0.1` (and so on) is created from its `v0.1.0` tag. A fix for a released version is merged into `main` first, then cherry-picked onto the maintenance branch through a pull request, and tagged `v0.1.1`.
- **Protection.** `main`, `release/*` and `v*` tags cannot be pushed to, force-pushed or deleted; changes enter only through pull requests.

### Releasing

The documents are aligned with a release **before** its tag exists, because a tag is never moved and
the tagged commit is the one the release workflow builds and the readers clone. The same steps serve a
minor release (from `main`) and a patch release (from the maintenance branch, after the fix is on
`main`):

1. **The fix or the content is on the branch to tag**: `main` for a new minor version, `release/X.Y`
   for a patch, reached through a pull request (a fix is merged into `main` first and cherry-picked,
   `git cherry-pick -x -s`, without anything else from `main`).
2. **Write the release page `docs/releases/vX.Y.Z.md`**: what is in, what was verified and where, the
   known limits, the upgrade notes and the maintainer actions. It is the text of the release.
3. **Align the documents in the same pull request**: the README (the status paragraph and the quick
   start, which clones the tag `vX.Y.Z` and names the image), `docs/operations/compose.md`,
   `SECURITY.md` (supported versions) and, for a new minor version, the Compose default image
   (`ghcr.io/pandokos/pandokos:X.Y`, the moving tag of the minor version, so that a patch release
   reaches an installation without a change of the files). `TestLatestReleaseIsPinned`
   (`test/repo/release_test.go`) compares them with the newest page of `docs/releases` and fails
   when one still names another version. Open the same change against `main`, so that it does not
   lose the page.
4. **Merge, then tag immediately.** Check that the CI and the code scanning of the branch are green,
   then, on the commit that holds the page: `git tag -s -a vX.Y.Z -m "vX.Y.Z"` and
   `git push origin vX.Y.Z`. Between the merge and the push the quick start names a tag that does not
   exist yet, so the two are done together.
5. **The release workflow does the rest** (`release.yml`): it checks the tag and runs the CI, then
   builds and signs the image, attaches the SBOM and the provenance, verifies them, and creates the
   GitHub release with the SBOM and the administration API description. Verify the image with the
   commands of [docs/operations/container.md](docs/operations/container.md#verifying-a-release) and
   paste or link the release page as the release text.
6. **A release that fails is not retried with the same tag**: fix forward with the next patch
   (`v0.1.2`), or the next candidate (`v0.2.0-rc.2`).

## Sign-off

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE). Please sign off your commits (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).
