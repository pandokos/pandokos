package admin

import (
	"net/http"
	"sync/atomic"

	"github.com/pandokos/pandokos/internal/account"
)

// Handler is the administration API: an http.Handler that also reports what it counts.
type Handler struct {
	http.Handler
	s *server
}

// adminCounters are what the handler counts for the metrics. Nothing of a tenant, a user or a subject.
type adminCounters struct {
	grantsRefused atomic.Int64
	supportOpened atomic.Int64
	supportClosed atomic.Int64 // by the platform
	supportEnded  atomic.Int64 // by the tenant
	oidc          account.SignInCounter
}

// Stats is a snapshot of what the handler counts.
type Stats struct {
	GrantsRefused int64
	SupportOpened int64
	SupportEnded  map[string]int64 // by cause: closed (by the platform), ended (by the tenant)
	SignIn        account.SignInStats
}

// Stats returns a copy of the adminCounters.
func (h *Handler) Stats() Stats {
	c := &h.s.stats
	return Stats{
		GrantsRefused: c.grantsRefused.Load(),
		SupportOpened: c.supportOpened.Load(),
		SupportEnded:  map[string]int64{"closed": c.supportClosed.Load(), "ended": c.supportEnded.Load()},
		SignIn:        c.oidc.Snapshot(),
	}
}
