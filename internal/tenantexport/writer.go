package tenantexport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

const (
	// ManifestFormat is the value of the manifest's format member.
	ManifestFormat = "pandokos-tenant-export"
	// ManifestVersion is the version of the format; it changes when a member is removed or changes
	// meaning, not when a member is added.
	ManifestVersion = 1
	// UsageWindow is the longest window of one request for the usage ledger.
	UsageWindow = 31 * 24 * time.Hour
	// DefaultUsageMonths is how far back the ledger is read when no start is given.
	DefaultUsageMonths = 60
)

// listOperation is the operation that lists each kind that is a plain page of items.
var listOperation = map[string]string{
	"service_account": "listServiceAccounts", "virtual_key": "listVirtualKeys", "credential": "listCredentials",
	"token_issuer": "listTokenIssuers", "subject_mapping": "listSubjectMappings", "platform_issuer": "listTenantPlatformIssuers",
	"limit": "listLimits", "platform_limit": "listTenantPlatformLimits", "limit_event": "listLimitEvents",
	"price_list_version": "listPriceListVersions", "member": "listMembers", "role": "listRoles", "support_action": "listSupportActions",
}

// settingsOperations are the four reads of settings.json, in the order of its members.
var settingsOperations = []struct{ Member, Operation string }{
	{"time_zone", "getTenantTimeZone"}, {"currency", "getActiveCurrency"},
	{"metadata_keys", "getMetadataKeys"}, {"model_restriction", "getModelRestriction"},
}

// readError says what an operation that failed was reading; a refusal names the permission.
func readError(opID string, err error) error {
	var pe *adminapi.ProblemError
	if errors.As(err, &pe) && pe.Problem.Status == http.StatusForbidden {
		for _, op := range adminapi.Operations {
			if op.ID == opID {
				return fmt.Errorf("tenantexport: %s was refused: the key or session needs the permission %s", opID, op.Permission)
			}
		}
	}
	return fmt.Errorf("tenantexport: %s: %w", opID, err)
}

func pathOf(opID, tenant string) (string, error) {
	for _, op := range adminapi.Operations {
		if op.ID == opID {
			return strings.ReplaceAll(op.Path, "{tenant}", url.PathEscape(tenant)), nil
		}
	}
	return "", fmt.Errorf("tenantexport: unknown operation %s", opID)
}

// file is a file being written: its hash and its lines are counted as it goes.
type file struct {
	f       *os.File
	hash    hash.Hash
	records int64
}

func (w *file) write(p []byte) error {
	if _, err := w.hash.Write(p); err != nil {
		return err
	}
	_, err := w.f.Write(p)
	return err
}

// line writes one compact JSON value and a newline.
func (w *file) line(raw []byte) error {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return fmt.Errorf("the answer is not JSON: %w", err)
	}
	b.WriteByte('\n')
	if err := w.write(b.Bytes()); err != nil {
		return err
	}
	w.records++
	return nil
}

type page struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

// Write reads the tenant's data through c and writes the export to dir (through dir.partial,
// renamed at the end). dir and dir.partial must not exist. A failure leaves dir.partial without a
// manifest.
func Write(ctx context.Context, c *adminapi.Client, tenant, dir string, opts Options) (Manifest, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	created := now().UTC()
	usageFrom := opts.UsageFrom.UTC()
	if opts.UsageFrom.IsZero() {
		usageFrom = created.AddDate(0, -DefaultUsageMonths, 0)
	}
	dir = filepath.Clean(dir)
	partial := dir + ".partial"
	for _, p := range []string{dir, partial} {
		if _, err := os.Lstat(p); err == nil {
			return Manifest{}, fmt.Errorf("tenantexport: %s exists: the export does not write into it, remove it or choose another directory", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Manifest{}, fmt.Errorf("tenantexport: %w", err)
		}
	}
	if err := os.Mkdir(partial, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("tenantexport: %w", err)
	}

	m := Manifest{Format: ManifestFormat, Version: ManifestVersion, TenantID: tenant, CreatedAt: created, PandokosVersion: opts.PandokosVersion,
		UsageFrom: usageFrom, UsageTo: created}
	for _, k := range Kinds {
		f, err := os.OpenFile(filepath.Join(partial, k.File), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the name is a constant of Kinds
		if err != nil {
			return Manifest{}, fmt.Errorf("tenantexport: %w", err)
		}
		w := &file{f: f, hash: sha256.New()}
		err = writeKind(ctx, c, tenant, k, w, &m, usageFrom, created)
		if cerr := w.f.Sync(); err == nil {
			err = cerr
		}
		if cerr := w.f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return Manifest{}, err
		}
		m.Files = append(m.Files, ManifestFile{Name: k.File, Kind: k.Name, Records: w.records, SHA256: hex.EncodeToString(w.hash.Sum(nil))})
	}

	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(partial, "manifest.json"), append(raw, '\n'), 0o600); err != nil {
		return Manifest{}, fmt.Errorf("tenantexport: %w", err)
	}
	if err := os.Rename(partial, dir); err != nil {
		return Manifest{}, fmt.Errorf("tenantexport: %w", err)
	}
	return m, nil
}

