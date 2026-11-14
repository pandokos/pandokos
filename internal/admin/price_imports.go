package admin

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// importOut is the representation of an import row; the report is added by the caller.
func importOut(r queries.PandokosPriceImport) adminapi.PriceImport {
	created := r.CreatedAt.UTC()
	return adminapi.PriceImport{
		ID: r.ID, Source: adminapi.PriceImportSource(r.Source), SourceRef: r.SourceRef, DatasetFormat: r.DatasetFormat, Digest: r.Digest,
		Outcome: r.Outcome, ErrorClass: r.ErrorClass, ActorType: r.ActorType, ModelsSeen: int(r.ModelsSeen), VersionsCreated: int(r.VersionsCreated),
		Unchanged: int(r.Unchanged), Overridden: int(r.Overridden), Unmatched: int(r.Unmatched), Skipped: int(r.Skipped),
		StartedAt: r.StartedAt.UTC(), CreatedAt: &created,
	}
}

// ListPriceImports lists the imports, newest first, without their reports.
func (s *server) ListPriceImports(ctx context.Context, in adminapi.ListPriceImportsInput) (*adminapi.PriceImportList, error) {
	after, _, id, err := decodeCursor(in.Cursor, "id")
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListPriceImportsRow
	err = s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		rows, err = q.ListPriceImports(ctx, queries.ListPriceImportsParams{After: after, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.PriceImportList{Items: []adminapi.PriceImport{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor("id", "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, importOut(queries.PandokosPriceImport{
			ID: r.ID, StartedAt: r.StartedAt, CreatedAt: r.CreatedAt, Source: r.Source, SourceRef: r.SourceRef, DatasetFormat: r.DatasetFormat,
			Digest: r.Digest, Outcome: r.Outcome, ErrorClass: r.ErrorClass, ActorType: r.ActorType, ActorID: r.ActorID, ModelsSeen: r.ModelsSeen,
			VersionsCreated: r.VersionsCreated, Unchanged: r.Unchanged, Overridden: r.Overridden, Unmatched: r.Unmatched, Skipped: r.Skipped,
		}))
	}
	return out, nil
}

// storedReport is the report of an import as the import row keeps it.
type storedReport struct {
	Items []struct {
		Provider     string `json:"provider"`
		Model        string `json:"model"`
		DatasetModel string `json:"dataset_model"`
		Outcome      string `json:"outcome"`
		Issues       []struct {
			Reason string `json:"reason"`
			Detail string `json:"detail"`
		} `json:"issues"`
	} `json:"items"`
	Truncated int `json:"truncated"`
}

// GetPriceImport reads an import with its report.
func (s *server) GetPriceImport(ctx context.Context, in adminapi.GetPriceImportInput) (*adminapi.PriceImport, error) {
	var row queries.PandokosPriceImport
	err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		row, err = q.GetPriceImport(ctx, in.PriceImport)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	out := importOut(row)
	var rep storedReport
	if err := json.Unmarshal([]byte(row.Report), &rep); err != nil {
		return nil, errors.New("admin: the report of an import cannot be read")
	}
	out.Truncated = rep.Truncated
	for _, it := range rep.Items {
		item := adminapi.PriceImportItem{Provider: it.Provider, Model: it.Model, DatasetModel: it.DatasetModel, Outcome: it.Outcome}
		for _, is := range it.Issues {
			item.Issues = append(item.Issues, adminapi.PriceImportIssue{Reason: is.Reason, Detail: is.Detail})
		}
		out.Items = append(out.Items, item)
	}
	return &out, nil
}

// RunPriceImport prices the models of the catalogue from the embedded snapshot or from the
// download the configuration names. The download happens before any transaction is opened; the
// import itself is one platform transaction (catalog.Import).
func (s *server) RunPriceImport(ctx context.Context, in adminapi.RunPriceImportInput) (*adminapi.PriceImport, error) {
	b := in.Body
	p := principalOf(ctx)
	st := stateOf(ctx)
	started := time.Now()
	var src catalog.Source
	switch b.Source {
	case adminapi.PriceImportRunSourceEmbedded:
		src = catalog.Source{Kind: catalog.SourceEmbedded, Data: s.snapshot}
	case adminapi.PriceImportRunSourceRemote:
		if !s.refresh.Enabled {
			return nil, invalid("source", "remote is not available: the download of prices is not enabled")
		}
		data, err := s.refresh.Fetch(ctx, s.refresh.URL)
		if err != nil {
			var fe *catalog.FetchError
			if !errors.As(err, &fe) {
				fe = &catalog.FetchError{Class: "unreachable"}
			}
			s.log.Warn("price import failed", "source", catalog.SourceRemote, "error_class", fe.Class, "duration_ms", time.Since(started).Milliseconds())
			return nil, fe
		}
		src = catalog.Source{Kind: catalog.SourceRemote, Ref: s.refresh.URL, Data: data}
	default:
		return nil, invalid("source", "must be embedded or remote")
	}
	rep, err := catalog.Import(ctx, s.db, src, catalog.ImportOptions{Actor: audit.ActorOf(p), DryRun: b.DryRun, Now: s.now, RequestID: st.id})
	if err != nil {
		s.log.Warn("price import failed", "source", src.Kind, "error_class", catalog.ErrorClass(err), "duration_ms", time.Since(started).Milliseconds())
		return nil, err
	}
	out := adminapi.PriceImport{
		Source: adminapi.PriceImportSource(src.Kind), DatasetFormat: "genai-prices-v2", Digest: rep.Digest, Outcome: "success",
		ModelsSeen: rep.ModelsSeen, VersionsCreated: rep.Created, Unchanged: rep.Unchanged, Overridden: rep.Overridden,
		Unmatched: rep.Unmatched, Skipped: rep.Skipped, DryRun: b.DryRun, StartedAt: s.now().UTC(),
	}
	if !b.DryRun {
		var row queries.PandokosPriceImport
		err := s.inPlatform(ctx, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
			var err error
			row, err = q.GetPriceImport(ctx, rep.ImportID)
			return err
		})
		if err != nil {
			return nil, err
		}
		out = importOut(row)
		s.log.Info("prices imported", "source", src.Kind, "import_id", rep.ImportID, "digest", rep.Digest, "created", rep.Created,
			"unchanged", rep.Unchanged, "overridden", rep.Overridden, "unmatched", rep.Unmatched, "skipped", rep.Skipped, "duration_ms", time.Since(started).Milliseconds())
	}
	// The unmatched models come last, as in the stored report.
	items := append([]catalog.ReportItem(nil), rep.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Outcome == catalog.OutcomeUnmatched && items[j].Outcome != catalog.OutcomeUnmatched
	})
	for _, it := range items {
		item := adminapi.PriceImportItem{Provider: it.Provider, Model: it.Model, DatasetModel: it.DatasetModel, Outcome: it.Outcome}
		for _, is := range it.Issues {
			item.Issues = append(item.Issues, adminapi.PriceImportIssue{Reason: is.Reason, Detail: is.Detail})
		}
		out.Items = append(out.Items, item)
	}
	return &out, nil
}
