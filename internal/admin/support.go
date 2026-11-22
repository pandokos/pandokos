package admin

// Support actions of the platform inside a tenant, seen from both sides (sprint 4.3, section 5.6).

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	minSupportSeconds = 60
	maxReasonRunes    = 500
)

// supportOut is the API form of a support action as of now. An action whose time has passed without
// anybody closing it reads as expired, and as closed by the expiry, at the time it ran out.
func supportOut(r queries.PandokosSupportAction, now time.Time) adminapi.SupportAction {
	out := adminapi.SupportAction{
		ID: r.ID, TenantID: r.TenantID, Reason: r.Reason, Permissions: nonNil(r.Permissions),
		Actor:    adminapi.SupportActionActor{Type: adminapi.SupportActionActorType(r.ActorType), ID: r.ActorID, Name: r.ActorName},
		OpenedAt: r.OpenedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC(), CloseReason: r.CloseReason,
	}
	switch {
	case r.ClosedAt != nil:
		c := r.ClosedAt.UTC()
		out.ClosedAt = &c
		out.ClosedBy = adminapi.SupportActionClosedBy(r.ClosedBy)
		switch r.ClosedBy {
		case "tenant":
			out.State = adminapi.SupportActionStateEnded
		case "expiry":
			out.State = adminapi.SupportActionStateExpired
		default:
			out.State = adminapi.SupportActionStateClosed
		}
	case !r.ExpiresAt.After(now):
		c := r.ExpiresAt.UTC()
		out.ClosedAt, out.ClosedBy, out.State = &c, adminapi.SupportActionClosedByExpiry, adminapi.SupportActionStateExpired
	default:
		out.State = adminapi.SupportActionStateOpen
	}
	return out
}

// isOpen says whether the action is still in force.
func isOpen(r queries.PandokosSupportAction, now time.Time) bool {
	return r.ClosedAt == nil && r.ExpiresAt.After(now)
}

// supportListParams turns the query of a list into the parameters of the statement.
func supportListParams(tid tenant.ID, state adminapi.SupportActionFilter, sortBy, cursor string, limit int32) (queries.ListSupportActionsParams, error) {
	after, name, id, err := decodeCursor(cursor, sortBy)
	if err != nil {
		return queries.ListSupportActionsParams{}, err
	}
	par := queries.ListSupportActionsParams{TenantID: tid.String(), State: string(state), After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: limit}
	if after {
		if par.AfterOpenedAt, err = time.Parse(time.RFC3339Nano, name); err != nil {
			return par, invalid("cursor", "is not a cursor of this collection and sort")
		}
	}
	return par, nil
}

func (s *server) supportList(rows []queries.PandokosSupportAction, limit int, sortBy string) *adminapi.SupportActionList {
	now := s.now()
	out := &adminapi.SupportActionList{Items: []adminapi.SupportAction{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeCursor(sortBy, last.OpenedAt.UTC().Format(time.RFC3339Nano), last.ID)
			break
		}
		out.Items = append(out.Items, supportOut(r, now))
	}
	return out
}

func supportSort(in adminapi.SupportActionSort) string {
	if in == "" {
		return string(adminapi.SupportActionSortMinusOpenedAt)
	}
	return string(in)
}

// ListTenantSupportActions lists the support actions of a tenant, for the platform.
func (s *server) ListTenantSupportActions(ctx context.Context, in adminapi.ListTenantSupportActionsInput) (*adminapi.SupportActionList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	sortBy := supportSort(in.Sort)
	limit, ask := limitOf(in.Limit)
	par, err := supportListParams(tid, in.State, sortBy, in.Cursor, ask)
	if err != nil {
		return nil, err
	}
	var rows []queries.PandokosSupportAction
	err = s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if ok, err := q.TenantExists(ctx, tid.String()); err != nil || !ok {
			return notFoundOr(err)
		}
		var err error
		rows, err = q.ListSupportActions(ctx, par)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.supportList(rows, limit, sortBy), nil
}

// notFoundOr is errNotFound when err is nil: the caller found no row and no failure.
func notFoundOr(err error) error {
	if err != nil {
		return err
	}
	return errNotFound
}

