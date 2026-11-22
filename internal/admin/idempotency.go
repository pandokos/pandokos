package admin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/idem"
	"github.com/pandokos/pandokos/internal/tenant"
)

// maxStoredResponse is the largest response kept for a replay; the table refuses more.
const maxStoredResponse = 262144

// replayError is the stored response of an idempotency key, written again by fail.
type replayError struct {
	status int
	body   []byte
}

func (e *replayError) Error() string { return "admin: idempotent replay" }

// validIdempotencyKey reports whether k is 1 to 128 printable ASCII characters.
func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// beginIdempotent claims the Idempotency-Key of a request to an operation that creates (section 5.11),
// after authorization. Without the header, or on an operation that does not create, nothing changes.
func (s *server) beginIdempotent(ctx context.Context, r *http.Request, op *adminapi.Operation, params map[string]string) (context.Context, error) {
	vals, present := r.Header["Idempotency-Key"]
	if !op.Idempotent || !present || s.idempotency == nil {
		return ctx, nil
	}
	if len(vals) != 1 || !validIdempotencyKey(vals[0]) {
		return ctx, invalid("Idempotency-Key", "must be 1 to 128 printable ASCII characters")
	}
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBodyBytes)); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return ctx, &adminapi.RequestError{Status: http.StatusRequestEntityTooLarge, Code: adminapi.ProblemCodeRequestTooLarge}
			}
			return ctx, invalid("body", "could not be read")
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	p := principalOf(ctx)
	scope := idem.Scope{PrincipalKind: "service_account", PrincipalID: p.ID}
	switch p.Kind {
	case authz.KindPlatform:
		scope.PrincipalKind = "platform"
	case authz.KindUser:
		scope.PrincipalKind = "user"
	}
	if perm, ok := authz.Lookup(op.Permission); ok && perm.Scope == authz.ScopeTenant {
		tid, err := tenant.ParseID(params["tenant"])
		if err != nil {
			return ctx, errNotFound
		}
		scope.TenantID = tid
	}
	ctx2, replay, err := s.idempotency.Begin(ctx, scope, op.ID, r.URL.Path, vals[0], body)
	if err != nil {
		return ctx, err
	}
	if replay != nil {
		return ctx, &replayError{status: replay.Status, body: replay.Body}
	}
	st := stateOf(ctx)
	st.claimed, st.capture = ctx2, &bytes.Buffer{}
	return ctx2, nil
}

// finishIdempotent stores the response of a claimed request, or releases the claim. It runs after
// the handler returned, and does not depend on the context of the request.
func (s *server) finishIdempotent(st *reqState, status int) {
	if st.claimed == nil {
		return
	}
	if st.captureOverflow || st.capture == nil {
		status = 0
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(st.claimed), 2*time.Second)
	defer cancel()
	var body []byte
	if st.capture != nil {
		body = st.capture.Bytes()
	}
	if err := s.idempotency.Finish(ctx, status, body); err != nil {
		s.log.Warn("idempotency response not stored", "request_id", st.id, "operation", st.operation, "status", strconv.Itoa(status))
	}
}

// writeReplay writes the stored response of a key.
func writeReplay(w http.ResponseWriter, e *replayError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(e.status)
	_, _ = w.Write(e.body)
}
