package config

import "time"

func init() { keyTable = append(keyTable, lifecycleKeys()...) }

// lifecycleKeys returns the keys of the deletion of tenants (sprint 6.3, section 5.15).
func lifecycleKeys() []keyDef {
	return []keyDef{
		serveDef("tenant_deletion.grace_period", "duration", "168h", "Time between the request to delete a tenant and the start of its removal, read when the request is made; 0 to 2160h, 0 starts at the worker's next pass",
			setDuration(func(c *Config) *Duration { return &c.TenantDeletion.GracePeriod })),
		serveDef("tenant_deletion.interval", "duration", "1m", "Time between two passes of the tenant deletion job that the worker keeps; 10s to 1h",
			setDuration(func(c *Config) *Duration { return &c.TenantDeletion.Interval })),
	}
}

// maxUnwrapKeyFiles is the most files vault.kek.local.unwrap_key_files accepts.
const maxUnwrapKeyFiles = 8

// validateLifecycle applies the rules of the keys of the deletion of tenants and of the unwrap
// key files; only serve reads them. The content of the files is read by loadKEK.
func validateLifecycle(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	if d := time.Duration(c.TenantDeletion.GracePeriod); d < 0 || d > 2160*time.Hour {
		add("tenant_deletion.grace_period", "must be between 0 and 2160h")
	}
	if d := time.Duration(c.TenantDeletion.Interval); d < 10*time.Second || d > time.Hour {
		add("tenant_deletion.interval", "must be between 10s and 1h")
	}
	files := c.Vault.KEK.Local.UnwrapKeyFiles
	if len(files) > maxUnwrapKeyFiles {
		add("vault.kek.local.unwrap_key_files", "must list at most 8 files")
	}
	seen := map[string]bool{}
	if f := c.Vault.KEK.Local.KeyFile; f != "" {
		seen[f] = true
	}
	for _, f := range files {
		if seen[f] {
			add("vault.kek.local.unwrap_key_files", "must not repeat a path or list the path of vault.kek.local.key_file")
			break
		}
		seen[f] = true
	}
	return ps
}
