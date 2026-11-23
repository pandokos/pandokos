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
	// exports counts the exports by kind (usage, audit, platform_audit) and by result (complete, aborted).
	exports [3][2]atomic.Int64
}

// exportKinds are the kinds of export in the order of adminCounters.exports.
var exportKinds = [3]string{exportUsage, exportAudit, exportPlatformAudit}

// exportDone counts an export that ended: complete or aborted.
func (c *adminCounters) exportDone(kind string, complete bool) {
	for i, k := range exportKinds {
		if k == kind {
			r := 1
			if complete {
				r = 0
			}
			c.exports[i][r].Add(1)
		}
	}
}

// Stats is a snapshot of what the handler counts.
type Stats struct {
	GrantsRefused int64
	SupportOpened int64
	SupportEnded  map[string]int64 // by cause: closed (by the platform), ended (by the tenant)
	SignIn        account.SignInStats
	// ExportsComplete and ExportsAborted count the exports by kind: usage, audit, platform_audit.
	ExportsComplete, ExportsAborted map[string]int64
}

// Stats returns a copy of the adminCounters.
func (h *Handler) Stats() Stats {
	c := &h.s.stats
	out := Stats{ExportsComplete: map[string]int64{}, ExportsAborted: map[string]int64{},
		GrantsRefused: c.grantsRefused.Load(),
		SupportOpened: c.supportOpened.Load(),
		SupportEnded:  map[string]int64{"closed": c.supportClosed.Load(), "ended": c.supportEnded.Load()},
		SignIn:        c.oidc.Snapshot(),
	}
	for i, k := range exportKinds {
		out.ExportsComplete[k], out.ExportsAborted[k] = c.exports[i][0].Load(), c.exports[i][1].Load()
	}
	return out
}
