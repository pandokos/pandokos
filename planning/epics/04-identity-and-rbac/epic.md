---
id: EPIC-4
title: Identity federation and RBAC
status: todo
release: v0.1 (sprint 4.1), v0.2
depends_on: [EPIC-1]
---

# Epic 4 — Identity federation and RBAC

## Outcome

Workloads call with tokens from their own identity provider; people sign in with a local account
or through an external OIDC provider and obtain a session; the full role model is in place and
the API can tell a user what they are allowed to do. The console that uses all this is epic 11.

## Decisions it implements

ADR 0005 (trusted issuers), 0006, 0007, 0022.

## Sprints

### 4.1 Trusted token issuers — part of v0.1

- Issuer registration: tenant-bound, or platform-level with a tenant claim and an explicit tenant
  list.
- OIDC discovery and JWKS retrieval with caching, bounded refetch on unknown key IDs.
- Token validation: signature, algorithm allow-list, issuer, audience, expiry, required claims.
- Subject mapping to service accounts; unmapped subjects refused.
- The same principal as for virtual keys; no change downstream.
- Tests with two different open-source identity providers.

Requirements: IAM-06, IAM-07, IAM-08.

Exit: a token from a platform-level issuer with tenant A in its claim acts for tenant A and cannot
act for a tenant outside the registration's list; a token with the wrong audience is refused; the
gateway keeps working through a short outage of the identity provider.

### 4.2 Users, local accounts and sessions

The security-sensitive part first: passwords and sessions.

- User records; memberships in tenants; platform users.
- Local accounts: creation by administrators, Argon2id hashing with stored parameters, minimum
  length, generated initial password with forced change, own password change.
- Reset from the administration API and from the host (`pandokos user reset-password`);
  `pandokos init` creates the first platform admin account.
- Throttling and temporary lock; uniform answers for unknown users; audit of failures.
- Sessions: token and hashed storage, cookie attributes, idle and absolute lifetimes, sign-out,
  revocation, end on password change and on disabling; cross-site request forgery checks.
- Sessions accepted on the administration listener only; no grant for the gateway role on account
  and session tables, verified by the isolation harness.
- Sign-in through an external OIDC provider as relying party: code flow with PKCE, state and
  nonce, ID token validation, same session; optional mapping of provider groups to roles.
- Switch to disable local accounts; refusal of sign-in over plain HTTP outside loopback.
- Idempotency keys on create operations.

Requirements: IAM-09 to IAM-12, IAM-13 (accounts), IAM-14 (account), IAM-18 to IAM-22, API-05,
PRV-01.

Exit: with no identity provider, a platform admin signs in over the API with a local account and
creates another user, who must change the generated password; ten wrong passwords lock the
account and appear in the audit log; a session cookie presented to the inference port is ignored;
with an identity provider configured, the same user flows work through it; disabling a user ends
their sessions within the bound.

### 4.3 Roles

- All built-in tenant roles; platform auditor and platform billing operator.
- Custom roles from the permission catalogue; no-escalation rule; deactivation; deletion guard.
- `GET /v1/me`: identity, platform permissions, memberships and effective permissions per tenant.
- Platform support action inside a tenant: explicit permission, mandatory reason, tenant audit
  record, configuration switch to disable.
- Reasons required on high-impact operations.
- A generated permission reference in the documentation.

Requirements: AZ-03 to AZ-06, AZ-09, AZ-10, AZ-11, SEC-08.

Exit: for each built-in role a test matrix shows which routes are allowed and denied, and
`/v1/me` reports exactly the permissions the matrix expects; a tenant admin cannot grant a
permission it lacks; a platform admin cannot read a tenant's usage or audit without a recorded
support action.

## Risks

- Password and session code is where small mistakes become vulnerabilities. The sprint follows
  OWASP ASVS for these chapters as a checklist, and its review is done by a second pair of eyes.
- Identity providers differ in claim shapes for groups. Mapping is configuration, and the tests
  use more than one provider to keep it honest.
