// Package tenantexport writes the export of a tenant's own data through the administration API
// (ADR 0069): one NDJSON file per kind of record, a manifest, and the JSON Schema of both.
package tenantexport

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// Schema is the JSON Schema (draft 2020-12) of the manifest and of every kind; the file is
// generated from the administration API's specification by make generate.
//
//go:embed tenant-export.v1.schema.json
var Schema []byte

// Kind is one kind of record of the export: its name, the file it is written to and the entry of
// the schema's $defs that a line of the file validates against.
type Kind struct{ Name, File, Def string }

// Kinds are the kinds of the export, in the order of the files (sprint 6.3, section 5.11).
var Kinds = []Kind{
	{"settings", "settings.json", "settings"},
	{"service_account", "service_accounts.ndjson", "service_account"},
	{"virtual_key", "virtual_keys.ndjson", "virtual_key"},
	{"credential", "credentials.ndjson", "credential"},
	{"token_issuer", "token_issuers.ndjson", "token_issuer"},
	{"subject_mapping", "subject_mappings.ndjson", "subject_mapping"},
	{"platform_issuer", "platform_issuers.ndjson", "platform_issuer"},
	{"limit", "limits.ndjson", "limit"},
	{"platform_limit", "platform_limits.ndjson", "platform_limit"},
	{"limit_event", "limit_events.ndjson", "limit_event"},
	{"price_list_version", "price_list_versions.ndjson", "price_list_version"},
	{"member", "members.ndjson", "member"},
	{"role", "roles.ndjson", "role"},
	{"support_action", "support_actions.ndjson", "support_action"},
	{"usage_record", "usage_records.ndjson", "usage_record"},
	{"audit_event", "audit_events.ndjson", "audit_event"},
}

// Options are the choices of an export.
type Options struct {
	// UsageFrom is the start of the usage window; zero means 60 months before Now.
	UsageFrom time.Time
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// PandokosVersion is recorded in the manifest.
	PandokosVersion string
}

// ManifestFile is one entry of Manifest.Files.
type ManifestFile struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Records int64  `json:"records"`
	SHA256  string `json:"sha256"`
}

// ManifestAuditHead is the head of the audit chain at the time of the export.
type ManifestAuditHead struct {
	Seq       int64  `json:"seq"`
	Hash      string `json:"hash"`
	AnchorSeq int64  `json:"anchor_seq"`
}

// Manifest is manifest.json, written last.
type Manifest struct {
	Format          string            `json:"format"`
	Version         int               `json:"version"`
	TenantID        string            `json:"tenant_id"`
	CreatedAt       time.Time         `json:"created_at"`
	PandokosVersion string            `json:"pandokos_version"`
	Files           []ManifestFile    `json:"files"`
	AuditHead       ManifestAuditHead `json:"audit_head"`
	UsageFrom       time.Time         `json:"usage_from"`
	UsageTo         time.Time         `json:"usage_to"`
}

// Write reads the tenant's data through c and writes the export to dir (through dir.partial,
// renamed at the end).
func Write(_ context.Context, _ *adminapi.Client, _, _ string, _ Options) (Manifest, error) {
	return Manifest{}, errors.New("tenantexport: not implemented")
}
