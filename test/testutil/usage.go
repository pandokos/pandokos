package testutil

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
)

// RandomUUID returns a random version 4 UUID in canonical form.
func RandomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidRecord returns a usage record of a successful, non-streamed call that passes
// usage.Record.Validate and the constraints of the table: provider-reported tokens, a known
// cost, no metadata. Every identifier is synthetic; none is content.
func ValidRecord(t tenant.ID) usage.Record {
	start := time.Now().UTC().Truncate(time.Microsecond)
	stream := false
	ttfb := 40 * time.Millisecond
	reqBytes, respBytes := int64(120), int64(480)
	amount, err := pricing.ParseDecimal("0.000099")
	if err != nil {
		panic(err)
	}
	return usage.Record{
		ID:        usage.NewID(start),
		TenantID:  t,
		StartedAt: start,
		RequestID: randomHex(16), TraceID: "",

		PrincipalKind: "service_account", PrincipalID: RandomUUID(), KeyID: "synthetic-key",
		CredentialID: RandomUUID(), Funding: "byok",

		Provider: "openai", APIFamily: "openai", Operation: "chat.completions",
		Endpoint: "synthetic-endpoint", Model: "synthetic-model", ModelID: RandomUUID(), UpstreamModel: "synthetic-upstream",
		Stream: &stream,

		Outcome: usage.OutcomeOK, UpstreamStatus: 200, TTFB: &ttfb, Duration: 250 * time.Millisecond,
		RequestBytes: &reqBytes, ResponseBytes: &respBytes,

		InputSource: usage.SourceProvider, OutputSource: usage.SourceProvider,
		Usage: family.Usage{InputTokens: 9, OutputTokens: 12, Reported: map[string]int64{"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21}},

		Cost:       pricing.Cost{Known: true, Amount: amount, Currency: "USD", VersionID: RandomUUID()},
		CostStatus: "known",
		Metadata:   map[string]string{},
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}
