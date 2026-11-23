package outbox

import (
	"encoding/json"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

// Event is an event as the log holds it.
type Event struct {
	Seq     int64 // sequence number in the log of the tenant or of the platform; 0: none (a test event)
	ID      string
	Type    string
	Subject string
	Time    time.Time
	Data    json.RawMessage
}

// Envelope is what the rendering adds to an event: the source of the installation and, for an
// event of a tenant, the tenant and its external reference as they are when the document is
// rendered.
type Envelope struct {
	Source    string
	TenantID  tenant.ID // the zero value: no tenant attributes
	TenantRef string
}

// Render returns the CloudEvents 1.0 document of e (sprint 6.4, section 5.3): structured JSON, the
// members in a fixed order. A zero Seq leaves out the sequence attribute.
func Render(e Event, env Envelope) []byte {
	_, _ = e, env
	return nil
}

// Types returns the types of event that a tenant's destination can receive, sorted.
func Types() []string { return nil }

// Schema returns the JSON Schema (2020-12) of an event type, envelope included.
func Schema(typ string) ([]byte, bool) {
	_ = typ
	return nil, false
}
