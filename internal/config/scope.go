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
// that ends in a dot. The longest match wins, so an exact name is an exception to its prefix and
// carries its reason. A key with no match is an installation setting (the conservative answer).
//
// The rule behind every row (sprint 12.3, 5.10): if two instances on one database disagreed on the
// key, would a request be decided differently depending on which instance took it, or would stored
// data be read wrongly? Then the key is installation. What is only the shape of one process
// (listeners, pool sizes, timeouts, cache lifetimes, buffers, telemetry, the network path it
// reaches the database and the providers by) is instance. Where the rule does not decide, the key
// is installation. The password files of the database are instance (a password is rolled instance
// by instance, through a file mounted differently on each), the role names are the installation's.
// The table is sorted by name; TestKeyScope holds the assignment.
var scopes = map[string]string{
	"auth.":                            ScopeInstallation,
	"cache.":                           ScopeInstance,
	"database.":                        ScopeInstallation,
	"database.admin.max_connections":   ScopeInstance, // a pool size: the shape of one process
	"database.admin.password_file":     ScopeInstance, // a path to a secret, rolled instance by instance
	"database.connect_timeout":         ScopeInstance, // a timeout of one process
	"database.gateway.max_connections": ScopeInstance, // a pool size
	"database.gateway.password_file":   ScopeInstance, // as database.admin.password_file
	"database.owner.password_file":     ScopeInstance, // as database.admin.password_file
	"database.tls.ca_file":             ScopeInstance, // how this process reaches the database, not what it reads there
	"database.tls.mode":                ScopeInstance, // as database.tls.ca_file
	"database.worker.max_connections":  ScopeInstance, // a pool size
	"database.worker.password_file":    ScopeInstance, // as database.admin.password_file
	"events.":                          ScopeInstallation,
	"idempotency.":                     ScopeInstallation,
	"ledger.":                          ScopeInstance, // the buffering of one process, as a pool size
	"limits.":                          ScopeInstallation,
	"limits.max_request_duration":      ScopeInstance, // a timeout of one process
	"limits.request_body_read_timeout": ScopeInstance, // a timeout of one process
	"listeners.":                       ScopeInstance,
	"log.":                             ScopeInstance,
	"pricing.":                         ScopeInstallation,
	"reports.":                         ScopeInstallation, // a job of the worker, as tenant_deletion.interval
	"retention.":                       ScopeInstallation,
	"shutdown.":                        ScopeInstance,
	"support.":                         ScopeInstallation,
	"telemetry.":                       ScopeInstance,
	"tenant_deletion.":                 ScopeInstallation,
	"tokens.":                          ScopeInstallation,
	"upstream.":                        ScopeInstance, // the network path of one process to the providers
	"vault.":                           ScopeInstallation,
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
