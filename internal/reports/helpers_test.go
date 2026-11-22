package reports_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	return c
}

// seed returns the seed of the random runs: the environment variable PANDOKOS_TEST_SEED when it is
// set (to replay a failure), else the clock. It is logged.
func seed(t *testing.T) *rand.Rand {
	t.Helper()
	n := time.Now().UnixNano()
	if v := os.Getenv("PANDOKOS_TEST_SEED"); v != "" {
		if p, err := strconv.ParseInt(v, 10, 64); err == nil {
			n = p
		}
	}
	t.Logf("seed %d (PANDOKOS_TEST_SEED replays it)", n)
	return rand.New(rand.NewSource(n))
}

// rec is one usage record as the tests write it, as the owner, so that recorded_at is theirs.
type rec struct {
	Started, Recorded time.Time
	Principal, Key    string
	Provider, Model   string
	ModelID           string // "" for none
	Outcome           string
	Credential        bool
	In, Out           int64  // reported by the provider when InSource is "provider"
	InSource          string // provider, estimated or none
	OutSource         string
	CacheRead, Reason int64  // only with a provider source
	Est               int64  // estimated input tokens, with InSource estimated
	CostStatus        string // known, unknown or none
	Cost              string // a decimal; with CostStatus known
	Currency          string
	PriceSource       string // catalogue or tenant, "" for none; a price version is set when there is a cost or a source
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// insertRecords writes the records of a tenant as the owner.
func insertRecords(t testing.TB, d *dbtest.Database, id tenant.ID, recs ...rec) {
	t.Helper()
	c := ctx(t)
	tx, err := d.SharedSuper(t).Begin(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(c) }()
	for i, r := range recs {
		in, out, est, estOut, cache, reason := any(nil), any(nil), any(nil), any(nil), any(nil), any(nil)
		switch r.InSource {
		case "provider":
			in = r.In
			if r.CacheRead > 0 {
				cache = r.CacheRead
			}
		case "estimated":
			est = r.Est
		default:
			r.InSource = "none"
		}
		switch r.OutSource {
		case "provider":
			out = r.Out
			if r.Reason > 0 {
				reason = r.Reason
			}
		case "estimated":
			estOut = r.Out
		default:
			r.OutSource = "none"
		}
		if r.Outcome == "" {
			r.Outcome = "ok"
		}
		if r.CostStatus == "" {
			r.CostStatus = "none"
		}
		var cost, currency, version, source any
		var reasonText string
		if r.CostStatus == "known" {
			cost, currency, version = r.Cost, nullable(r.Currency), "gen_random_uuid()"
		}
		if r.CostStatus == "unknown" {
			reasonText = "no_price"
		}
		if r.PriceSource != "" {
			source, version = r.PriceSource, "gen_random_uuid()"
		}
		_ = currency
		if _, err := tx.Exec(c, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, recorded_at, request_id, principal_kind, principal_id, key_id, credential_id,
				provider, api_family, operation, model, model_id, outcome, duration_ms, input_source, output_source, input_tokens, output_tokens,
				cache_read_tokens, reasoning_tokens, estimated_input_tokens, estimated_output_tokens, cost_status, cost_reason, cost, currency, price_version_id, price_source)
			VALUES ($1::uuid, pandokos.uuid_v7(), $2, $3, md5(random()::text), 'service_account', $4, $5, CASE WHEN $6 THEN gen_random_uuid() END,
				$7, $7, 'chat.completions', $8, $9::uuid, $10, 1, $11, $12, $13::bigint, $14::bigint, $15::bigint, $16::bigint, $17::bigint, $18::bigint,
				$19, $20, $21::numeric, $22::text, CASE WHEN $23::text IS NULL AND $21::numeric IS NULL THEN NULL ELSE gen_random_uuid() END, $24::text)`,
			id.String(), r.Started.UTC(), r.Recorded.UTC(), r.Principal, r.Key, r.Credential, r.Provider, r.Model, nullable(r.ModelID), r.Outcome,
			r.InSource, r.OutSource, in, out, cache, reason, est, estOut, r.CostStatus, reasonText, cost, nullable(r.Currency), version, source); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if err := tx.Commit(c); err != nil {
		t.Fatal(err)
	}
}

func uuidOf(n int) string { return fmt.Sprintf("0192f0f0-0000-7000-8000-%012d", n) }
