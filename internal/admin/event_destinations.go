package admin

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/outbox"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
)

// defaultPreviousSecretSeconds is how long a replaced signing secret keeps signing when a
// rotation does not say (sprint 6.4, section 5.9).
const defaultPreviousSecretSeconds = 86400

// destinationRow is the columns of a destination that the list and the read statements share.
type destinationRow = queries.GetEventDestinationRow

// destinationOut is the API form of a destination (section 5.8). The sealed secrets are not columns
// the statements read, so nothing here could show one.
func destinationOut(r destinationRow) adminapi.EventDestination {
	d := adminapi.EventDestination{
		ID: r.ID, TenantID: r.TenantID, Name: r.Name, Description: r.Description, URL: r.Url, Enabled: r.Enabled,
		SecretCreatedAt: r.SecretCreatedAt.UTC(), PreviousSecretExpiresAt: utcPtr(r.PreviousSecretExpiresAt),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), EventTypes: []adminapi.EventType{},
	}
	for _, t := range r.EventTypes {
		d.EventTypes = append(d.EventTypes, adminapi.EventType(t))
	}
	state := adminapi.EventDeliveryStateIdle
	switch {
	case !r.Enabled:
		state = adminapi.EventDeliveryStateDisabled
	case r.Failures > 0:
		state = adminapi.EventDeliveryStateFailing
	case r.CursorSeq < r.HeadSeq:
		state = adminapi.EventDeliveryStateDelivering
	}
	d.Delivery = adminapi.EventDelivery{
		State: state, Cursor: int(r.CursorSeq), Head: int(r.HeadSeq), Failures: int(r.Failures), FailingSince: utcPtr(r.FailingSince),
		NextAttemptAt: r.NextAttemptAt.UTC(), LastSuccessAt: utcPtr(r.LastSuccessAt), LastFailureAt: utcPtr(r.LastFailureAt),
		LastError: r.LastError, DeadLetters: int(r.DeadLetters), ExpiredEvents: int(r.ExpiredEvents),
	}
	if r.LastStatus != nil {
		d.Delivery.LastStatus = int(*r.LastStatus)
	}
	d.Test = testOut(r)
	return d
}

// testOut is the last test request of a destination; nil when none was made. A request that the
// worker has not answered yet has no completion, even when an older one did.
func testOut(r destinationRow) *adminapi.EventDestinationTest {
	if r.TestRequestedAt == nil || r.TestEventID == nil {
		return nil
	}
	t := &adminapi.EventDestinationTest{EventID: *r.TestEventID, RequestedAt: r.TestRequestedAt.UTC()}
	if r.TestCompletedAt != nil && !r.TestCompletedAt.Before(*r.TestRequestedAt) {
		t.CompletedAt = utcPtr(r.TestCompletedAt)
		t.Outcome = adminapi.DeliveryOutcome(r.TestOutcome)
		if r.TestStatus != nil {
			t.Status = int(*r.TestStatus)
		}
	}
	return t
}

// urlHost is what the audit log keeps of a destination's URL: the host, never the path or the query,
// which can hold a token.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// checkDestinationURL validates the address of a destination; the message never quotes it.
func (s *server) checkDestinationURL(raw string) error {
	if err := outbox.ValidateURL(raw, s.events.privateHosts); err != nil {
		return invalid("url", "must be an https URL with a host, without user information or fragment (http only for the hosts the installation lists)")
	}
	return nil
}

// checkEventTypes validates the types a destination subscribes to: the types of a tenant's log.
func checkEventTypes(types []adminapi.EventType) ([]string, error) {
	known := outbox.Types()
	out := make([]string, 0, len(types))
	for _, t := range types {
		if !slices.Contains(known, string(t)) {
			return nil, invalid("event_types", "must name types of the tenant's events")
		}
		if slices.Contains(out, string(t)) {
			return nil, invalid("event_types", "must not repeat an item")
		}
		out = append(out, string(t))
	}
	return out, nil
}

