// Package store is the database access of Pandokos: one pool per process role, transactions
// that carry the tenant, and the start-up and readiness checks. The notification listener holds
// the only session state (LISTEN), on a connection of its own outside the pools.
package store
