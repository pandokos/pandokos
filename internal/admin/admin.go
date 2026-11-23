// Package admin implements the handlers of the administration API (sprint 1.3, section 5.12):
// authentication and authorization hooks, the problem writer and one method per operation.
package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/idem"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/requestid"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
)

const maxBodyBytes = 1048576

// Options configure the handler.
type Options struct {
	Logger        *slog.Logger         // required
	DB            *store.DB            // required: the admin pool
	Authenticator *authn.Authenticator // required: the admin authenticator
	Now           func() time.Time     // time.Now when nil
	// Sealer seals the secrets of credentials and creates the data keys of tenants (required).
	Sealer *vault.Sealer
	// Validator presents a credential to its provider for `validate: true`; nil refuses
	// `validate`.
	Validator vault.Validator
	// Snapshot is the embedded price dataset (genaiprices.Snapshot), the source of an import with
	// source embedded (required).
	Snapshot []byte
	// Refresh configures the download of the price dataset; an import with source remote is
	// refused unless Refresh.Enabled.
	Refresh RefreshOptions
	// DefaultCurrency is pricing.default_currency: the active currency of a tenant that chose
	// none; USD when empty.
	DefaultCurrency string
	// Sessions, Accounts, OIDC and Idempotency are the services of sprint 4.2; Origin and Auth
	// are the settings of section 5.15 that the handlers read. OIDC is nil when the identity
	// provider is off. Part B declares them; step C3 reads them.
	Sessions    *session.Store
	Accounts    *account.Service
	OIDC        *oidc.Provider
	Idempotency *idem.Store
	Origin      OriginOptions
	Auth        AuthOptions
	// TenantDeletionGrace is tenant_deletion.grace_period: the time between a request for the
	// deletion of a tenant and its start; 168 hours when nil.
	TenantDeletionGrace *time.Duration
	// Support is the configuration of support actions (sprint 4.3): support.enabled and
	// support.max_duration.
	Support struct {
		Enabled     bool
		MaxDuration time.Duration
	}
	// Events are the settings of the event operations (sprint 6.4): events.source, which the
	// exports put in the source of every document, and events.webhook.private_hosts, the hosts for
	// which an http URL and a private address are valid. Part B declares them; step C6 reads them.
	Events struct {
		Source       string
		PrivateHosts []string
	}
	// Limits are the settings of the capability check: limits.on_unknown_price ("refuse", the
	// default, or "allow") and the zones that give the calendar of each tenant.
	Limits struct {
		OnUnknownPrice string
		// Zones gives the calendar of each tenant's zone: it validates a time zone, computes the
		// instant of a change and gives the capability check the periods of the tenant. Required.
		Zones *limits.Zones
		// Ledger reads the usage of the current periods for the capability check; cmd/pandokos
		// builds it (T-R43). Required.
		Ledger limits.Ledger
	}
}

// OriginOptions say which origin the administration listener is reached at (section 5.7): Public
// is auth.public_origin, TLS says that the listener terminates TLS, Loopback that it listens on a
// loopback address, and TrustedProxies are listeners.admin.trusted_proxies.
type OriginOptions struct {
	Public         string
	TLS, Loopback  bool
	TrustedProxies []netip.Prefix
}

// AuthOptions are the sign-in settings of section 5.15 that the handlers use.
type AuthOptions struct {
	LocalEnabled    bool        // auth.local.enabled
	OIDCDisplayName string      // auth.oidc.display_name
	PostSignInPath  string      // auth.oidc.post_sign_in_path; "/" when empty
	GroupRules      []oidc.Rule // auth.oidc.group_mappings, parsed
	AutoProvision   bool        // auth.oidc.auto_provision
}

// RefreshOptions say whether and from where the price dataset may be downloaded.
type RefreshOptions struct {
	Enabled bool
	// URL is the only address that is ever downloaded; a request never names one.
	URL string
	// Fetch downloads url. It returns a *catalog.FetchError for a failure; any other error is
	// treated as an unreachable source. It runs before, never inside, a transaction.
	Fetch func(ctx context.Context, url string) ([]byte, error)
}

