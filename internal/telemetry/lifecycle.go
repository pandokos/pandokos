package telemetry

// The attribute keys of the lifecycle instruments (M47, M48, M50) and their closed values.
const (
	// AttrDeletionStep is the step of a tenant deletion (M47).
	AttrDeletionStep = "pandokos.deletion.step"
	// AttrDeletionState is the state of an open tenant deletion (M48).
	AttrDeletionState = "pandokos.deletion.state"
	// AttrKEKState is the state of a key-encryption key version (M50).
	AttrKEKState = "pandokos.kek.state"
)

// The values of AttrDeletionStep.
const (
	DeletionStepBegin         = "begin"
	DeletionStepAccess        = "access"
	DeletionStepSecrets       = "secrets"
	DeletionStepConfiguration = "configuration"
	DeletionStepUsage         = "usage"
	DeletionStepAudit         = "audit"
	DeletionStepComplete      = "complete"
)

// The values of AttrDeletionState.
const (
	DeletionScheduled = "scheduled"
	DeletionRunning   = "running"
)

// The values of AttrKEKState.
const (
	KEKActive   = "active"
	KEKRetiring = "retiring"
	KEKRetired  = "retired"
)

// DeletionSteps are the values of AttrDeletionStep, in the order of the metrics reference.
var DeletionSteps = []string{DeletionStepBegin, DeletionStepAccess, DeletionStepSecrets, DeletionStepConfiguration, DeletionStepUsage, DeletionStepAudit, DeletionStepComplete}

// DeletionStates are the values of AttrDeletionState.
var DeletionStates = []string{DeletionScheduled, DeletionRunning}

// KEKStates are the values of AttrKEKState.
var KEKStates = []string{KEKActive, KEKRetiring, KEKRetired}

// LifecycleStats is the deletion of tenants and the rotation of the key-encryption key. Nothing in
// it names a tenant.
type LifecycleStats struct {
	Steps           map[[2]string]int64 // deletion steps by step and result (ok, failed)
	Open            map[string]int64    // open deletions by state
	Rewraps         map[string]int64    // data keys re-wrapped by result (ok, failed)
	KEKVersions     map[string]int64    // key versions by state
	ErasuresPending int64
}
