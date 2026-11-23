package telemetry

import "time"

// The attribute keys of the report and retention instruments (M44, M46) and their closed values.
const (
	// AttrRetentionKind is the kind of what retention removed (M44).
	AttrRetentionKind = "pandokos.retention.kind"
	// AttrExportKind is the kind of an export (M46).
	AttrExportKind = "pandokos.export.kind"
)

// The values of AttrRetentionKind.
const (
	RetentionUsagePartition     = "usage_partition"
	RetentionUsageRecord        = "usage_record"
	RetentionUsageRollup        = "usage_rollup"
	RetentionAuditEvent         = "audit_event"
	RetentionPlatformAuditEvent = "platform_audit_event"
	// The records kept for deleted tenants (sprint 6.3).
	RetentionDeletedTenantUsageRecord = "deleted_tenant_usage_record"
	RetentionDeletedTenantAuditEvent  = "deleted_tenant_audit_event"
)

// The values of AttrExportKind.
const (
	ExportUsage         = "usage"
	ExportAudit         = "audit"
	ExportPlatformAudit = "platform_audit"
	// ExportDeletedTenantAudit is the log kept for a deleted tenant (sprint 6.3).
	ExportDeletedTenantAudit = "deleted_tenant_audit"
)

// RetentionKinds are the values of AttrRetentionKind, in the order of the metrics reference.
var RetentionKinds = []string{RetentionUsagePartition, RetentionUsageRecord, RetentionUsageRollup, RetentionAuditEvent, RetentionPlatformAuditEvent}

// ExportKinds are the values of AttrExportKind, in the order of the metrics reference.
var ExportKinds = []string{ExportUsage, ExportAudit, ExportPlatformAudit}

// ReportsStats is the usage rollup, retention and the exports. Nothing in it names a tenant.
type ReportsStats struct {
	Rollups          map[string]int64 // tenant passes by result: ok, failed
	RollupLag        time.Duration    // the largest now - watermark of the last pass; 0 before the first
	RetentionRemoved map[string]int64 // by kind, one of RetentionKinds
	RetentionRuns    map[string]int64 // by result: ok, failed
	Exports          map[ExportKey]int64
}

// ExportKey is the key of ReportsStats.Exports: the kind (one of ExportKinds) and the result
// (complete, aborted).
type ExportKey struct{ Kind, Result string }
