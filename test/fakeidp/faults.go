package fakeidp

import (
	"bytes"
	"net/http"
	"os"
	"time"
)

type faultKind int

const (
	faultNone faultKind = iota
	faultRefuse
	faultStatus
	faultDelay
	faultOversize
	faultEndless
	faultRedirect
	faultWrongIssuer
	faultGarbage
	faultPadded
)

// Fault is a way for the issuer to misbehave. The zero value is None.
type Fault struct {
	kind   faultKind
	status int
	d      time.Duration
	to     string
	n      int
}

// The faults without a parameter.
var (
	// None is a well-behaved issuer.
	None = Fault{}
	// Refuse closes the connection without an answer.
	Refuse = Fault{kind: faultRefuse}
	// Status500 answers 500 with an empty body.
	Status500 = Fault{kind: faultStatus, status: http.StatusInternalServerError}
	// Oversize answers with a valid document followed by 64 MiB of blanks, written as fast as the
	// client reads: a client that reads to the end takes all of it.
	Oversize = Fault{kind: faultOversize}
	// Endless answers 200 and then sends one blank a few times a second for ever, until the
	// client goes away.
	Endless = Fault{kind: faultEndless}
	// WrongIssuer makes the discovery document name an issuer that is not the server's.
	WrongIssuer = Fault{kind: faultWrongIssuer}
	// Garbage answers 200 with a body that is not JSON.
	Garbage = Fault{kind: faultGarbage}
)

// Status answers with the status code and an empty body.
func Status(code int) Fault { return Fault{kind: faultStatus, status: code} }

// Delay waits d before answering normally.
func Delay(d time.Duration) Fault { return Fault{kind: faultDelay, d: d} }

// Redirect answers 302 to the address.
func Redirect(to string) Fault { return Fault{kind: faultRedirect, to: to} }

// Padded answers the valid document followed by blanks, so that the body is exactly n bytes long
// (the document itself when it is longer): the tests of the size limits use it at the boundary.
func Padded(n int) Fault { return Fault{kind: faultPadded, n: n} }

func (f Fault) apply(s *Server, w http.ResponseWriter, r *http.Request, doc []byte, discovery bool) {
	ok := func(body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = s.send(w, body)
	}
	switch f.kind {
	case faultRefuse:
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
	case faultStatus:
		w.WriteHeader(f.status)
	case faultDelay:
		select {
		case <-time.After(f.d):
		case <-r.Context().Done():
			return
		}
		ok(doc)
	case faultOversize:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = s.send(w, bytes.TrimSpace(doc))
		blank := bytes.Repeat([]byte(" "), 32<<10)
		for sent := 0; sent < 64<<20; sent += len(blank) {
			if err := s.send(w, blank); err != nil {
				return
			}
		}
	case faultEndless:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_ = s.send(w, []byte(" "))
		for {
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			if err := s.send(w, []byte(" ")); err != nil {
				return
			}
		}
	case faultRedirect:
		http.Redirect(w, r, f.to, http.StatusFound)
	case faultWrongIssuer:
		if discovery {
			ok(bytes.Replace(doc, []byte(`"issuer":"`+s.Issuer()+`"`), []byte(`"issuer":"https://other.example.test/"`), 1))
			return
		}
		ok(doc)
	case faultGarbage:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = s.send(w, []byte("this is not json"))
	case faultPadded:
		body := bytes.TrimSpace(doc)
		if len(body) < f.n {
			body = append(body, bytes.Repeat([]byte(" "), f.n-len(body))...)
		}
		ok(body)
	default:
		ok(doc)
	}
}

func openFile(path string) (*os.File, error) { return os.Open(path) }
