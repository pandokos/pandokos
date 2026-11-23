// Package lifecycle runs the deletion of tenants (ADR 0067): the worker calls the erasure step
// function of the database, one tenant at a time, and nothing else.
package lifecycle
