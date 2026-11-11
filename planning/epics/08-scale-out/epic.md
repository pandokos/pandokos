---
id: EPIC-8
title: Scale-out and deployment
status: todo
release: v0.4
depends_on: [EPIC-7]
---

# Epic 8 — Scale-out and deployment

## Outcome

Pandokos runs as several replicas with consistent limits, installs on Kubernetes, keeps its
key-encryption key in an external service, and can dedicate a cell to a tenant.

## Decisions it implements

ADR 0012 (Valkey store), 0003 (separate roles), 0008 (external provider), 0016, 0005 (mutual TLS).

## Sprints

### 8.1 Shared counters

- Valkey counter store with atomic reserve and settle scripts.
- Compose reference with replicas behind a load balancer.
- Multi-instance tests: limits, revocation and cache invalidation across replicas.
- Failure tests for the counter store, in both configured behaviours.

Requirements: LIM-10, OPS-03, OPS-14.

Exit: three replicas under load enforce one tenant's limits as one instance would; a revocation
reaches all replicas within the bound.

### 8.2 Kubernetes and split roles

- Gateway, admin and worker as separate processes with their own database credentials.
- Helm chart: configuration, secrets, probes, rolling updates, migration job, network policies.
- External key-encryption key provider over a transit API.
- Upgrade test: rolling from the previous release with traffic.

Requirements: OPS-04, OPS-05 (separate), VLT-12 (external).

Exit: the chart installs a working system on a test cluster; a rolling upgrade drops no stream
that ends within the drain deadline; with the transit provider, no key-encryption key exists on
the Pandokos hosts.

### 8.3 Cells and mutual TLS

- Pinned cell: refuses any tenant but its own at start-up and on every request.
- Bridge guide: routing tenants to cells at the edge.
- Procedure and tooling to move a tenant between cells.
- Client-certificate authentication mapped to service accounts, including SPIFFE IDs.

Requirements: OPS-06, OPS-07, IAM-15.

Exit: a silo cell rejects a valid credential of another tenant; a tenant moved from pool to silo
keeps its identifiers, configuration and usage history.

## Risks

- Moving credentials between cells with different keys needs an offline step. If it proves
  error-prone, the documented path is re-entry by the tenant.
