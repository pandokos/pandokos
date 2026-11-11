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
- Describe what changed and why.

## Sign-off

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE). Please sign off your commits (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).
