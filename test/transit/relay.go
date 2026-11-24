//go:build transit

package transit

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Relay is a TCP relay in front of a service. It makes the outages of a real service that a real
// service cannot be asked to make (it is never sealed): it stops accepting, accepts and never
// answers, or delays every chunk.
type Relay struct {
	tb       testing.TB
	upstream string
	addr     string

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	mode   string // "open", "blackhole"
	delay  time.Duration
	closed bool
}

// Listen starts a relay on a loopback port that forwards to upstream (a host:port).
func Listen(tb testing.TB, upstream string) *Relay {
	tb.Helper()
	r := &Relay{tb: tb, upstream: upstream, conns: map[net.Conn]struct{}{}, mode: "open"}
	r.listen("127.0.0.1:0")
	tb.Cleanup(r.close)
	return r
}

// Addr is the address the relay listens on, host:port.
func (r *Relay) Addr() string { return r.addr }

func (r *Relay) listen(addr string) {
	var ln net.Listener
	var err error
	var lc net.ListenConfig
	for i := 0; i < 100; i++ {
		if ln, err = lc.Listen(context.Background(), "tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		r.tb.Fatalf("relay: %v", err)
	}
	r.mu.Lock()
	r.ln, r.addr = ln, ln.Addr().String()
	r.mu.Unlock()
	go r.accept(ln)
}

func (r *Relay) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			_ = c.Close()
			return
		}
		r.conns[c] = struct{}{}
		mode := r.mode
		r.mu.Unlock()
		if mode == "blackhole" {
			go func() { _, _ = io.Copy(io.Discard, c) }()
			continue
		}
		go r.forward(c)
	}
}

func (r *Relay) forward(c net.Conn) {
	d := net.Dialer{Timeout: 3 * time.Second}
	up, err := d.DialContext(context.Background(), "tcp", r.upstream)
	if err != nil {
		_ = c.Close()
		return
	}
	r.mu.Lock()
	r.conns[up] = struct{}{}
	r.mu.Unlock()
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 16<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				r.mu.Lock()
				delay := r.delay
				r.mu.Unlock()
				if delay > 0 {
					time.Sleep(delay)
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		_ = dst.Close()
		_ = src.Close()
	}
	go pipe(up, c)
	go pipe(c, up)
}

func (r *Relay) dropAll() {
	r.mu.Lock()
	for c := range r.conns {
		_ = c.Close()
	}
	r.conns = map[net.Conn]struct{}{}
	r.mu.Unlock()
}

// Refuse stops accepting: connections are refused, and the open ones are closed.
func (r *Relay) Refuse() {
	r.mu.Lock()
	ln := r.ln
	r.ln = nil
	r.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	r.dropAll()
}

// Blackhole accepts connections and never answers; the open ones are closed.
func (r *Relay) Blackhole() {
	r.mu.Lock()
	r.mode = "blackhole"
	have := r.ln != nil
	addr := r.addr
	r.mu.Unlock()
	r.dropAll()
	if !have {
		r.listen(addr)
	}
}

// Delay delays every chunk in both directions by d.
func (r *Relay) Delay(d time.Duration) {
	r.mu.Lock()
	r.delay = d
	r.mu.Unlock()
}

// Open forwards again, on the same address, without delay.
func (r *Relay) Open() {
	r.mu.Lock()
	r.mode, r.delay = "open", 0
	have := r.ln != nil
	addr := r.addr
	r.mu.Unlock()
	r.dropAll()
	if !have {
		r.listen(addr)
	}
}

func (r *Relay) close() {
	r.mu.Lock()
	r.closed = true
	ln := r.ln
	r.ln = nil
	r.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	r.dropAll()
}
