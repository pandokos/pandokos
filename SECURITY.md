# Security Policy

Pandokos is designed to hold provider credentials and to separate the data of different tenants. Security reports are therefore taken seriously.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting instead:

1. Go to the [Security tab](https://github.com/pandokos/pandokos/security) of this repository.
2. Choose **Report a vulnerability**.
3. Describe the problem, the affected version or commit, and the steps to reproduce it.

You will receive an acknowledgement as described in the [response process](#response-process). Please give us reasonable time to fix the problem before disclosing it publicly.

## Response process

What happens after a report:

- A report is acknowledged within 3 working days.
- Triage (confirmed or not, severity, affected versions) is done within 10 working days.
- The fix is prepared in a private fork attached to a GitHub security advisory, with a test that
  reproduces the problem.
- The fix is released as a new version from `main` and, for the supported minor version, from its
  maintenance branch (see [CONTRIBUTING.md](CONTRIBUTING.md)). Tags are never moved.
- The advisory is published with the release, and a CVE is requested through GitHub when the
  problem affects released versions. The reporter is credited unless they prefer otherwise.
- If a fix needs longer than 90 days, the reporter is told why and when to expect it.

## Scope

Particularly relevant to this project:

- access across tenant boundaries (reading, modifying or deleting another tenant's data);
- exposure of provider credentials or other secrets, including through logs, traces, metrics or error messages;
- authentication or authorization bypass;
- server-side request forgery or forwarding of requests to unauthorized destinations;
- bypass of quotas, rate limits or budgets.

Keys issued by Pandokos have a recognizable format; a leaked key matches the pattern in
[docs/operations/keys.md](docs/operations/keys.md) and should be revoked at once.

## Supported versions

There is no stable release yet. Until the first release, only the `main` branch is considered supported.
