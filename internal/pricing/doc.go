// Package pricing is the arithmetic of prices: decimal numbers on math/big, price versions and
// the cost of a request computed from normalized usage. It does no I/O and uses no floating
// point (ADR 0011, ADR 0035, ADR 0036); the catalogue stores versions and the importer of
// package genaiprices reads them from the open dataset.
package pricing
