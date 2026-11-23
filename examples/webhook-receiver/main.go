// Command webhook-receiver is an example receiver of Pandokos events. It listens for deliveries,
// verifies their signature, keeps the IDs it has seen in a file and prints one line for each new
// event. It uses the standard library only.
//
// The signing secrets are read from the environment variable WEBHOOK_SECRETS (the one printed
// when the destination was created, and during a rotation the previous one after it, separated
// by spaces or commas), never from an argument, and are never printed.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pandokos/pandokos/examples/webhook-receiver/receiver"
)

// fileStore keeps the IDs it has seen in memory and appends each new one to a file, so that a
// restart still recognises a repeated delivery.
type fileStore struct {
	mu   sync.Mutex
	seen map[string]bool
	f    *os.File
	out  *log.Logger
}

func openStore(path string, out *log.Logger) (*fileStore, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // the path is a flag of the operator
	if err != nil {
		return nil, err
	}
	s := &fileStore{seen: map[string]bool{}, f: f, out: out}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if id := strings.TrimSpace(sc.Text()); id != "" {
			s.seen[id] = true
		}
	}
	return s, sc.Err()
}

// Put records the ID and reports whether it was new. The ID is written before the event is
// acknowledged: a failure answers 500 and the sender tries again.
func (s *fileStore) Put(id string, e receiver.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[id] {
		return false, nil
	}
	if strings.ContainsAny(id, "\r\n") {
		return false, errors.New("an ID with a line break")
	}
	if _, err := fmt.Fprintln(s.f, id); err != nil {
		return false, err
	}
	if err := s.f.Sync(); err != nil {
		return false, err
	}
	s.seen[id] = true
	s.out.Printf("event %s type=%s tenant=%s ref=%s sequence=%s", e.ID, e.Type, e.TenantID, e.TenantRef, e.Sequence)
	return true, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "address to listen on")
	path := flag.String("store", "events-seen.txt", "file that keeps the IDs of the events seen")
	flag.Parse()

	secrets := strings.FieldsFunc(os.Getenv("WEBHOOK_SECRETS"), func(r rune) bool { return r == ' ' || r == ',' || r == '\n' })
	if len(secrets) == 0 {
		fmt.Fprintln(os.Stderr, "webhook-receiver: set WEBHOOK_SECRETS to the signing secret of the destination")
		os.Exit(2)
	}
	logger := log.New(os.Stdout, "", log.LstdFlags)
	store, err := openStore(*path, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "webhook-receiver: cannot open the store: %v\n", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.Handle("/", &receiver.Handler{Secrets: secrets, Store: store})
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	logger.Printf("listening on %s, %d secret(s), store %s", *addr, len(secrets), *path)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "webhook-receiver: %v\n", err)
		os.Exit(1)
	}
}