type server struct {
	stats     adminCounters
	log       *slog.Logger
	db        *store.DB
	auth      *authn.Authenticator
	now       func() time.Time
	limiter   *failureLimiter
	sealer    *vault.Sealer
	validator vault.Validator
	snapshot  []byte
	refresh   RefreshOptions
	// defaultCurrency is Options.DefaultCurrency, USD when that is empty.
	defaultCurrency string
	limits          struct {
		OnUnknownPrice string
		Zones          *limits.Zones
		Ledger         limits.Ledger
	}
	// The services and settings of sprint 4.2; each may be nil or zero when the listener has no
	// sign-in (a session is then never accepted).
	sessions    *session.Store
	accounts    *account.Service
	oidc        *oidc.Provider
	idempotency *idem.Store
	origin      OriginOptions
	authOpts    AuthOptions
	// deletionGrace is Options.TenantDeletionGrace.
	deletionGrace time.Duration
	// events is Options.Events.
	events struct {
		source       string
		privateHosts []string
	}
	// support is Options.Support.
	support struct {
		enabled     bool
		maxDuration time.Duration
	}
}

// reqState is the state of one request, shared by the outer handler and the hooks.
type reqState struct {
	id        string
	operation string
	principal *authz.Principal
	errCode   adminapi.ProblemCode
	// header holds the headers and cookies that a handler or the hook adds; the status recorder
	// copies them into the response before the status line.
	header http.Header
	// session and user are those of a request that carries a session cookie, once it is accepted.
	session *session.Session
	user    *account.User
	// source is the address the request is counted against (section 5.5); presented is the session
	// cookie of a sign-in, which the sign-in ends so that no session is fixed.
	source    netip.Addr
	presented string
	// seed is the binding cookie of a sign-in through the identity provider, read for the callback only.
	seed secret.Secret
	// claimed is the context that carries the idempotency claim of the request, and capture the
	// response body kept for it (section 5.11); both are nil without an Idempotency-Key.
	claimed         context.Context
	capture         *bytes.Buffer
	captureOverflow bool
}

// setCookie adds a Set-Cookie header to the response of the request.
func setCookie(ctx context.Context, c *http.Cookie) {
	st := stateOf(ctx)
	if st.header == nil {
		st.header = http.Header{}
	}
	st.header.Add("Set-Cookie", c.String())
}

type stateKey struct{}

func stateOf(ctx context.Context) *reqState {
	st, _ := ctx.Value(stateKey{}).(*reqState)
	if st == nil {
		return &reqState{}
	}
	return st
}

