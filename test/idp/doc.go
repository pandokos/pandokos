// Package idp holds the tests of trusted token issuers against real identity providers: Keycloak
// and Ory Hydra in containers (sprint 4.1, section 7). They run only with the build tag idp, after
// "make db-up" and "make idp-up", as "make test-idp". A test behind the tag fails, never skips,
// when a provider or the database is missing.
package idp
