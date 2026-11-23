package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// exportPage is the number of rows an export reads in one transaction (section 5.5).
const exportPage = 1000

// The kinds of export, the values of the attribute of the metric M46.
const (
	exportUsage         = "usage"
	exportAudit         = "audit"
	exportPlatformAudit = "platform_audit"
)

// exportPageFn reads the next page of an export into buf (with the header of a CSV when first) and
// says whether another page may follow.
type exportPageFn func(ctx context.Context, buf *bytes.Buffer, first bool) (more bool, err error)

// flusher is what the writer of a streamed answer offers: send what is buffered.
type flusher interface{ Flush() error }

// stream builds the streamed answer of an export from a function that reads one page at a time:
// each page is read completely before a byte of it is written, so that a failure to read leaves
// the answer as it was (a problem before the first page, an aborted transfer after it).
func (s *server) stream(kind, contentType, filename string, st *reqState, page exportPageFn) *adminapi.Stream {
	return &adminapi.Stream{ContentType: contentType, Filename: filename, Write: func(ctx context.Context, w io.Writer) error {
		started := time.Now()
		wrote := false
		fail := func(err error) error {
			s.stats.exportDone(kind, false)
			if wrote {
				s.log.Warn("export aborted", "request_id", st.id, "error_class", exportErrorClass(err), "duration_ms", time.Since(started).Milliseconds())
			}
			return err
		}
		var buf bytes.Buffer
		for first := true; ; first = false {
			buf.Reset()
			more, err := page(ctx, &buf, first)
			if err != nil {
				return fail(err)
			}
			if buf.Len() > 0 || first {
				wrote = true
				if _, err := w.Write(buf.Bytes()); err != nil {
					return fail(err)
				}
				if f, ok := w.(flusher); ok {
					if err := f.Flush(); err != nil {
						return fail(err)
					}
				}
			}
			if !more {
				break
			}
		}
		s.stats.exportDone(kind, true)
		return nil
	}}
}

// exportErrorClass names a failure without its text, which could hold data.
func exportErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case store.IsUnavailable(err):
		return "unavailable"
	case store.SQLState(err) != "":
		return "database"
	default:
		return "write"
	}
}