// GetTenantSupportAction reads a support action of a tenant, for the platform.
func (s *server) GetTenantSupportAction(ctx context.Context, in adminapi.GetTenantSupportActionInput) (*adminapi.SupportAction, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var row queries.PandokosSupportAction
	err = s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if ok, err := q.TenantExists(ctx, tid.String()); err != nil || !ok {
			return notFoundOr(err)
		}
		var err error
		row, err = q.GetSupportAction(ctx, queries.GetSupportActionParams{ID: in.Action, TenantID: tid.String()})
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	out := supportOut(row, s.now())
	return &out, nil
}

// ListSupportActions lists the support actions of the tenant, for the tenant.
func (s *server) ListSupportActions(ctx context.Context, in adminapi.ListSupportActionsInput) (*adminapi.SupportActionList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	sortBy := supportSort(in.Sort)
	limit, ask := limitOf(in.Limit)
	par, err := supportListParams(tid, in.State, sortBy, in.Cursor, ask)
	if err != nil {
		return nil, err
	}
	var rows []queries.PandokosSupportAction
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListSupportActions(ctx, par)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.supportList(rows, limit, sortBy), nil
}

// GetSupportAction reads a support action of the tenant, for the tenant.
func (s *server) GetSupportAction(ctx context.Context, in adminapi.GetSupportActionInput) (*adminapi.SupportAction, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var row queries.PandokosSupportAction
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetSupportAction(ctx, queries.GetSupportActionParams{ID: in.Action, TenantID: tid.String()})
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	out := supportOut(row, s.now())
	return &out, nil
}

// OpenSupportAction opens a support action of the platform in a tenant: the row, and the event in
// the tenant's log and in the platform's, in one transaction. An open action of the same actor in the
// same tenant is closed first.
func (s *server) OpenSupportAction(ctx context.Context, in adminapi.OpenSupportActionInput) (*adminapi.SupportAction, error) {
	if !s.support.enabled {
		return nil, errSupportDisabled
	}
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if err := checkReasonText(b.Reason); err != nil {
		return nil, err
	}
	allowed := authz.SupportPermissions()
	for _, name := range b.Permissions {
		if !slices.Contains(allowed, name) {
			return nil, invalid("permissions", "names a permission that a support action cannot carry: "+name)
		}
	}
	perms := sortedCopy(b.Permissions)
	seconds := int(s.support.maxDuration / time.Second)
	if b.DurationSeconds != 0 {
		if b.DurationSeconds < minSupportSeconds {
			return nil, invalid("duration_seconds", "must be at least "+strconv.Itoa(minSupportSeconds))
		}
		if b.DurationSeconds > seconds {
			return nil, invalid("duration_seconds", "must be at most "+strconv.Itoa(seconds))
		}
		seconds = b.DurationSeconds
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	actor := audit.ActorOf(p)
	var out adminapi.SupportAction
	err = s.db.ActInTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if ok, err := q.TenantExists(ctx, tid.String()); err != nil || !ok {
			return notFoundOr(err)
		}
		closed, err := q.CloseOpenSupportActions(ctx, queries.CloseOpenSupportActionsParams{
			ClosedByID: actor.ID, TenantID: tid.String(), ActorType: string(actor.Type), ActorID: actor.ID,
		})
		if err != nil {
			return err
		}
		// An action that had expired is closed as expiry: it was not replaced.
		var replaced []string
		for _, c := range closed {
			if c.ClosedBy == "platform" {
				replaced = append(replaced, c.ID)
			}
		}
		row, err := q.OpenSupportAction(ctx, queries.OpenSupportActionParams{
			TenantID: tid.String(), ActorType: string(actor.Type), ActorID: actor.ID, ActorName: actor.Name, KeyID: actor.KeyID,
			Reason: b.Reason, Permissions: perms, RequestID: st.id, Seconds: int32(seconds), //nolint:gosec // at most 24 h by the configuration
		})
		if err != nil {
			return err
		}
		out = supportOut(row, s.now())
		detail := map[string]any{"permissions": perms, "expires_at": fmtTime(&row.ExpiresAt)}
		if len(replaced) > 0 {
			detail["replaces"] = replaced
		}
		return audit.RecordIntervention(ctx, tx, tid, audit.Event{Actor: actor, Action: "support_action.open", ResourceType: "support_action",
			ResourceID: row.ID, Outcome: "success", RequestID: st.id, Reason: b.Reason, Detail: detail, SupportActionID: row.ID})
	})
	if err != nil {
		return nil, mapSupportConstraint(err)
	}
	return &out, nil
}

