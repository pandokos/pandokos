// Package reports holds the jobs of the worker that keep usage reports cheap and bounded: the
// usage rollup, kept per tenant in the tenant's session, and retention of usage and audit rows
// through the two audited functions of migration 0015 (ADR 0064, ADR 0065). Parts C3 and C6 of
// sprint 6.2 build them; until then the jobs do nothing.
package reports