// New returns the handler of the administration API: adminapi.NewHandler with the hooks below,
// inside the handler that sets the request ID and writes the log line.
func New(o Options) (*Handler, error) {
	switch {
	case o.Logger == nil:
		return nil, errors.New("admin: Logger is required")
	case o.DB == nil:
		return nil, errors.New("admin: DB is required")
	case o.Authenticator == nil:
		return nil, errors.New("admin: Authenticator is required")
	case o.Sealer == nil:
		return nil, errors.New("admin: Sealer is required")
	case len(o.Snapshot) == 0:
		return nil, errors.New("admin: Snapshot is required")
	case o.Refresh.Enabled && (o.Refresh.URL == "" || o.Refresh.Fetch == nil):
		return nil, errors.New("admin: Refresh needs a URL and a Fetch function when enabled")
	}
	if err := checkGroupRules(o); err != nil {
		return nil, err
	}
	s := &server{log: o.Logger, db: o.DB, auth: o.Authenticator, now: o.Now, sealer: o.Sealer, validator: o.Validator,
		snapshot: o.Snapshot, refresh: o.Refresh,
		sessions: o.Sessions, accounts: o.Accounts, oidc: o.OIDC, idempotency: o.Idempotency, origin: o.Origin, authOpts: o.Auth}
	s.limits.OnUnknownPrice, s.limits.Ledger = o.Limits.OnUnknownPrice, o.Limits.Ledger
	s.limits.Zones = o.Limits.Zones
	s.support.enabled, s.support.maxDuration = o.Support.Enabled, o.Support.MaxDuration
	s.events.source, s.events.privateHosts = o.Events.Source, slices.Clone(o.Events.PrivateHosts)
	if s.defaultCurrency = o.DefaultCurrency; s.defaultCurrency == "" {
		s.defaultCurrency = "USD"
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.deletionGrace = defaultDeletionGrace; o.TenantDeletionGrace != nil {
		s.deletionGrace = *o.TenantDeletionGrace
	}
	s.limiter = newFailureLimiter(s.now)
	api := adminapi.NewHandler(s, adminapi.Hooks{Before: s.before, Error: s.fail, MaxBodyBytes: maxBodyBytes})
	return &Handler{s: s, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &reqState{id: requestid.New(), operation: "unmatched"}
		if op, _ := adminapi.Match(r.Method, r.URL.Path); op != nil {
			st.operation = op.ID
		}
		w.Header().Set(requestid.Header, st.id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		rec := &statusRecorder{ResponseWriter: w, state: st}
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					// A streamed answer that failed after its first byte: the server closes
					// the connection without the chunk terminator.
					s.finishIdempotent(st, rec.status)
					s.logRequest(r, st, rec.status, time.Since(start))
					panic(p)
				}
				s.log.Error("panic recovered", "request_id", st.id, "panic_type", fmt.Sprintf("%T", p), "stack", string(debug.Stack()))
				if rec.status == 0 {
					st.errCode = adminapi.ProblemCodeInternal
					writeProblem(rec, st.id, adminapi.ProblemCodeInternal, nil)
				}
			}
			s.finishIdempotent(st, rec.status)
			s.logRequest(r, st, rec.status, time.Since(start))
		}()
		api.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), stateKey{}, st)))
	})}, nil
}

func logMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

func (s *server) logRequest(r *http.Request, st *reqState, status int, d time.Duration) {
	attrs := []any{"request_id", st.id, "method", logMethod(r.Method), "operation", st.operation, "status", status, "duration_ms", d.Milliseconds()}
	if st.errCode != "" {
		attrs = append(attrs, "error_code", string(st.errCode))
	}
	// A principal of the platform is named: its activity is the platform's. A tenant's principal
	// is not, nor its tenant (ADR 0055).
	if p := st.principal; p != nil {
		attrs = append(attrs, "principal_type", string(p.Kind))
		// A support principal is logged as the platform principal it is, and says so; the line
		// names neither its tenant nor its action.
		support := p.SupportActionID != ""
		if support {
			attrs = append(attrs, "support", true)
		}
		if support || p.Kind == authz.KindPlatform || (p.Kind == authz.KindUser && p.TenantID.IsZero()) {
			attrs = append(attrs, "principal_id", p.ID)
			if p.Kind == authz.KindPlatform {
				attrs = append(attrs, "key_id", p.KeyID)
			}
		}
	}
	s.log.Info("admin request", attrs...)
}

// bearer extracts the key from an Authorization header: the scheme Bearer in any case, one space.
func bearer(h string) (string, bool) {
	const scheme = "bearer "
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	return h[len(scheme):], true
}

// before is the hook that authenticates and authorizes (sections 5.1 and 5.3): the first row that
// applies wins. A public operation reads no credential; a request with an Authorization header is
// a key (the cookie is ignored); a request with the session cookie is a session; otherwise 401.
func (s *server) before(ctx context.Context, r *http.Request, op *adminapi.Operation, params map[string]string) (context.Context, error) {
	ctx, err := s.authenticate(ctx, r, op, params)
	if err != nil {
		return ctx, err
	}
	return s.beginIdempotent(ctx, r, op, params)
}

