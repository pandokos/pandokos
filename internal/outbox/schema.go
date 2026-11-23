package outbox

import (
	"embed"
	"slices"
	"strings"
)

//go:embed schema/*.schema.json
var schemas embed.FS

// tenantTypes are the types of a tenant's log, the ones a destination can ask for.
var tenantTypes = []string{
	"io.pandokos.limit.exhausted.v1",
	"io.pandokos.limit.threshold_reached.v1",
	"io.pandokos.usage.recorded.v1",
}

// Types returns the types of event that a tenant's destination can receive, sorted.
func Types() []string { return slices.Clone(tenantTypes) }

// Schema returns the JSON Schema (2020-12) of an event type, envelope included. The files are
// named by the type without its io.pandokos. prefix.
func Schema(typ string) ([]byte, bool) {
	name, ok := strings.CutPrefix(typ, "io.pandokos.")
	if !ok || name == "" || strings.ContainsAny(name, "/\\") {
		return nil, false
	}
	b, err := schemas.ReadFile("schema/" + name + ".schema.json")
	if err != nil {
		return nil, false
	}
	return b, true
}
