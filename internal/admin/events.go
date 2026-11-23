package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/outbox"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The size of an export of events (section 5.10): limit is 1 to 10000, 1000 by default.
const defaultEventExport = 1000

// eventExportLimit reads limit: absent means the default.
func eventExportLimit(n int) (int, error) {
	switch {
	case n == 0:
		return defaultEventExport, nil
	case n < 1 || n > 10000:
		return 0, invalid("limit", "must be between 1 and 10000")
	}
	return n, nil
}

// ExportEvents streams the events of a tenant with a sequence number above after, oldest first, one
// CloudEvents document per line (section 5.10). A consumer keeps the sequence of the last line and
// asks again with it.
func (s *server) ExportEvents(ctx context.Context, in adminapi.ExportEventsInput) (*adminapi.Stream, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	limit, err := eventExportLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	if in.After < 0 {
		return nil, invalid("after", "must be at least 0")
	}
	types := make([]string, 0, len(in.Type))
	for _, t := range in.Type {
		if !t.Valid() {
			return nil, invalid("type", "must name types of the tenant's events")
		}
		types = append(types, string(t))
	}
	after, remaining := int64(in.After), limit
	page := func(ctx context.Context, buf *bytes.Buffer, _ bool) (bool, error) {
		size := min(exportPage, remaining)
		var rows []queries.ListOutboxEventsAfterRow
		var ref string
		err := s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			q := queries.New(tx)
			t, err := q.GetTenant(ctx, tid.String())
			if err != nil {
				return err
			}
			ref = t.ExternalReference
			rows, err = q.ListOutboxEventsAfter(ctx, queries.ListOutboxEventsAfterParams{After: after, Types: types, Max: int32(size)}) //nolint:gosec // at most 1000
			return err
		})
		if err != nil {
			return false, err
		}
		env := outbox.Envelope{Source: s.events.source, TenantID: tid, TenantRef: ref}
		for _, r := range rows {
			buf.Write(outbox.Render(outbox.Event{Seq: r.Seq, ID: r.ID, Type: r.Type, Subject: r.Subject, Time: r.OccurredAt, Data: json.RawMessage(r.Data)}, env))
			buf.WriteByte('\n')
		}
		if len(rows) > 0 {
			after = rows[len(rows)-1].Seq
		}
		remaining -= len(rows)
		return len(rows) == size && remaining > 0, nil
	}
	name := "events-" + in.Tenant + "-" + fileStamp(s.now()) + ".ndjson"
	return s.stream(exportEvent, "application/x-ndjson", name, stateOf(ctx), page), nil
}

// ExportPlatformEvents is ExportEvents over the platform's log: the deletions of tenants and the
// versions of the key-encryption key. An event that concerns a tenant carries its identifier and
// its external reference; the others carry no tenant attribute.
func (s *server) ExportPlatformEvents(ctx context.Context, in adminapi.ExportPlatformEventsInput) (*adminapi.Stream, error) {
	limit, err := eventExportLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	if in.After < 0 {
		return nil, invalid("after", "must be at least 0")
	}
	types := make([]string, 0, len(in.Type))
	for _, t := range in.Type {
		if !t.Valid() {
			return nil, invalid("type", "must name types of the platform's events")
		}
		types = append(types, string(t))
	}
	after, remaining := int64(in.After), limit
	page := func(ctx context.Context, buf *bytes.Buffer, _ bool) (bool, error) {
		size := min(exportPage, remaining)
		var rows []queries.ListPlatformOutboxEventsAfterRow
		err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			rows, err = queries.New(tx).ListPlatformOutboxEventsAfter(ctx, queries.ListPlatformOutboxEventsAfterParams{After: after, Types: types, Max: int32(size)}) //nolint:gosec // at most 1000
			return err
		})
		if err != nil {
			return false, err
		}
		for _, r := range rows {
			env := outbox.Envelope{Source: s.events.source}
			if r.TenantID != nil {
				if tid, err := tenant.ParseID(*r.TenantID); err == nil {
					env.TenantID = tid
					env.TenantRef = platformEventRef(r)
				}
			}
			buf.Write(outbox.Render(outbox.Event{Seq: r.Seq, ID: r.ID, Type: r.Type, Subject: r.Subject, Time: r.OccurredAt, Data: json.RawMessage(r.Data)}, env))
			buf.WriteByte('\n')
		}
		if len(rows) > 0 {
			after = rows[len(rows)-1].Seq
		}
		remaining -= len(rows)
		return len(rows) == size && remaining > 0, nil
	}
	return s.stream(exportPlatformEvent, "application/x-ndjson", "platform-events-"+fileStamp(s.now())+".ndjson", stateOf(ctx), page), nil
}

// platformEventRef is the external reference of the tenant of a platform event: the tenant's own
// while its row exists, else the one the event carries (the tenant is gone once it is deleted).
func platformEventRef(r queries.ListPlatformOutboxEventsAfterRow) string {
	if ref := r.ExternalReference; ref != "" {
		return ref
	}
	var d struct {
		ExternalReference string `json:"external_reference"`
	}
	if strings.Contains(r.Data, "external_reference") && json.Unmarshal([]byte(r.Data), &d) == nil {
		return d.ExternalReference
	}
	return ""
}
