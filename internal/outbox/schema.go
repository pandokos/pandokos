package outbox

import (
	"slices"

	"github.com/pandokos/pandokos/internal/outbox/schema"
)

// tenantTypes are the types of a tenant's log, the ones a destination can ask for.
var tenantTypes = []string{
	"io.pandokos.limit.exhausted.v1",
	"io.pandokos.limit.threshold_reached.v1",
	"io.pandokos.usage.recorded.v1",
}

// Types returns the types of event that a tenant's destination can receive, sorted.
func Types() []string { return slices.Clone(tenantTypes) }

// SchemaTypes returns every type that has an embedded schema, the platform's types included, sorted.
func SchemaTypes() []string { return schema.Types() }

// Schema returns the JSON Schema (2020-12) of an event type, envelope included. The files are
// named by the type without its io.pandokos. prefix.
func Schema(typ string) ([]byte, bool) { return schema.Get(typ) }