// mapSupportConstraint maps the unique violation of an opening that met another opening of the same
// actor in the same tenant, committed meanwhile, to 409 conflict.
func mapSupportConstraint(err error) error {
	if kind, name, ok := store.ConstraintViolation(err); ok && kind == "unique" && name == "support_action_open_key" {
		return errConflict
	}
	return err
}

// CloseSupportAction closes a support action of a tenant, for the platform; closing an action that is
// closed or has expired answers it unchanged.
func (s *server) CloseSupportAction(ctx context.Context, in adminapi.CloseSupportActionInput) (*adminapi.SupportAction, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	reason := in.Body.Reason
	if reason != "" {
		if err := checkReasonText(reason); err != nil {
			return nil, err
		}
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	actor := audit.ActorOf(p)
	var out adminapi.SupportAction
	err = s.db.ActInTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if ok, err := q.TenantExists(ctx, tid.String()); err != nil || !ok {
			return notFoundOr(err)
		}
		row, err := q.LockSupportAction(ctx, queries.LockSupportActionParams{ID: in.Action, TenantID: tid.String()})
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if !isOpen(row, s.now()) {
			out = supportOut(row, s.now())
			return nil
		}
		if _, err := q.CloseSupportAction(ctx, queries.CloseSupportActionParams{ClosedBy: "platform", ClosedByID: actor.ID, CloseReason: reason, ID: row.ID}); err != nil {
			return err
		}
		if row, err = q.GetSupportAction(ctx, queries.GetSupportActionParams{ID: row.ID, TenantID: tid.String()}); err != nil {
			return err
		}
		out = supportOut(row, s.now())
		return audit.RecordIntervention(ctx, tx, tid, audit.Event{Actor: actor, Action: "support_action.close", ResourceType: "support_action",
			ResourceID: row.ID, Outcome: "success", RequestID: st.id, Reason: reason, SupportActionID: row.ID})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// EndSupportAction ends a support action of the platform in the tenant, for the tenant: the platform
// asked to come in, and the tenant can say that it is enough.
func (s *server) EndSupportAction(ctx context.Context, in adminapi.EndSupportActionInput) (*adminapi.SupportAction, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	reason := in.Body.Reason
	if reason != "" {
		if err := checkReasonText(reason); err != nil {
			return nil, err
		}
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.SupportAction
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		row, err := q.LockSupportAction(ctx, queries.LockSupportActionParams{ID: in.Action, TenantID: tid.String()})
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if !isOpen(row, s.now()) {
			out = supportOut(row, s.now())
			return nil
		}
		if _, err := q.CloseSupportAction(ctx, queries.CloseSupportActionParams{ClosedBy: "tenant", ClosedByID: p.ID, CloseReason: reason, ID: row.ID}); err != nil {
			return err
		}
		if row, err = q.GetSupportAction(ctx, queries.GetSupportActionParams{ID: row.ID, TenantID: tid.String()}); err != nil {
			return err
		}
		out = supportOut(row, s.now())
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(p), Action: "support_action.end", ResourceType: "support_action",
			ResourceID: row.ID, Outcome: "success", RequestID: st.id, Reason: reason})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// supportPrincipal is the principal that a platform principal becomes in the tenant when it has an
// open support action there (section 5.6): its kind, ID and name, the tenant, no role, the action and
// the action's permissions. It returns false, and the caller keeps the answer that it had before this
// sprint, unless support.enabled, the principal has no tenant, it still holds tenant_support:open and
// a row of the platform's scope names it as the actor of an open action of the tenant.
func (s *server) supportPrincipal(ctx context.Context, p authz.Principal, tid tenant.ID) (authz.Principal, bool, error) {
	if !s.support.enabled || !p.TenantID.IsZero() || !slices.Contains(p.Effective(), authz.TenantSupportOpen) {
		return p, false, nil
	}
	actor := audit.ActorOf(p)
	var row queries.GetOpenSupportActionRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetOpenSupportAction(ctx, queries.GetOpenSupportActionParams{TenantID: tid.String(), ActorType: string(actor.Type), ActorID: actor.ID})
		return err
	})
	if isNoRowsErr(err) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	sp := p
	sp.TenantID, sp.TenantName, sp.TenantState = tid, row.TenantName, tenant.State(row.TenantState)
	sp.Roles, sp.Granted, sp.SupportActionID = nil, row.Permissions, row.ID
	return sp, true, nil
}
