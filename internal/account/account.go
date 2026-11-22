// Package account is the sign-in of people with a local account: the check of a password, the
// throttling and lock of a username, the change and the reset of a password (sprint 4.2). Part A
// gives the package its names and no behaviour.
package account

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

var errNotImplemented = errors.New("account: not implemented")

// ErrThrottled is what a Refusal wraps when the sign-in is delayed or locked.
var ErrThrottled = errors.New("account: too many attempts")

// User is a person as the administration API knows it.
type User struct {
	ID, SignIn, DisplayName, Email, State, Username string
	TenantID                                        tenant.ID // the tenant that manages the account; zero: the platform
	MustChange                                      bool
}

// Session is one sign-in of a user. internal/session stores it; the type is defined here because
// the package session imports account (a lookup returns the User) and Go has no import cycles.
type Session struct {
	ID, UserID, Method               string
	CreatedAt, LastUsedAt, ExpiresAt time.Time
}

// SessionStore creates and ends the sessions that a sign-in produces; *session.Store is one.
type SessionStore interface {
	Create(ctx context.Context, tx store.Tx, userID, method string) (secret.Secret, Session, error)
	Delete(ctx context.Context, tx store.Tx, id string) error
	DeleteOfUser(ctx context.Context, tx store.Tx, userID, exceptID string) (int64, error)
}

// SignInResult is a successful sign-in: the user, the token of the new session (to be set as the
// cookie) and the session.
type SignInResult struct {
	User    User
	Token   secret.Secret
	Session Session
}

// Refusal is a refused sign-in. It wraps authz.ErrUnauthenticated, or ErrThrottled when RetryAfter
// is set. Reason is the audit reason (unknown_user, wrong_password, user_disabled, ...).
type Refusal struct {
	Reason     string
	UserID     string
	TenantID   tenant.ID
	RetryAfter time.Duration
}

func (r *Refusal) Error() string { return "account: sign-in refused: " + r.Reason }

// Unwrap returns the sentinel that errors.Is looks for.
func (r *Refusal) Unwrap() error {
	if r.RetryAfter > 0 {
		return ErrThrottled
	}
	return authz.ErrUnauthenticated
}

// PolicyError is a refusal of a password change that the API answers 400 invalid_request on the
// member Field (current_password or new_password), with Violation as the fixed sentence.
type PolicyError struct{ Field, Violation string }

func (e *PolicyError) Error() string { return "account: " + e.Field + " " + e.Violation }

// Options configure a Service.
type Options struct {
	DB       *store.DB
	Hasher   *password.Hasher
	Sessions SessionStore
	Now      func() time.Time

	MinLength                      int
	GeneratedPasswordTTL           time.Duration
	MaxFailures, SourceMaxFailures int
	FailureWindow, LockDuration    time.Duration
}

// Service signs people in and changes their passwords.
type Service struct{ o Options }

// NewService returns a Service.
func NewService(o Options) *Service { return &Service{o: o} }

// SignIn checks a username and a password from a source address and, when presented holds a
// session of the same user, replaces it.
func (s *Service) SignIn(ctx context.Context, username string, pw secret.Secret, source netip.Addr, presented string) (SignInResult, error) {
	_, _, _, _, _, _ = ctx, username, pw, source, presented, s
	return SignInResult{}, errNotImplemented
}

// ChangePassword changes the password of the user of sess and ends their other sessions.
func (s *Service) ChangePassword(ctx context.Context, sess Session, current, next secret.Secret, source netip.Addr) (SignInResult, error) {
	_, _, _, _, _, _ = ctx, sess, current, next, source, s
	return SignInResult{}, errNotImplemented
}

// ResetPassword gives the user a generated password that must be changed, and ends their sessions.
func (s *Service) ResetPassword(ctx context.Context, tx store.Tx, userID string) (secret.Secret, error) {
	_, _, _, _ = ctx, tx, userID, s
	return secret.Secret{}, errNotImplemented
}

// Unlock removes the lock and the failure count of a username.
func (s *Service) Unlock(ctx context.Context, tx store.Tx, username string) error {
	_, _, _, _ = ctx, tx, username, s
	return errNotImplemented
}

// BootstrapAdmin creates a platform-managed local user with the role platform-admin and a
// generated password; it returns the password and the ID of the user.
func BootstrapAdmin(ctx context.Context, tx store.Tx, h *password.Hasher, username string, ttl time.Duration) (secret.Secret, string, error) {
	_, _, _, _, _ = ctx, tx, h, username, ttl
	return secret.Secret{}, "", errNotImplemented
}

// Purge removes the throttle rows older than the failure window plus the lock duration and the
// sign-in transactions that have expired.
func (s *Service) Purge(ctx context.Context) error {
	_, _ = ctx, s
	return errNotImplemented
}
