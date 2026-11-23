package admin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// defaultDeletionGrace is the grace period of an installation that configures none (§5.15).
const defaultDeletionGrace = 168 * time.Hour

// deletionRow is a row of tenant_deletion as the three statements that return it give it.
type deletionRow struct {
	TenantID, State, Step, Reason               string
	RequestedByType, RequestedByID, RequestedBy string
	RequestID                                   string
	RequestedAt, NotBefore                      time.Time
	StartedAt, CompletedAt                      *time.Time
	Passes                                      int32
	KekVersions, Pending                        []string
	Removed, Audit                              string
	CompletionSeq                               *int64
}

func deletionOf(r queries.GetTenantDeletionRow) deletionRow {
	return deletionRow{r.TenantID, r.State, r.Step, r.Reason, r.RequestedByType, r.RequestedByID, r.RequestedByName, r.RequestID,
		r.RequestedAt, r.NotBefore, r.StartedAt, r.CompletedAt, r.Passes, r.KekVersions, r.PendingKekVersions, r.Removed, r.Audit, r.CompletionSeq}
}

// out is the API form of the row. It never holds the salt: the statements do not read it.
func (r deletionRow) out() adminapi.TenantDeletion {
	d := adminapi.TenantDeletion{
		TenantID: r.TenantID, State: adminapi.TenantDeletionState(r.State), Step: adminapi.TenantDeletionStep(r.Step), Reason: r.Reason,
		RequestedBy: adminapi.TenantDeletionRequester{Type: adminapi.TenantDeletionRequesterType(r.RequestedByType), ID: r.RequestedByID, Name: r.RequestedBy},
		RequestID:   r.RequestID, RequestedAt: r.RequestedAt.UTC(), NotBefore: r.NotBefore.UTC(), Passes: int(r.Passes),
		Removed: json.RawMessage(r.Removed), KekVersions: nonNil(r.KekVersions),
		BackupErasure: adminapi.TenantDeletionBackupErasure{State: adminapi.BackupErasureStateNotApplicable, PendingKekVersions: nonNil(r.Pending)},
	}
	if len(d.Removed) == 0 {
		d.Removed = json.RawMessage(`{}`)
	}
	if r.StartedAt != nil {
		t := r.StartedAt.UTC()
		d.StartedAt = &t
	}
	if r.CompletedAt != nil {
		t := r.CompletedAt.UTC()
		d.CompletedAt = &t
	}
	if r.CompletionSeq != nil {
		d.CompletionSeq = int(*r.CompletionSeq)
	}
	// §5.12: audit once the step audit ran (it fills the column), and the erasure in backups once
	// the step secrets ran (the steps after it, or a second pass, which only follows the step audit).
	var a adminapi.TenantDeletionAudit
	if r.Audit != "" && json.Unmarshal([]byte(r.Audit), &a) == nil && a.ResealedHead != "" {
		d.Audit = &a
	}
	if d.Audit != nil || secretsRan[r.Step] {
		d.BackupErasure.State = adminapi.BackupErasureStateComplete
		if len(r.Pending) > 0 {
			d.BackupErasure.State = adminapi.BackupErasureStatePending
		}
	}
	return d
}

// secretsRan are the steps a deletion reaches only after the step secrets ran.
var secretsRan = map[string]bool{"configuration": true, "usage": true, "audit": true, "complete": true, "done": true}

