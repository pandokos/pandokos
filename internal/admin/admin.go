// Package admin implements the handlers of the administration API (sprint 1.3, section 5.12):
// authentication and authorization hooks, the problem writer and one method per operation.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/requestid"
	"github.com/pandokos/pandokos/internal/store"
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
	// Validators maps a provider identifier to the validator used for `validate: true`; a
	// provider without one refuses `validate`.
	Validators map[string]vault.Validator
	// Snapshot is the embedded price dataset (genaiprices.Snapshot), the source of an import with
	// source embedded (required).
	Snapshot []byte
	// Refresh configures the download of the price dataset; an import with source remote is
	// refused unless Refresh.Enabled.
	Refresh RefreshOptions
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
	log     *slog.Logger
	db      *store.DB
	auth    *authn.Authenticator
	now     func() time.Time
	limiter *failureLimiter
	sealer  *vault.Sealer
	// validators is a private copy of Options.Validators.
	validators map[string]vault.Validator
	snapshot   []byte
	refresh    RefreshOptions
}

// reqState is the state of one request, shared by the outer handler and the hooks.
type reqState struct {
	id        string
	operation string
	principal *authz.Principal
	errCode   adminapi.ProblemCode
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
func New(o Options) (http.Handler, error) {
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
	s := &server{log: o.Logger, db: o.DB, auth: o.Authenticator, now: o.Now, sealer: o.Sealer, validators: maps.Clone(o.Validators),
		snapshot: o.Snapshot, refresh: o.Refresh}
	if s.now == nil {
		s.now = time.Now
	}
	s.limiter = newFailureLimiter(s.now)
	api := adminapi.NewHandler(s, adminapi.Hooks{Before: s.before, Error: s.fail, MaxBodyBytes: maxBodyBytes})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &reqState{id: requestid.New(), operation: "unmatched"}
		if op, _ := adminapi.Match(r.Method, r.URL.Path); op != nil {
			st.operation = op.ID
		}
		w.Header().Set(requestid.Header, st.id)
		w.Header().Set("Cache-Control", "no-store")
		rec := &statusRecorder{ResponseWriter: w, state: st}
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic recovered", "request_id", st.id, "panic_type", fmt.Sprintf("%T", p), "stack", string(debug.Stack()))
				if rec.status == 0 {
					st.errCode = adminapi.ProblemCodeInternal
					writeProblem(rec, st.id, adminapi.ProblemCodeInternal, nil)
				}
			}
			s.logRequest(r, st, rec.status, time.Since(start))
		}()
		api.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), stateKey{}, st)))
	}), nil
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
	if p := st.principal; p != nil {
		attrs = append(attrs, "principal_type", string(p.Kind), "principal_id", p.ID, "key_id", p.KeyID)
		if p.Kind == authz.KindServiceAccount {
			attrs = append(attrs, "tenant_id", p.TenantID.String())
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

func (s *server) before(ctx context.Context, r *http.Request, op *adminapi.Operation, params map[string]string) (context.Context, error) {
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
	st.principal = &p
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

// authFailed logs a failed authentication and records it in the audit log, within the cap.
func (s *server) authFailed(ctx context.Context, st *reqState, f *authn.Failure) {
	reason, keyID, tenantID := f.Detail()
	audited := false
	if s.limiter.allow() {
		audited = s.recordFailure(ctx, st, f) == nil
		if !audited {
			s.log.Debug("authentication failure not recorded", "request_id", st.id)
		}
	}
	attrs := []any{"listener", "admin", "request_id", st.id, "reason", reason}
	if keyID != "" {
		attrs = append(attrs, "key_id", keyID)
	}
	if tenantID != "" {
		attrs = append(attrs, "tenant_id", tenantID)
	}
	attrs = append(attrs, "audited", audited)
	s.log.Warn("authentication failed", attrs...)
}

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
	var fe *catalog.FetchError
	switch {
	case errors.As(err, &re):
		code, fields = re.Code, re.Fields
	case errors.As(err, &ie):
		code, fields = adminapi.ProblemCodeInvalidRequest, ie.fields
	case errors.Is(err, errUnauthenticated):
		code = adminapi.ProblemCodeUnauthenticated
	case errors.Is(err, errForbidden):
		code = adminapi.ProblemCodeForbidden
	case errors.Is(err, errTenantState):
		code = adminapi.ProblemCodeTenantSuspended
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
			attrs = []any{"request_id", st.id, "operation", st.operation, "tenant_id", se.TenantID.String()}
			if se.CredentialID != "" {
				attrs = append(attrs, "credential_id", se.CredentialID)
			}
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
