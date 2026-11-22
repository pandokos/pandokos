package auditchain_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestCanonicalAgainstSQL is T-936, the database half: the golden file is written (with -update)
// from pandokos.audit_canonical, and Hash of fixed rows equals pandokos.audit_row_hash run by the
// owner on the same values.
func TestCanonicalAgainstSQL(t *testing.T) {
	d := dbtest.NewMigrated(t)
	super := d.Super(t)
	ctx := context.Background()
	if _, err := super.Exec(ctx, "SET ROLE pandokos_owner"); err != nil {
		t.Fatal(err)
	}

	if *update {
		var b strings.Builder
		b.WriteString("# name <TAB> the JSON text jsonb stores <TAB> hex of pandokos.audit_canonical(jsonb)\n")
		b.WriteString("# written by `go test ./internal/auditchain -run TestCanonicalAgainstSQL -update`; reviewed by hand once\n")
		names := make([]string, 0, len(canonicalCases))
		in := map[string]string{}
		for _, c := range canonicalCases {
			names = append(names, c.name)
			in[c.name] = c.in
		}
		sort.Strings(names)
		for _, n := range names {
			var stored, enc string
			if err := super.QueryRow(ctx, `SELECT $1::jsonb::text, encode(pandokos.audit_canonical($1::jsonb), 'hex')`, in[n]).Scan(&stored, &enc); err != nil {
				t.Fatalf("%s: %v", n, err)
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\n", n, stored, enc)
		}
		if err := os.WriteFile("testdata/canonical.golden", []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("Canonical equals the SQL function", func(t *testing.T) {
		for _, c := range canonicalCases {
			var stored, want string
			if err := super.QueryRow(ctx, `SELECT $1::jsonb::text, encode(pandokos.audit_canonical($1::jsonb), 'hex')`, c.in).Scan(&stored, &want); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			got, err := auditchain.Canonical([]byte(stored))
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if hex.EncodeToString(got) != want {
				t.Errorf("%s: Go %x, SQL %s", c.name, got, want)
			}
		}
	})

	t.Run("Hash equals audit_row_hash", func(t *testing.T) {
		support := "0192f0f0-4444-7000-8000-0000000000aa"
		rows := []auditchain.Row{fixedRow("tenant:"+testTenant, 7), fixedRow("platform", 3)}
		rows[0].Action = "support.intervention"
		rows[0].SupportActionID = support
		rows[0].Reason = "ticket 4711, curly “quotes”"
		rows[0].KeyID = "tabcdefghijklmnop"
		rows[1].Detail = []byte(`{"before": "2026-10-01", "months": 2, "partitions": ["usage_record_y2026m08"]}`)
		for i := range rows {
			rows[i].PrevHash = make([]byte, 32)
			rows[i].PrevHash[0], rows[i].PrevHash[31] = byte(i+1), 0xfe
			var sup any
			if rows[i].SupportActionID != "" {
				sup = rows[i].SupportActionID
			}
			var want []byte
			err := super.QueryRow(ctx, `SELECT pandokos.audit_row_hash($1, $2, $3::uuid, $4::timestamptz, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::uuid, $16::jsonb, $17)`,
				rows[i].Scope, rows[i].Seq, rows[i].ID, rows[i].OccurredAt, rows[i].ActorType, rows[i].ActorID, rows[i].ActorName, rows[i].KeyID,
				rows[i].Action, rows[i].ResourceType, rows[i].ResourceID, rows[i].Outcome, rows[i].RequestID, rows[i].Reason, sup,
				string(rows[i].Detail), rows[i].PrevHash).Scan(&want)
			if err != nil {
				t.Fatalf("%s: %v", rows[i].Scope, err)
			}
			got, err := auditchain.Hash(rows[i])
			if err != nil {
				t.Fatalf("%s: %v", rows[i].Scope, err)
			}
			if !bytesEqual(got[:], want) {
				t.Errorf("%s: Go %x, SQL %x", rows[i].Scope, got, want)
			}
		}
	})

	t.Run("a row read back from the database hashes to its stored hash", func(t *testing.T) {
		if _, err := super.Exec(ctx, "RESET ROLE"); err != nil {
			t.Fatal(err)
		}
		tid := d.SeedTenant(t, "chain-a")
		if _, err := super.Exec(ctx, `INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, reason, detail)
			VALUES ($1::uuid, 'user', 'Zoë', 'member.create', 'member', 'x', 'success', 'r1', 'because', '{"k": [1.50, "é", null]}')`, tid.String()); err != nil {
			t.Fatal(err)
		}
		var r auditchain.Row
		var occurred time.Time
		var stored []byte
		var detail string
		err := super.QueryRow(ctx, `SELECT seq, id::text, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome,
			request_id, reason, COALESCE(support_action_id::text, ''), detail::text, prev_hash, hash FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, tid.String()).
			Scan(&r.Seq, &r.ID, &occurred, &r.ActorType, &r.ActorID, &r.ActorName, &r.KeyID, &r.Action, &r.ResourceType, &r.ResourceID, &r.Outcome,
				&r.RequestID, &r.Reason, &r.SupportActionID, &detail, &r.PrevHash, &stored)
		if err != nil {
			t.Fatal(err)
		}
		r.Scope, r.OccurredAt, r.Detail = "tenant:"+tid.String(), occurred, []byte(detail)
		got, err := auditchain.Hash(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytesEqual(got[:], stored) {
			t.Errorf("Go %x, stored %x", got, stored)
		}
	})
}

func bytesEqual(a, b []byte) bool { return hex.EncodeToString(a) == hex.EncodeToString(b) }
