---
id: EPIC-11
title: Console
status: todo
release: v0.2
depends_on: [EPIC-4, EPIC-5, EPIC-6]
---

# Epic 11 — Console

## Outcome

People administer the platform and their tenants from a web console that ships inside Pandokos.
They sign in with a local account or through an identity provider, and each of them finds only
the functions their roles allow. This epic closes v0.2.

## Decisions it implements

ADR 0021, and the browser side of 0006 and 0022.

## Sprints

### 11.1 Shell and sign-in

The parts every page depends on, and the ones with security weight.

- Front-end project in `web/console/`; build in CI; static output embedded in the binary and
  served under `/console/`; switch to disable.
- API client generated from the OpenAPI document; a check that fails when it is stale.
- Sign-in page: username and password, and a button per configured identity provider; forced
  password change; sign-out; own password change.
- Application shell: `/v1/me`, navigation built from permissions, the permission guard used by
  routes, pages and actions; tenant switcher; platform and tenant areas.
- Security headers and content security policy; no third-party resources; nothing in browser
  storage.
- Front-end dependency lock and audit in CI.
- End-to-end tests in a browser: sign-in both ways, lock after failures, session expiry.

Requirements: CON-01, CON-04, CON-05, CON-07, CON-09, API-08.

Exit: a user signs in and sees an empty shell with exactly the areas of their roles; the response
headers pass a content-security-policy check; signing out makes the cookie useless.

### 11.2 Tenant area

- Members and role assignments; custom roles.
- Service accounts and virtual keys, with the show-once dialog for a new key.
- Provider credentials: write-only form, validation result, fingerprint and metadata, rotation.
- Model access for the tenant and per key.
- Limits and budgets; usage and cost by period, provider, model and caller; audit log with
  filters.
- Generated table of console functions and required permissions; a test that checks it against
  the API.
- End-to-end test per built-in tenant role: what is visible, and that a hidden action called
  directly is refused by the API.

Requirements: CON-02, CON-04, CON-06.

Exit: a tenant admin completes the whole onboarding of a tenant from the console (credential,
service account, key, limit) and a call made with that key shows up in the usage page; a usage
viewer sees usage and nothing else.

### 11.3 Platform area

- Tenants: create, suspend, reactivate, delete with confirmation and reason.
- Providers, endpoints, models, grants to tenants, price versions and overrides.
- Users, local accounts (create, reset, disable) and platform roles.
- Platform limits; platform audit log; credential metadata of tenants without secrets.
- Support action into a tenant, with the mandatory reason.
- First-run experience: from `pandokos init` to a working tenant, documented with screenshots.

Requirements: CON-03, CON-05, and the console side of UC-15 and UC-16.

Exit: on a fresh installation without an identity provider, a platform admin signs in with the
account created by `init`, changes the password, creates a tenant and its administrator, and the
tenant administrator carries on alone; a platform auditor can read the platform audit and cannot
change anything.

## Risks

- Front-end work is where the maintainer has least experience. The stack is conventional, the
  component set is ready-made, and pages follow one list-and-form pattern; visual design beyond
  that is out of scope for this epic.
- The console can lag behind the API. From v0.3 the definition of done ties each feature to its
  page.
- Three sprints may be tight for the number of pages. If so, the usage and audit pages of 11.2 can
  ship as plain tables first.
