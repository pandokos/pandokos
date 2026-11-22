package auditchain_test

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/auditchain"
)

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

const testTenant = "0192f0f0-1111-7000-8000-000000000001"

var zeroHash = make([]byte, 32)

// fixedRow is a row with every field set, for the chain at seq.
func fixedRow(scope string, seq int64) auditchain.Row {
	r := auditchain.Row{
		Scope:        scope,
		Seq:          seq,
		ID:           fmt.Sprintf("0192f0f0-2222-7000-8000-%012d", seq),
		OccurredAt:   time.Date(2026, 11, 3, 10, 0, 0, 123456000, time.UTC).Add(time.Duration(seq) * time.Second),
		ActorType:    "user",
		ActorID:      "0192f0f0-3333-7000-8000-000000000001",
		ActorName:    "Ada Ünicode",
		KeyID:        "",
		Action:       "member.create",
		ResourceType: "member",
		ResourceID:   fmt.Sprintf("m-%d", seq),
		Outcome:      "success",
		RequestID:    "0123456789abcdef0123456789abcdef",
		Reason:       "",
		Detail:       []byte(fmt.Sprintf(`{"n": %d, "price": 1.50}`, seq)),
	}
	return r
}

// buildChain makes n sealed rows of a chain, numbered from first, the first one linked to prev.
func buildChain(t testing.TB, scope string, first, n int64, prev []byte) []auditchain.Row {
	t.Helper()
	rows := make([]auditchain.Row, 0, n)
	for i := int64(0); i < n; i++ {
		r := fixedRow(scope, first+i)
		r.PrevHash = append([]byte(nil), prev...)
		h, err := auditchain.Hash(r)
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		r.Hash = h[:]
		prev = r.Hash
		rows = append(rows, r)
	}
	return rows
}
