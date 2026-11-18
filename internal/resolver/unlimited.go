package resolver

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

// UnlimitedNoter is told about the first unlimited request of a tenant in a day of the tenant
// (sprint 5.3, section 5.6). Note never blocks.
type UnlimitedNoter interface {
	Note(t tenant.ID, day, zone, provider, model, requestID string)
}

// UnlimitedAuditor records, once per tenant and day, that a request was admitted with no limit rule
// applying to it, through pandokos.record_unlimited_traffic on the gateway pool. The shape is
// placed by sprint 5.3 part B; the body by step C3. Until then it records nothing, so that the
// tests of the sprint compile and fail for the right reason.
type UnlimitedAuditor struct{}

var _ UnlimitedNoter = (*UnlimitedAuditor)(nil)

// NewUnlimitedAuditor returns an auditor that writes on db and takes its time from now (time.Now
// when nil).
func NewUnlimitedAuditor(_ *store.DB, _ *slog.Logger, _ func() time.Time) *UnlimitedAuditor {
	return &UnlimitedAuditor{}
}

// Note offers the day of a tenant to the auditor: day is the civil date of the request in the
// tenant's zone ("2006-01-02"), zone the name of that zone.
func (a *UnlimitedAuditor) Note(_ tenant.ID, _, _, _, _, _ string) {}

// Run writes what Note queued until ctx ends.
func (a *UnlimitedAuditor) Run(_ context.Context) {}

// Close writes what is queued, waiting until ctx ends.
func (a *UnlimitedAuditor) Close(_ context.Context) {}
