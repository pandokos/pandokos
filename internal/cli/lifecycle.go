package cli

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
)

func init() {
	commands = append(commands, lifecycleCommands...)
	composites = append(composites, lifecycleComposites...)
}

// lifecycleCommands are the commands of sprint 6.3 that call one operation each: the request, the
// reading and the cancellation of a tenant's deletion, the kept audit log, and the key versions.
var lifecycleCommands = []command{
	{[]string{"tenant", "delete"}, "requestTenantDeletion", func(a *app, args []string) error {
		fs := a.flags("tenant delete")
		reason := fs.String("reason", "", "recorded in the audit log and with the deletion (required)")
		confirm := fs.String("confirm-name", "", "the name of the tenant, to confirm the request (required)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if err := requireReason("tenant delete", *reason); err != nil {
			return err
		}
		if strings.TrimSpace(*confirm) == "" {
			return usagef("tenant delete needs --confirm-name, the name of the tenant")
		}
		if err := a.connect(); err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		d, err := a.client.RequestTenantDeletion(a.ctx, adminapi.RequestTenantDeletionInput{Tenant: id, Body: adminapi.TenantDeletionRequest{Reason: *reason, ConfirmName: *confirm}})
		if err != nil {
			return err
		}
		return a.printResource(d)
	}},

	{[]string{"tenant", "deletion", "get"}, "getTenantDeletion", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("tenant deletion get"), args, 1)
		if err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		d, err := a.client.GetTenantDeletion(a.ctx, adminapi.GetTenantDeletionInput{Tenant: id})
		if err != nil {
			return err
		}
		return a.printResource(d)
	}},

	{[]string{"tenant", "deletion", "cancel"}, "cancelTenantDeletion", func(a *app, args []string) error {
		fs := a.flags("tenant deletion cancel")
		reason := fs.String("reason", "", "recorded in the audit log (required)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if err := requireReason("tenant deletion cancel", *reason); err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		t, err := a.client.CancelTenantDeletion(a.ctx, adminapi.CancelTenantDeletionInput{Tenant: id, Body: adminapi.RequiredReason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(t)
	}},

	{[]string{"tenant", "deletion", "list"}, "listTenantDeletions", func(a *app, args []string) error {
		fs := a.flags("tenant deletion list")
		var l listFlags
		l.registerPaging(fs)
		fs.StringVar(&l.state, "state", "", "only deletions in this state: scheduled, running or completed")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if l.state != "" && !adminapi.TenantDeletionState(l.state).Valid() {
			return usagef("--state must be scheduled, running or completed")
		}
		if err := a.connect(); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTenantDeletions(a.ctx, adminapi.ListTenantDeletionsInput{State: adminapi.TenantDeletionState(l.state), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"tenant", "deletion", "audit-head"}, "getDeletedTenantAuditHead", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("tenant deletion audit-head"), args, 1)
		if err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		h, err := a.client.GetDeletedTenantAuditHead(a.ctx, adminapi.GetDeletedTenantAuditHeadInput{Tenant: id})
		if err != nil {
			return err
		}
		return a.printResource(h)
	}},

	{[]string{"tenant", "deletion", "audit-export"}, "exportDeletedTenantAuditEvents", func(a *app, args []string) error {
		fs := a.flags("tenant deletion audit-export")
		out := fs.String("out", "", "write to this file (through FILE.partial) instead of standard output")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		return a.writeExport(*out, func(w io.Writer) error {
			body, err := a.client.ExportDeletedTenantAuditEvents(a.ctx, adminapi.ExportDeletedTenantAuditEventsInput{Tenant: id})
			if err != nil {
				return err
			}
			defer body.Body.Close()
			_, err = io.Copy(w, body.Body)
			return err
		})
	}},

	{[]string{"kek-version", "list"}, "listKEKVersions", func(a *app, args []string) error {
		if _, err := a.begin(a.flags("kek-version list"), args, 0); err != nil {
			return err
		}
		out, err := a.client.ListKEKVersions(a.ctx, adminapi.ListKEKVersionsInput{})
		if err != nil {
			return err
		}
		if a.output == "json" {
			return a.printJSON(out)
		}
		a.printTable(toAny(out.Items))
		return nil
	}},
}

