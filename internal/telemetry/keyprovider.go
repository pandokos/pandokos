package telemetry

import "time"

// The values of the attributes of the key provider instruments (M59 and M60), in the order of the
// metrics reference.
var (
	// KeyOperations are the values of pandokos.kek.operation.
	KeyOperations = []string{"wrap", "unwrap", "describe", "identify", "lookup", "renew", "login"}
	// KeyOutcomes are the values of pandokos.kek.outcome.
	KeyOutcomes = []string{"ok", "unavailable", "denied", "not_found", "rejected", "refused"}
)

// KeyProviderStats is the transit key provider (M59, M61 and M62). Nothing in it names an
// address, a namespace, a key or a version. The time of a request (M60) is recorded as it happens,
// with ObserveKeyProviderRequest.
type KeyProviderStats struct {
	Requests     map[[2]string]int64 // requests by operation and outcome (M59)
	Up           bool                // requests are sent (M61): false while the refusal is in force
	TokenExpires bool                // false for a token without a lifetime: M62 is not reported
	TokenTTL     time.Duration       // what is left of the token's lifetime (M62)
}

// ObserveKeyProviderRequest records the time of one request that was sent to the key service (M60).
//
//nolint:revive // scaffolding: implemented in step C8
func (m *Metrics) ObserveKeyProviderRequest(operation string, d time.Duration) {}
