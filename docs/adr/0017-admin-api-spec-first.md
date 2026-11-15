# 0017. Administration API: specification first, with a CLI

- Status: accepted
- Date: 2026-11-11

## Context

The administration API is the product's main public contract besides the proxy itself:
integrators automate tenant provisioning with it (API-03, API-06). It must be documented, versioned
and never drift from its documentation (API-02).

## Decision

- The API is described in an **OpenAPI 3.1 document kept in the repository**, written before the
  handlers. Server interfaces and request validation are generated from it; so is the Go client
  used by the CLI. A CI check fails when generated code and specification differ.
- Style: resource-oriented REST under `/v1`. Platform resources at the top (`/v1/tenants`,
  `/v1/providers`, `/v1/models`); tenant resources under the tenant
  (`/v1/tenants/{tenant}/service-accounts`, `…/credentials`, `…/limits`, `…/usage`, `…/audit`).
  The tenant in the path selects the tenant to act on and is always checked against the
  principal: for a tenant-scoped key it must be the key's own tenant.
- Collections use cursor pagination. Errors follow RFC 9457. Create operations accept an
  `Idempotency-Key`.
- Breaking changes require a new major path. Additive changes do not.
- The **CLI** is part of the same binary (`pandokos tenant create`, `pandokos key create`, …) and
  is a thin client of the API. Nothing is possible in the CLI that is not possible over HTTP,
  except the two local commands `migrate` and `init`.
- **Interfaces for people** are clients of this API like any other, and Pandokos ships none. So
  that one can be built, the API includes sign-in and session endpoints and `GET /v1/me`, which
  returns the caller's identity, memberships and effective permissions.

## Consequences

- Integrators can generate clients in any language.
- The specification is reviewed as a design artefact before code is written.
- Pandokos addresses operators and developers. People who are neither are served by whatever
  interface an integrator builds on the API.

## Alternatives considered

**Code first, with the specification generated from handlers.** Faster at the start; the contract
then follows the implementation instead of the other way round.

**gRPC or GraphQL.** Less universal for the automation and scripting this API is for.

**A graphical interface in the product.** A second toolchain and a second surface for every
feature, for integrators who already have their own interface.
