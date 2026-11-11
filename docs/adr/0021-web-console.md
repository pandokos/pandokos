# 0021. Web console: embedded single-page application, profiled by permissions

- Status: proposed
- Date: 2026-11-11

## Context

Tenant administrators and platform administrators are often not the people who write scripts
against an API. Products of this kind are judged by their console, and several well-known
self-hosted systems ship one inside the server: RabbitMQ's management interface, Grafana, MinIO,
Gitea, Argo CD. The requirements ask for a console with a platform area and a tenant area, whose
functions follow roles and permissions (CON-01 to CON-09), without breaking the single-container
installation (OPS-01) or the supply-chain rules ([ADR 0018](0018-supply-chain.md)).

## Decision

**Form.** The console is a single-page application. Its build output, static files, is embedded
in the Pandokos binary and served by the `admin` role on the administration listener under
`/console/`. Nothing else has to be deployed. A configuration switch turns it off.

**A client like any other.** The console calls only the documented administration API
([ADR 0017](0017-admin-api-spec-first.md)). Its API client is generated from the OpenAPI
document, so the console cannot drift from the contract. It holds no key and has no endpoint of
its own.

**Two areas.**

- *Platform area*, for platform roles: tenants and their lifecycle, providers, models and
  prices, users and platform roles, platform limits, platform audit; later, platform credentials,
  funding and event destinations.
- *Tenant area*, for tenant roles: members and roles, service accounts and virtual keys,
  provider credentials, model access, limits, usage and cost, audit.

A user sees the areas their roles give them. A platform role alone opens no tenant's data; the
audited support action of [ADR 0007](0007-authorization-rbac.md) is the only way in.

**Profiling by permissions.**

- `GET /v1/me` returns the user, their platform permissions, and for each tenant they belong to
  the effective permissions there.
- In the console, every route, page and action declares the permission it requires: the same
  `resource:action` string the API operation requires. One guard component decides visibility
  from the `/v1/me` answer. Role names are never tested in the console, so custom roles work
  without changes.
- The table of console functions and required permissions is generated into the documentation,
  and a test checks it against the permissions declared by the API operations each function calls.
- The API remains the control. Whatever the console shows or hides, each call is authorized on
  the server.

**Secrets.** A new virtual key is displayed once. Provider credentials are write-only fields;
the console shows their fingerprint and metadata.

**Browser security.** A strict content security policy with no inline script, no resources from
other origins, fonts and icons shipped with the application, no credential in browser storage.
Authentication is a session cookie the scripts cannot read
([ADR 0022](0022-local-accounts-and-sessions.md)).

**Technology.** React with TypeScript, built with Vite. Data fetching through one query library
over the generated client. One accessible component set whose source lives in the repository
(Radix primitives styled with Tailwind), rather than a large runtime component framework.
Interface text is kept in message files. Tests: component tests, and end-to-end tests in a
browser for sign-in and for the profiling of each built-in role.

## Consequences

- The installation stays one container; console and API always have the same version.
- The repository gains a second toolchain. Its packages are build-time only, locked and audited,
  but they are many more than the Go dependencies, and they need their own review habit.
- Every administrative feature now has two surfaces to build. From v0.3 a feature is not done
  without its console page.
- The administration listener serves HTML to browsers, so it needs the browser-facing protections
  listed above, and sign-in requires TLS except on the loopback address.
- The maintainer's strength is the back end. A conventional stack and a ready-made component set
  are chosen so that the console can be built and reviewed without specialist front-end design
  work.

## Alternatives considered

**Server-rendered pages with small scripts (Go templates, htmx).** One language, almost no
front-end dependencies, a good fit with the supply-chain stance. Rejected because the pages would
call internal functions rather than the public API, which removes the guarantee that everything
the console does is possible over the API, and because data-heavy screens (usage, audit) are
harder to make pleasant.

**A separate console image.** Independent releases, at the cost of a second deployable and a
version matrix.

**Profiling by role name in the console.** Simple, and it breaks with custom roles and whenever a
built-in role changes.

**No console; a third-party admin tool over the API.** No such tool knows this API.
