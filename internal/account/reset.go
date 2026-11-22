package account

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ResetPassword gives the user a generated password that must be changed, ends their sessions and
// removes the throttle row of their username (section 5.4). tx is a session of the platform scope,
// or of the tenant that manages the user; in the second the throttle table is not visible and
// Unlock finishes the work in the platform scope. It returns the password, shown once, and the time
// it stops working.
func (s *Service) ResetPassword(ctx context.Context, tx store.Tx, userID string) (secret.Secret, time.Time, error) {
	q := queries.New(tx)
	u, err := q.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNoRows) {
		return secret.Secret{}, time.Time{}, ErrNoUser
	}
	if err != nil {
		return secret.Secret{}, time.Time{}, err
	}
	if u.SignIn != "local" {
		return secret.Secret{}, time.Time{}, ErrNotLocal
	}
	pw, err := password.Generate()
	if err != nil {
		return secret.Secret{}, time.Time{}, err
	}
	hctx, cancel := context.WithTimeout(ctx, hashWait)
	defer cancel()
	hash, err := s.o.Hasher.Hash(hctx, pw)
	if err != nil {
		return secret.Secret{}, time.Time{}, err
	}
	expires := s.o.Now().Add(s.o.GeneratedPasswordTTL).UTC().Truncate(time.Second)
	if _, err := q.SetPassword(ctx, queries.SetPasswordParams{PasswordHash: hash, MustChange: true, PasswordExpiresAt: &expires, UserID: userID}); err != nil {
		return secret.Secret{}, time.Time{}, err
	}
	if _, err := q.DeleteSessionsOfUser(ctx, queries.DeleteSessionsOfUserParams{UserID: userID}); err != nil {
		return secret.Secret{}, time.Time{}, err
	}
	if _, err := q.DeleteThrottle(ctx, UsernameKey(u.Username)); err != nil {
		return secret.Secret{}, time.Time{}, err
	}
	return pw, expires, nil
}

// Unlock removes the lock and the failure count of a username. tx is a session of the platform scope.
func (s *Service) Unlock(ctx context.Context, tx store.Tx, username string) error {
	_, err := queries.New(tx).DeleteThrottle(ctx, UsernameKey(username))
	return err
}
