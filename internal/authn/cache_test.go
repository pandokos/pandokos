package authn

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestCache(ttl time.Duration) (*cache, *clock) {
	clk := &clock{t: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	return newCache(clk.now, ttl, slog.New(slog.NewTextHandler(io.Discard, nil))), clk
}

func keyIDFor(i int) string { return fmt.Sprintf("t%016d", i) }

// TestCache is T-208.
func TestCache(t *testing.T) {
	const tenantA, tenantB = "0192f0f0-0000-7000-8000-00000000000a", "0192f0f0-0000-7000-8000-00000000000b"

	t.Run("an entry lives for the lifetime from its creation however often it is read", func(t *testing.T) {
		c, clk := newTestCache(30 * time.Second)
		c.put("tkey", tenantA, &entry{})
		for i := 0; i < 29; i++ {
			clk.advance(time.Second)
			if _, ok := c.get("tkey"); !ok {
				t.Fatalf("the entry is gone after %d s", i+1)
			}
		}
		clk.advance(time.Second - time.Nanosecond)
		if _, ok := c.get("tkey"); !ok {
			t.Fatal("the entry is gone before its lifetime ends")
		}
		clk.advance(time.Nanosecond)
		if _, ok := c.get("tkey"); ok {
			t.Fatal("the entry outlived its lifetime: use extends it")
		}
		if pos, _ := c.sizes(); pos != 0 {
			t.Errorf("an expired entry is still counted: %d", pos)
		}
	})

	t.Run("negative entries last 5 seconds", func(t *testing.T) {
		c, clk := newTestCache(time.Minute)
		c.putNegative("tunknown")
		if !c.negative("tunknown") || c.negative("tother") {
			t.Fatal("a negative entry is missing or too wide")
		}
		clk.advance(5*time.Second - time.Nanosecond)
		if !c.negative("tunknown") {
			t.Fatal("the negative entry expired early")
		}
		clk.advance(time.Nanosecond)
		if c.negative("tunknown") {
			t.Fatal("the negative entry lasted more than 5 s")
		}
	})

	t.Run("a tenant payload removes the entries of that tenant only", func(t *testing.T) {
		c, _ := newTestCache(time.Minute)
		c.put("ta1", tenantA, &entry{})
		c.put("ta2", tenantA, &entry{})
		c.put("tb1", tenantB, &entry{})
		c.putNegative("tunknown")
		c.invalidate("tenant:" + tenantA)
		if _, ok := c.get("ta1"); ok {
			t.Error("ta1 survived")
		}
		if _, ok := c.get("ta2"); ok {
			t.Error("ta2 survived")
		}
		if _, ok := c.get("tb1"); !ok {
			t.Error("the other tenant lost an entry")
		}
		if !c.negative("tunknown") {
			t.Error("a negative entry was removed by a tenant payload")
		}
		// A key that is put again after the invalidation is cached again.
		c.put("ta1", tenantA, &entry{})
		if _, ok := c.get("ta1"); !ok {
			t.Error("the cache refuses an entry after an invalidation")
		}
	})

	t.Run("platform and probes remove nothing", func(t *testing.T) {
		c, _ := newTestCache(time.Minute)
		c.put("ta1", tenantA, &entry{})
		c.putNegative("tunknown")
		for _, p := range []string{"platform", "probe:0123456789abcdef", "probe:"} {
			c.invalidate(p)
		}
		if pos, neg := c.sizes(); pos != 1 || neg != 1 {
			t.Errorf("sizes %d/%d after payloads that remove nothing", pos, neg)
		}
	})

	t.Run("an unknown payload and Flush remove everything", func(t *testing.T) {
		for _, how := range []string{"unknown", "flush", "malformed tenant", "empty"} {
			c, _ := newTestCache(time.Minute)
			c.put("ta1", tenantA, &entry{})
			c.put("tb1", tenantB, &entry{})
			c.putNegative("tunknown")
			switch how {
			case "unknown":
				c.invalidate("something:else")
			case "flush":
				c.flush("reset")
			case "malformed tenant":
				c.invalidate("tenant:")
			case "empty":
				c.invalidate("")
			}
			if how == "malformed tenant" {
				// A tenant payload without an ID names nobody; forward compatibility says flush.
				if pos, neg := c.sizes(); pos != 0 || neg != 0 {
					t.Errorf("%s: sizes %d/%d", how, pos, neg)
				}
				continue
			}
			if pos, neg := c.sizes(); pos != 0 || neg != 0 {
				t.Errorf("%s: sizes %d/%d, want 0/0", how, pos, neg)
			}
		}
	})

	t.Run("a full map is emptied", func(t *testing.T) {
		c, _ := newTestCache(time.Minute)
		for i := 0; i < maxEntries; i++ {
			c.put(keyIDFor(i), tenantA, &entry{})
		}
		if pos, _ := c.sizes(); pos != maxEntries {
			t.Fatalf("%d positive entries, want %d", pos, maxEntries)
		}
		c.put("tone-more", tenantA, &entry{})
		if _, ok := c.get(keyIDFor(0)); ok {
			t.Error("the map was not emptied when it was full")
		}
		if _, ok := c.get("tone-more"); !ok {
			t.Error("the new entry is missing after the flush")
		}
		if pos, _ := c.sizes(); pos != 1 {
			t.Errorf("%d positive entries after the flush, want 1", pos)
		}
		for i := 0; i < maxEntries; i++ {
			c.putNegative(keyIDFor(i))
		}
		c.putNegative("tone-more")
		if c.negative(keyIDFor(0)) || !c.negative("tone-more") {
			t.Error("the negative map was not emptied when it was full")
		}
	})
}
