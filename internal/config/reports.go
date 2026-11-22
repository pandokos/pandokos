package config

import "time"

func init() { keyTable = append(keyTable, reportsKeys()...) }

// reportsKeys returns the keys of sprint 6.2, in the order of the fields of Config.
func reportsKeys() []keyDef {
	return []keyDef{
		serveDef("reports.rollup_interval", "duration", "1m", "Time between two passes of the usage rollup that the worker keeps; 10s to 1h",
			setDuration(func(c *Config) *Duration { return &c.Reports.RollupInterval })),
		serveDef("retention.usage_months", "integer", "0", "Months of usage kept: the current UTC month and this many full months before it; 0 keeps everything, otherwise 2 to 120",
			setInt(func(c *Config) *int { return &c.Retention.UsageMonths })),
		serveDef("retention.audit_months", "integer", "0", "Months of audit logs kept: the rows written since the start of the UTC month this many months back; 0 keeps everything, otherwise 1 to 1200; below 12 warns",
			setInt(func(c *Config) *int { return &c.Retention.AuditMonths })),
	}
}

// validateReports applies the rules of the keys of the usage rollup and of retention (sprint 6.2,
// section 5.16); only serve reads them.
func validateReports(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	if d := time.Duration(c.Reports.RollupInterval); d < 10*time.Second || d > time.Hour {
		add("reports.rollup_interval", "must be between 10s and 1h")
	}
	if n := c.Retention.UsageMonths; n != 0 && (n < 2 || n > 120) {
		add("retention.usage_months", "must be 0 (keep everything) or between 2 and 120")
	}
	if n := c.Retention.AuditMonths; n != 0 && (n < 1 || n > 1200) {
		add("retention.audit_months", "must be 0 (keep everything) or between 1 and 1200")
	}
	return ps
}

// retentionWarnings warns that audit rows are kept for less than a year.
func retentionWarnings(c *Config) []Warning {
	if n := c.Retention.AuditMonths; n > 0 && n < 12 {
		return []Warning{{Key: "retention.audit_months", Message: "audit rows older than the value are removed for good: a value below 12 months keeps less than a year of the log"}}
	}
	return nil
}