// authenticate is the credential half of the hook.
func (s *server) authenticate(ctx context.Context, r *http.Request, op *adminapi.Operation, params map[string]string) (context.Context, error) {
	st := stateOf(ctx)
	st.source = sourceAddr(r, s.origin)
	if op.Public {
		if c, err := r.Cookie(session.CookieName); err == nil {
			st.presented = c.Value
		}
		if c, err := r.Cookie(oidc.SeedCookieName); err == nil && op.ID == "completeOidcSignIn" {
			st.seed = secret.New(c.Value)
		}
		return s.beforePublic(ctx, r, op)
	}
	if _, ok := r.Header["Authorization"]; !ok {
		if c, err := r.Cookie(session.CookieName); err == nil {
			return s.beforeSession(ctx, r, op, params, c.Value)
		}
	}
	return s.beforeKey(ctx, r, op, params)
}

// beforePublic applies the origin rule to a public operation, and the checks of section 5.7 to
// signIn. No credential is read.
func (s *server) beforePublic(ctx context.Context, r *http.Request, op *adminapi.Operation) (context.Context, error) {
	origin, ok := expectedOrigin(r, s.origin)
	if !ok {
		return ctx, errSignInDisabled
	}
	if op.ID == "signIn" && !sameSite(r, origin) {
		return ctx, errCSRF
	}
	return ctx, nil
}

// sameSite applies the three checks of section 5.7: the header, the Origin and Sec-Fetch-Site.
func sameSite(r *http.Request, expected string) bool {
	if r.Header.Get("X-Pandokos-Csrf") != "1" {
		return false
	}
	if o := r.Header.Values("Origin"); len(o) != 1 || o[0] != expected {
		return false
	}
	if f := r.Header.Values("Sec-Fetch-Site"); len(f) > 0 && (len(f) != 1 || f[0] != "same-origin") {
		return false
	}
	return true
}

// beforeKey is the path of a key: no cookie is read and no CSRF check is made, because no browser
// attaches an Authorization header by itself.
func (s *server) beforeKey(ctx context.Context, r *http.Request, op *adminapi.Operation, params map[string]string) (context.Context, error) {
	st := stateOf(ctx)
	var p authz.Principal
	var err error
	if key, ok := bearer(r.Header.Get("Authorization")); ok {
		p, err = s.auth.Authenticate(ctx, key)
	} else {
		err = &authn.Failure{Reason: "malformed"}
	}
	if err != nil {
		var f *authn.Failure
		if errors.As(err, &f) {
			s.authFailed(ctx, st, f)
			return ctx, errUnauthenticated
		}
		return ctx, err
	}
	if perm, ok := authz.Lookup(op.Permission); ok && perm.Scope == authz.ScopeTenant && p.Kind == authz.KindPlatform {
		// The one place where a platform key has nothing to do in a tenant: unless it has an open
		// support action there (section 5.6). A path that names no tenant keeps its old answer.
		if tid, perr := tenant.ParseID(params["tenant"]); perr == nil {
			sp, ok, err := s.supportPrincipal(ctx, p, tid)
			if err != nil {
				return ctx, err
			}
			if ok {
				p = sp
			}
		}
	}
	st.principal = &p
	return s.decide(ctx, p, op, params)
}

// decide applies authz.Decide to a principal for the operation.
func (s *server) decide(ctx context.Context, p authz.Principal, op *adminapi.Operation, params map[string]string) (context.Context, error) {
	target := authz.Target{}
	if perm, ok := authz.Lookup(op.Permission); ok && perm.Scope == authz.ScopeTenant {
		if id, perr := tenant.ParseID(params["tenant"]); perr == nil {
			target.TenantID = id
		}
	}
	switch authz.Decide(p, op.Permission, target) {
	case authz.Allow:
	case authz.DenyNotFound:
		return ctx, errNotFound
	case authz.DenyTenantState:
		return ctx, errTenantState
	default:
		return ctx, errForbidden
	}
	return authz.WithPrincipal(ctx, p), nil
}

// mustChangeAllowed are the operations that a session whose account must change its password may call.
var mustChangeAllowed = map[string]bool{"getMe": true, "changePassword": true, "signOut": true}

