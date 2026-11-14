package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ErrDatasetInvalid is returned by Import when the data is not a price dataset this version can
// read. It never quotes the data.
var ErrDatasetInvalid = errors.New("catalog: the price dataset is not valid")

// Names of the host actors of the jobs and the command (audit actor name).
const (
	ActorImportPrices = "import-prices"
	ActorAtStart      = "price-import-at-start"
	ActorRefresh      = "price-refresh"
)

// Kinds of a Source.
const (
	SourceEmbedded = "embedded"
	SourceFile     = "file"
	SourceRemote   = "remote"
)

// Outcomes of a model in a Report.
const (
	OutcomeCreated    = "created"
	OutcomeUnchanged  = "unchanged"
	OutcomeOverridden = "overridden"
	OutcomeUnmatched  = "unmatched"
	OutcomeSkipped    = "skipped"
)

// ReasonMalformedPrices is the issue of a model whose prices the dataset states in a shape the
// importer cannot read: the model is skipped (no version, the price in force stays) and the
// import goes on.
const ReasonMalformedPrices = "malformed_prices"

// maxStoredReport bounds the report kept in the import row (the column holds 64 KiB).
const maxStoredReport = 60000

// Source is a dataset and where it came from.
type Source struct {
	Kind string // embedded, file, remote
	Ref  string // file path or URL as given by the operator; "" for embedded
	Data []byte
}

// ImportOptions configure Import.
type ImportOptions struct {
	Actor     audit.Actor // host for the CLI, start and refresh; the platform principal for the API
	DryRun    bool
	Now       func() time.Time
	RequestID string
}

// ReportItem is what an import did with one catalogue model.
type ReportItem struct {
	Provider, Model, DatasetModel, Outcome string
	Issues                                 []genaiprices.Issue
}

// Report is the result of an import.
type Report struct {
	ImportID                                                       string
	Digest                                                         string
	ModelsSeen, Created, Unchanged, Overridden, Unmatched, Skipped int
	// Items lists the models that were not unchanged or that had issues.
	Items []ReportItem
	// DatasetIssues are problems of the dataset as a whole (a pattern RE2 refuses).
	DatasetIssues []genaiprices.Issue
}

// pending is a version the plan will write.
type pending struct {
	modelID, provider, model string
	version                  pricing.Version
	previousID               string
}

// ErrorClass names the class of a failure of an import or a download for the logs and the
// failure rows: dataset_invalid, a FetchError class, or internal.
func ErrorClass(err error) string {
	var fe *FetchError
	switch {
	case errors.Is(err, ErrDatasetInvalid):
		return "dataset_invalid"
	case errors.As(err, &fe):
		return fe.Class
	default:
		return "internal"
	}
}

func jobActor(a audit.Actor) bool {
	return a.Type == audit.ActorHost && (a.Name == ActorAtStart || a.Name == ActorRefresh)
}

