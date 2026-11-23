package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// PriceDetailActions names the audit actions whose detail holds a price, a rate or the active
// currency: the detail is withheld from a reader without price_list:read (ADR 0065). It is the one
// definition of what is withheld.
var PriceDetailActions = map[string]bool{
	"tenant_price.create":           true,
	"tenant_price.delete":           true,
	"tenant.active_currency_update": true,
}

// auditRow is a row of either log as the projection sees it.
type auditRow struct {
	Seq                                              int64
	ID                                               string
	OccurredAt                                       time.Time
	ActorType, ActorID, ActorName, KeyID             string
	Action, ResourceType, ResourceID                 string
	Outcome, RequestID, Reason, SupportActionID, Det string
	PrevHash, Hash                                   []byte
}

// auditEvent is the projection of a row (section 5.7). A price action's detail is withheld from a
// reader without price_list:read: an empty object, the flag and the digest of the detail, which the
// verifier uses in place of the detail.
func auditEvent(r auditRow, canReadPrices bool) (adminapi.AuditEvent, error) {
	e := adminapi.AuditEvent{
		Seq: int(r.Seq), ID: r.ID, OccurredAt: r.OccurredAt.UTC(), ActorType: r.ActorType, ActorID: r.ActorID,
		ActorName: r.ActorName, KeyID: r.KeyID, Action: r.Action, ResourceType: r.ResourceType, ResourceID: r.ResourceID,
		Outcome: r.Outcome, RequestID: r.RequestID, Reason: r.Reason, SupportActionID: r.SupportActionID,
		Detail: json.RawMessage(r.Det), PrevHash: hex.EncodeToString(r.PrevHash), Hash: hex.EncodeToString(r.Hash),
	}
	if PriceDetailActions[r.Action] && !canReadPrices {
		d, err := auditchain.DetailDigest(json.RawMessage(r.Det))
		if err != nil {
			return adminapi.AuditEvent{}, err
		}
		e.Detail, e.DetailWithheld, e.DetailDigest = json.RawMessage(`{}`), true, hex.EncodeToString(d[:])
	}
	return e, nil
}

// canReadPrices is whether the caller may see the details of price actions.
func canReadPrices(ctx context.Context, target authz.Target) bool {
	return authz.Decide(principalOf(ctx), authz.PriceListRead, target) == authz.Allow
}

func auditHeadOut(seq int64, hash []byte, anchorSeq int64, anchorHash []byte) *adminapi.AuditHead {
	return &adminapi.AuditHead{Seq: int(seq), Hash: hex.EncodeToString(hash), AnchorSeq: int(anchorSeq), AnchorHash: hex.EncodeToString(anchorHash)}
}

// emptyHead is the head of a chain that has no row yet.
func emptyHead() *adminapi.AuditHead {
	zero := make([]byte, 32)
	return auditHeadOut(0, zero, 1, zero)
}

// auditFilters are the filters of a list; a cursor carries them in its q member, so that a page
// is bound to the filters it was made with.
type auditFilters struct {
	From, To, Action, ResourceType, ResourceID, ActorType, ActorID, Outcome, RequestID, SupportActionID string
}

func (f auditFilters) values() url.Values {
	v := url.Values{}
	for k, x := range map[string]string{"from": f.From, "to": f.To, "action": f.Action, "resource_type": f.ResourceType,
		"resource_id": f.ResourceID, "actor_type": f.ActorType, "actor_id": f.ActorID, "outcome": f.Outcome,
		"request_id": f.RequestID, "support_action_id": f.SupportActionID} {
		if x != "" {
			v.Set(k, x)
		}
	}
	return v
}

func auditFiltersOf(v url.Values) auditFilters {
	return auditFilters{From: v.Get("from"), To: v.Get("to"), Action: v.Get("action"), ResourceType: v.Get("resource_type"),
		ResourceID: v.Get("resource_id"), ActorType: v.Get("actor_type"), ActorID: v.Get("actor_id"), Outcome: v.Get("outcome"),
		RequestID: v.Get("request_id"), SupportActionID: v.Get("support_action_id")}
}

// auditPage is a validated request for a page of a list: the position and the filters.
type auditPage struct {
	before   int64
	filters  auditFilters
	from, to *time.Time
}

// parseAuditPage checks the filters and the cursor of a list. With a cursor, the filters given must
// be exactly those of the cursor.
func parseAuditPage(sortKey, rawCursor string, given auditFilters) (auditPage, error) {
	p := auditPage{before: math.MaxInt64, filters: given}
	if rawCursor != "" {
		c, err := decodeFullCursor(rawCursor, sortKey)
		if err != nil {
			return p, err
		}
		seq, perr := strconv.ParseInt(c.N, 10, 64)
		q, qerr := url.ParseQuery(c.Q)
		if perr != nil || seq < 1 || qerr != nil {
			return p, invalid("cursor", "is not a cursor of this collection and sort")
		}
		if given.values().Encode() != q.Encode() {
			return p, invalid("cursor", "is not a cursor of this collection and sort")
		}
		p.before, p.filters = seq, auditFiltersOf(q)
	}
	var errs []*adminapi.FieldError
	parse := func(name, v string) *time.Time {
		if v == "" {
			return nil
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			errs = append(errs, &adminapi.FieldError{Field: name, Message: "must be an RFC 3339 instant with a zone"})
			return nil
		}
		t = t.UTC()
		return &t
	}
	p.from, p.to = parse("from", p.filters.From), parse("to", p.filters.To)
	if p.from != nil && p.to != nil && !p.from.Before(*p.to) {
		errs = append(errs, &adminapi.FieldError{Field: "from", Message: "must be before to"})
	}
	if err := fieldsError(errs...); err != nil {
		return p, err
	}
	return p, nil
}