// beforeSession is the path of a session cookie (sections 5.3, 5.6 and 5.7): the origin and the
// forgery checks first, before any credential is looked at; then the session; the principal built for
// the scope of the operation; the must-change gate; the decision; the touch.
func (s *server) beforeSession(ctx context.Context, r *http.Request, op *adminapi.Operation, params map[string]string, token string) (context.Context, error) {
	st := stateOf(ctx)
	origin, ok := expectedOrigin(r, s.origin)
	if !ok {
		return ctx, errSignInDisabled
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameSite(r, origin) {
		return ctx, errCSRF
	}
	if s.sessions == nil {
		return ctx, errUnauthenticated
	}
	sess, user, err := s.sessions.Lookup(ctx, token)
	if errors.Is(err, session.ErrNoSession) {
		setCookie(ctx, session.ClearCookie())
		s.log.Warn("authentication failed", "listener", "admin", "request_id", st.id, "reason", "session", "audited", false)
		return ctx, errUnauthenticated
	}
	if err != nil {
		return ctx, err
	}
	if user.MustChange && !mustChangeAllowed[op.ID] {
		return ctx, errMustChange
	}
	p := authz.Principal{Kind: authz.KindUser, ID: user.ID, Name: user.Username, SessionID: sess.ID, MustChangePassword: user.MustChange}
	if p.Name == "" {
		p.Name = user.DisplayName
	}
	perm, known := authz.Lookup(op.Permission)
	if known && perm.Scope == authz.ScopeTenant {
		tid, perr := tenant.ParseID(params["tenant"])
		if perr != nil {
			return ctx, errNotFound
		}
		mp, err := s.memberPrincipal(ctx, user, sess, tid)
		if errors.Is(err, errNotFound) {
			// Not a member: the platform user's support action in this tenant, if it has one.
			base := p
			base.Roles = user.PlatformRoles
			sp, ok, serr := s.supportPrincipal(ctx, base, tid)
			if serr != nil {
				return ctx, serr
			}
			if ok {
				mp, err = sp, nil
			}
		}
		if err != nil {
			return ctx, err
		}
		p = mp
	} else {
		p.Roles = user.PlatformRoles
	}
	st.principal, st.session, st.user = &p, &sess, &user
	ctx, err = s.decide(ctx, p, op, params)
	if err != nil {
		return ctx, err
	}
	if err := s.sessions.Touch(ctx, sess.ID); err != nil {
		s.log.Debug("session use not recorded", "request_id", st.id, "error", err.Error())
	}
	return ctx, nil
}

// authFailed logs a failed authentication and records it in the audit log, within the cap.
func (s *server) authFailed(ctx context.Context, st *reqState, f *authn.Failure) {
	d := f.Detail()
	audited := false
	if s.limiter.allow() {
		audited = s.recordFailure(ctx, st, f) == nil
		if !audited {
			s.log.Debug("authentication failure not recorded", "request_id", st.id)
		}
	}
	// The line says why, never who: the key and the tenant go to the audit log only (ADR 0055).
	attrs := []any{"listener", "admin", "request_id", st.id, "reason", d.Reason, "audited", audited}
	s.log.Warn("authentication failed", attrs...)
}

// errNoAuditLog says that a failure names no log to be written to.
var errNoAuditLog = errors.New("admin: failure names no audit log")

func (s *server) recordFailure(ctx context.Context, st *reqState, f *authn.Failure) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	resource := "credential"
	switch {
	case strings.HasPrefix(f.KeyID, "t"):
		resource = "virtual_key"
	case strings.HasPrefix(f.KeyID, "p"):
		resource = "platform_key"
	}
	e := audit.Event{
		Actor:  audit.Actor{Type: audit.ActorAnonymous, KeyID: f.KeyID},
		Action: "auth.failure", ResourceType: resource, ResourceID: f.KeyID, Outcome: "failure", RequestID: st.id,
		Detail: map[string]any{"reason": f.Reason, "listener": "admin"},
	}
	if f.IssuerID != "" {
		// A refused token names the registration; the subject only when its signature was verified.
		e.ResourceType, e.ResourceID = "token_issuer", f.IssuerID
		e.Detail["issuer_scope"] = f.IssuerScope
		if f.Subject != "" {
			e.Detail["subject"] = f.Subject
		}
		if f.TenantID.IsZero() {
			// No tenant to write to: logged only, as on the gateway.
			return errNoAuditLog
		}
	}
	if !f.TenantID.IsZero() {
		return s.db.WithTenant(ctx, f.TenantID, func(ctx context.Context, tx store.Tx) error {
			return audit.RecordTenant(ctx, tx, f.TenantID, e)
		})
	}
	return s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error { return audit.RecordPlatform(ctx, tx, e) })
}

