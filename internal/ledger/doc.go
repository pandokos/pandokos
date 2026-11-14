// Package ledger writes the usage ledger: a bounded buffer with admission, a writing loop that
// inserts batches per tenant, and the keeper of the monthly partitions. Only the command
// composes it (the lint rule ledger-is-wiring).
package ledger
