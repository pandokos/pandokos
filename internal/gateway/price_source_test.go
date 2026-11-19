package gateway_test

import (
	"bufio"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// listVersion is a version of a currency, started an hour ago unless from says otherwise, with an
// input rate of in and an output rate of out per million.
func listVersion(t *testing.T, id, currency, source string, from time.Time, in, out string) pricing.Version {
	t.Helper()
	i, err := pricing.ParseDecimal(in)
	if err != nil {
		t.Fatal(err)
	}
	o, err := pricing.ParseDecimal(out)
	if err != nil {
		t.Fatal(err)
	}
	return pricing.Version{ID: id, ModelID: "synthetic-model-openai-" + "fake-basic", ValidFrom: from, Currency: currency, Source: source,
		Rates: []pricing.Rate{{Category: pricing.CategoryInput, PerMillion: i}, {Category: pricing.CategoryOutput, PerMillion: o}}}
}

// TestRecordPriceSource is T-735: the record's cost, currency and price source for a tenant price, the
// catalogue's price, a catalogue price in another currency (unknown, no_price) and an end; a version
// that starts while a request runs does not change that request.
func TestRecordPriceSource(t *testing.T) {
	hourAgo := time.Now().Add(-time.Hour)
	usd, eur := pricing.Active{Current: "USD"}, pricing.Active{Current: "EUR"}

	// run sends one request for fake-basic to a target built by setup and returns its record.
	run := func(t *testing.T, setup func(*gateway.Target)) (rec recordView) {
		t.Helper()
		h := newHarness(t, nil)
		tgt := h.targets["openai/fake-basic"]
		setup(&tgt)
		h.targets["openai/fake-basic"] = tgt
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		if c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		r := h.oneRecord(t)
		return recordView{r.CostStatus, r.Cost.Known, r.Cost.Currency, r.Cost.Source, r.Cost.VersionID, r.Cost.Reason}
	}

	t.Run("a tenant price", func(t *testing.T) {
		got := run(t, func(g *gateway.Target) {
			g.Currency = eur
			g.TenantPrices = []pricing.Version{listVersion(t, "tenant-eur", "EUR", pricing.SourceTenant, hourAgo, "1", "2")}
		})
		if got != (recordView{"known", true, "EUR", pricing.SourceTenant, "tenant-eur", ""}) {
			t.Errorf("%+v", got)
		}
	})

	t.Run("the catalogue's price", func(t *testing.T) {
		got := run(t, func(g *gateway.Target) { g.Currency = usd })
		if got.status != "known" || got.currency != "USD" || got.source != pricing.SourceCatalogue || got.version == "" {
			t.Errorf("%+v", got)
		}
	})

	t.Run("a catalogue price in another currency is no price", func(t *testing.T) {
		got := run(t, func(g *gateway.Target) { g.Currency = eur }) // the catalogue prices in USD
		if got.status != "unknown" || got.known || got.currency != "" || got.source != "" || got.version != "" || got.reason != pricing.ReasonNoPrice {
			t.Errorf("%+v, want an unknown cost for no price, without a currency, a version or a source", got)
		}
	})

	t.Run("an end gives the catalogue price back, in its currency only", func(t *testing.T) {
		own := listVersion(t, "tenant-eur", "EUR", pricing.SourceTenant, hourAgo, "1", "2")
		end := pricing.Version{ID: "tenant-end", ModelID: own.ModelID, ValidFrom: hourAgo.Add(30 * time.Minute), Currency: "EUR", Source: pricing.SourceTenant, End: true}
		got := run(t, func(g *gateway.Target) {
			g.Currency = eur
			g.Prices = append(g.Prices, listVersion(t, "catalogue-eur", "EUR", "override", hourAgo.Add(10*time.Minute), "7", "8")) // newer than the harness's USD price: the catalogue's version in force is the one in EUR
			g.TenantPrices = []pricing.Version{own, end}
		})
		if got.status != "known" || got.currency != "EUR" || got.source != pricing.SourceCatalogue || got.version != "catalogue-eur" {
			t.Errorf("with a catalogue price in EUR: %+v", got)
		}
		got = run(t, func(g *gateway.Target) {
			g.Currency = eur
			g.TenantPrices = []pricing.Version{own, end}
		})
		if got.status != "unknown" || got.reason != pricing.ReasonNoPrice || got.source != "" {
			t.Errorf("with the catalogue in USD only: %+v", got)
		}
	})

	t.Run("a version that starts while a request runs does not change that request", func(t *testing.T) {
		h := newHarness(t, nil)
		tgt := h.targets["openai/"+fakeprovider.ModelGatedStream]
		start := time.Now()
		tgt.Currency = eur
		tgt.TenantPrices = []pricing.Version{
			listVersion(t, "tenant-old", "EUR", pricing.SourceTenant, hourAgo, "1", "2"),
			listVersion(t, "tenant-new", "EUR", pricing.SourceTenant, start.Add(400*time.Millisecond), "100", "200"),
		}
		h.targets["openai/"+fakeprovider.ModelGatedStream] = tgt
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		if _, err := testutil.ReadEvent(br); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(start.Add(600 * time.Millisecond))) // the new version has started when the request ends
		h.up.Release()
		if _, err := io.ReadAll(br); err != nil {
			t.Fatal(err)
		}
		r := h.oneRecord(t)
		if r.Cost.VersionID != "tenant-old" || r.Cost.Source != pricing.SourceTenant || r.Cost.Currency != "EUR" {
			t.Errorf("the record is priced with %+v: the version in force at the start of the request is the one that prices it", r.Cost)
		}
	})
}

// recordView is what T-735 reads of a usage record.
type recordView struct {
	status   string
	known    bool
	currency string
	source   string
	version  string
	reason   string
}
