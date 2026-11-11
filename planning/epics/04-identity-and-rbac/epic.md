---
id: EPIC-4
title: Identity federation and RBAC
status: todo
release: v0.1 (sprint 4.1), v0.2
depends_on: [EPIC-1]
---

# Epic 4 — Identity federation and RBAC

## Outcome

Workloads call with tokens from their own identity provider, people administer through an external
OIDC provider, and the full role model is in place.

## Decisions it implements

ADR 0005 (trusted issuers), 0006, 0007.

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

### 4.2 Users and OIDC

- Local user records keyed by issuer and subject; memberships in tenants.
- The admin API accepts OIDC tokens for people; tenant in the path checked against memberships.
- Local disable with the documented bound.
- Optional mapping of identity-provider groups to roles.
- CLI sign-in with the authorization code flow and PKCE.
- Idempotency keys on create operations.
- Compose profile with an example identity provider.

Requirements: IAM-09 to IAM-12, API-05, PRV-01.

Exit: a person signs in from the CLI through the identity provider and administers two tenants
with different roles; disabling the user locally stops access within the bound.

### 4.3 Roles

- All built-in tenant roles; platform auditor and platform billing operator.
- Custom roles from the permission catalogue; no-escalation rule; deactivation; deletion guard.
- Platform support action inside a tenant: explicit permission, mandatory reason, tenant audit
  record, configuration switch to disable.
- Reasons required on high-impact operations.
- A generated permission reference in the documentation.

Requirements: AZ-03 to AZ-06, AZ-09, AZ-10, SEC-08.

Exit: for each built-in role a test matrix shows which routes are allowed and denied; a tenant
admin cannot grant a permission it lacks; a platform admin cannot read a tenant's usage or audit
without a recorded support action.

## Risks

- Identity providers differ in claim shapes for tenants and groups. Mapping is configuration, and
  the tests use more than one provider to keep it honest.
