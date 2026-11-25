//go:build drill

package drill

import "fmt"

// The key cases and the recovery: part C6.

// notImplemented ends a test that needs a part of the drill that sprint 12.3 builds in a later part
// of part C: the scaffolding compiles and those tests fail here, loudly, never skip.
func (w *World) notImplemented(what, part string, _ ...any) {
	w.T.Helper()
	fail(w.T, fmt.Sprintf("drill: %s is not implemented yet (sprint 12.3 part %s)", what, part))
}

// RecoverLostKey runs step 6.3 or 6.4: the recovery script for the version, a new key, and starts
// serve on it; it returns the script's output.
func (r *Restored) RecoverLostKey(version string) string {
	r.W.notImplemented("Restored.RecoverLostKey", "C6", version)
	return ""
}

// ServiceSnapshot takes the snapshot of the key service (GET sys/storage/raft/snapshot) and returns
// the file; the unseal share and a token are kept with it.
func (w *World) ServiceSnapshot(label string) string {
	w.notImplemented("ServiceSnapshot", "C6", label)
	return ""
}

// ServiceRotate rotates the key in the service (a new version).
func (w *World) ServiceRotate() { w.notImplemented("ServiceRotate", "C6") }

// ServiceTrim raises min_decryption_version and trims, with the root token.
func (w *World) ServiceTrim(minVersion int) { w.notImplemented("ServiceTrim", "C6", minVersion) }

// ServiceTrimAsPandokos tries the same with the token of Pandokos's policy and returns the status:
// the policy cannot.
func (w *World) ServiceTrimAsPandokos(minVersion int) int {
	w.notImplemented("ServiceTrimAsPandokos", "C6", minVersion)
	return 0
}

// ServiceDeleteKey deletes the key in the service.
func (w *World) ServiceDeleteKey() { w.notImplemented("ServiceDeleteKey", "C6") }

// ServiceLose removes the service and its volumes, with no snapshot taken.
func (w *World) ServiceLose() { w.notImplemented("ServiceLose", "C6") }

// CredentialState is the state of a tenant's provider credential ("active", "revoked").
func (r *Restored) CredentialState(tenant string) string {
	r.W.notImplemented("Restored.CredentialState", "C6", tenant)
	return ""
}

// EnterCredential makes the tenant enter a provider credential again (a new canary, accepted by the
// provider) and returns its value.
func (r *Restored) EnterCredential(tenant string) string {
	r.W.notImplemented("Restored.EnterCredential", "C6", tenant)
	return ""
}

// RotateDestinationSecret runs event-destination rotate-secret for the tenant's destination and
// returns the new secret.
func (r *Restored) RotateDestinationSecret(tenant string) string {
	r.W.notImplemented("Restored.RotateDestinationSecret", "C6", tenant)
	return ""
}

// KekCheck is "pandokos kek check": its exit code and output.
func (r *Restored) KekCheck() (int, string) {
	r.W.notImplemented("Restored.KekCheck", "C6")
	return 0, ""
}

// fail ends the test. It is a variable so that the stubs above do not look, to a static analysis, as
// if they never returned: the code of the tests that follows them is the code that part C runs.
var fail = func(t interface {
	Helper()
	Fatal(args ...any)
}, msg string) {
	t.Helper()
	t.Fatal(msg)
}