// lifecycleComposites are the commands made of several operations, or of none.
var lifecycleComposites = []command{
	{[]string{"tenant", "deletion", "verify"}, "", func(a *app, args []string) error { return a.verifyDeletion(args) }},
	{[]string{"tenant-export", "schema"}, "", func(a *app, args []string) error { return a.exportSchema(args) }},
	{[]string{"tenant-export"}, "", func(a *app, args []string) error { return a.tenantExport(args) }},
}

// The reasons of a failed verification of a deletion (section 5.5).
const (
	reasonRecordMissing  = "record_missing"
	reasonRecordMismatch = "record_mismatch"
	reasonPlatformBreak  = "platform_chain_break"
	reasonTenantBreak    = "tenant_chain_break"
	reasonHeadMismatch   = "head_mismatch"
)

// completionDetail is the detail of the platform row tenant.delete_complete.
type completionDetail struct {
	RequestedAt time.Time                    `json:"requested_at"`
	StartedAt   time.Time                    `json:"started_at"`
	Passes      int                          `json:"passes"`
	Removed     json.RawMessage              `json:"removed"`
	KekVersions []string                     `json:"kek_versions"`
	Audit       adminapi.TenantDeletionAudit `json:"audit"`
	Residue     int                          `json:"residue"`
}

// sameJSON reports whether two JSON values are equal as values.
func sameJSON(x, y json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(x, &a) != nil || json.Unmarshal(y, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// chainRows reads an NDJSON export as rows of the chain.
func chainRows(r io.Reader, scope string, each func(auditchain.Row)) (iter.Seq2[auditchain.Row, error], *error) {
	var readErr error
	rows := func(yield func(auditchain.Row, error) bool) {
		readErr = readLines(r, func(line []byte) error {
			row, err := rowOf(scope, line)
			if err != nil {
				return err
			}
			if each != nil {
				each(row)
			}
			if !yield(row, nil) {
				return errStop
			}
			return nil
		})
	}
	return rows, &readErr
}

// verifyDeletion is pandokos tenant deletion verify (section 5.5).
func (a *app) verifyDeletion(args []string) error {
	pos, err := a.begin(a.flags("tenant deletion verify"), args, 1)
	if err != nil {
		return err
	}
	tid, err := a.resolveTenant(pos[0])
	if err != nil {
		return err
	}
	d, err := a.client.GetTenantDeletion(a.ctx, adminapi.GetTenantDeletionInput{Tenant: tid})
	if err != nil {
		return err
	}
	var failures []string
	fail := func(reason, format string, args ...any) {
		failures = append(failures, reason+": "+fmt.Sprintf(format, args...))
	}
	finish := func() error {
		for _, f := range failures {
			fmt.Fprintln(a.stdout, f)
		}
		return errBreaks
	}
	if d.State != adminapi.TenantDeletionStateCompleted || d.Audit == nil || d.CompletionSeq == 0 {
		fmt.Fprintf(a.stdout, "the deletion of %s is %s, not completed: there is nothing to check yet\n", tid, d.State)
		return errBreaks
	}

	// 1. The record of the completion in the platform's log.
	list, err := a.client.ListPlatformAuditEvents(a.ctx, adminapi.ListPlatformAuditEventsInput{Action: "tenant.delete_complete", ResourceID: tid, Limit: 100})
	if err != nil {
		return err
	}
	var record *adminapi.AuditEvent
	for i := range list.Items {
		if list.Items[i].Seq == d.CompletionSeq {
			record = &list.Items[i]
		}
	}
	switch {
	case record == nil && len(list.Items) == 0:
		fail(reasonRecordMissing, "the platform log has no tenant.delete_complete for the tenant")
	case record == nil:
		fail(reasonRecordMismatch, "the platform log has the record, not at seq %d", d.CompletionSeq)
	default:
		var c completionDetail
		switch {
		case json.Unmarshal(record.Detail, &c) != nil:
			fail(reasonRecordMismatch, "the detail of the record at seq %d cannot be read", record.Seq)
		case !c.RequestedAt.Equal(d.RequestedAt) || d.StartedAt == nil || !c.StartedAt.Equal(*d.StartedAt) || c.Passes != d.Passes ||
			!sameJSON(c.Removed, d.Removed) || !reflect.DeepEqual(nilEmpty(c.KekVersions), nilEmpty(d.KekVersions)) || c.Audit != *d.Audit || c.Residue != 0:
			fail(reasonRecordMismatch, "the detail of the record at seq %d is not what the deletion says", record.Seq)
		}
	}

	// 2. The platform's chain, up to its head, and the record's place in it.
	head, err := a.client.GetPlatformAuditHead(a.ctx, adminapi.GetPlatformAuditHeadInput{})
	if err != nil {
		return err
	}
	ph, err := headOf(head)
	if err != nil {
		return fmt.Errorf("invalid head of the platform log: %w", err)
	}
	body, err := a.client.ExportPlatformAuditEvents(a.ctx, adminapi.ExportPlatformAuditEventsInput{ToSeq: head.Seq})
	if err != nil {
		return err
	}
	var atRecord *auditchain.Row
	rows, readErr := chainRows(body.Body, "platform", func(r auditchain.Row) {
		if r.Seq == int64(d.CompletionSeq) {
			atRecord = &r
		}
	})
	res := auditchain.Verify(rows, ph, nil)
	_ = body.Body.Close()
	if *readErr != nil && !errors.Is(*readErr, errStop) {
		return fmt.Errorf("invalid export of the platform log: %w", *readErr)
	}
	for _, b := range res.Breaks {
		fail(reasonPlatformBreak, "seq %d: %s", b.Seq, b.Reason)
	}
	if record != nil && (atRecord == nil || hex.EncodeToString(atRecord.Hash) != record.Hash) {
		fail(reasonRecordMismatch, "the row at seq %d of the platform chain is not the record that was read", d.CompletionSeq)
	}

	// 3. The kept log, against the re-sealed head.
	kept, err := a.client.GetDeletedTenantAuditHead(a.ctx, adminapi.GetDeletedTenantAuditHeadInput{Tenant: tid})
	if err != nil {
		return err
	}
	kh, err := headOf(kept)
	if err != nil {
		return fmt.Errorf("invalid head of the kept log: %w", err)
	}
	resealed, err := hex.DecodeString(d.Audit.ResealedHead)
	if err != nil || len(resealed) != 32 {
		fail(reasonRecordMismatch, "the deletion's resealed head is not 64 hexadecimal digits")
		resealed = nil
	}
	body, err = a.client.ExportDeletedTenantAuditEvents(a.ctx, adminapi.ExportDeletedTenantAuditEventsInput{Tenant: tid})
	if err != nil {
		return err
	}
	var last *auditchain.Row
	emptied := kept.AnchorSeq == d.Audit.LastSeq+1 // retention has removed every row
	rows, readErr = chainRows(body.Body, "tenant:"+tid, func(r auditchain.Row) { last = &r })
	var kres auditchain.Result
	if emptied {
		kres = auditchain.Verify(rows, nil, nil)
		if kres.Rows != 0 {
			fail(reasonTenantBreak, "the log is past its anchor but holds %d rows", kres.Rows)
		}
	} else {
		kres = auditchain.Verify(rows, kh, resealed)
	}
	_ = body.Body.Close()
	if *readErr != nil && !errors.Is(*readErr, errStop) {
		return fmt.Errorf("invalid export of the kept log: %w", *readErr)
	}
	for _, b := range kres.Breaks {
		reason := reasonTenantBreak
		if b.Reason == auditchain.ReasonExpectedHeadMismatch || b.Reason == auditchain.ReasonHeadMismatch {
			reason = reasonHeadMismatch
		}
		fail(reason, "seq %d: %s", b.Seq, b.Reason)
	}
	if !emptied && (last == nil || last.Action != "tenant.delete") {
		fail(reasonTenantBreak, "the last row of the kept log is not tenant.delete")
	}
	if len(failures) > 0 {
		return finish()
	}
	headHash := d.Audit.ResealedHead
	if emptied {
		headHash = "none"
	}
	fmt.Fprintf(a.stdout, "deletion of %s verified: record at seq %d, %d audit rows kept, head %s\n", tid, d.CompletionSeq, kres.Rows, headHash)
	return nil
}

func nilEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
