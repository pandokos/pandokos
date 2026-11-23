// Package schema holds the JSON Schemas of the event types (2020-12, envelope included), one file
// per type named by the type without its io.pandokos. prefix. It is a leaf: the command line
// prints the schemas from it without reaching the database.
package schema

import (
	"embed"
	"slices"
	"strings"
)

//go:embed *.schema.json
var files embed.FS

// Types returns every type that has a schema, the platform's types included, sorted.
func Types() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".schema.json"); ok {
			out = append(out, "io.pandokos."+name)
		}
	}
	slices.Sort(out)
	return out
}

// Get returns the schema of an event type.
func Get(typ string) ([]byte, bool) {
	name, ok := strings.CutPrefix(typ, "io.pandokos.")
	if !ok || name == "" || strings.ContainsAny(name, "/\\") {
		return nil, false
	}
	b, err := files.ReadFile(name + ".schema.json")
	if err != nil {
		return nil, false
	}
	return b, true
}