// fileStamp writes an instant for a file name.
func fileStamp(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// ExportUsageRecords streams the usage records of a tenant in a window of at most 31 days, oldest
// first, as NDJSON (the items of listUsageRecords) or CSV.
func (s *server) ExportUsageRecords(ctx context.Context, in adminapi.ExportUsageRecordsInput) (*adminapi.Stream, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	format := in.Format
	var fromErr, toErr, provErr, formatErr *adminapi.FieldError
	if format == "" {
		format = adminapi.ExportFormatNdjson
	}
	if !format.Valid() {
		formatErr = &adminapi.FieldError{Field: "format", Message: "must be one of ndjson, csv"}
	}
	to := time.Now().UTC()
	if in.To != "" {
		t, perr := time.Parse(time.RFC3339Nano, in.To)
		if perr != nil {
			toErr = &adminapi.FieldError{Field: "to", Message: "must be an RFC 3339 instant with a zone"}
		} else {
			to = t.UTC()
		}
	}
	from := to.Add(-defaultUsageWindow)
	if in.From != "" {
		t, perr := time.Parse(time.RFC3339Nano, in.From)
		if perr != nil {
			fromErr = &adminapi.FieldError{Field: "from", Message: "must be an RFC 3339 instant with a zone"}
		} else {
			from = t.UTC()
		}
	}
	if in.Provider != "" && catalog.ValidateProviderName(in.Provider) != nil {
		provErr = &adminapi.FieldError{Field: "provider", Message: "is not a provider name"}
	}
	if err := fieldsError(fromErr, toErr, provErr, formatErr); err != nil {
		return nil, err
	}
	switch {
	case !from.Before(to):
		fromErr = &adminapi.FieldError{Field: "from", Message: "must be before to"}
	case to.Sub(from) > maxUsageWindow:
		fromErr = &adminapi.FieldError{Field: "from", Message: "the window is at most 31 days"}
	}
	if err := fieldsError(fromErr); err != nil {
		return nil, err
	}

	var (
		after     bool
		afterAt   time.Time
		afterKey  string
		header    []string
		csvFields []int
	)
	if format == adminapi.ExportFormatCsv {
		header, csvFields = usageCSVColumns()
	}
	page := func(ctx context.Context, buf *bytes.Buffer, first bool) (bool, error) {
		var rows []queries.ExportUsageRecordsRow
		err := s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			var err error
			rows, err = queries.New(tx).ExportUsageRecords(ctx, queries.ExportUsageRecordsParams{
				FromAt: from, ToAt: to, Provider: in.Provider, After: after, AfterStartedAt: afterAt, AfterID: afterID(after, afterKey), PageSize: exportPage,
			})
			return err
		})
		if err != nil {
			return false, err
		}
		var cw *csv.Writer
		if format == adminapi.ExportFormatCsv {
			cw = csv.NewWriter(buf)
			if first {
				_ = cw.Write(header)
			}
		}
		for _, r := range rows {
			item := usageRecordOut(queries.ListUsageRecordsRow(r))
			if cw != nil {
				_ = cw.Write(usageCSVRow(item, csvFields))
				continue
			}
			b, err := json.Marshal(item)
			if err != nil {
				return false, err
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if cw != nil {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return false, err
			}
		}
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			after, afterAt, afterKey = true, last.StartedAt, last.ID
		}
		return len(rows) == exportPage, nil
	}
	ext, contentType := "ndjson", "application/x-ndjson"
	if format == adminapi.ExportFormatCsv {
		ext, contentType = "csv", "text/csv; charset=utf-8"
	}
	name := "usage-" + in.Tenant + "-" + fileStamp(from) + "-" + fileStamp(to) + "." + ext
	return s.stream(exportUsage, contentType, name, stateOf(ctx), page), nil
}

// usageCSVColumns returns the header of the CSV of usage records: the members of the UsageRecord
// schema in the order of its definition, and the index of each field.
func usageCSVColumns() ([]string, []int) {
	typ := reflect.TypeOf(adminapi.UsageRecord{})
	var names []string
	var idx []int
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
		idx = append(idx, i)
	}
	return names, idx
}

// usageCSVRow writes a record as the cells of a CSV line: text, numbers and times as text, arrays
// as their JSON text in one cell, a cost as its decimal string, empty when there is none.
func usageCSVRow(r adminapi.UsageRecord, idx []int) []string {
	v := reflect.ValueOf(r)
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		f := v.Field(i)
		switch x := f.Interface().(type) {
		case string:
			out = append(out, csvSafe(x))
		case int:
			out = append(out, strconv.Itoa(x))
		case bool:
			out = append(out, strconv.FormatBool(x))
		case time.Time:
			out = append(out, x.UTC().Format(time.RFC3339Nano))
		case adminapi.Nullable[string]:
			if x.Set && !x.Null {
				out = append(out, x.Value)
			} else {
				out = append(out, "")
			}
		default:
			b, err := json.Marshal(x)
			if err != nil {
				b = nil
			}
			out = append(out, string(b))
		}
	}
	return out
}

// csvSafe keeps a spreadsheet from reading a text cell as a formula: a cell that starts with =, +,
// -, @, a tab or a carriage return is written with a leading apostrophe (OWASP's advice).
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// auditRange reads from_seq and to_seq against the head read at the start of an export: from
// defaults to the anchor, to to the head, and a to past the head is the head.
func auditRange(in struct{ From, To int }, head *adminapi.AuditHead) (from, to int64, err error) {
	from, to = int64(head.AnchorSeq), int64(head.Seq)
	if in.From > 0 {
		from = int64(in.From)
	}
	if in.To > 0 {
		to = min(int64(in.To), to)
	}
	if in.From > 0 && in.To > 0 && in.From > in.To {
		return 0, 0, invalid("from_seq", "must not be after to_seq")
	}
	return from, to, nil
}