// destinationConstraint maps what the database refuses on a destination: a name in use and the ninth
// destination are conflicts; any other check is the second line of defence for what the handler
// validates first.
func destinationConstraint(err error) error {
	if err == nil {
		return nil
	}
	if kind, name, ok := store.ConstraintViolation(err); ok {
		switch {
		case kind == "unique" && name == "event_destination_name_key":
			return errConflict
		case kind == "check" && name == "event_destination_limit":
			return errConflict
		case kind == "check" && len(name) > len("event_destination_") && name[:len("event_destination_")] == "event_destination_":
			return invalid("body", "does not satisfy the rules of a destination")
		}
	}
	return err
}

// newID asks the database for a version 7 identifier, so that the secret of a destination can be
// sealed for the ID it will have.
func newID(ctx context.Context, tx store.Tx) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT pandokos.uuid_v7()::text`).Scan(&id)
	return id, err
}

func (s *server) recordDestination(ctx context.Context, tx store.Tx, tid tenant.ID, action, id string, detail map[string]any) error {
	return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: "event_destination." + action,
		ResourceType: "event_destination", ResourceID: id, Outcome: "success", RequestID: stateOf(ctx).id, Detail: detail})
}

// ListEventDestinations lists the destinations of a tenant in the order of their creation.
func (s *server) ListEventDestinations(ctx context.Context, in adminapi.ListEventDestinationsInput) (*adminapi.EventDestinationList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	after, _, id, err := decodeCursor(in.Cursor, "created_at")
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListEventDestinationsRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListEventDestinations(ctx, queries.ListEventDestinationsParams{After: after, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.EventDestinationList{Items: []adminapi.EventDestination{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("created_at", "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, destinationOut(destinationRow(r)))
	}
	return out, nil
}

// CreateEventDestination creates a destination and answers with its signing secret, once.
func (s *server) CreateEventDestination(ctx context.Context, in adminapi.CreateEventDestinationInput) (*adminapi.EventDestinationCreated, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if err := s.checkDestinationURL(b.URL); err != nil {
		return nil, err
	}
	types, err := checkEventTypes(b.EventTypes)
	if err != nil {
		return nil, err
	}
	key, err := outbox.NewKey()
	if err != nil {
		return nil, err
	}
	var out adminapi.EventDestinationCreated
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		id, err := newID(ctx, tx)
		if err != nil {
			return err
		}
		sealed, err := s.sealer.SealNamed(ctx, tx, tid, vault.KindWebhookSecret, id, key.Text())
		if err != nil {
			return err
		}
		if err := s.recordDataKey(ctx, tx, tid, sealed); err != nil {
			return err
		}
		q := queries.New(tx)
		if _, err := q.CreateEventDestination(ctx, queries.CreateEventDestinationParams{
			ID: id, Name: b.Name, Description: b.Description, Url: b.URL, EventTypes: types, Enabled: b.Enabled,
			Secret: sealed.Secret, SecretKeyVersion: sealed.KeyVersion,
		}); err != nil {
			return err
		}
		row, err := q.GetEventDestination(ctx, id)
		if err != nil {
			return err
		}
		out = adminapi.EventDestinationCreated{Destination: destinationOut(row), Secret: revealOnce(key.Text())}
		return s.recordDestination(ctx, tx, tid, "create", id, map[string]any{"name": row.Name, "host": urlHost(row.Url), "event_types": row.EventTypes, "enabled": row.Enabled})
	})
	if err = destinationConstraint(err); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetEventDestination reads a destination.
func (s *server) GetEventDestination(ctx context.Context, in adminapi.GetEventDestinationInput) (*adminapi.EventDestination, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	var row destinationRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetEventDestination(ctx, did.String())
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	out := destinationOut(row)
	return &out, nil
}

// UpdateEventDestination changes the members that are present; enabling a disabled destination clears
// its failure state.
func (s *server) UpdateEventDestination(ctx context.Context, in adminapi.UpdateEventDestinationInput) (*adminapi.EventDestination, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	b := in.Body
	par := queries.UpdateEventDestinationParams{ID: did.String()}
	detail := map[string]any{}
	var changed []string
	if b.Name.Set {
		par.Name = &b.Name.Value
		changed = append(changed, "name")
	}
	if b.Description.Set {
		par.Description = &b.Description.Value
		changed = append(changed, "description")
	}
	if b.URL.Set {
		if err := s.checkDestinationURL(b.URL.Value); err != nil {
			return nil, err
		}
		par.Url = &b.URL.Value
		detail["host"] = urlHost(b.URL.Value)
		changed = append(changed, "url")
	}
	if b.EventTypes.Set {
		types, err := checkEventTypes(b.EventTypes.Value)
		if err != nil {
			return nil, err
		}
		par.EventTypes = types
		detail["event_types"] = types
		changed = append(changed, "event_types")
	}
	if b.Enabled.Set {
		par.Enabled = &b.Enabled.Value
		detail["enabled"] = b.Enabled.Value
		changed = append(changed, "enabled")
	}
	if len(changed) == 0 {
		return nil, invalid("body", "must change at least one member")
	}
	detail["changed"] = changed
	var out adminapi.EventDestination
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		n, err := q.UpdateEventDestination(ctx, par)
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		row, err := q.GetEventDestination(ctx, par.ID)
		if err != nil {
			return err
		}
		detail["name"] = row.Name
		out = destinationOut(row)
		return s.recordDestination(ctx, tx, tid, "update", par.ID, detail)
	})
	if err = destinationConstraint(err); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteEventDestination deletes a destination with its dead letters.
func (s *server) DeleteEventDestination(ctx context.Context, in adminapi.DeleteEventDestinationInput) (*adminapi.NoContent, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		row, err := q.GetEventDestination(ctx, did.String())
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if n, err := q.DeleteEventDestination(ctx, did.String()); err != nil {
			return err
		} else if n == 0 {
			return errNotFound
		}
		return s.recordDestination(ctx, tx, tid, "delete", row.ID, map[string]any{"name": row.Name, "host": urlHost(row.Url)})
	})
	if err != nil {
		return nil, err
	}
	return &adminapi.NoContent{}, nil
}

// RotateEventDestinationSecret replaces the signing secret. The replaced one keeps signing for
// previous_valid_for_seconds (24 hours by default, none for 0), so that a receiver can switch.
func (s *server) RotateEventDestinationSecret(ctx context.Context, in adminapi.RotateEventDestinationSecretInput) (*adminapi.EventDestinationCreated, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	seconds := defaultPreviousSecretSeconds
	if in.Body.PreviousValidForSeconds.Set {
		seconds = in.Body.PreviousValidForSeconds.Value
	}
	key, err := outbox.NewKey()
	if err != nil {
		return nil, err
	}
	var out adminapi.EventDestinationCreated
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.GetEventDestination(ctx, did.String())
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		sealed, err := s.sealer.SealNamed(ctx, tx, tid, vault.KindWebhookSecret, cur.ID, key.Text())
		if err != nil {
			return err
		}
		if err := s.recordDataKey(ctx, tx, tid, sealed); err != nil {
			return err
		}
		if n, err := q.RotateEventDestinationSecret(ctx, queries.RotateEventDestinationSecretParams{
			ID: cur.ID, Secret: sealed.Secret, SecretKeyVersion: sealed.KeyVersion, PreviousSeconds: int32(seconds), //nolint:gosec // at most 604800 by the API document
		}); err != nil {
			return err
		} else if n == 0 {
			return errNotFound
		}
		row, err := q.GetEventDestination(ctx, cur.ID)
		if err != nil {
			return err
		}
		out = adminapi.EventDestinationCreated{Destination: destinationOut(row), Secret: revealOnce(key.Text())}
		return s.recordDestination(ctx, tx, tid, "rotate_secret", cur.ID, map[string]any{"name": row.Name, "previous_valid_for_seconds": seconds})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// TestEventDestination records a request for a test event with a new ID. The worker sends it and
// records the outcome; this process makes no request and opens no secret.
func (s *server) TestEventDestination(ctx context.Context, in adminapi.TestEventDestinationInput) (*adminapi.EventDestinationTest, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	var out *adminapi.EventDestinationTest
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		id, err := newID(ctx, tx)
		if err != nil {
			return err
		}
		q := queries.New(tx)
		n, err := q.RequestEventDestinationTest(ctx, queries.RequestEventDestinationTestParams{ID: did.String(), EventID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		row, err := q.GetEventDestination(ctx, did.String())
		if err != nil {
			return err
		}
		out = testOut(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RewindEventDestination sets the cursor back to a sequence number or to the last event recorded
// before a time; it never moves the cursor forward.
func (s *server) RewindEventDestination(ctx context.Context, in adminapi.RewindEventDestinationInput) (*adminapi.EventDestination, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	b := in.Body
	par := queries.RewindEventDestinationParams{ID: did.String()}
	detail := map[string]any{}
	switch {
	case b.After.Set == b.Since.Set:
		return nil, invalid("body", "must have after or since, not both")
	case b.After.Set:
		if b.After.Value < 0 {
			return nil, invalid("after", "must be at least 0")
		}
		a := int64(b.After.Value)
		par.After = &a
		detail["cursor"] = a
	default:
		if b.Since.Value == nil {
			return nil, invalid("since", "must be an RFC 3339 instant")
		}
		at := b.Since.Value.UTC()
		par.Since = &at
		detail["since"] = at.Format(time.RFC3339)
	}
	var out adminapi.EventDestination
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cursorSeq, err := q.RewindEventDestination(ctx, par)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		row, err := q.GetEventDestination(ctx, par.ID)
		if err != nil {
			return err
		}
		detail["name"], detail["cursor_after"] = row.Name, cursorSeq
		if _, ok := detail["cursor"]; !ok {
			detail["cursor"] = cursorSeq
		}
		out = destinationOut(row)
		return s.recordDestination(ctx, tx, tid, "rewind", par.ID, detail)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListEventDeadLetters lists the events a destination could not be given, by sequence number.
func (s *server) ListEventDeadLetters(ctx context.Context, in adminapi.ListEventDeadLettersInput) (*adminapi.EventDeadLetterList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	var after int64
	if in.Cursor != "" {
		c, err := decodeFullCursor(in.Cursor, "seq")
		if err != nil || c.I != did.String() {
			return nil, invalid("cursor", "is not a cursor of this collection and sort")
		}
		if after, err = strconv.ParseInt(c.N, 10, 64); err != nil || after < 0 {
			return nil, invalid("cursor", "is not a cursor of this collection and sort")
		}
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListEventDeadLettersRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if _, err := q.GetEventDestination(ctx, did.String()); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		rows, err = q.ListEventDeadLetters(ctx, queries.ListEventDeadLettersParams{DestinationID: did.String(), After: after, Max: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.EventDeadLetterList{Items: []adminapi.EventDeadLetter{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeFullCursor(cursor{S: "seq", N: strconv.FormatInt(rows[limit-1].Seq, 10), I: did.String()})
			break
		}
		l := adminapi.EventDeadLetter{
			Seq: int(r.Seq), EventID: r.EventID, Type: adminapi.EventType(r.Type), State: adminapi.EventDeadLetterState(r.State), Attempts: int(r.Attempts),
			Reason: adminapi.DeliveryOutcome(r.Reason), ParkedAt: r.ParkedAt.UTC(), ReplayRequestedAt: utcPtr(r.ReplayRequestedAt),
		}
		if r.Status != nil {
			l.Status = int(*r.Status)
		}
		out.Items = append(out.Items, l)
	}
	return out, nil
}

// ReplayEventDeadLetters asks the worker to send dead letters again: the chosen sequence numbers or,
// with none, every dead letter of the destination. Letters already in replay are not counted.
func (s *server) ReplayEventDeadLetters(ctx context.Context, in adminapi.ReplayEventDeadLettersInput) (*adminapi.EventDeadLetterReplayed, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	did, err := tenantID(in.Destination)
	if err != nil {
		return nil, err
	}
	seqs := make([]int64, 0, len(in.Body.Seqs))
	for _, n := range in.Body.Seqs {
		seqs = append(seqs, int64(n))
	}
	var n int64
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		row, err := q.GetEventDestination(ctx, did.String())
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if n, err = q.ReplayEventDeadLetters(ctx, queries.ReplayEventDeadLettersParams{DestinationID: row.ID, Seqs: seqs}); err != nil {
			return err
		}
		return s.recordDestination(ctx, tx, tid, "replay", row.ID, map[string]any{"name": row.Name, "count": n})
	})
	if err != nil {
		return nil, err
	}
	return &adminapi.EventDeadLetterReplayed{Replayed: int(n)}, nil
}
