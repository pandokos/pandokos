package auditchain_test

import (
	"iter"
	"slices"
	"testing"

	"github.com/pandokos/pandokos/internal/auditchain"
)

func seq(rows []auditchain.Row) iter.Seq2[auditchain.Row, error] {
	return func(yield func(auditchain.Row, error) bool) {
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func headOf(rows []auditchain.Row, anchorSeq int64, anchorHash []byte) *auditchain.Head {
	last := rows[len(rows)-1]
	return &auditchain.Head{Seq: last.Seq, Hash: last.Hash, AnchorSeq: anchorSeq, AnchorHash: anchorHash}
}

func reasons(res auditchain.Result) []string {
	var out []string
	for _, b := range res.Breaks {
		out = append(out, b.Reason)
	}
	return out
}

// rehash recomputes the hash of row i and relinks the rest, as someone who can write the tables
// would do to hide a change.
func rehashFrom(t *testing.T, rows []auditchain.Row, i int) {
	t.Helper()
	for ; i < len(rows); i++ {
		if i > 0 {
			rows[i].PrevHash = rows[i-1].Hash
		}
		h, err := auditchain.Hash(rows[i])
		if err != nil {
			t.Fatal(err)
		}
		rows[i].Hash = h[:]
	}
}

// TestVerify is T-939: synthetic exports, each break reason of section 5.9 alone, withheld rows,
// a chain starting at the anchor after retention and one starting elsewhere, --expect-head.
func TestVerify(t *testing.T) {
	scope := "tenant:" + testTenant

	t.Run("a valid chain", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 12, zeroHash)
		res := auditchain.Verify(seq(rows), headOf(rows, 1, zeroHash), nil)
		if len(res.Breaks) != 0 || res.Rows != 12 || res.First != 1 || res.Last != 12 || res.LinkUnverified {
			t.Errorf("result %+v", res)
		}
		if !bytesEqual(res.LastHash, rows[11].Hash) {
			t.Errorf("LastHash %x, want %x", res.LastHash, rows[11].Hash)
		}
	})

	t.Run("the platform scope", func(t *testing.T) {
		rows := buildChain(t, "platform", 1, 5, zeroHash)
		if res := auditchain.Verify(seq(rows), headOf(rows, 1, zeroHash), nil); len(res.Breaks) != 0 {
			t.Errorf("breaks %v", res.Breaks)
		}
	})

	t.Run("hash_mismatch", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 12, zeroHash)
		head := headOf(rows, 1, zeroHash)
		rows[9].Detail = []byte(`{"n": 10, "price": 9.99}`) // changed after sealing
		res := auditchain.Verify(seq(rows), head, nil)
		if len(res.Breaks) == 0 || res.Breaks[0].Reason != auditchain.ReasonHashMismatch || res.Breaks[0].Seq != 10 {
			t.Errorf("breaks %v, want hash_mismatch at 10 first", res.Breaks)
		}
		// Every other field is covered as well.
		for name, mutate := range map[string]func(*auditchain.Row){
			"actor_name":  func(r *auditchain.Row) { r.ActorName = "someone else" },
			"action":      func(r *auditchain.Row) { r.Action = "member.delete" },
			"occurred_at": func(r *auditchain.Row) { r.OccurredAt = r.OccurredAt.Add(1000) },
			"support":     func(r *auditchain.Row) { r.SupportActionID = "0192f0f0-4444-7000-8000-0000000000aa" },
			"id":          func(r *auditchain.Row) { r.ID = "0192f0f0-2222-7000-8000-0000000000ff" },
		} {
			rows := buildChain(t, scope, 1, 6, zeroHash)
			head := headOf(rows, 1, zeroHash)
			mutate(&rows[2])
			res := auditchain.Verify(seq(rows), head, nil)
			if len(res.Breaks) == 0 || res.Breaks[0].Reason != auditchain.ReasonHashMismatch || res.Breaks[0].Seq != 3 {
				t.Errorf("%s: breaks %v, want hash_mismatch at 3 first", name, res.Breaks)
			}
		}
	})

	t.Run("prev_mismatch", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 8, zeroHash)
		// Row 5 is sealed again over a different previous hash: its own hash is right, its link is not.
		rows[4].PrevHash = append([]byte(nil), rows[3].PrevHash...)
		h, err := auditchain.Hash(rows[4])
		if err != nil {
			t.Fatal(err)
		}
		rows[4].Hash = h[:]
		rehashFrom(t, rows, 5)
		rows[5].PrevHash = rows[4].Hash
		res := auditchain.Verify(seq(rows), headOf(rows, 1, zeroHash), nil)
		if got := reasons(res); !slices.Contains(got, auditchain.ReasonPrevMismatch) || res.Breaks[0].Seq != 5 {
			t.Errorf("breaks %v, want prev_mismatch at 5 first", res.Breaks)
		}
	})

	t.Run("seq_gap", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 8, zeroHash)
		head := headOf(rows, 1, zeroHash)
		rows = slices.Delete(rows, 4, 5) // row 5 removed
		res := auditchain.Verify(seq(rows), head, nil)
		if len(res.Breaks) == 0 || res.Breaks[0].Reason != auditchain.ReasonSeqGap || res.Breaks[0].Seq != 6 {
			t.Errorf("breaks %v, want seq_gap at 6 first", res.Breaks)
		}
	})

	t.Run("anchor_mismatch", func(t *testing.T) {
		anchor := make([]byte, 32)
		anchor[0] = 0x11
		rows := buildChain(t, scope, 5, 4, anchor)
		wrong := make([]byte, 32)
		wrong[0] = 0x22
		res := auditchain.Verify(seq(rows), headOf(rows, 5, wrong), nil)
		if len(res.Breaks) == 0 || res.Breaks[0].Reason != auditchain.ReasonAnchorMismatch {
			t.Errorf("breaks %v, want anchor_mismatch", res.Breaks)
		}
	})

	t.Run("head_mismatch: the log ends before the head", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 8, zeroHash)
		head := headOf(rows, 1, zeroHash)
		res := auditchain.Verify(seq(rows[:7]), head, nil) // the last row removed
		if got := reasons(res); !slices.Contains(got, auditchain.ReasonHeadMismatch) {
			t.Errorf("breaks %v, want head_mismatch", res.Breaks)
		}
	})

	t.Run("head_mismatch: the log goes past the head", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 8, zeroHash)
		head := headOf(rows[:7], 1, zeroHash)
		res := auditchain.Verify(seq(rows), head, nil)
		if got := reasons(res); !slices.Contains(got, auditchain.ReasonHeadMismatch) {
			t.Errorf("breaks %v, want head_mismatch", res.Breaks)
		}
	})

	t.Run("head_mismatch: a forged last row", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 8, zeroHash)
		head := headOf(rows, 1, zeroHash)
		rows[7].Detail = []byte(`{"forged": true}`)
		rehashFrom(t, rows, 7) // a consistent chain, but the head says otherwise
		res := auditchain.Verify(seq(rows), head, nil)
		if got := reasons(res); !slices.Contains(got, auditchain.ReasonHeadMismatch) {
			t.Errorf("breaks %v, want head_mismatch", res.Breaks)
		}
	})

	t.Run("a whole rewritten chain passes without an expected head and fails with one", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 8, zeroHash)
		earlier := rows[7].Hash
		rows[2].Detail = []byte(`{"rewritten": true}`)
		rehashFrom(t, rows, 2)
		head := headOf(rows, 1, zeroHash)
		if res := auditchain.Verify(seq(rows), head, nil); len(res.Breaks) != 0 {
			t.Errorf("the rewritten chain reports %v: the limit stated in the audit guide", res.Breaks)
		}
		res := auditchain.Verify(seq(rows), head, earlier)
		if got := reasons(res); !slices.Equal(got, []string{auditchain.ReasonExpectedHeadMismatch}) {
			t.Errorf("breaks %v, want expected_head_mismatch alone", res.Breaks)
		}
	})

	t.Run("--expect-head equal", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 8, zeroHash)
		if res := auditchain.Verify(seq(rows), headOf(rows, 1, zeroHash), rows[7].Hash); len(res.Breaks) != 0 {
			t.Errorf("breaks %v", res.Breaks)
		}
	})

	t.Run("a withheld row is verified by its digest", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 6, zeroHash)
		head := headOf(rows, 1, zeroHash)
		digest, err := auditchain.DetailDigest(rows[3].Detail)
		if err != nil {
			t.Fatal(err)
		}
		rows[3].Detail = []byte(`{}`)
		rows[3].DetailDigest = digest[:]
		if res := auditchain.Verify(seq(rows), head, nil); len(res.Breaks) != 0 {
			t.Errorf("a withheld row with the right digest breaks: %v", res.Breaks)
		}
		rows[3].DetailDigest = append([]byte(nil), digest[:]...)
		rows[3].DetailDigest[5] ^= 0xff
		res := auditchain.Verify(seq(rows), head, nil)
		if len(res.Breaks) == 0 || res.Breaks[0].Reason != auditchain.ReasonHashMismatch || res.Breaks[0].Seq != 4 {
			t.Errorf("a withheld row with an altered digest: %v, want hash_mismatch at 4", res.Breaks)
		}
	})

	t.Run("a chain that starts at the anchor after retention", func(t *testing.T) {
		all := buildChain(t, scope, 1, 12, zeroHash)
		kept := all[5:] // rows 6..12; the anchor is the hash of row 5
		head := headOf(all, 6, all[4].Hash)
		res := auditchain.Verify(seq(kept), head, nil)
		if len(res.Breaks) != 0 || res.LinkUnverified || res.First != 6 || res.Rows != 7 {
			t.Errorf("result %+v", res)
		}
	})

	t.Run("a chain that starts elsewhere has an unverified link", func(t *testing.T) {
		all := buildChain(t, scope, 1, 12, zeroHash)
		kept := all[8:] // rows 9..12 of a log that starts at 1
		head := headOf(all, 1, zeroHash)
		head.Start = 9 // an explicit --from-seq above the anchor
		res := auditchain.Verify(seq(kept), head, nil)
		if len(res.Breaks) != 0 {
			t.Errorf("breaks %v", res.Breaks)
		}
		if !res.LinkUnverified || res.First != 9 {
			t.Errorf("result %+v, want the link reported unverified", res)
		}
		// The same rows without a start: the log is read from its anchor, so the rows before 9 are missing.
		res = auditchain.Verify(seq(kept), headOf(all, 1, zeroHash), nil)
		if got := reasons(res); len(got) == 0 || res.Breaks[0].Seq != 9 || got[0] != auditchain.ReasonSeqGap || res.LinkUnverified {
			t.Errorf("result %+v, want seq_gap at 9", res)
		}
	})

	t.Run("the first rows are deleted", func(t *testing.T) {
		all := buildChain(t, scope, 1, 5, zeroHash)
		head := headOf(all, 1, zeroHash)
		for _, from := range []int{1, 2} { // rows 1-2, rows 1-3 removed
			res := auditchain.Verify(seq(all[from+1:]), head, nil)
			if len(res.Breaks) == 0 || res.Breaks[0].Seq != all[from+1].Seq || res.LinkUnverified {
				t.Errorf("rows 1..%d deleted: %+v, want a break at seq %d", from+1, res, all[from+1].Seq)
			}
		}
		// Asking for a start that is not the first row read.
		head.Start = 2
		res := auditchain.Verify(seq(all[2:]), head, nil)
		if len(res.Breaks) == 0 || res.Breaks[0].Seq != 3 || res.Breaks[0].Reason != auditchain.ReasonSeqGap {
			t.Errorf("start 2, first row 3: %+v, want seq_gap at 3", res)
		}
		// A chain whose first rows were removed together with the anchor row's link: wrong anchor.
		res = auditchain.Verify(seq(all[1:]), headOf(all, 2, zeroHash), nil)
		if len(res.Breaks) == 0 || res.Breaks[0].Reason != auditchain.ReasonAnchorMismatch {
			t.Errorf("a wrong anchor: %+v", res)
		}
	})

	t.Run("an empty export", func(t *testing.T) {
		rows := buildChain(t, scope, 1, 3, zeroHash)
		res := auditchain.Verify(seq(nil), headOf(rows, 1, zeroHash), nil)
		if got := reasons(res); !slices.Contains(got, auditchain.ReasonHeadMismatch) {
			t.Errorf("breaks %v, want head_mismatch (the log ends before the head)", res.Breaks)
		}
	})
}
