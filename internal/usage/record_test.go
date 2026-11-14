package usage_test

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/testutil"
)

const migrationFile = "../store/migrations/sql/0006_usage_ledger.sql"

// constraintNames reads the names of the constraints of usage_record from the migration.
func constraintNames(t testing.TB) []string {
	t.Helper()
	b, err := os.ReadFile(migrationFile)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range regexp.MustCompile(`CONSTRAINT (usage_record_\w+)`).FindAllStringSubmatch(string(b), -1) {
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}

func tid() tenant.ID {
	id, err := tenant.ParseID(testutil.RandomUUID())
	if err != nil {
		panic(err)
	}
	return id
}

func many(n, keyLen int) map[string]int64 {
	m := make(map[string]int64, n)
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%0*d", keyLen, i)
		m[k] = int64(i)
	}
	return m
}

type violation struct {
	mutate func(*usage.Record)
	fields []string // the error names one of these (a column of the table)
	leak   string   // a value that must not appear in the error
}

// canary values planted in the fields under test: none may appear in an error.
const (
	badText = "SyntheticBadValue"
	badNum  = int64(-7771)
)

// violations has one case for every constraint of the table that Validate checks. The writer's
// column mapping (T-471) makes the source constraints true by construction; they are listed in
// byConstruction.
var violations = map[string]violation{
	"usage_record_request_id_format":    {func(r *usage.Record) { r.RequestID = badText }, []string{"request_id"}, badText},
	"usage_record_trace_id_format":      {func(r *usage.Record) { r.TraceID = badText }, []string{"trace_id"}, badText},
	"usage_record_principal_kind_valid": {func(r *usage.Record) { r.PrincipalKind = badText }, []string{"principal_kind"}, badText},
	"usage_record_principal_id_format":  {func(r *usage.Record) { r.PrincipalID = badText }, []string{"principal_id"}, badText},
	"usage_record_key_id_format":        {func(r *usage.Record) { r.KeyID = badText + " !" }, []string{"key_id"}, badText},
	"usage_record_funding_valid":        {func(r *usage.Record) { r.Funding = badText }, []string{"funding"}, badText},
	"usage_record_provider_format":      {func(r *usage.Record) { r.Provider = badText }, []string{"provider"}, badText},
	"usage_record_api_family_format":    {func(r *usage.Record) { r.APIFamily = badText }, []string{"api_family"}, badText},
	"usage_record_operation_format":     {func(r *usage.Record) { r.Operation = badText }, []string{"operation"}, badText},
	"usage_record_endpoint_length":      {func(r *usage.Record) { r.Endpoint = strings.Repeat("e", 64) }, []string{"endpoint"}, strings.Repeat("e", 64)},
	"usage_record_model_length": {func(r *usage.Record) { r.UpstreamModel = strings.Repeat("m", 257) }, []string{"upstream_model", "model"},
		strings.Repeat("m", 257)},
	"usage_record_outcome_valid":         {func(r *usage.Record) { r.Outcome = usage.Outcome(badText) }, []string{"outcome"}, badText},
	"usage_record_error_code_format":     {func(r *usage.Record) { r.ErrorCode = badText }, []string{"error_code"}, badText},
	"usage_record_upstream_status_range": {func(r *usage.Record) { r.UpstreamStatus = 777 }, []string{"upstream_status"}, "777"},
	"usage_record_times_not_negative":    {func(r *usage.Record) { r.Duration = time.Duration(badNum) * time.Millisecond }, []string{"duration_ms"}, "7771"},
	"usage_record_source_valid":          {func(r *usage.Record) { r.InputSource = usage.Source(badText) }, []string{"input_source"}, badText},
	"usage_record_tokens_not_negative":   {func(r *usage.Record) { r.Usage.InputTokens = badNum }, []string{"input_tokens"}, "7771"},
	"usage_record_other_units_valid":     {func(r *usage.Record) { r.Usage.Extra = many(60, 60) }, []string{"other_units"}, ""},
	"usage_record_provider_usage_valid":  {func(r *usage.Record) { r.Usage.Reported = many(100, 60) }, []string{"provider_usage"}, ""},
	"usage_record_cost_status_valid":     {func(r *usage.Record) { r.CostStatus = badText }, []string{"cost_status"}, badText},
	"usage_record_cost_reason_valid": {func(r *usage.Record) {
		r.CostStatus, r.Cost = "unknown", pricing.Cost{Reason: badText}
	}, []string{"cost_reason"}, badText},
	"usage_record_cost_by_status":  {func(r *usage.Record) { r.CostStatus = "none" }, []string{"cost"}, ""},
	"usage_record_currency_format": {func(r *usage.Record) { r.Cost.Currency = "usd" }, []string{"currency"}, "usd"},
	"usage_record_cost_fallbacks_bound": {func(r *usage.Record) {
		r.Cost.Fallbacks = make([]pricing.Category, 9)
		for i := range r.Cost.Fallbacks {
			r.Cost.Fallbacks[i] = pricing.CategoryCacheRead
		}
	}, []string{"cost_fallbacks"}, ""},
	"usage_record_metadata_valid": {func(r *usage.Record) {
		r.Metadata = map[string]string{}
		for i := 0; i < 100; i++ {
			r.Metadata[fmt.Sprintf("k%03d", i)] = strings.Repeat("v", 60)
		}
	}, []string{"metadata"}, strings.Repeat("v", 60)},
	"usage_record_metadata_dropped_range": {func(r *usage.Record) { r.MetadataDropped = 1001 }, []string{"metadata_dropped"}, "1001"},
}

