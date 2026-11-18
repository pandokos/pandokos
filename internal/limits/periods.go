package limits

import (
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

// Piece is the ledger aggregate of [From, To).
type Piece struct {
	From, To time.Time
	Rows     []UsageGroup
}

// Cut returns the cut of a tenant: the instant before which the ledger is the truth of its
// period counters.
func (m *Memory) Cut(_ tenant.ID) (time.Time, bool) { return time.Time{}, false }

// SetCut gives the tenant its cut, only when it has none.
func (m *Memory) SetCut(_ tenant.ID, _ time.Time) {}

// Missing returns the period limits of ls that Reserve could not serve: no counter, and the
// tenant has no cut or the period started before it.
func (m *Memory) Missing(_ []Limit) []Limit { return nil }

// Load creates the counters of ls for one period from rows read with liveFrom = the tenant's
// cut. A counter that exists is left alone.
func (m *Memory) Load(_ tenant.ID, _ Period, _ []Limit, _ []UsageGroup) {}

// Fold adds the pieces to the base of every counter of t whose period contains them, drops the
// live minutes before cut and sets the cut. cut must not be before the current one.
func (m *Memory) Fold(_ tenant.ID, _ []Piece, _ time.Time) {}

// Rebase replaces the base of every counter of t with period p by the sum of rows.
func (m *Memory) Rebase(_ tenant.ID, _ Period, _ []UsageGroup) {}

// PeriodTenants returns the tenants that have a cut.
func (m *Memory) PeriodTenants() []tenant.ID { return nil }

// Periods returns the distinct periods of the period counters of t.
func (m *Memory) Periods(_ tenant.ID) []Period { return nil }

// Counted returns what the counter of l counts (base plus live), for tests and metrics.
func (m *Memory) Counted(_ Limit) (Amount, bool) { return Amount{}, false }
