package telemetry

import "sync/atomic"

// redactions counts the values the log handler rewrote because they had the shape of a secret
// (M41). Any non-zero value means a secret reached a logger.
var redactions atomic.Int64

// Redactions returns how many values the log handler has rewritten by pattern since the process
// started.
func Redactions() int64 { return redactions.Load() }
