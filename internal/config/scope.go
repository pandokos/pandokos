package config

import "strings"

// The scope of a key says whether two instances that share one database may differ in it.
const (
	// ScopeInstance: may differ between two instances of one installation (listeners, log level,
	// cache lifetimes, pool sizes, timeouts, telemetry, shutdown).
	ScopeInstance = "instance"
	// ScopeInstallation: must be equal on every instance that shares a database, because a request
	// would be decided differently by two instances, or stored data read wrongly.
	ScopeInstallation = "installation"
)

// scopes is the one place where the scope of every key is assigned: an exact key name, or a prefix
// that ends in a dot. The longest match wins. A key with no match is an installation setting (the
// conservative answer). Part A assigns by prefix only; sprint 12.3 part C3 reviews each key.
var scopes = map[string]string{
	"log.":       ScopeInstance,
	"listeners.": ScopeInstance,
	"upstream.":  ScopeInstance,
	"shutdown.":  ScopeInstance,
	"cache.":     ScopeInstance,
	"telemetry.": ScopeInstance,

	"database.":        ScopeInstallation,
	"vault.":           ScopeInstallation,
	"limits.":          ScopeInstallation,
	"pricing.":         ScopeInstallation,
	"ledger.":          ScopeInstallation,
	"tokens.":          ScopeInstallation,
	"auth.":            ScopeInstallation,
	"idempotency.":     ScopeInstallation,
	"support.":         ScopeInstallation,
	"tenant_deletion.": ScopeInstallation,
	"reports.":         ScopeInstallation,
	"retention.":       ScopeInstallation,
	"events.":          ScopeInstallation,
}

// scopeOf returns the scope of the key called name.
func scopeOf(name string) string {
	best, scope := -1, ScopeInstallation
	for k, v := range scopes {
		if (k == name || (strings.HasSuffix(k, ".") && strings.HasPrefix(name, k))) && len(k) > best {
			best, scope = len(k), v
		}
	}
	return scope
}