func nextAuditCursor(sortKey string, last auditRow, f auditFilters) string {
	return encodeFullCursor(cursor{S: sortKey, N: strconv.FormatInt(last.Seq, 10), I: last.ID, Q: f.values().Encode()})
}

// ListAuditEvents lists the audit log of a tenant, newest first by sequence number. It runs in the
// tenant's session, so the row policy returns only its rows.
func (s *server) ListAuditEvents(ctx context.Context, in adminapi.ListAuditEventsInput) (*adminapi.AuditEventList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	const sortKey = "audit"
	pg, err := parseAuditPage(sortKey, in.Cursor, auditFilters{From: in.From, To: in.To, Action: in.Action,
		ResourceType: in.ResourceType, ResourceID: in.ResourceID, ActorType: string(in.ActorType), ActorID: in.ActorID,
		Outcome: string(in.Outcome), RequestID: in.RequestID, SupportActionID: in.SupportActionID})
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	f := pg.filters
	var rows []queries.ListAuditEventsRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListAuditEvents(ctx, queries.ListAuditEventsParams{
			BeforeSeq: pg.before, FromAt: pg.from, ToAt: pg.to, Action: f.Action, ResourceType: f.ResourceType,
			ResourceID: f.ResourceID, ActorType: f.ActorType, ActorID: f.ActorID, Outcome: f.Outcome,
			RequestID: f.RequestID, SupportActionID: f.SupportActionID, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	prices := canReadPrices(ctx, authz.Target{TenantID: tid})
	out := &adminapi.AuditEventList{Items: []adminapi.AuditEvent{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = nextAuditCursor(sortKey, auditRow{Seq: last.Seq, ID: last.ID}, f)
			break
		}
		e, err := auditEvent(auditRow{r.Seq, r.ID, r.OccurredAt, r.ActorType, r.ActorID, r.ActorName, r.KeyID, r.Action,
			r.ResourceType, r.ResourceID, r.Outcome, r.RequestID, r.Reason, r.SupportActionID, r.Detail, r.PrevHash, r.Hash}, prices)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

// GetAuditHead reads the head and the anchor of a tenant's chain.
func (s *server) GetAuditHead(ctx context.Context, in adminapi.GetAuditHeadInput) (*adminapi.AuditHead, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var h queries.GetAuditHeadRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		h, err = queries.New(tx).GetAuditHead(ctx)
		return err
	})
	if isNoRowsErr(err) {
		return emptyHead(), nil
	}
	if err != nil {
		return nil, err
	}
	return auditHeadOut(h.Seq, h.Hash, h.AnchorSeq, h.AnchorHash), nil
}

// ListPlatformAuditEvents lists the platform's audit log, newest first by sequence number.
func (s *server) ListPlatformAuditEvents(ctx context.Context, in adminapi.ListPlatformAuditEventsInput) (*adminapi.AuditEventList, error) {
	const sortKey = "platform-audit"
	pg, err := parseAuditPage(sortKey, in.Cursor, auditFilters{From: in.From, To: in.To, Action: in.Action,
		ResourceType: in.ResourceType, ResourceID: in.ResourceID, ActorType: string(in.ActorType), ActorID: in.ActorID,
		Outcome: string(in.Outcome), RequestID: in.RequestID})
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	f := pg.filters
	var rows []queries.ListPlatformAuditEventsRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		rows, err = q.ListPlatformAuditEvents(ctx, queries.ListPlatformAuditEventsParams{
			BeforeSeq: pg.before, FromAt: pg.from, ToAt: pg.to, Action: f.Action, ResourceType: f.ResourceType,
			ResourceID: f.ResourceID, ActorType: f.ActorType, ActorID: f.ActorID, Outcome: f.Outcome,
			RequestID: f.RequestID, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	prices := canReadPrices(ctx, authz.Target{})
	out := &adminapi.AuditEventList{Items: []adminapi.AuditEvent{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = nextAuditCursor(sortKey, auditRow{Seq: last.Seq, ID: last.ID}, f)
			break
		}
		e, err := auditEvent(auditRow{r.Seq, r.ID, r.OccurredAt, r.ActorType, r.ActorID, r.ActorName, r.KeyID, r.Action,
			r.ResourceType, r.ResourceID, r.Outcome, r.RequestID, r.Reason, "", r.Detail, r.PrevHash, r.Hash}, prices)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

// GetPlatformAuditHead reads the head and the anchor of the platform's chain.
func (s *server) GetPlatformAuditHead(ctx context.Context, _ adminapi.GetPlatformAuditHeadInput) (*adminapi.AuditHead, error) {
	var h queries.GetPlatformAuditHeadRow
	err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		h, err = q.GetPlatformAuditHead(ctx)
		return err
	})
	if isNoRowsErr(err) {
		return emptyHead(), nil
	}
	if err != nil {
		return nil, err
	}
	return auditHeadOut(h.Seq, h.Hash, h.AnchorSeq, h.AnchorHash), nil
}