// fail is the Error hook: the one place where an error becomes a response.
func (s *server) fail(w http.ResponseWriter, r *http.Request, _ *adminapi.Operation, err error) {
	st := stateOf(r.Context())
	code := adminapi.ProblemCodeInternal
	var fields []adminapi.FieldError
	var re *adminapi.RequestError
	var ie *invalidError
	var st2 *stateError
	var fe *catalog.FetchError
	var rt *retryError
	var refusal *account.Refusal
	var pe *account.PolicyError
	var rp *replayError
	if errors.As(err, &rp) {
		writeReplay(w, rp)
		return
	}
	switch {
	case errors.As(err, &re):
		code, fields = re.Code, re.Fields
	case errors.As(err, &ie):
		code, fields = adminapi.ProblemCodeInvalidRequest, ie.fields
	case errors.As(err, &st2):
		code, fields = adminapi.ProblemCodeInvalidState, st2.fields
	case errors.As(err, &rt):
		code = rt.code
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter(rt.after)))
	case errors.As(err, &refusal):
		// A refused sign-in is a 401 whatever the reason; a delayed one is a 429 that says when.
		code = adminapi.ProblemCodeUnauthenticated
		if refusal.RetryAfter > 0 {
			code = adminapi.ProblemCodeTooManyAttempts
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter(refusal.RetryAfter)))
		}
	case errors.As(err, &pe):
		code, fields = adminapi.ProblemCodeInvalidRequest, []adminapi.FieldError{{Field: pe.Field, Message: pe.Violation}}
	case errors.Is(err, password.ErrBusy):
		code = adminapi.ProblemCodeUnavailable
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, idem.ErrKeyReused):
		code = adminapi.ProblemCodeIdempotencyKeyReused
	case errors.Is(err, idem.ErrInProgress):
		code = adminapi.ProblemCodeIdempotencyInProgress
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, idem.ErrResponseLost):
		code = adminapi.ProblemCodeIdempotencyResponseLost
	case errors.Is(err, errCSRF):
		code = adminapi.ProblemCodeCsrfRejected
	case errors.Is(err, errSignInDisabled):
		code = adminapi.ProblemCodeSignInDisabled
	case errors.Is(err, errMustChange):
		code = adminapi.ProblemCodePasswordChangeRequired
	case errors.Is(err, errUnauthenticated):
		code = adminapi.ProblemCodeUnauthenticated
	case errors.Is(err, errForbidden):
		code = adminapi.ProblemCodeForbidden
	case errors.Is(err, errTenantState):
		code = adminapi.ProblemCodeTenantSuspended
	case errors.Is(err, errEscalation):
		code = adminapi.ProblemCodeEscalationRefused
	case errors.Is(err, errSupportDisabled):
		code = adminapi.ProblemCodeSupportDisabled
	case errors.Is(err, errRoleInUse):
		code = adminapi.ProblemCodeRoleInUse
	case errors.Is(err, errNotFound):
		code = adminapi.ProblemCodeNotFound
	case errors.Is(err, errConflict):
		code = adminapi.ProblemCodeConflict
	case errors.Is(err, errInvalidState):
		code = adminapi.ProblemCodeInvalidState
	case errors.Is(err, errCredentialRejected):
		code = adminapi.ProblemCodeCredentialRejected
	case errors.Is(err, errValidationUnavailable):
		code = adminapi.ProblemCodeValidationUnavailable
	case errors.As(err, &fe):
		// Nothing of the failure but its class reaches the log: the cause can carry addresses.
		code = adminapi.ProblemCodePriceSourceUnavailable
	case errors.Is(err, catalog.ErrDatasetInvalid):
		code = adminapi.ProblemCodePriceDatasetInvalid
	case errors.Is(err, vault.ErrUnavailable):
		// A key-encryption key version that is not available, or a data key that does not
		// unwrap. Nothing of the error is logged but its class and identifiers: provider errors
		// can carry paths. The write-time counterpart of the line of section 5.10.
		code = adminapi.ProblemCodeUnavailable
		attrs := []any{"request_id", st.id, "operation", st.operation, "cause", "kek_unavailable"}
		var se *vault.SealError
		if errors.As(err, &se) {
			attrs = []any{"request_id", st.id, "operation", st.operation}
			if se.DataKeyVersion != 0 {
				attrs = append(attrs, "data_key_version", se.DataKeyVersion)
			}
			if se.KEKVersion != "" {
				attrs = append(attrs, "kek_version", se.KEKVersion)
			}
			attrs = append(attrs, "cause", se.Cause)
		}
		s.log.Error("credential cannot be sealed", attrs...)
	case errors.Is(err, authz.ErrUnavailable) || store.IsUnavailable(err):
		code = adminapi.ProblemCodeUnavailable
	case constraintProblem(err, &code, &fields):
	default:
		attrs := []any{"request_id", st.id, "operation", st.operation}
		if sqlstate := store.SQLState(err); sqlstate != "" {
			attrs = append(attrs, "sqlstate", sqlstate)
		} else {
			attrs = append(attrs, "error", err.Error())
		}
		s.log.Error("internal error", attrs...)
	}
	st.errCode = code
	writeProblem(w, st.id, code, fields)
}

