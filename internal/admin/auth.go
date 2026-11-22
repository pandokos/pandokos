package admin

// Handlers of the operations on sign-in and the caller's own sessions (sprint 4.2, sections 5.4 to
// 5.6). The two operations of the identity provider are written in step C8.

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/oidc"
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

// oidcStateTTL is how long a sign-in transaction lives; maxOidcLogins bounds the live ones.
const (
	oidcStateTTL  = 10 * time.Minute
	maxOidcLogins = 10000
)

// StartOidcSignIn sends the browser to the identity provider (section 5.8): one transaction row, the
// seed in a cookie that binds the flow to this browser, and a 303 to the authorization endpoint.
func (s *server) StartOidcSignIn(ctx context.Context, _ adminapi.StartOidcSignInInput) (*adminapi.Redirect, error) {
	if s.oidc == nil {
		return nil, errSignInDisabled
	}
	var live int32
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		live, err = queries.New(tx).CountOidcLogins(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if live >= maxOidcLogins {
		return nil, &retryError{code: adminapi.ProblemCodeTooManyAttempts, after: time.Minute}
	}
	redirect, seed, hash, err := s.oidc.Start(ctx)
	if err != nil {
		return nil, err
	}
	err = s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		return queries.New(tx).CreateOidcLogin(ctx, queries.CreateOidcLoginParams{StateHash: hash[:], ExpiresAt: s.now().Add(oidcStateTTL)})
	})
	if err != nil {
		return nil, err
	}
	setCookie(ctx, oidc.SeedCookie(seed))
	return &adminapi.Redirect{Location: redirect}, nil
}

// CompleteOidcSignIn is the callback of the identity provider (section 5.8): the binding cookie and the
// state, the one-use transaction, the code and the ID token, the user, the rules, the session. It
// always answers a redirect and clears the seed cookie; nothing of the provider's answer reaches the
// response.
func (s *server) CompleteOidcSignIn(ctx context.Context, in adminapi.CompleteOidcSignInInput) (*adminapi.Redirect, error) {
	if s.oidc == nil {
		return nil, errSignInDisabled
	}
	st := stateOf(ctx)
	setCookie(ctx, oidc.ClearSeedCookie())
	failed := func(reason, userID string, tid tenant.ID) (*adminapi.Redirect, error) {
		s.stats.oidc.Add("oidc", account.SignInRefused)
		s.oidcFailed(ctx, st, reason, userID, "", tid)
		return s.postSignIn("sign_in_failed"), nil
	}
	query := url.Values{}
	for k, v := range map[string]string{"code": in.Code, "state": in.State, "error": in.Error, "iss": in.Iss} {
		if v != "" {
			query.Set(k, v)
		}
	}
	hash, err := oidc.StateHash(st.seed, query)
	if err != nil {
		return failed(oidcReason(err), "", tenant.ID{})
	}
	var taken int64
	err = s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		taken, err = queries.New(tx).TakeOidcLogin(ctx, hash[:])
		return err
	})
	if err != nil {
		return s.oidcUnavailable(st, err), nil
	}
	if taken == 0 {
		return failed("state_unknown", "", tenant.ID{})
	}
	id, err := s.oidc.Complete(ctx, st.seed, query)
	if err != nil {
		var f *oidc.Failure
		if errors.As(err, &f) {
			return failed(f.Reason, "", tenant.ID{})
		}
		return s.oidcUnavailable(st, err), nil
	}
	in2, err := s.federatedInput(ctx, id, st.presented)
	if err != nil {
		return s.oidcUnavailable(st, err), nil
	}
	res, err := s.accounts.SignInFederated(ctx, in2)
	if err != nil {
		var r *account.Refusal
		if errors.As(err, &r) {
			// The token was validated: the subject is the provider's word and goes to the audit row.
			s.stats.oidc.Add("oidc", account.SignInRefused)
			s.oidcFailed(ctx, st, r.Reason, r.UserID, id.Subject, r.TenantID)
			return s.postSignIn("sign_in_failed"), nil
		}
		return s.oidcUnavailable(st, err), nil
	}
	s.stats.oidc.Add("oidc", account.SignInOK)
	setCookie(ctx, session.Cookie(res.Token))
	return &adminapi.Redirect{Location: s.postSignInPath()}, nil
}

