package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// The text of the warning that notifications do not arrive.
const unavailableDetail = "notifications do not arrive on this connection; revocation takes up to cache.principal_ttl; set database.direct_address when database.address is a pooler in transaction mode"

const (
	probeWait    = 5 * time.Second
	idleProbe    = 30 * time.Second
	backoffStart = time.Second
	backoffMax   = 30 * time.Second
)

// ListenerOptions configure a Listener.
type ListenerOptions struct {
	Conn     ConnOptions // the gateway user; Host and Port are the direct address
	Role     Role
	Direct   bool // database.direct_address is set; only for the log line
	Logger   *slog.Logger
	OnNotify func(payload string) // called for every payload that is not this listener's probe
	OnReset  func()               // called when notifications may have been missed
}

// Listener holds one dedicated connection that listens on the cache channel. LISTEN is session
// state, the one exception to the rule that no session state exists (ADR 0027).
type Listener struct{ o ListenerOptions }

// NewListener validates o.
func NewListener(o ListenerOptions) (*Listener, error) {
	switch {
	case o.Logger == nil:
		return nil, errors.New("store: the listener needs a logger")
	case o.OnNotify == nil || o.OnReset == nil:
		return nil, errors.New("store: the listener needs OnNotify and OnReset")
	}
	if _, err := connConfig(o.Conn, "pandokos-"+string(o.Role)+"-listen"); err != nil {
		return nil, err
	}
	return &Listener{o: o}, nil
}

// probe sends a notification from a second connection and waits for it on the listening one.
// Sending it on the listening connection itself would succeed behind a transaction pooler that
// happens to give the same server connection back, and say nothing about notifications of other
// sessions: they are the ones that must arrive.
func (l *Listener) probe(ctx context.Context, conn *pgx.Conn) bool {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return false
	}
	nonce := "probe:" + hex.EncodeToString(b[:])
	cfg, err := connConfig(l.o.Conn, "pandokos-"+string(l.o.Role)+"-listen-probe")
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, l.o.Conn.ConnectTimeout)
	aux, err := pgx.ConnectConfig(cctx, cfg)
	cancel()
	if err != nil {
		return false
	}
	_, err = aux.Exec(ctx, "SELECT pg_catalog.pg_notify('pandokos_cache', $1)", nonce)
	cc, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	_ = aux.Close(cc)
	cancelClose()
	if err != nil {
		return false
	}
	wctx, cancel := context.WithTimeout(ctx, probeWait)
	defer cancel()
	for {
		n, err := conn.WaitForNotification(wctx)
		if err != nil {
			return false
		}
		if n.Payload == nonce {
			return true
		}
		if len(n.Payload) < 6 || n.Payload[:6] != "probe:" {
			l.o.OnNotify(n.Payload)
		}
	}
}

// Run connects, listens and reconnects until ctx is done. It never returns an error: the cache
// lifetime is the guarantee, the listener only shortens it.
func (l *Listener) Run(ctx context.Context) {
	log := l.o.Logger
	backoff := backoffStart
	warned := false
	wait := func() {
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > backoffMax {
			backoff = backoffMax
		}
	}
	for ctx.Err() == nil {
		cfg, err := connConfig(l.o.Conn, "pandokos-"+string(l.o.Role)+"-listen")
		if err != nil {
			log.Warn("cache listener disconnected", "role", string(l.o.Role), "error", err.Error())
			wait()
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, l.o.Conn.ConnectTimeout)
		conn, err := pgx.ConnectConfig(cctx, cfg)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("cache listener disconnected", "role", string(l.o.Role), "error", err.Error())
			}
			wait()
			continue
		}
		closeConn := func() {
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = conn.Close(c)
		}
		if _, err := conn.Exec(ctx, "LISTEN pandokos_cache"); err != nil {
			closeConn()
			if ctx.Err() == nil {
				log.Warn("cache listener disconnected", "role", string(l.o.Role), "error", err.Error())
			}
			wait()
			continue
		}
		l.o.OnReset()
		log.Info("cache listener connected", "role", string(l.o.Role), "direct", l.o.Direct)
		if !l.probe(ctx, conn) {
			if ctx.Err() == nil && !warned {
				warned = true
				log.Warn("cache invalidation unavailable", "role", string(l.o.Role), "detail", unavailableDetail)
			}
			closeConn()
			l.o.OnReset()
			wait()
			continue
		}
		warned = false
		backoff = backoffStart
		for ctx.Err() == nil {
			wctx, cancel := context.WithTimeout(ctx, idleProbe)
			n, err := conn.WaitForNotification(wctx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				if errors.Is(err, context.DeadlineExceeded) && l.probe(ctx, conn) {
					continue
				}
				break
			}
			if len(n.Payload) >= 6 && n.Payload[:6] == "probe:" {
				continue
			}
			l.o.OnNotify(n.Payload)
		}
		closeConn()
		l.o.OnReset()
		if ctx.Err() == nil {
			log.Warn("cache listener disconnected", "role", string(l.o.Role), "error", "connection lost")
			wait()
		}
	}
}