// byConstruction are the constraints Validate does not check on a record: a Record has no NULL to
// put where a source says there is no value, because the writer's mapping sets the columns by the
// source (T-471 reads the rows back), and the two keys are not about the values of a record.
var byConstruction = map[string]string{
	"usage_record_pkey":                     "the key is made of the tenant, the start and the ID, set by the writer",
	"usage_record_tenant_fkey":              "the tenant is the session's, set by the statement",
	"usage_record_input_by_source":          "the writer fills the provider and estimate columns by the source",
	"usage_record_output_by_source":         "the writer fills the provider and estimate columns by the source",
	"usage_record_detail_needs_provider":    "the writer fills cache columns only for the provider source",
	"usage_record_reasoning_needs_provider": "the writer fills reasoning only for the provider source",
}

// TestValidate is T-469: one failing record per constraint of the table, read from the migration;
// the error names the field and holds no value.
func TestValidate(t *testing.T) {
	names := constraintNames(t)
	if len(names) < 30 {
		t.Fatalf("read %d constraints from the migration", len(names))
	}

	t.Run("a valid record", func(t *testing.T) {
		r := testutil.ValidRecord(tid())
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	for _, name := range names {
		v, ok := violations[name]
		if !ok {
			if _, ok := byConstruction[name]; !ok {
				t.Errorf("the migration has the constraint %s and this test has no case for it", name)
			}
			continue
		}
		t.Run(name, func(t *testing.T) {
			r := testutil.ValidRecord(tid())
			v.mutate(&r)
			err := r.Validate()
			if err == nil {
				t.Fatal("the record was accepted")
			}
			named := false
			for _, f := range v.fields {
				named = named || strings.Contains(err.Error(), f)
			}
			if !named {
				t.Errorf("the error %q names none of %v", err, v.fields)
			}
			if v.leak != "" && strings.Contains(err.Error(), v.leak) {
				t.Errorf("the error holds the value: %q", err)
			}
		})
	}
	for name := range violations {
		if !contains(names, name) {
			t.Errorf("the case %s is not a constraint of the migration", name)
		}
	}
	for name := range byConstruction {
		if !contains(names, name) {
			t.Errorf("%s is not a constraint of the migration", name)
		}
	}

	t.Run("a cost the column cannot hold", func(t *testing.T) {
		r := testutil.ValidRecord(tid())
		big, err := pricing.ParseDecimal("100000000000000000000") // 1e20; numeric(38,18) holds less
		if err != nil {
			t.Fatal(err)
		}
		r.CostStatus, r.Cost = "known", pricing.Cost{Known: true, Amount: big, Currency: "USD", VersionID: "v"}
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "cost") {
			t.Errorf("%v", err)
		}
	})

	t.Run("records the gateway builds are valid", func(t *testing.T) {
		stream := true
		ttfb := time.Duration(0)
		n := int64(0)
		for name, mutate := range map[string]func(*usage.Record){
			"a refusal": func(r *usage.Record) {
				*r = usage.Record{ID: r.ID, TenantID: r.TenantID, StartedAt: r.StartedAt, RequestID: r.RequestID,
					PrincipalKind: r.PrincipalKind, PrincipalID: r.PrincipalID, KeyID: r.KeyID, Funding: "byok",
					Provider: "openai", APIFamily: "openai", Operation: "chat.completions",
					Outcome: usage.OutcomeRejectedPolicy, ErrorCode: "forbidden", Duration: time.Millisecond,
					InputSource: usage.SourceNone, OutputSource: usage.SourceNone, CostStatus: "none"}
			},
			"an estimate": func(r *usage.Record) {
				r.InputSource, r.OutputSource = usage.SourceEstimated, usage.SourceEstimated
				r.Usage = family.Usage{}
				r.Estimated = family.Usage{InputTokens: 10, OutputTokens: 5}
			},
			"an unknown cost": func(r *usage.Record) {
				r.CostStatus, r.Cost = "unknown", pricing.Cost{Reason: pricing.ReasonNoPrice}
			},
			"a stream with a first byte at zero": func(r *usage.Record) { r.Stream, r.TTFB = &stream, &ttfb },
			"zero bytes":                         func(r *usage.Record) { r.RequestBytes, r.ResponseBytes = &n, &n },
			"a trace ID":                         func(r *usage.Record) { r.TraceID = strings.Repeat("ab", 16) },
			"an upstream error":                  func(r *usage.Record) { r.Outcome, r.UpstreamStatus = usage.OutcomeUpstreamError, 503 },
			"metadata":                           func(r *usage.Record) { r.Metadata, r.MetadataDropped = map[string]string{"team": "alpha"}, 2 },
			"the longest upstream id and model name of the catalogue": func(r *usage.Record) {
				r.UpstreamModel, r.Model = strings.Repeat("u", 256), strings.Repeat("m", 128)
			},
			"a cancelled request": func(r *usage.Record) {
				r.Outcome, r.UpstreamStatus, r.Stream = usage.OutcomeCancelled, 0, nil
			},
		} {
			r := testutil.ValidRecord(tid())
			mutate(&r)
			if err := r.Validate(); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