// retryAfter rounds a delay up to whole seconds, at least one.
func retryAfter(d time.Duration) int {
	n := int((d + time.Second - 1) / time.Second)
	if n < 1 {
		return 1
	}
	return n
}

// roleNameFormat is the format of a role name (the RoleName schema of the API).
var roleNameFormat = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ruleRole says what a group rule's role is: a built-in role that a person can hold in the scope of
// the rule (ok, not custom), a name that only a tenant's database can vouch for (ok, custom) or
// neither. A custom role is known to the database, not to the configuration, so for a tenant target
// only the format is checked here.
func ruleRole(r oidc.Rule) (ok, custom bool) {
	scope := authz.ScopeTenant
	if r.Platform {
		scope = authz.ScopePlatform
	}
	if def, found := authz.RoleByName(r.Role); found {
		return def.Scope == scope && def.Members, false
	}
	return !r.Platform && roleNameFormat.MatchString(r.Role), true
}

// customRolesOf reads the state of the custom roles of the tenant that have one of the names, by
// name, in the tenant's session.
func customRolesOf(ctx context.Context, db *store.DB, tid tenant.ID, names []string) (map[string]string, error) {
	var out map[string]string
	err := db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = lockCustomRoles(ctx, queries.New(tx), names)
		return err
	})
	return out, err
}

// lockCustomRoles reads, FOR SHARE, the state of the custom roles of the session's tenant that have
// one of the names, by name: a deletion waits for the transaction, or the role is not found.
func lockCustomRoles(ctx context.Context, q *queries.Queries, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := q.LockTenantRolesForAssignment(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Name] = r.State
	}
	return out, nil
}