func writeKind(ctx context.Context, c *adminapi.Client, tenant string, k Kind, w *file, m *Manifest, usageFrom, usageTo time.Time) error {
	switch k.Name {
	case "settings":
		return writeSettings(ctx, c, tenant, w)
	case "usage_record":
		return writeUsage(ctx, c, tenant, w, usageFrom, usageTo)
	case "audit_event":
		return writeAudit(ctx, c, tenant, w, m)
	}
	opID := listOperation[k.Name]
	if opID == "" {
		return fmt.Errorf("tenantexport: no operation reads the kind %s", k.Name)
	}
	path, err := pathOf(opID, tenant)
	if err != nil {
		return err
	}
	cursor := ""
	for {
		q := url.Values{}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		raw, err := c.GetRaw(ctx, path, q)
		if err != nil {
			return readError(opID, err)
		}
		var p page
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("tenantexport: %s: %w", opID, err)
		}
		for _, item := range p.Items {
			if err := w.line(item); err != nil {
				return fmt.Errorf("tenantexport: %s: %w", opID, err)
			}
		}
		if p.NextCursor == "" || p.NextCursor == cursor {
			return nil
		}
		cursor = p.NextCursor
	}
}

// writeSettings writes the one object of the four settings reads.
func writeSettings(ctx context.Context, c *adminapi.Client, tenant string, w *file) error {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, s := range settingsOperations {
		path, err := pathOf(s.Operation, tenant)
		if err != nil {
			return err
		}
		raw, err := c.GetRaw(ctx, path, nil)
		if err != nil {
			return readError(s.Operation, err)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:", s.Member)
		if err := json.Compact(&b, raw); err != nil {
			return fmt.Errorf("tenantexport: %s: %w", s.Operation, err)
		}
	}
	b.WriteByte('}')
	return w.line(b.Bytes())
}

// copyLines copies the lines of a stream to the file; a body that ends early is an error.
func copyLines(body *adminapi.StreamBody, w *file, opID string) error {
	defer body.Body.Close()
	r := bufio.NewReaderSize(body.Body, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if err == io.EOF {
				return fmt.Errorf("tenantexport: %s: the transfer ended in the middle of a line", opID)
			}
			if werr := w.line(line); werr != nil {
				return fmt.Errorf("tenantexport: %s: %w", opID, werr)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tenantexport: %s: the transfer was cut: %w", opID, err)
		}
	}
}

// writeUsage reads the ledger oldest first in windows of at most 31 days.
func writeUsage(ctx context.Context, c *adminapi.Client, tenant string, w *file, from, to time.Time) error {
	for start := from; start.Before(to); {
		end := start.Add(UsageWindow)
		if end.After(to) {
			end = to
		}
		body, err := c.ExportUsageRecords(ctx, adminapi.ExportUsageRecordsInput{Tenant: tenant, From: start.Format(time.RFC3339), To: end.Format(time.RFC3339)})
		if err != nil {
			return readError("exportUsageRecords", err)
		}
		if err := copyLines(body, w, "exportUsageRecords"); err != nil {
			return err
		}
		start = end
	}
	return nil
}

// writeAudit reads the head, then the log up to it, and records the head in the manifest.
func writeAudit(ctx context.Context, c *adminapi.Client, tenant string, w *file, m *Manifest) error {
	head, err := c.GetAuditHead(ctx, adminapi.GetAuditHeadInput{Tenant: tenant})
	if err != nil {
		return readError("getAuditHead", err)
	}
	m.AuditHead = ManifestAuditHead{Seq: int64(head.Seq), Hash: head.Hash, AnchorSeq: int64(head.AnchorSeq)}
	body, err := c.ExportAuditEvents(ctx, adminapi.ExportAuditEventsInput{Tenant: tenant, ToSeq: head.Seq})
	if err != nil {
		return readError("exportAuditEvents", err)
	}
	return copyLines(body, w, "exportAuditEvents")
}
