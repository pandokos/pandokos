# 0028. Administration API: in-repository generator and conventions

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0017](0017-admin-api-spec-first.md).

## Context

ADR 0017 wants server interfaces, validation and a client generated from the OpenAPI document; ADR 0018 wants the standard library first. Verified on 2026-11-12: `oapi-codegen` v2.8.2 reads OpenAPI 3.1, but what it generates needs the module `github.com/oapi-codegen/runtime` at v1.8.0 or later; v1.8.0 names gin, echo and iris in its `go.mod`, and v1.9.0, which does not, requires Go 1.26, above the floor of ADR 0025. It generates no request validation and cannot carry the permission of a route. `ogen` brings a larger runtime.

## Decision

The repository owns a generator, `internal/apigen`, for a strict subset of OpenAPI 3.1; its output uses only the standard library and is committed; CI regenerates it and fails on any difference. Every operation carries `x-pandokos-permission`, which is mandatory. The conventions of the API (IDs in paths, names as filters, strict bodies and queries, verb sub-resources that are idempotent, `null` only where a bound is removed, one fixed sentence per problem code) are the contract integrators build on. The document stays valid OpenAPI 3.1 for other generators.

## Consequences

The project owns about 800 lines of generator; a construct outside the subset is generator work, done in the sprint that needs it. If the Go floor moves to 1.26 the comparison with `oapi-codegen` is repeated.

## Alternatives considered

`oapi-codegen` (a runtime module, no validation, no permission); `ogen`; handlers written by hand and checked against the document by a test.
