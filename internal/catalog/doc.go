// Package catalog is the catalogue of providers, endpoints and models: who may use which model,
// the gateway's cache of that decision, and the import of prices into the database (ADR 0011,
// ADR 0035, ADR 0036). It is used by the administration and the inference planes and does not
// know either of them.
package catalog