func oidcReason(err error) string {
	var f *oidc.Failure
	if errors.As(err, &f) {
		return f.Reason
	}
	return "provider_error"
}

// oidcUnavailable answers an outage of the provider or the database with the redirect that says so,
// and logs the class of the error, never the error itself.
func (s *server) oidcUnavailable(st *reqState, err error) *adminapi.Redirect {
	s.stats.oidc.Add("oidc", account.SignInUnavailable)
	if errors.Is(err, authz.ErrUnavailable) || store.IsUnavailable(err) {
		s.log.Warn("sign-in through the identity provider unavailable", "request_id", st.id)
		return s.postSignIn("unavailable")
	}
	st.errCode = adminapi.ProblemCodeInternal
	attrs := []any{"request_id", st.id, "operation", st.operation}
	if sqlstate := store.SQLState(err); sqlstate != "" {
		attrs = append(attrs, "sqlstate", sqlstate)
	}
	s.log.Error("internal error", attrs...)
	return s.postSignIn("sign_in_failed")
}

func (s *server) postSignInPath() string {
	if s.authOpts.PostSignInPath == "" {
		return "/"
	}
	return s.authOpts.PostSignInPath
}

// postSignIn is the redirect of a refused sign-in: the configured path with sign_in_error.
func (s *server) postSignIn(reason string) *adminapi.Redirect {
	p := s.postSignInPath()
	sep := "?"
	if strings.Contains(p, "?") {
		sep = "&"
	}
	return &adminapi.Redirect{Location: p + sep + "sign_in_error=" + reason}
}

// federatedInput resolves the group rules for this sign-in: the tenants that rules name are looked up
// each time, and a rule whose tenant no longer exists is skipped with a log line that gives its index.
func (s *server) federatedInput(ctx context.Context, id oidc.Identity, presented string) (account.FederatedInput, error) {
	in := account.FederatedInput{
		Issuer: s.oidc.Issuer(), Subject: id.Subject, DisplayName: id.Name, Email: id.Email,
		AutoProvision: s.authOpts.AutoProvision, Reconcile: len(s.authOpts.GroupRules) > 0, Presented: presented,
	}
	rules := s.authOpts.GroupRules
	if len(rules) == 0 {
		return in, nil
	}
	var names []string
	for _, r := range rules {
		if !r.Platform {
			names = append(names, r.Tenant)
		}
	}
	ids := map[string]string{}
	if len(names) > 0 {
		var rows []queries.TenantIDsByNameRow
		err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			rows, err = queries.New(tx).TenantIDsByName(ctx, names)
			return err
		})
		if err != nil {
			return in, err
		}
		for _, r := range rows {
			ids[r.Name] = r.ID
		}
	}
	// The custom roles that the rules name, by tenant: a rule whose role the tenant lacks or has
	// deactivated gives nothing (section 5.3).
	customOf := map[string][]string{}
	for _, r := range rules {
		if _, ok := ids[r.Tenant]; !r.Platform && ok {
			if _, custom := ruleRole(r); custom {
				customOf[r.Tenant] = append(customOf[r.Tenant], r.Role)
			}
		}
	}
	states := map[string]map[string]string{}
	for name, roles := range customOf {
		tid, err := tenant.ParseID(ids[name])
		if err != nil {
			return in, err
		}
		if states[name], err = customRolesOf(ctx, s.db, tid, roles); err != nil {
			return in, err
		}
	}
	live := make([]oidc.Rule, 0, len(rules))
	position := make([]int, 0, len(rules)) // of each live rule, in the configuration
	for i, r := range rules {
		if !r.Platform {
			// The tenant is gone, or the custom role is missing or inactive: the rule gives nothing, and
			// the line names the position of the rule only.
			_, ok := ids[r.Tenant]
			if _, custom := ruleRole(r); !ok || (custom && states[r.Tenant][r.Role] != "active") {
				ruleSkipped(s.log, i+1, stateOf(ctx).id)
				continue
			}
		}
		live = append(live, r)
		position = append(position, i+1)
	}
	platform, byName := oidc.Grants(live, id.Groups)
	in.PlatformRoles = platform
	in.TenantRoles = map[string][]string{}
	for name, roles := range byName {
		in.TenantRoles[ids[name]] = roles
	}
	requestID := stateOf(ctx).id
	// The roles are read again in the transaction that assigns them: a custom role deleted or
	// deactivated since the read above is not assigned.
	in.Assignable = func(ctx context.Context, q *queries.Queries, tid tenant.ID, roles []string) ([]string, error) {
		var custom []string
		for _, role := range roles {
			if _, builtin := authz.RoleByName(role); !builtin {
				custom = append(custom, role)
			}
		}
		states, err := lockCustomRoles(ctx, q, custom)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(roles))
		for _, role := range roles {
			if _, builtin := authz.RoleByName(role); builtin || states[role] == "active" {
				out = append(out, role)
				continue
			}
			for i, r := range live {
				if !r.Platform && r.Role == role && ids[r.Tenant] == tid.String() {
					ruleSkipped(s.log, position[i], requestID)
				}
			}
		}
		return out, nil
	}
	return in, nil
}