// sourceRef is what is recorded of where a dataset came from: a file's base name, a URL's host.
// Never a path, a query or user information.
func sourceRef(s Source) string {
	var ref string
	switch s.Kind {
	case SourceFile:
		ref = filepath.Base(strings.ReplaceAll(s.Ref, "\\", "/"))
		if ref == "." || ref == "/" {
			ref = ""
		}
	case SourceRemote:
		if u, err := url.Parse(s.Ref); err == nil {
			ref = u.Host
		}
	}
	if utf8.RuneCountInString(ref) > 255 {
		ref = string([]rune(ref)[:255])
	}
	return ref
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Import reads a dataset and prices the models of the catalogue with it (section 5.6). The
// dataset is parsed first; then one platform transaction, serialized with other imports by an
// advisory lock, compares every candidate with the version in force, writes the import row, the
// new versions with their rates and the audit rows. A dry run writes nothing. A dataset that
// cannot be read is recorded as a failed run and returns ErrDatasetInvalid.
func Import(ctx context.Context, db *store.DB, src Source, o ImportOptions) (Report, error) {
	rep, _, err := runImport(ctx, db, src, o, nil)
	return rep, err
}

// importCheck runs inside the import transaction, after the lock, and may decide that the
// import is not needed: it returns the reason.
type importCheck func(ctx context.Context, q *queries.Queries) (skip string, err error)

func runImport(ctx context.Context, db *store.DB, src Source, o ImportOptions, check importCheck) (Report, string, error) {
	switch src.Kind {
	case SourceEmbedded, SourceFile, SourceRemote:
	default:
		return Report{}, "", errors.New("catalog: unknown source kind")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	digest := digestOf(src.Data)

	ds, err := genaiprices.Parse(src.Data)
	if err != nil {
		if !o.DryRun {
			if ferr := recordFailure(ctx, db, src, o, started, digest, "dataset_invalid"); ferr != nil {
				return Report{Digest: digest}, "", ferr
			}
		}
		return Report{Digest: digest}, "", ErrDatasetInvalid
	}

	var rep Report
	skip := ""
	run := func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if !o.DryRun {
			if err := q.LockPriceImports(ctx); err != nil {
				return err
			}
			if check != nil {
				reason, err := check(ctx, q)
				if err != nil {
					return err
				}
				if reason != "" {
					skip = reason
					return nil
				}
			}
		}
		plan, r, err := planImport(ctx, tx, q, ds, now())
		if err != nil {
			return err
		}
		r.Digest = digest
		rep = r
		if o.DryRun {
			return nil
		}
		return writeImport(ctx, tx, q, src, o, started, &rep, plan)
	}
	if runErr := db.Platform(ctx, run); runErr != nil {
		return Report{Digest: digest}, "", fmt.Errorf("catalog: import: %w", runErr)
	}
	return rep, skip, nil
}

// recordFailure writes a failed run in its own platform transaction.
func recordFailure(ctx context.Context, db *store.DB, src Source, o ImportOptions, started time.Time, digest, class string) error {
	err := db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		row, err := queries.New(tx).CreatePriceImport(ctx, queries.CreatePriceImportParams{
			StartedAt: started, Source: src.Kind, SourceRef: sourceRef(src), DatasetFormat: "genai-prices-v2", Digest: digest,
			Outcome: "failure", ErrorClass: class, ActorType: string(o.Actor.Type), ActorID: o.Actor.ID, Report: "{}",
		})
		if err != nil {
			return err
		}
		if jobActor(o.Actor) {
			return nil
		}
		return audit.RecordPlatform(ctx, tx, audit.Event{Actor: o.Actor, Action: "price_import.run", ResourceType: "price_import", ResourceID: row.ID,
			Outcome: "failure", RequestID: o.RequestID,
			Detail: map[string]any{"source": src.Kind, "source_ref": sourceRef(src), "digest": digest, "error_class": class, "dry_run": false}})
	})
	if err != nil {
		return fmt.Errorf("catalog: record a failed import: %w", err)
	}
	return nil
}

// planImport compares every candidate with the version in force and computes the whole plan
// and the report; it writes nothing.
func planImport(ctx context.Context, tx store.Tx, q *queries.Queries, ds *genaiprices.Dataset, at time.Time) ([]pending, Report, error) {
	provs, err := q.LoadProviders(ctx)
	if err != nil {
		return nil, Report{}, err
	}
	models, err := q.LoadModels(ctx)
	if err != nil {
		return nil, Report{}, err
	}
	type prov struct{ name, dataset string }
	byID := make(map[string]prov, len(provs))
	for _, p := range provs {
		byID[p.ID] = prov{p.Name, p.DatasetProvider}
	}
	sort.SliceStable(models, func(i, j int) bool {
		a, b := byID[models[i].ProviderID].name, byID[models[j].ProviderID].name
		if a != b {
			return a < b
		}
		return models[i].Name < models[j].Name
	})
	today := time.Date(at.UTC().Year(), at.UTC().Month(), at.UTC().Day(), 0, 0, 0, 0, time.UTC)

	var rep Report
	var plan []pending
	rep.DatasetIssues = ds.Issues()
	for _, m := range models {
		p, ok := byID[m.ProviderID]
		if !ok || p.dataset == "" {
			continue
		}
		rep.ModelsSeen++
		item := ReportItem{Provider: p.name, Model: m.Name}
		dm, found := ds.Find(p.dataset, m.UpstreamID)
		switch {
		case !found:
			item.Outcome = OutcomeUnmatched
			rep.Unmatched++
		case m.PriceOverride:
			item.DatasetModel = dm.ID
			item.Outcome = OutcomeOverridden
			rep.Overridden++
		default:
			item.DatasetModel = dm.ID
			cand, issues, err := genaiprices.Extract(dm, today)
			item.Issues = issues
			switch {
			case errors.Is(err, genaiprices.ErrSkipped):
				item.Outcome = OutcomeSkipped
				item.Issues = withoutIgnoredCategories(item.Issues)
				rep.Skipped++
			case errors.Is(err, genaiprices.ErrMalformedPrices):
				item.Issues = append(item.Issues, genaiprices.Issue{Reason: ReasonMalformedPrices})
				item.Outcome = OutcomeSkipped
				rep.Skipped++
			case err != nil:
				return nil, Report{}, errors.New("catalog: extract a price failed")
			default:
				cur, has, err := PriceAt(ctx, tx, m.ID, at)
				if err != nil {
					return nil, Report{}, err
				}
				if has && cur.Currency == cand.Currency && cur.SameRates(cand) {
					item.Outcome = OutcomeUnchanged
					rep.Unchanged++
					break
				}
				item.Outcome = OutcomeCreated
				rep.Created++
				pd := pending{modelID: m.ID, provider: p.name, model: m.Name, version: cand}
				if has {
					pd.previousID = cur.ID
				}
				plan = append(plan, pd)
			}
		}
		if item.Outcome != OutcomeUnchanged || len(item.Issues) > 0 {
			rep.Items = append(rep.Items, item)
		}
	}
	return plan, rep, nil
}

