package admin

// Handlers of the operations on sign-in and the caller's own sessions (sprint 4.2, sections 5.4 to
// 5.6). The two operations of the identity provider are written in step C8.

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// GetAuthMethods says which ways of signing in the listener offers.
func (s *server) GetAuthMethods(_ context.Context, _ adminapi.GetAuthMethodsInput) (*adminapi.AuthMethods, error) {
	m := &adminapi.AuthMethods{Local: adminapi.LocalAuthMethod{Enabled: s.authOpts.LocalEnabled}}
	if s.oidc != nil {
		m.Oidc = adminapi.OidcAuthMethod{Enabled: true, DisplayName: s.authOpts.OIDCDisplayName, StartPath: "/v1/auth/oidc/start"}
	}
	return m, nil
}

// CompleteOidcSignIn is not implemented yet.
func (s *server) CompleteOidcSignIn(_ context.Context, _ adminapi.CompleteOidcSignInInput) (*adminapi.Redirect, error) {
	return nil, errors.New("not implemented")
}

// StartOidcSignIn is not implemented yet.
func (s *server) StartOidcSignIn(_ context.Context, _ adminapi.StartOidcSignInInput) (*adminapi.Redirect, error) {
	return nil, errors.New("not implemented")
}

// SignIn checks a username and a password (section 5.5). A refusal is a 401 whatever the reason, a
// delayed or locked attempt a 429; neither says anything about the account.
func (s *server) SignIn(ctx context.Context, in adminapi.SignInInput) (*adminapi.Session, error) {
	if !s.authOpts.LocalEnabled {
		return nil, errSignInDisabled
	}
	if s.accounts == nil {
		return nil, errSignInDisabled
	}
	st := stateOf(ctx)
	res, err := s.accounts.SignIn(ctx, in.Body.Username, secret.New(in.Body.Password), st.source, st.presented)
	if err != nil {
		var r *account.Refusal
		if errors.As(err, &r) && r.RetryAfter == 0 {
			s.signInFailed(ctx, st, r)
		}
		return nil, err
	}
	setCookie(ctx, session.Cookie(res.Token))
	return s.sessionBody(res.Session, res.User, true), nil
}

// signInFailed logs a refused sign-in and records it in the audit log, within the cap of failures. The
// row names the user when the account exists and never the username that was typed.
func (s *server) signInFailed(ctx context.Context, st *reqState, r *account.Refusal) {
	audited := false
	if s.limiter.allow() {
		audited = s.recordSignInFailure(ctx, st, r) == nil
		if !audited {
			s.log.Debug("authentication failure not recorded", "request_id", st.id)
		}
	}
	s.log.Warn("authentication failed", "listener", "admin", "request_id", st.id, "reason", r.Reason, "audited", audited)
}

func (s *server) recordSignInFailure(ctx context.Context, st *reqState, r *account.Refusal) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	e := audit.Event{
		Actor:  audit.Actor{Type: audit.ActorAnonymous},
		Action: "auth.failure", ResourceType: "user", ResourceID: r.UserID, Outcome: "failure", RequestID: st.id,
		Detail: map[string]any{"reason": r.Reason, "method": "local", "listener": "admin"},
	}
	if !r.TenantID.IsZero() {
		return s.db.WithTenant(ctx, r.TenantID, func(ctx context.Context, tx store.Tx) error {
			return audit.RecordTenant(ctx, tx, r.TenantID, e)
		})
	}
	return s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error { return audit.RecordPlatform(ctx, tx, e) })
}

// SignOut ends the session of the request and clears the cookie.
func (s *server) SignOut(ctx context.Context, _ adminapi.SignOutInput) (*adminapi.SignedOut, error) {
	st := stateOf(ctx)
	if st.session == nil {
		panic("admin: signOut without a session")
	}
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error { return s.sessions.Delete(ctx, tx, st.session.ID) })
	if err != nil {
		return nil, err
	}
	setCookie(ctx, session.ClearCookie())
	return &adminapi.SignedOut{SignedOut: true}, nil
}

// ChangePassword changes the password of the user of the session; the session is replaced (section 5.4).
func (s *server) ChangePassword(ctx context.Context, in adminapi.ChangePasswordInput) (*adminapi.Session, error) {
	st := stateOf(ctx)
	if st.session == nil || st.user == nil {
		panic("admin: changePassword without a session")
	}
	res, err := s.accounts.ChangePassword(ctx, *st.session, secret.New(in.Body.CurrentPassword), secret.New(in.Body.NewPassword), st.source)
	if errors.Is(err, account.ErrNotLocal) {
		return nil, &stateError{fields: []adminapi.FieldError{{Field: "user", Message: "has no password"}}}
	}
	if err != nil {
		return nil, err
	}
	setCookie(ctx, session.Cookie(res.Token))
	return s.sessionBody(res.Session, res.User, true), nil
}

// ListOwnSessions lists the sessions of the user of the request, the current one marked.
func (s *server) ListOwnSessions(ctx context.Context, _ adminapi.ListOwnSessionsInput) (*adminapi.SessionList, error) {
	st := stateOf(ctx)
	if st.session == nil || st.user == nil {
		panic("admin: listOwnSessions without a session")
	}
	var rows []queries.ListSessionsOfUserRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListSessionsOfUser(ctx, st.user.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.SessionList{Items: []adminapi.Session{}}
	for _, r := range rows {
		sess := session.Session{ID: r.ID, UserID: r.UserID, Method: r.Method, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt}
		out.Items = append(out.Items, *s.sessionBody(sess, *st.user, sess.ID == st.session.ID))
	}
	return out, nil
}

// DeleteOwnSession ends one session of the user of the request; another user's is a 404.
func (s *server) DeleteOwnSession(ctx context.Context, in adminapi.DeleteOwnSessionInput) (*adminapi.Session, error) {
	st := stateOf(ctx)
	if st.session == nil || st.user == nil {
		panic("admin: deleteOwnSession without a session")
	}
	if _, err := tenant.ParseID(in.Session); err != nil {
		return nil, errNotFound
	}
	var out *adminapi.Session
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		rows, err := q.ListSessionsOfUser(ctx, st.user.ID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.ID != in.Session {
				continue
			}
			sess := session.Session{ID: r.ID, UserID: r.UserID, Method: r.Method, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt}
			out = s.sessionBody(sess, *st.user, sess.ID == st.session.ID)
		}
		if out == nil {
			return errNotFound
		}
		_, err = q.DeleteSessionOfUser(ctx, queries.DeleteSessionOfUserParams{ID: in.Session, UserID: st.user.ID})
		return err
	})
	if err != nil {
		return nil, err
	}
	if out.Current {
		setCookie(ctx, session.ClearCookie())
	}
	return out, nil
}
