# 0003. One binary with three roles; separate planes

- Status: accepted
- Date: 2026-11-11

## Context

The requirements ask for a control plane and a data plane that are not confused (principle 9), and
for an installation that is a single container (OPS-01). Splitting the product into separately
built services satisfies the first and breaks the second.

## Decision

Pandokos is one program with three runtime roles: `gateway` (data plane), `admin` (control plane)
and `worker` (background jobs). `pandokos serve` runs them together; `--role` runs any subset.

The separation is enforced where it matters, whatever the process layout:

- **Network.** The inference API and the administration API always listen on different ports and
  can bind to different interfaces. They never share a router.
- **Privilege.** Each role connects to the database with its own role and grants. In the
  single-process layout the process holds three connection pools.
- **Code.** The gateway package does not import the administration handlers and the reverse.
  A test checks the import graph.

Schema migration (`migrate`) and first-time initialization (`init`) are separate commands that
exit, and are the only ones that use owner credentials.

## Consequences

- The smallest installation is one container plus PostgreSQL, and it is the same code that scales
  out.
- A vulnerability in the administration API does not by itself give the ability to append usage
  or call the key-lookup function, and a vulnerability in the gateway does not give write access
  to the catalogue, provided the roles run apart. In the single-process layout this protection is
  reduced to what the database grants enforce per connection pool; the documentation says so.
- One version number covers everything: no compatibility matrix between components.

## Alternatives considered

**Separate services with separate repositories or images.** Cleaner blast radius by default, but
it multiplies the operational surface of the simplest installation and creates version skew.

**A single role doing everything on one port.** Simplest, but the administration API would be
exposed wherever inference is, and least privilege inside the database would be impossible.