// withoutIgnoredCategories drops the category_ignored issues of a model that was skipped because
// no category it states is supported: "no_supported_price" says it all.
func withoutIgnoredCategories(issues []genaiprices.Issue) []genaiprices.Issue {
	noSupported := false
	for _, is := range issues {
		noSupported = noSupported || is.Reason == genaiprices.ReasonNoSupportedPrice
	}
	if !noSupported {
		return issues
	}
	out := issues[:0:0]
	for _, is := range issues {
		if is.Reason != genaiprices.ReasonCategoryIgnored {
			out = append(out, is)
		}
	}
	return out
}

type storedIssue struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

type storedItem struct {
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	DatasetModel string        `json:"dataset_model,omitempty"`
	Outcome      string        `json:"outcome"`
	Issues       []storedIssue `json:"issues,omitempty"`
}

// storedReport renders the report for the import row: the items that changed or have issues
// first, the unmatched ones after, cut at maxStoredReport with a count of what was left out.
func storedReport(rep Report) (string, error) {
	items := append([]ReportItem(nil), rep.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Outcome == OutcomeUnmatched && items[j].Outcome != OutcomeUnmatched
	})
	type doc struct {
		Items         []storedItem  `json:"items"`
		DatasetIssues []storedIssue `json:"dataset_issues,omitempty"`
		Truncated     int           `json:"truncated"`
	}
	d := doc{Items: []storedItem{}}
	for i, is := range rep.DatasetIssues {
		if i >= 100 {
			break
		}
		d.DatasetIssues = append(d.DatasetIssues, storedIssue{Reason: is.Reason, Detail: is.Detail})
	}
	size := 100 + len(d.DatasetIssues)*200
	for i, it := range items {
		si := storedItem{Provider: it.Provider, Model: it.Model, DatasetModel: it.DatasetModel, Outcome: it.Outcome}
		for _, is := range it.Issues {
			si.Issues = append(si.Issues, storedIssue{Reason: is.Reason, Detail: is.Detail})
		}
		b, err := json.Marshal(si)
		if err != nil {
			return "", errors.New("catalog: encode the report")
		}
		if size+len(b)+1 > maxStoredReport {
			d.Truncated = len(items) - i
			break
		}
		size += len(b) + 1
		d.Items = append(d.Items, si)
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", errors.New("catalog: encode the report")
	}
	return string(b), nil
}

