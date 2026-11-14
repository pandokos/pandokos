// Package egress is the only place where Pandokos makes outbound HTTP calls to providers and
// other upstream hosts. It owns the transports, the address classification and the refusal of
// addresses an operator never meant to reach (ADR 0038, GW-05, GW-06, SEC-01).
package egress
