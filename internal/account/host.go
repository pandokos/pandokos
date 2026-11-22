package account

// What the host commands use (sprint 4.2, section 5.13): the first administrator that init creates, and
// the reset and the unlock that the commands of the host make, audited with the actor host.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// ErrPlatformRoleExists is returned by BootstrapAdmin when an active user already holds a platform role.
var ErrPlatformRoleExists = errors.New("an active user with a platform role exists already: create further users through the administration API")

// ErrAuditIncomplete is returned by a host command whose change was committed and whose audit row in
// the tenant's log could not be written (the platform's row is in the transaction of the change).
var ErrAuditIncomplete = errors.New("the change was made, but its audit row in the tenant's log was not written")

// HostActor is the actor of what a host command writes to the audit log.
func HostActor(command string) audit.Actor { return audit.Actor{Type: audit.ActorHost, Name: command} }

// BootstrapAdmin creates a platform-managed local user with the role platform-admin and a generated
// password that must be changed within ttl, in the transaction tx of the platform scope; it returns
// the password and the ID of the user. It is refused when an active user holds a platform role. The
// audit rows are the host's.
func BootstrapAdmin(ctx context.Context, tx store.Tx, h *password.Hasher, username string, ttl time.Duration) (secret.Secret, string, error) {
	q := queries.New(tx)
	n, err := q.CountPlatformRoleHolders(ctx, authz.RolePlatformAdmin)
	if err != nil {
		return secret.Secret{}, "", err
	}
	if n > 0 {
		return secret.Secret{}, "", ErrPlatformRoleExists
	}
	pw, err := password.Generate()
	if err != nil {
		return secret.Secret{}, "", err
	}
	hctx, cancel := context.WithTimeout(ctx, hashWait)
	defer cancel()
	hash, err := h.Hash(hctx, pw)
	if err != nil {
		return secret.Secret{}, "", err
	}
	u, err := q.CreateUser(ctx, queries.CreateUserParams{SignIn: "local"})
	if err != nil {
		return secret.Secret{}, "", err
	}
	expires := time.Now().Add(ttl).UTC().Truncate(time.Second)
	if err := q.CreateLocalAccount(ctx, queries.CreateLocalAccountParams{UserID: u.ID, Username: username, PasswordHash: hash, MustChange: true, PasswordExpiresAt: &expires}); err != nil {
		return secret.Secret{}, "", err
	}
	if err := q.AddPlatformRole(ctx, queries.AddPlatformRoleParams{UserID: u.ID, Role: authz.RolePlatformAdmin, Source: "manual"}); err != nil {
		return secret.Secret{}, "", err
	}
	actor := HostActor("pandokos init")
	for _, e := range []audit.Event{
		{Actor: actor, Action: "user.create", ResourceType: "user", ResourceID: u.ID, Outcome: "success", Detail: map[string]any{"sign_in": "local", "username": username}},
		{Actor: actor, Action: "platform_role.assign", ResourceType: "user", ResourceID: u.ID, Outcome: "success", Detail: map[string]any{"role": authz.RolePlatformAdmin, "source": "manual"}},
	} {
		if err := audit.RecordPlatform(ctx, tx, e); err != nil {
			return secret.Secret{}, "", err
		}
	}
	return pw, u.ID, nil
}

// HasAdministrator reports whether an active user holds a platform role.
func HasAdministrator(ctx context.Context, db *store.DB) (bool, error) {
	var n int32
	err := db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		n, err = queries.New(tx).CountPlatformRoleHolders(ctx, authz.RolePlatformAdmin)
		return err
	})
	return n > 0, err
}

// HostReset gives the local account of username a generated password that must be changed, and ends
// its sessions, as the host command does: the audit row is written with the actor host, in the
// platform log and, for a user that a tenant manages, in that tenant's log too. beforeCommit runs
// inside the transaction after the change, so that the caller can write the password to a file and
// roll the change back when it cannot. It returns ErrNoUser when no account has the username, and the
// password together with ErrAuditIncomplete when the change was made.
func (s *Service) HostReset(ctx context.Context, username string, beforeCommit func(pw secret.Secret) error) (secret.Secret, error) {
	g, err := s.Generate(ctx)
	if err != nil {
		return secret.Secret{}, err
	}
	var pw secret.Secret
	err = s.host(ctx, username, "user.password_reset", func(ctx context.Context, tx store.Tx, userID string) error {
		var err error
		if pw, _, err = s.ResetPassword(ctx, tx, userID, g); err != nil {
			return err
		}
		return beforeCommit(pw)
	})
	if err != nil && !errors.Is(err, ErrAuditIncomplete) {
		return secret.Secret{}, err
	}
	return pw, err
}

// HostUnlock removes the lock and the failure count of the account of username, as the host command does.
func (s *Service) HostUnlock(ctx context.Context, username string) error {
	return s.host(ctx, username, "user.unlock", func(ctx context.Context, tx store.Tx, _ string) error {
		return s.Unlock(ctx, tx, username)
	})
}

func (s *Service) host(ctx context.Context, username, action string, do func(ctx context.Context, tx store.Tx, userID string) error) error {
	username = strings.ToLower(username)
	var managing tenant.ID
	var userID string
	err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		acct, err := queries.New(tx).GetAccountForSignIn(ctx, username)
		if errors.Is(err, store.ErrNoRows) {
			return ErrNoUser
		}
		if err != nil {
			return err
		}
		userID = acct.UID
		if managing, err = parseManaging(acct.ManagingTenantID); err != nil {
			return err
		}
		if err := do(ctx, tx, userID); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, audit.Event{Actor: HostActor("pandokos user"), Action: action, ResourceType: "user", ResourceID: userID, Outcome: "success"})
	})
	if err != nil || managing.IsZero() {
		return err
	}
	err = s.o.DB.WithTenant(ctx, managing, func(ctx context.Context, tx store.Tx) error {
		return audit.RecordTenant(ctx, tx, managing, audit.Event{Actor: HostActor("pandokos user"), Action: action, ResourceType: "user", ResourceID: userID, Outcome: "success"})
	})
	if err != nil {
		return ErrAuditIncomplete
	}
	return nil
}
