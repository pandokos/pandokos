package gateway_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestMetadataKeyPolicy is T-964: the keys a tenant accepts (sprint 6.2, section 5.11), read from the
// tenant's catalogue view over the database. Without a policy the behaviour is that of T-488; with a
// policy only its keys are kept, the others are counted as dropped; a request refused before the
// tenant's view is in hand records no metadata and counts its entries as dropped; a change of the
// policy is seen by the next request after the notification.
func TestMetadataKeyPolicy(t *testing.T) {
	const p = "X-Pandokos-Metadata-"
	d := dbtest.NewMigrated(t)
	tn := d.SeedTenant(t, "policy")
	w := newPriceWorld(t, d)
	s := w.stack(t, tn, priceOptions{listener: true})
	super := d.Super(t)

	send := func(extra http.Header, body []byte) captured {
		t.Helper()
		h := s.hdr("a")
		for k, v := range extra {
			h[k] = v
		}
		if body == nil {
			body = chat("fake-basic", false)
		}
		return s.h.send(t, http.MethodPost, "/openai/v1/chat/completions", body, h)
	}
	last := func(n int) map[string]string {
		t.Helper()
		recs := s.h.records(t, n)
		return recs[len(recs)-1].Metadata
	}
	dropped := func(n int) int {
		recs := s.h.records(t, n)
		return recs[len(recs)-1].MetadataDropped
	}
	setPolicy := func(keys string) {
		t.Helper()
		if _, err := super.Exec(ctx5(t), `DELETE FROM pandokos.metadata_key_policy WHERE tenant_id = $1::uuid`, tn.String()); err != nil {
			t.Fatal(err)
		}
		if keys != "" {
			if _, err := super.Exec(ctx5(t), `INSERT INTO pandokos.metadata_key_policy (tenant_id, keys) VALUES ($1::uuid, `+keys+`)`, tn.String()); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("no policy: as before", func(t *testing.T) {
		c := send(http.Header{p + "Team": {"alpha"}, p + "Project": {"beta"}}, nil)
		if c.status != 200 || c.header.Get("X-Pandokos-Metadata-Dropped") != "" {
			t.Fatalf("%d %v", c.status, c.header)
		}
		if m := last(1); len(m) != 2 || m["team"] != "alpha" || m["project"] != "beta" || dropped(1) != 0 {
			t.Errorf("metadata %v dropped %d", m, dropped(1))
		}
	})

	t.Run("a policy keeps its keys and counts the others as dropped", func(t *testing.T) {
		setPolicy(`ARRAY['team']`)
		var c captured
		testutil.Eventually(t, 5*time.Second, "the policy to be seen after the notification", func() bool {
			c = send(http.Header{p + "Team": {"alpha"}, p + "Project": {metaCanary}}, nil)
			// The record of the request is written after its answer: wait for it too.
			recs := s.h.rec.Records()
			return c.header.Get("X-Pandokos-Metadata-Dropped") == "1" && len(recs) > 0 && recs[len(recs)-1].MetadataDropped == 1
		})
		recs := s.h.rec.Records()
		r := recs[len(recs)-1]
		if len(r.Metadata) != 1 || r.Metadata["team"] != "alpha" || r.MetadataDropped != 1 {
			t.Errorf("metadata %v dropped %d, want team kept and project dropped", r.Metadata, r.MetadataDropped)
		}
		for _, v := range r.Metadata {
			if v == metaCanary {
				t.Error("the value of a key the tenant refused was stored")
			}
		}
		for _, req := range w.up.Requests() {
			for name := range req.Header {
				if len(name) >= 10 && name[:10] == "X-Pandokos" {
					t.Errorf("the provider received %s", name)
				}
			}
		}
	})

	t.Run("an empty policy keeps nothing", func(t *testing.T) {
		setPolicy(`ARRAY[]::text[]`)
		testutil.Eventually(t, 5*time.Second, "the empty policy to be seen", func() bool {
			c := send(http.Header{p + "Team": {"alpha"}}, nil)
			// The record of the request is written after its answer: wait for it too.
			recs := s.h.rec.Records()
			return c.header.Get("X-Pandokos-Metadata-Dropped") == "1" && len(recs) > 0 && len(recs[len(recs)-1].Metadata) == 0
		})
		recs := s.h.rec.Records()
		if r := recs[len(recs)-1]; len(r.Metadata) != 0 || r.MetadataDropped != 1 {
			t.Errorf("metadata %v dropped %d, want none kept and one dropped", r.Metadata, r.MetadataDropped)
		}
	})

	t.Run("a request refused before the view is in hand records no metadata", func(t *testing.T) {
		setPolicy(`ARRAY['team']`)
		testutil.Eventually(t, 5*time.Second, "the policy to be seen", func() bool {
			c := send(http.Header{p + "Team": {"alpha"}, p + "Project": {"x"}}, nil)
			return c.header.Get("X-Pandokos-Metadata-Dropped") == "1"
		})
		before := len(s.h.rec.Records())
		// A body that is not JSON is refused before the model is known: the entries are counted, not stored.
		c := send(http.Header{p + "Team": {"alpha"}, p + "Project": {metaCanary}}, []byte(`{`))
		if c.status != 400 {
			t.Fatalf("%d %s", c.status, c.body)
		}
		recs := s.h.records(t, before+1)
		r := recs[len(recs)-1]
		if len(r.Metadata) != 0 || r.MetadataDropped != 2 {
			t.Errorf("a refusal before step 7: metadata %v dropped %d, want none and 2 (the entries it kept are counted as dropped)", r.Metadata, r.MetadataDropped)
		}
		if got := c.header.Get("X-Pandokos-Metadata-Dropped"); got != "2" {
			t.Errorf("the response says %q, want 2", got)
		}
	})

	t.Run("a change is seen by the next request after the notification", func(t *testing.T) {
		setPolicy(`ARRAY['project']`)
		testutil.Eventually(t, 5*time.Second, "the new policy to be seen: project kept, team dropped", func() bool {
			c := send(http.Header{p + "Team": {"alpha"}, p + "Project": {"beta"}}, nil)
			recs := s.h.rec.Records()
			r := recs[len(recs)-1]
			return c.status == 200 && r.Metadata["project"] == "beta" && r.Metadata["team"] == "" && r.MetadataDropped == 1
		})
		setPolicy("")
		testutil.Eventually(t, 5*time.Second, "the policy to be cleared: every key kept", func() bool {
			send(http.Header{p + "Team": {"alpha"}, p + "Project": {"beta"}}, nil)
			recs := s.h.rec.Records()
			r := recs[len(recs)-1]
			return len(r.Metadata) == 2 && r.MetadataDropped == 0
		})
	})
}