// checkGroupRules refuses at start a group rule that names a role that a user cannot hold in its scope
// or a tenant that does not exist (section 5.9). An installation without any tenant cannot name one
// yet, so nothing is refused there; a tenant created later is found at each sign-in. A rule that names
// a custom role that its tenant does not have is not refused: a tenant that deletes a role would stop
// the installation. It is skipped, as at sign-in, with a warning that gives its position only.
func checkGroupRules(o Options) error {
	var names []string
	for i, r := range o.Auth.GroupRules {
		if ok, _ := ruleRole(r); !ok {
			return fmt.Errorf("admin: group rule %d names a role that a user cannot hold in that scope", i+1)
		}
		if !r.Platform {
			names = append(names, r.Tenant)
		}
	}
	if len(names) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var rows []queries.TenantIDsByNameRow
	var existing []queries.PandokosTenant
	err := o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if rows, err = queries.New(tx).TenantIDsByName(ctx, names); err != nil {
			return err
		}
		existing, err = queries.New(tx).ListTenants(ctx, queries.ListTenantsParams{Sort: "created_at", AfterID: "00000000-0000-0000-0000-000000000000", PageSize: 1})
		return err
	})
	if err != nil {
		return fmt.Errorf("admin: group rules: tenants cannot be read: %w", err)
	}
	if len(existing) == 0 {
		return nil // a new installation: its tenants are made through this very API, and the rules are skipped until they exist
	}
	found := map[string]string{}
	for _, r := range rows {
		found[r.Name] = r.ID
	}
	customOf := map[string][]string{}
	for i, r := range o.Auth.GroupRules {
		if r.Platform {
			continue
		}
		if _, ok := found[r.Tenant]; !ok {
			return fmt.Errorf("admin: group rule %d names a tenant that does not exist", i+1)
		}
		if _, custom := ruleRole(r); custom {
			customOf[r.Tenant] = append(customOf[r.Tenant], r.Role)
		}
	}
	for i, r := range o.Auth.GroupRules {
		if r.Platform {
			continue
		}
		if _, custom := ruleRole(r); !custom {
			continue
		}
		tid, err := tenant.ParseID(found[r.Tenant])
		if err != nil {
			return fmt.Errorf("admin: group rules: %w", err)
		}
		have, err := customRolesOf(ctx, o.DB, tid, customOf[r.Tenant])
		if err != nil {
			return fmt.Errorf("admin: group rules: custom roles cannot be read: %w", err)
		}
		if _, ok := have[r.Role]; !ok {
			ruleSkipped(o.Logger, i+1, "")
		}
	}
	return nil
}

// memberPrincipal builds the principal of a user that is a member of the tenant: its roles and the
// permissions of its active custom roles, read in the tenant's session. errNotFound: the user is
// not a member. No other code builds a member's principal.
func (s *server) memberPrincipal(ctx context.Context, u account.User, sess session.Session, tid tenant.ID) (authz.Principal, error) {
	var row queries.GetMemberPrincipalRow
	err := s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetMemberPrincipal(ctx, u.ID)
		return err
	})
	if errors.Is(err, store.ErrNoRows) {
		return authz.Principal{}, errNotFound
	}
	if err != nil {
		return authz.Principal{}, err
	}
	p := authz.Principal{Kind: authz.KindUser, ID: u.ID, Name: u.Username, SessionID: sess.ID, MustChangePassword: u.MustChange,
		TenantID: tid, TenantName: row.TenantName, TenantState: tenant.State(row.TenantState), Roles: row.Roles, Granted: row.Granted}
	if p.Name == "" {
		p.Name = u.DisplayName
	}
	return p, nil
}

// constraintProblem maps the refusals of the functions and triggers of migration 0016 (§5.16): a
// write for a tenant that is being deleted or is deleted, a reactivation held by a requested
// deletion, and a request that raced a reactivation. It reports whether err is one of them.
func constraintProblem(err error, code *adminapi.ProblemCode, fields *[]adminapi.FieldError) bool {
	switch store.ErrorConstraint(err) {
	case "tenant_open":
		*code = adminapi.ProblemCodeInvalidState
		*fields = []adminapi.FieldError{{Field: "tenant", Message: "the tenant is being deleted or is deleted"}}
	case "tenant_deletion_holds":
		*code = adminapi.ProblemCodeInvalidState
		*fields = []adminapi.FieldError{{Field: "state", Message: "the deletion of the tenant is requested; cancel it first"}}
	case "tenant_deletion_suspended":
		*code = adminapi.ProblemCodeInvalidState
	default:
		return false
	}
	return true
}
