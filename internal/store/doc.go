// Package store is the database access of Pandokos: one pool per process role, transactions
// that carry the tenant, and the start-up and readiness checks.
package store

import _ "github.com/jackc/pgx/v5/pgxpool" // replaced by real use in step C5
