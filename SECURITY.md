# Security Policy

Pandokos is designed to hold provider credentials and to separate the data of different tenants. Security reports are therefore taken seriously.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting instead:

1. Go to the [Security tab](https://github.com/pandokos/pandokos/security) of this repository.
2. Choose **Report a vulnerability**.
3. Describe the problem, the affected version or commit, and the steps to reproduce it.

You will receive an acknowledgement as soon as possible. Please give us reasonable time to fix the problem before disclosing it publicly.

## Scope

Particularly relevant to this project:

- access across tenant boundaries (reading, modifying or deleting another tenant's data);
- exposure of provider credentials or other secrets, including through logs, traces, metrics or error messages;
- authentication or authorization bypass;
- server-side request forgery or forwarding of requests to unauthorized destinations;
- bypass of quotas, rate limits or budgets.

## Supported versions

There is no stable release yet. Until the first release, only the `main` branch is considered supported.
