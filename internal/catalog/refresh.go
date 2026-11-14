package catalog

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// Refresher downloads the price dataset on a schedule and imports it (D-16). The first run is
// FirstDelay after Run starts, the others Interval apart; a run is skipped when the last
// successful remote import, by this or another instance, is younger than half the interval. A
// failure is logged and recorded and not retried before the next interval.
type Refresher struct {
	DB         *store.DB // the worker pool
	Logger     *slog.Logger
	URL        string        // https URL, from the configuration only
	Interval   time.Duration // pricing.refresh.interval
	FirstDelay time.Duration // default one minute
	// Fetch downloads the dataset; the default is the package's Fetch with its defaults. Tests
	// and the administration plane inject their own.
	Fetch func(ctx context.Context, url string) ([]byte, error)
	Now   func() time.Time
}

// Run blocks until ctx ends.
func (r *Refresher) Run(ctx context.Context) {
	delay := r.FirstDelay
	if delay <= 0 {
		delay = time.Minute
	}
	for {
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		r.once(ctx)
		delay = r.Interval
	}
}

func (r *Refresher) once(ctx context.Context) {
	started := time.Now()
	defer func() {
		if p := recover(); p != nil {
			r.Logger.Warn("price import failed", "source", SourceRemote, "error_class", "internal", "duration_ms", time.Since(started).Milliseconds())
		}
	}()
	now := r.Now
	if now == nil {
		now = time.Now
	}
	fail := func(err error) {
		r.Logger.Warn("price import failed", "source", SourceRemote, "error_class", ErrorClass(err), "duration_ms", time.Since(started).Milliseconds())
	}

	var last time.Time
	if err := r.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		h, err := queries.New(tx).PriceImportHistory(ctx, queries.PriceImportHistoryParams{Digest: "", Since: time.Unix(0, 0).UTC()})
		last = h.LastRemote
		return err
	}); err != nil {
		fail(err)
		return
	}
	if now().Sub(last) < r.Interval/2 {
		r.Logger.Info("price import skipped", "source", SourceRemote, "reason", "recent_remote")
		return
	}

	actor := audit.Actor{Type: audit.ActorHost, Name: ActorRefresh}
	src := Source{Kind: SourceRemote, Ref: r.URL}
	fetch := r.Fetch
	if fetch == nil {
		fetch = func(ctx context.Context, u string) ([]byte, error) { return Fetch(ctx, u, FetchOptions{}) }
	}
	// The download happens before any transaction is opened.
	data, err := fetch(ctx, r.URL)
	if err != nil {
		class := ErrorClass(err)
		if rerr := recordFailure(ctx, r.DB, src, ImportOptions{Actor: actor}, started, "", class); rerr != nil {
			r.Logger.Warn("price import failure not recorded", "source", SourceRemote)
		}
		fail(err)
		return
	}
	src.Data = data
	rep, err := Import(ctx, r.DB, src, ImportOptions{Actor: actor, Now: now})
	if err != nil {
		fail(err)
		return
	}
	logImported(r.Logger, SourceRemote, rep, started)
}
