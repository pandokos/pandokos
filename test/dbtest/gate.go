// Package dbtest gives tests a PostgreSQL database of their own, with the four login users of the
// product, and decides whether a test that needs PostgreSQL runs, fails or is skipped.
package dbtest

// Names of the environment variables that configure the test database (sprint 1.2, section 7.1).
const (
	EnvDatabaseURL = "PANDOKOS_TEST_DATABASE_URL"
	EnvPoolerAddr  = "PANDOKOS_TEST_POOLER_ADDR"
	EnvNoDatabase  = "PANDOKOS_TEST_NO_DATABASE"
	EnvNoPooler    = "PANDOKOS_TEST_NO_POOLER"
)

type action int

const (
	run action = iota
	skip
	fail
)

// gate decides what a test that needs a resource does. value is the resource's variable,
// optOut the value of its opt-out variable, ci the value of CI.
func gate(name, optOutName, value, optOut, ci string) (action, string) {
	inCI := ci != "" && ci != "false"
	switch {
	case optOut != "" && inCI:
		return fail, optOutName + " is set, but database tests cannot be skipped in CI"
	case value != "":
		return run, ""
	case optOut != "":
		return skip, name + " is not set and " + optOutName + " is set: skipped"
	default:
		return fail, name + ` is not set: this test needs PostgreSQL. Run "make db-up" and "make test", ` +
			"or set " + optOutName + "=1 to skip these tests locally (refused in CI)"
	}
}