// RequestTenantDeletion suspends a tenant and schedules its deletion (§5.1).
func (s *server) RequestTenantDeletion(ctx context.Context, in adminapi.RequestTenantDeletionInput) (*adminapi.TenantDeletion, error) {
	if _, err := tenantID(in.Tenant); err != nil {
		return nil, err
	}
	if err := checkReasonText(in.Body.Reason); err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	grace := s.deletionGrace
	var out adminapi.TenantDeletion
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.GetTenantForUpdate(ctx, in.Tenant)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if cur.State != "active" && cur.State != "suspended" {
			return errInvalidState
		}
		if in.Body.ConfirmName != cur.Name {
			return invalid("confirm_name", "must be the name of the tenant")
		}
		existing, err := q.GetTenantDeletion(ctx, cur.ID)
		if err == nil {
			out = deletionOf(existing).out()
			return nil
		}
		if !isNoRowsErr(err) {
			return err
		}
		if cur.State == "active" {
			if _, err := q.SetTenantState(ctx, queries.SetTenantStateParams{ID: cur.ID, State: "suspended"}); err != nil {
				return err
			}
		}
		actor := audit.ActorOf(p)
		ins, err := q.InsertTenantDeletion(ctx, queries.InsertTenantDeletionParams{
			TenantID: cur.ID, Reason: in.Body.Reason, RequestedByType: string(actor.Type), RequestedByID: actor.ID, RequestedByName: actor.Name,
			RequestID: st.id, Grace: store.Interval(grace),
		})
		if err != nil {
			return err
		}
		if err := audit.RecordPlatform(ctx, tx, audit.Event{Actor: actor, Action: "tenant.delete_request", ResourceType: "tenant", ResourceID: cur.ID,
			Outcome: "success", RequestID: st.id, Reason: in.Body.Reason,
			Detail: map[string]any{"from": cur.State, "not_before": ins.NotBefore.UTC().Format(time.RFC3339)}}); err != nil {
			return err
		}
		got, err := q.GetTenantDeletion(ctx, cur.ID)
		if err != nil {
			return err
		}
		out = deletionOf(got).out()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelTenantDeletion removes a deletion that has not started; the tenant stays suspended.
func (s *server) CancelTenantDeletion(ctx context.Context, in adminapi.CancelTenantDeletionInput) (*adminapi.Tenant, error) {
	if _, err := tenantID(in.Tenant); err != nil {
		return nil, err
	}
	if err := checkReasonText(in.Body.Reason); err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.Tenant
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.GetTenantForUpdate(ctx, in.Tenant)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		n, err := q.DeleteScheduledTenantDeletion(ctx, cur.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.GetTenantDeletion(ctx, cur.ID); err == nil {
				return errInvalidState
			} else if !isNoRowsErr(err) {
				return err
			}
			return errNotFound
		}
		out = s.tenantOut(cur)
		return audit.RecordPlatform(ctx, tx, audit.Event{Actor: audit.ActorOf(p), Action: "tenant.delete_cancel", ResourceType: "tenant", ResourceID: cur.ID,
			Outcome: "success", RequestID: st.id, Reason: in.Body.Reason, Detail: map[string]any{}})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTenantDeletion reads the deletion of a tenant.
func (s *server) GetTenantDeletion(ctx context.Context, in adminapi.GetTenantDeletionInput) (*adminapi.TenantDeletion, error) {
	if _, err := tenantID(in.Tenant); err != nil {
		return nil, err
	}
	var row queries.GetTenantDeletionRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetTenantDeletion(ctx, in.Tenant)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	d := deletionOf(row).out()
	return &d, nil
}

// ListTenantDeletions lists the deletions, newest request first.
func (s *server) ListTenantDeletions(ctx context.Context, in adminapi.ListTenantDeletionsInput) (*adminapi.TenantDeletionList, error) {
	const sortKey = "tenant-deletions"
	filter := "state=" + string(in.State)
	par := queries.ListTenantDeletionsParams{State: string(in.State)}
	if in.Cursor != "" {
		c, err := decodeFullCursor(in.Cursor, sortKey)
		at, terr := time.Parse(time.RFC3339Nano, c.N)
		if err != nil || terr != nil || c.Q != filter {
			return nil, invalid("cursor", "is not a cursor of this collection and sort")
		}
		par.After, par.AfterAt, par.AfterID = true, at, c.I
	} else {
		par.AfterID = "00000000-0000-0000-0000-000000000000"
	}
	limit, ask := limitOf(in.Limit)
	par.PageSize = ask
	var rows []queries.ListTenantDeletionsRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListTenantDeletions(ctx, par)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.TenantDeletionList{Items: []adminapi.TenantDeletion{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeFullCursor(cursor{S: sortKey, N: last.RequestedAt.UTC().Format(time.RFC3339Nano), I: last.TenantID, Q: filter})
			break
		}
		out.Items = append(out.Items, deletionOf(queries.GetTenantDeletionRow(r)).out())
	}
	return out, nil
}

// requireDeleted answers errNotFound for a tenant that does not exist and errInvalidState for one
// that is not deleted.
func (s *server) requireDeleted(ctx context.Context, id string) error {
	if _, err := tenantID(id); err != nil {
		return err
	}
	var state string
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		t, err := queries.New(tx).GetTenant(ctx, id)
		state = t.State
		return err
	})
	if isNoRowsErr(err) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if state != "deleted" {
		return errInvalidState
	}
	return nil
}

// GetDeletedTenantAuditHead reads the head of the audit chain kept for a deleted tenant.
func (s *server) GetDeletedTenantAuditHead(ctx context.Context, in adminapi.GetDeletedTenantAuditHeadInput) (*adminapi.AuditHead, error) {
	if err := s.requireDeleted(ctx, in.Tenant); err != nil {
		return nil, err
	}
	return s.GetAuditHead(ctx, adminapi.GetAuditHeadInput(in))
}

// ExportDeletedTenantAuditEvents exports the audit chain kept for a deleted tenant. Its details are
// empty, never withheld.
func (s *server) ExportDeletedTenantAuditEvents(ctx context.Context, in adminapi.ExportDeletedTenantAuditEventsInput) (*adminapi.Stream, error) {
	if err := s.requireDeleted(ctx, in.Tenant); err != nil {
		return nil, err
	}
	return s.tenantAuditExport(ctx, in.Tenant, exportDeletedTenantAudit, in.FromSeq, in.ToSeq, true)
}
