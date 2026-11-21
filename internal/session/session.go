// Package session keeps the sessions of people on the administration listener: the token, its
// hash in the database, the lifetimes and the cookie (sprint 4.2). Part A gives the package its
// names and no behaviour.
package session

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
)

// CookieName is the name of the session cookie; the prefix makes a browser refuse it unless it is
// Secure, has Path=/ and no Domain. TokenPrefix starts every session token.
const (
	CookieName  = "__Host-pandokos_session"
	TokenPrefix = "pds_"
)

var errNotImplemented = errors.New("session: not implemented")

// ErrNoSession is returned by Lookup for a token that names no live session.
var ErrNoSession = errors.New("session: no such session")

// Session is one sign-in of a user; the type is account.Session.
type Session = account.Session

// Options configure a Store.
type Options struct {
	DB             *store.DB
	Now            func() time.Time
	Idle, Absolute time.Duration
	MaxPerUser     int
}

// Store creates, looks up and ends sessions.
type Store struct{ o Options }

// NewStore returns a Store.
func NewStore(o Options) *Store { return &Store{o: o} }

// NewToken returns a new session token and its SHA-256, the value stored.
func NewToken() (token secret.Secret, hash [32]byte, err error) {
	return secret.Secret{}, hash, errNotImplemented
}

// ParseToken checks the shape of a presented token and returns the SHA-256 that names its session.
func ParseToken(presented string) (hash [32]byte, ok bool) {
	_ = presented
	return hash, false
}

// Create makes a session of the user and returns its token, once.
func (s *Store) Create(ctx context.Context, tx store.Tx, userID, method string) (secret.Secret, Session, error) {
	_, _, _, _, _ = ctx, tx, userID, method, s
	return secret.Secret{}, Session{}, errNotImplemented
}

// Lookup finds the live session that presented names, with its user; ErrNoSession otherwise.
func (s *Store) Lookup(ctx context.Context, presented string) (Session, account.User, error) {
	_, _, _ = ctx, presented, s
	return Session{}, account.User{}, errNotImplemented
}

// Delete ends one session.
func (s *Store) Delete(ctx context.Context, tx store.Tx, id string) error {
	_, _, _, _ = ctx, tx, id, s
	return errNotImplemented
}

// DeleteOfUser ends the sessions of a user, except exceptID when it is not empty.
func (s *Store) DeleteOfUser(ctx context.Context, tx store.Tx, userID, exceptID string) (int64, error) {
	_, _, _, _, _ = ctx, tx, userID, exceptID, s
	return 0, errNotImplemented
}

// Purge removes the sessions that have expired or been idle too long.
func (s *Store) Purge(ctx context.Context) error {
	_, _ = ctx, s
	return errNotImplemented
}

// Cookie returns the cookie that carries token.
func Cookie(token secret.Secret) *http.Cookie {
	_ = token
	return &http.Cookie{Name: CookieName}
}

// ClearCookie returns the cookie that makes the browser forget the session.
func ClearCookie() *http.Cookie { return &http.Cookie{Name: CookieName, MaxAge: -1} }