// ruleSkipped is the line of a group rule that gives nothing: at start (no request) or at a sign-in.
// It names the position of the rule in the configuration and the request, nothing else; at start
// there is no request, and the line has no request_id.
func ruleSkipped(log *slog.Logger, position int, requestID string) {
	if requestID != "" {
		log = log.With("request_id", requestID)
	}
	log.Warn("group rule skipped", "rule", position)
}

// oidcFailed logs and audits a refused sign-in through the identity provider, within the cap. The
// user is recorded when the failure is about one. The subject is recorded, in the audit row only,
// when the ID token was valid (section 5.8, step 4); before that it is not the provider's word.
func (s *server) oidcFailed(ctx context.Context, st *reqState, reason, userID, subject string, tid tenant.ID) {
	audited := false
	if s.limiter.allow() {
		audited = s.recordAuthFailure(ctx, st, "oidc", reason, userID, subject, tid) == nil
		if !audited {
			s.log.Debug("authentication failure not recorded", "request_id", st.id)
		}
	}
	s.log.Warn("authentication failed", "listener", "admin", "request_id", st.id, "reason", reason, "audited", audited)
}

// SignIn checks a username and a password (section 5.5). A refusal is a 401 whatever the reason, a
// delayed or locked attempt a 429; neither says anything about the account.
func (s *server) SignIn(ctx context.Context, in adminapi.SignInInput) (*adminapi.Session, error) {
	st := stateOf(ctx)
	if !s.authOpts.LocalEnabled {
		s.signInFailed(ctx, st, &account.Refusal{Reason: "local_disabled"})
		return nil, errSignInDisabled
	}
	if s.accounts == nil {
		return nil, errSignInDisabled
	}
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
	return s.recordAuthFailure(ctx, st, "local", r.Reason, r.UserID, "", r.TenantID)
}

// recordAuthFailure writes the auth.failure row of a refused sign-in: in the log of the tenant that
// manages the user, else in the platform's.
func (s *server) recordAuthFailure(ctx context.Context, st *reqState, method, reason, userID, subject string, tid tenant.ID) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	e := audit.Event{
		Actor:  audit.Actor{Type: audit.ActorAnonymous},
		Action: "auth.failure", ResourceType: "user", ResourceID: userID, Outcome: "failure", RequestID: st.id,
		Detail: map[string]any{"reason": reason, "method": method, "listener": "admin"},
	}
	if subject != "" {
		e.Detail["subject"] = subject
	}
	if !tid.IsZero() {
		return s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			return audit.RecordTenant(ctx, tx, tid, e)
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
