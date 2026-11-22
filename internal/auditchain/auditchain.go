// Package auditchain is the one Go definition of the canonical encoding of an audit row and of its
// hash chain (ADR 0065); the SQL definition is audit_row_hash in migration 0015. The two are tested
// against each other. Nothing here reads a database.
package auditchain

import (
	"encoding/json"
	"errors"
	"iter"
	"time"
)

// errNotImplemented is what the stubs of sprint 6.2 part A return until part C1 builds them.
var errNotImplemented = errors.New("auditchain: not implemented")

// The reasons of a break (sprint 6.2, section 5.9).
const (
	ReasonHashMismatch         = "hash_mismatch"
	ReasonPrevMismatch         = "prev_mismatch"
	ReasonSeqGap               = "seq_gap"
	ReasonAnchorMismatch       = "anchor_mismatch"
	ReasonHeadMismatch         = "head_mismatch"
	ReasonExpectedHeadMismatch = "expected_head_mismatch"
)

// Row is one audit row as the chain sees it. Scope is "tenant:<id>" or "platform".
type Row struct {
	Scope           string
	Seq             int64
	ID              string
	OccurredAt      time.Time
	ActorType       string
	ActorID         string
	ActorName       string
	KeyID           string
	Action          string
	ResourceType    string
	ResourceID      string
	Outcome         string
	RequestID       string
	Reason          string
	SupportActionID string
	Detail          json.RawMessage
	// DetailDigest is the SHA-256 of the canonical detail, for a row whose detail is withheld.
	DetailDigest []byte
	PrevHash     []byte
	Hash         []byte
}

// Head is the head and the anchor of a chain.
type Head struct {
	Seq        int64
	Hash       []byte
	AnchorSeq  int64
	AnchorHash []byte
}

// Break is one place where a chain does not verify.
type Break struct {
	Seq    int64
	Reason string
}

// Result is what Verify found.
type Result struct {
	Rows     int64
	First    int64
	Last     int64
	LastHash []byte
	Breaks   []Break
	// LinkUnverified is true when the first row is not the anchor: its link to the rows before it
	// cannot be checked.
	LinkUnverified bool
}

// Canonical returns the canonical encoding (version 1) of a JSON detail.
func Canonical(detail json.RawMessage) ([]byte, error) {
	_ = detail
	return nil, errNotImplemented
}

// DetailDigest returns the SHA-256 of the canonical encoding of a detail.
func DetailDigest(detail json.RawMessage) ([32]byte, error) {
	_ = detail
	return [32]byte{}, errNotImplemented
}

// Hash returns the hash of a row: it uses r.DetailDigest when set, else the digest of r.Detail.
func Hash(r Row) ([32]byte, error) {
	_ = r
	return [32]byte{}, errNotImplemented
}

// Verify checks a chain row by row against its head and, when given, an expected head hash.
func Verify(rows iter.Seq2[Row, error], head *Head, expect []byte) Result {
	_, _, _ = rows, head, expect
	return Result{}
}
