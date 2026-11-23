// Package outbox renders, signs and delivers the events of the installation (sprint 6.4, ADR 0070
// to 0072). The database writes events, in the transaction that records the fact; this package
// reads them: the relay delivers them to the webhook destinations of a tenant, the janitor removes
// what retention no longer keeps, and the export renders them. Nothing here writes outbox_event,
// outbox_stream or platform_outbox_event.
//
// The types of this file set and the stubs after it are the scaffolding of part A of the plan: the
// behaviour comes with parts C2 to C5.
package outbox
