// Package resolver adapts the catalogue, the vault and the database to the ports of the
// gateway (gateway.Catalogue, gateway.Credentials, gateway.FailureRecorder). It is wiring: the
// gateway does not import it, and only the server and the command compose it.
package resolver
