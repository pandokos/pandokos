package gateway

import (
	"net/http"

	"github.com/pandokos/pandokos/internal/family"
)

// statusWriter records the first status written to the caller.
type statusWriter struct {
	http.ResponseWriter
	requestID string
	code      int
}

// WriteHeader drops informational (1xx) responses: a provider must not get headers to the
// caller around the allow-list. The request ID is set again before the final status, because
// the proxy clears the header map after an informational response.
func (w *statusWriter) WriteHeader(code int) {
	if code < 200 {
		return
	}
	if w.code == 0 {
		w.code = code
		w.ResponseWriter.Header().Set(family.HeaderRequestID, w.requestID)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
		w.ResponseWriter.Header().Set(family.HeaderRequestID, w.requestID)
	}
	return w.ResponseWriter.Write(p)
}

// Flush delegates to the real writer.
func (w *statusWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) wroteHeader() bool { return w.code != 0 }

func (w *statusWriter) status() int { return w.code }
