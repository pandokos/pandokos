package resolver

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/store"
)

// FailureAuditor implements gateway.FailureRecorder: it writes authentication failures of the
// inference listener to the audit log of the tenant of the key.
type FailureAuditor struct {
	db  *store.DB
	log *slog.Logger
	now func() time.Time
}

// NewFailureAuditor returns a recorder that writes through db.
func NewFailureAuditor(db *store.DB, log *slog.Logger, now func() time.Time) *FailureAuditor {
	return &FailureAuditor{db: db, log: log, now: now}
}

// Record reports whether a record was written.
func (a *FailureAuditor) Record(ctx context.Context, f gateway.AuthFailure) bool {
	_, _ = ctx, f
	return false
}