// writeImport writes the import row, then the versions with their rates and audit rows, then
// the audit row of the run.
func writeImport(ctx context.Context, tx store.Tx, q *queries.Queries, src Source, o ImportOptions, started time.Time, rep *Report, plan []pending) error {
	report, err := storedReport(*rep)
	if err != nil {
		return err
	}
	ref := sourceRef(src)
	row, err := q.CreatePriceImport(ctx, queries.CreatePriceImportParams{
		StartedAt: started, Source: src.Kind, SourceRef: ref, DatasetFormat: "genai-prices-v2", Digest: rep.Digest, Outcome: "success",
		ActorType: string(o.Actor.Type), ActorID: o.Actor.ID,
		ModelsSeen: int32(rep.ModelsSeen), VersionsCreated: int32(rep.Created), Unchanged: int32(rep.Unchanged), //nolint:gosec // counts of a bounded dataset
		Overridden: int32(rep.Overridden), Unmatched: int32(rep.Unmatched), Skipped: int32(rep.Skipped), Report: report,
	})
	if err != nil {
		return err
	}
	rep.ImportID = row.ID
	for _, pd := range plan {
		v := pd.version
		importID := row.ID
		created, err := q.CreatePriceVersion(ctx, queries.CreatePriceVersionParams{ModelID: pd.modelID, Currency: v.Currency, Source: "import", ImportID: &importID})
		if err != nil {
			return err
		}
		cats := make([]string, len(v.Rates))
		ths := make([]int64, len(v.Rates))
		rates := make([]string, len(v.Rates))
		detailRates := make([]map[string]any, len(v.Rates))
		for i, r := range v.Rates {
			cats[i], ths[i], rates[i] = string(r.Category), r.Threshold, r.PerMillion.String()
			detailRates[i] = map[string]any{"category": cats[i], "threshold": ths[i], "rate": rates[i]}
		}
		if _, err := q.CreatePriceRates(ctx, queries.CreatePriceRatesParams{PriceVersionID: created.ID, Categories: cats, Thresholds: ths, Rates: rates}); err != nil {
			return err
		}
		detail := map[string]any{"provider": pd.provider, "model": pd.model, "source": "import", "currency": v.Currency,
			"valid_from": created.ValidFrom.UTC().Format(time.RFC3339Nano), "import_id": row.ID, "rates": detailRates}
		if pd.previousID != "" {
			detail["previous_version"] = pd.previousID
		}
		if err := audit.RecordPlatform(ctx, tx, audit.Event{Actor: o.Actor, Action: "price.create", ResourceType: "price_version", ResourceID: created.ID,
			Outcome: "success", RequestID: o.RequestID, Detail: detail}); err != nil {
			return err
		}
	}
	if rep.Created == 0 && jobActor(o.Actor) {
		return nil
	}
	return audit.RecordPlatform(ctx, tx, audit.Event{Actor: o.Actor, Action: "price_import.run", ResourceType: "price_import", ResourceID: row.ID,
		Outcome: "success", RequestID: o.RequestID,
		Detail: map[string]any{"source": src.Kind, "source_ref": ref, "digest": rep.Digest, "models_seen": rep.ModelsSeen, "created": rep.Created,
			"unchanged": rep.Unchanged, "overridden": rep.Overridden, "unmatched": rep.Unmatched, "skipped": rep.Skipped, "dry_run": false}})
}

// ImportAtStart imports the embedded snapshot when serve starts (D-15): unless a successful
// import with the same digest exists, or a successful file or remote import was recorded after
// the snapshot's retrieval date (a newer dataset must not be replaced by an older release). A
// failure is logged as a warning and never returned. Instances that start together serialize on
// the import lock and the second one skips.
func ImportAtStart(ctx context.Context, db *store.DB, logger *slog.Logger) {
	started := time.Now()
	data := genaiprices.Snapshot()
	_, retrieved, _ := genaiprices.SnapshotInfo()
	digest := digestOf(data)
	check := func(ctx context.Context, q *queries.Queries) (string, error) {
		h, err := q.PriceImportHistory(ctx, queries.PriceImportHistoryParams{Digest: digest, Since: retrieved})
		switch {
		case err != nil:
			return "", err
		case h.NewerImport:
			return "newer_import", nil
		case h.DigestSeen:
			return "digest_seen", nil
		}
		return "", nil
	}
	skipped := func(reason string) {
		logger.Info("price import skipped", "source", SourceEmbedded, "reason", reason)
	}
	failed := func(err error) {
		logger.Warn("price import failed", "source", SourceEmbedded, "error_class", ErrorClass(err), "duration_ms", time.Since(started).Milliseconds())
	}

	// Cheap test first, so that the usual start parses nothing; the same test runs again under
	// the lock, where it is the one that counts.
	var reason string
	if err := db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		reason, err = check(ctx, queries.New(tx))
		return err
	}); err != nil {
		failed(err)
		return
	}
	if reason != "" {
		skipped(reason)
		return
	}
	actor := audit.Actor{Type: audit.ActorHost, Name: ActorAtStart}
	rep, reason, err := runImport(ctx, db, Source{Kind: SourceEmbedded, Data: data}, ImportOptions{Actor: actor}, check)
	switch {
	case err != nil:
		failed(err)
	case reason != "":
		skipped(reason)
	default:
		logImported(logger, SourceEmbedded, rep, started)
	}
}

func logImported(logger *slog.Logger, source string, rep Report, started time.Time) {
	logger.Info("prices imported", "source", source, "import_id", rep.ImportID, "digest", rep.Digest, "created", rep.Created, "unchanged", rep.Unchanged,
		"overridden", rep.Overridden, "unmatched", rep.Unmatched, "skipped", rep.Skipped, "duration_ms", time.Since(started).Milliseconds())
}
