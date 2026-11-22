// Package session keeps the sessions of people on the administration listener: the token, its
// hash in the database, the lifetimes and the cookie (sprint 4.2).
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// CookieName is the name of the session cookie; the prefix makes a browser refuse it unless it is
// Secure, has Path=/ and no Domain. TokenPrefix starts every session token.
const (
	CookieName  = "__Host-pandokos_session"
	TokenPrefix = "pds_"
)

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
func NewStore(o Options) *Store {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Store{o: o}
}

const tokenBytes = 32

var b64url = base64.RawURLEncoding

// IdleTimeout is the time without use after which a session ends.
func (s *Store) IdleTimeout() time.Duration { return s.o.Idle }

// NewToken returns a new session token and its SHA-256, the value stored: the prefix and 32 random
// bytes in unpadded base64url.
func NewToken() (token secret.Secret, hash [32]byte, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return secret.Secret{}, hash, fmt.Errorf("session: random: %w", err)
	}
	t := TokenPrefix + b64url.EncodeToString(b)
	return secret.New(t), sha256.Sum256([]byte(t)), nil
}

// ParseToken checks the shape of a presented token and returns the SHA-256 that names its session.
func ParseToken(presented string) (hash [32]byte, ok bool) {
	if len(presented) != len(TokenPrefix)+b64url.EncodedLen(tokenBytes) || presented[:len(TokenPrefix)] != TokenPrefix {
		return hash, false
	}
	for i := len(TokenPrefix); i < len(presented); i++ {
		c := presented[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return hash, false
		}
	}
	return sha256.Sum256([]byte(presented)), true
}

func fromRow(id, userID, method string, created, used, expires time.Time) Session {
	return Session{ID: id, UserID: userID, Method: method, CreatedAt: created, LastUsedAt: used, ExpiresAt: expires}
}

// Create makes a session of the user in the transaction tx (platform scope) and returns its token,
// once. The user's sessions beyond MaxPerUser are removed, the oldest first.
func (s *Store) Create(ctx context.Context, tx store.Tx, userID, method string) (secret.Secret, Session, error) {
	tok, hash, err := NewToken()
	if err != nil {
		return secret.Secret{}, Session{}, err
	}
	q := queries.New(tx)
	row, err := q.CreateSession(ctx, queries.CreateSessionParams{
		TokenHash: hash[:], UserID: userID, Method: method, ExpiresAt: s.o.Now().Add(s.o.Absolute),
	})
	if err != nil {
		return secret.Secret{}, Session{}, err
	}
	if s.o.MaxPerUser > 0 {
		if err := q.DeleteOldestSessions(ctx, queries.DeleteOldestSessionsParams{UserID: userID, Offset: int32(s.o.MaxPerUser)}); err != nil { //nolint:gosec // a small configured bound
			return secret.Secret{}, Session{}, err
		}
	}
	return tok, fromRow(row.ID, row.UserID, row.Method, row.CreatedAt, row.LastUsedAt, row.ExpiresAt), nil
}

// Lookup finds the live session that presented names, with its user, in one statement of the
// platform scope; ErrNoSession otherwise: no such session, expired, idle for too long, or a user
// that is not active.
func (s *Store) Lookup(ctx context.Context, presented string) (Session, account.User, error) {
	hash, ok := ParseToken(presented)
	if !ok {
		return Session{}, account.User{}, ErrNoSession
	}
	var row queries.GetSessionPrincipalRow
	err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetSessionPrincipal(ctx, hash[:])
		return err
	})
	if errors.Is(err, store.ErrNoRows) {
		return Session{}, account.User{}, ErrNoSession
	}
	if err != nil {
		return Session{}, account.User{}, err
	}
	now := s.o.Now()
	if !now.Before(row.ExpiresAt) || now.Sub(row.LastUsedAt) > s.o.Idle {
		// A session that has run out ends: ending is deleting the row. A failure here only leaves
		// the row to the purge; the answer is the same.
		_ = s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error { return s.Delete(ctx, tx, row.SID) })
		return Session{}, account.User{}, ErrNoSession
	}
	if row.State != "active" {
		return Session{}, account.User{}, ErrNoSession
	}
	u := account.User{
		ID: row.SUserID, SignIn: row.SignIn, DisplayName: row.DisplayName, State: row.State, Username: row.Username,
		MustChange: row.MustChange, PlatformRoles: row.PlatformRoles,
	}
	if row.ManagingTenantID != "" {
		if u.TenantID, err = tenant.ParseID(row.ManagingTenantID); err != nil {
			return Session{}, account.User{}, err
		}
	}
	return fromRow(row.SID, row.SUserID, row.Method, row.CreatedAt, row.LastUsedAt, row.ExpiresAt), u, nil
}

// Touch writes last_used_at of the session when the last write is more than a minute old, in a
// transaction of its own.
func (s *Store) Touch(ctx context.Context, id string) error {
	return s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		return queries.New(tx).TouchSession(ctx, id)
	})
}

// Delete ends one session.
func (s *Store) Delete(ctx context.Context, tx store.Tx, id string) error {
	_, err := queries.New(tx).DeleteSession(ctx, id)
	return err
}

// DeleteOfUser ends the sessions of a user, except exceptID when it is not empty.
func (s *Store) DeleteOfUser(ctx context.Context, tx store.Tx, userID, exceptID string) (int64, error) {
	arg := queries.DeleteSessionsOfUserParams{UserID: userID}
	if exceptID != "" {
		arg.ExceptID = &exceptID
	}
	return queries.New(tx).DeleteSessionsOfUser(ctx, arg)
}

// Purge removes the sessions that have expired or been idle too long.
func (s *Store) Purge(ctx context.Context) error {
	idle := store.Interval(s.o.Idle)
	return s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := queries.New(tx).PurgeSessions(ctx, idle)
		return err
	})
}

// Cookie returns the cookie that carries token: the attributes never vary, loopback included.
func Cookie(token secret.Secret) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: token.Reveal(), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

// ClearCookie returns the cookie that makes the browser forget the session.
func ClearCookie() *http.Cookie {
	return &http.Cookie{Name: CookieName, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}
