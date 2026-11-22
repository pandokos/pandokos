// Package account is the sign-in of people with a local account: the check of a password, the
// throttling and lock of a username, the change and the reset of a password (sprint 4.2).
package account

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// ErrNotLocal is returned for an operation that needs a local account on a user that has none.
var ErrNotLocal = errors.New("account: the user has no local account")

// ErrNoUser is returned when no account has the username or ID.
var ErrNoUser = errors.New("account: no such user")

// ErrThrottled is what a Refusal wraps when the sign-in is delayed or locked.
var ErrThrottled = errors.New("account: too many attempts")

// The results of a sign-in as the metrics count them (pandokos.result of pandokos.signin.attempts):
// the closed list the components map their errors to. No other value is used.
const (
	SignInOK          = "ok"          // a session was opened
	SignInFailed      = "failed"      // wrong credentials or unknown user
	SignInLocked      = "locked"      // the account or the source is locked
	SignInDisabled    = "disabled"    // the account or the user is disabled
	SignInUnavailable = "unavailable" // the hasher or the database could not answer
	SignInRefused     = "refused"     // OIDC: state, nonce, token or mapping refused
)

// SignInResults lists every result of a sign-in, in a fixed order.
var SignInResults = []string{SignInOK, SignInFailed, SignInLocked, SignInDisabled, SignInUnavailable, SignInRefused}

// User is a person as the administration API knows it.
type User struct {
	ID, SignIn, DisplayName, Email, State, Username string
	TenantID                                        tenant.ID // the tenant that manages the account; zero: the platform
	MustChange                                      bool
	PlatformRoles                                   []string // the roles of platform_role_assignment; filled by a session lookup
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
	// DeletePresented ends the session that the presented token names when it is a session of userID.
	DeletePresented(ctx context.Context, tx store.Tx, presented, userID string) error
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
func NewService(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// Purge removes the throttle rows older than the failure window plus the lock duration and the
// sign-in transactions that have expired.
func (s *Service) Purge(ctx context.Context) error {
	keep := store.Interval(s.o.FailureWindow + s.o.LockDuration)
	return s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if _, err := q.PurgeThrottle(ctx, keep); err != nil {
			return err
		}
		_, err := q.PurgeOidcLogins(ctx)
		return err
	})
}
