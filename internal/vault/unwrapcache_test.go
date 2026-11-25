package vault_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/vault"
)

// TestUnwrapCache is T-1070.
func TestUnwrapCache(t *testing.T) {
	t.Parallel()
	aad := []byte("aad")
	version := "transit-0123456789abcdef-v1"
	newCache := func(t *testing.T) (*vault.UnwrapCache, *fakeProvider, *tClock) {
		t.Helper()
		p := &fakeProvider{name: "transit", version: version}
		c := newClock()
		cache, err := vault.NewUnwrapCache(p, 5*time.Minute, c.now)
		if err != nil {
			t.Fatalf("NewUnwrapCache: %v", err)
		}
		return cache, p, c
	}
	wrapped := append([]byte{0x7f}, bytes.Repeat([]byte{3}, 32)...)

	t.Run("the lifetime is bounded and a provider is required", func(t *testing.T) {
		p := &fakeProvider{name: "transit"}
		if _, err := vault.NewUnwrapCache(nil, time.Minute, nil); err == nil {
			t.Errorf("a nil provider")
		}
		for _, ttl := range []time.Duration{0, -time.Second, 999 * time.Millisecond, 15*time.Minute + time.Second, time.Hour} {
			if _, err := vault.NewUnwrapCache(p, ttl, nil); err == nil {
				t.Errorf("a lifetime of %v", ttl)
			}
		}
		for _, ttl := range []time.Duration{time.Second, 5 * time.Minute, 15 * time.Minute} {
			if _, err := vault.NewUnwrapCache(p, ttl, nil); err != nil {
				t.Errorf("a lifetime of %v: %v", ttl, err)
			}
		}
	})

	t.Run("a second identical call reaches no provider and each result is a copy", func(t *testing.T) {
		cache, p, _ := newCache(t)
		first, err := cache.Unwrap(ctx(t), version, wrapped, aad)
		if err != nil {
			t.Fatal(err)
		}
		want := bytes.Repeat([]byte{3}, 32)
		if !bytes.Equal(first, want) {
			t.Fatalf("Unwrap = %x", first)
		}
		clear(first) // the caller clears its copy
		second, err := cache.Unwrap(ctx(t), version, wrapped, aad)
		if err != nil || !bytes.Equal(second, want) {
			t.Fatalf("the second call: %x, %v; want the key although the first result was cleared", second, err)
		}
		if _, u, _ := p.calls(); u != 1 {
			t.Errorf("the provider got %d unwraps for 2 identical calls", u)
		}
		clear(second)
		if third, _ := cache.Unwrap(ctx(t), version, wrapped, aad); !bytes.Equal(third, want) {
			t.Errorf("the third result is %x", third)
		}
	})

	t.Run("a difference in version, wrapped key or associated data misses", func(t *testing.T) {
		cache, p, _ := newCache(t)
		calls := []struct {
			version string
			wrapped []byte
			aad     []byte
		}{
			{version, wrapped, aad},
			{"transit-0123456789abcdef-v2", wrapped, aad},
			{version, append(append([]byte(nil), wrapped...), 9), aad},
			{version, wrapped, []byte("other")},
			{version, wrapped, nil},
		}
		for i, c := range calls {
			if _, err := cache.Unwrap(ctx(t), c.version, c.wrapped, c.aad); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
			if _, u, _ := p.calls(); u != i+1 {
				t.Errorf("call %d reached the provider %d times in all, want %d", i, u, i+1)
			}
		}
		for i, c := range calls {
			if _, err := cache.Unwrap(ctx(t), c.version, c.wrapped, c.aad); err != nil {
				t.Fatal(err)
			}
			if _, u, _ := p.calls(); u != len(calls) {
				t.Errorf("repeating call %d reached the provider again", i)
			}
		}
	})

	t.Run("an entry is gone after the lifetime and its bytes are zeroed", func(t *testing.T) {
		cache, p, c := newCache(t)
		if _, err := cache.Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Fatal(err)
		}
		c.advance(4*time.Minute + 59*time.Second)
		if _, err := cache.Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Fatal(err)
		}
		if _, u, _ := p.calls(); u != 1 {
			t.Errorf("the entry was gone before its lifetime: %d unwraps", u)
		}
		c.advance(2 * time.Second)
		if _, err := cache.Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Fatal(err)
		}
		if _, u, _ := p.calls(); u != 2 {
			t.Errorf("the entry was used after its lifetime: %d unwraps", u)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.returns) != 2 || !bytes.Equal(p.returns[0], make([]byte, 32)) {
			t.Errorf("the bytes of the expired entry were not zeroed: %x", p.returns[0])
		}
	})

	t.Run("an expired entry is zeroed when another key is stored, without being looked up", func(t *testing.T) {
		cache, p, c := newCache(t)
		if _, err := cache.Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Fatal(err)
		}
		c.advance(6 * time.Minute)
		other := append([]byte{0x7f}, bytes.Repeat([]byte{9}, 32)...)
		if _, err := cache.Unwrap(ctx(t), version, other, aad); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.returns) != 2 || !bytes.Equal(p.returns[0], make([]byte, 32)) {
			t.Errorf("the bytes of the expired entry were left in memory: %x", p.returns[0])
		}
		if bytes.Equal(p.returns[1], make([]byte, 32)) {
			t.Errorf("the bytes of the live entry were zeroed")
		}
	})

	t.Run("the cache is emptied at its bound", func(t *testing.T) {
		cache, p, _ := newCache(t)
		unwrap := func(i int) {
			t.Helper()
			if _, err := cache.Unwrap(ctx(t), version, wrapped, []byte(fmt.Sprintf("aad-%d", i))); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 10000; i++ {
			unwrap(i)
		}
		_, u, _ := p.calls()
		unwrap(5000)
		if _, u2, _ := p.calls(); u2 != u {
			t.Errorf("an entry of a full cache was not found")
		}
		unwrap(10000) // the 10001st: the cache is emptied
		_, u, _ = p.calls()
		unwrap(5000)
		if _, u2, _ := p.calls(); u2 != u+1 {
			t.Errorf("the cache was not emptied at its bound")
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if !bytes.Equal(p.returns[5000], make([]byte, 32)) {
			t.Errorf("the bytes of a dropped entry were not zeroed")
		}
	})

	t.Run("an error is not kept", func(t *testing.T) {
		cache, p, _ := newCache(t)
		p.err = vault.ErrProviderUnavailable
		if _, err := cache.Unwrap(ctx(t), version, wrapped, aad); !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Fatalf("Unwrap: %v", err)
		}
		p.mu.Lock()
		p.err = nil
		p.mu.Unlock()
		if got, err := cache.Unwrap(ctx(t), version, wrapped, aad); err != nil || len(got) != 32 {
			t.Errorf("after the error: %x, %v", got, err)
		}
		if _, u, _ := p.calls(); u != 2 {
			t.Errorf("the error was kept: %d unwraps", u)
		}
	})

	t.Run("Wrap, Available and Name pass through", func(t *testing.T) {
		cache, p, _ := newCache(t)
		if cache.Name() != "transit" {
			t.Errorf("Name = %q", cache.Name())
		}
		if v, _, err := cache.Wrap(ctx(t), []byte("k"), aad); err != nil || v != version {
			t.Errorf("Wrap = %q, %v", v, err)
		}
		if err := cache.Available(ctx(t), version); err != nil {
			t.Errorf("Available: %v", err)
		}
		p.mu.Lock()
		p.err = vault.ErrUnavailable
		p.mu.Unlock()
		if err := cache.Available(ctx(t), version); !errors.Is(err, vault.ErrUnavailable) {
			t.Errorf("Available did not pass the error: %v", err)
		}
		if w, _, a := p.calls(); w != 1 || a != 2 {
			t.Errorf("the provider got %d wraps and %d Available calls, want 1 and 2", w, a)
		}
	})
}