// auditExport streams rows of a log by sequence number, oldest first, in pages, each read in its
// own transaction, with the projection of the list (withheld price details included).
func (s *server) auditExport(kind, namePrefix string, st *reqState, from, to int64, prices bool,
	read func(ctx context.Context, after, to int64) ([]auditRow, error)) *adminapi.Stream {
	after := from - 1
	page := func(ctx context.Context, buf *bytes.Buffer, _ bool) (bool, error) {
		if after >= to {
			return false, nil
		}
		rows, err := read(ctx, after, to)
		if err != nil {
			return false, err
		}
		for _, r := range rows {
			e, err := auditEvent(r, prices)
			if err != nil {
				return false, err
			}
			b, err := json.Marshal(e)
			if err != nil {
				return false, err
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if len(rows) > 0 {
			after = rows[len(rows)-1].Seq
		}
		return len(rows) == exportPage, nil
	}
	name := namePrefix + "-" + strconv.FormatInt(from, 10) + "-" + strconv.FormatInt(to, 10) + ".ndjson"
	return s.stream(kind, "application/x-ndjson", name, st, page)
}

// ExportAuditEvents streams the audit log of a tenant from the anchor (or from_seq) to the head
// read at the start (or to_seq), oldest first.
func (s *server) ExportAuditEvents(ctx context.Context, in adminapi.ExportAuditEventsInput) (*adminapi.Stream, error) {
	head, err := s.GetAuditHead(ctx, adminapi.GetAuditHeadInput{Tenant: in.Tenant})
	if err != nil {
		return nil, err
	}
	from, to, err := auditRange(struct{ From, To int }{in.FromSeq, in.ToSeq}, head)
	if err != nil {
		return nil, err
	}
	tid, _ := tenantID(in.Tenant) // GetAuditHead checked it
	prices := canReadPrices(ctx, authz.Target{TenantID: tid})
	read := func(ctx context.Context, after, to int64) ([]auditRow, error) {
		var out []auditRow
		err := s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			rows, err := queries.New(tx).ExportAuditEvents(ctx, queries.ExportAuditEventsParams{AfterSeq: after, ToSeq: to, PageSize: exportPage})
			for _, r := range rows {
				out = append(out, auditRow{r.Seq, r.ID, r.OccurredAt, r.ActorType, r.ActorID, r.ActorName, r.KeyID, r.Action,
					r.ResourceType, r.ResourceID, r.Outcome, r.RequestID, r.Reason, r.SupportActionID, r.Detail, r.PrevHash, r.Hash})
			}
			return err
		})
		return out, err
	}
	return s.auditExport(exportAudit, "audit-"+in.Tenant, stateOf(ctx), from, to, prices, read), nil
}

// ExportPlatformAuditEvents streams the platform's audit log the same way.
func (s *server) ExportPlatformAuditEvents(ctx context.Context, in adminapi.ExportPlatformAuditEventsInput) (*adminapi.Stream, error) {
	head, err := s.GetPlatformAuditHead(ctx, adminapi.GetPlatformAuditHeadInput{})
	if err != nil {
		return nil, err
	}
	from, to, err := auditRange(struct{ From, To int }{in.FromSeq, in.ToSeq}, head)
	if err != nil {
		return nil, err
	}
	prices := canReadPrices(ctx, authz.Target{})
	read := func(ctx context.Context, after, to int64) ([]auditRow, error) {
		var out []auditRow
		err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
			rows, err := q.ExportPlatformAuditEvents(ctx, queries.ExportPlatformAuditEventsParams{AfterSeq: after, ToSeq: to, PageSize: exportPage})
			for _, r := range rows {
				out = append(out, auditRow{r.Seq, r.ID, r.OccurredAt, r.ActorType, r.ActorID, r.ActorName, r.KeyID, r.Action,
					r.ResourceType, r.ResourceID, r.Outcome, r.RequestID, r.Reason, "", r.Detail, r.PrevHash, r.Hash})
			}
			return err
		})
		return out, err
	}
	return s.auditExport(exportPlatformAudit, "platform-audit", stateOf(ctx), from, to, prices, read), nil
}
