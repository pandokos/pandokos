// Package auditchain is the one Go definition of the canonical encoding of an audit row and of its
// hash chain (ADR 0065); the SQL definition is audit_row_hash in migration 0015. The two are tested
// against each other. Nothing here reads a database.
package auditchain

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"sort"
	"strconv"
	"time"
)

// version tags every hash; a change of the encoding changes it (ADR 0065).
const version = "pandokos-audit-v1"

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
	// Start is the sequence number the first row read must have: the one the reader asked for
	// (--from-seq). Zero means the anchor. A first row above it is a missing row (seq_gap); only a
	// Start above the anchor whose row is present leaves its link to the earlier rows unverified.
	Start int64
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

// field is F(s): the byte length of the UTF-8 text as four bytes, big-endian, then the text.
func field(b *bytes.Buffer, s string) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(s))) //nolint:gosec // a detail is far below 4 GiB
	b.Write(n[:])
	b.WriteString(s)
}

func count(b *bytes.Buffer, n int) {
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], uint32(n)) //nolint:gosec // a member count is far below 4 billion
	b.Write(c[:])
}

// Canonical returns the canonical encoding (version 1) of a JSON detail: objects with their keys
// sorted by UTF-8 bytes, numbers as the text they are written in, no float ever involved. A
// duplicate key, trailing data or invalid JSON is an error.
func Canonical(detail json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(detail))
	dec.UseNumber()
	var b bytes.Buffer
	if err := encodeValue(dec, &b); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("auditchain: data after the JSON value")
	}
	return b.Bytes(), nil
}

func encodeValue(dec *json.Decoder, b *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("auditchain: invalid JSON: %w", err)
	}
	switch t := tok.(type) {
	case nil:
		b.WriteByte('z')
	case bool:
		if t {
			b.WriteByte('t')
		} else {
			b.WriteByte('f')
		}
	case string:
		b.WriteByte('s')
		field(b, t)
	case json.Number:
		b.WriteByte('n')
		field(b, t.String())
	case json.Delim:
		switch t {
		case '{':
			return encodeObject(dec, b)
		case '[':
			return encodeArray(dec, b)
		}
		return errors.New("auditchain: invalid JSON")
	default:
		return errors.New("auditchain: invalid JSON")
	}
	return nil
}

func encodeObject(dec *json.Decoder, b *bytes.Buffer) error {
	members := map[string][]byte{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return fmt.Errorf("auditchain: invalid JSON: %w", err)
		}
		key, ok := kt.(string)
		if !ok {
			return errors.New("auditchain: invalid JSON: object key")
		}
		if _, dup := members[key]; dup {
			return fmt.Errorf("auditchain: duplicate key %q", key)
		}
		var v bytes.Buffer
		if err := encodeValue(dec, &v); err != nil {
			return err
		}
		members[key] = v.Bytes()
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return fmt.Errorf("auditchain: invalid JSON: %w", err)
	}
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	sort.Strings(keys) // byte order of the UTF-8 text
	b.WriteByte('o')
	count(b, len(keys))
	for _, k := range keys {
		field(b, k)
		b.Write(members[k])
	}
	return nil
}

func encodeArray(dec *json.Decoder, b *bytes.Buffer) error {
	var elems bytes.Buffer
	n := 0
	for dec.More() {
		if err := encodeValue(dec, &elems); err != nil {
			return err
		}
		n++
	}
	if _, err := dec.Token(); err != nil { // the closing bracket
		return fmt.Errorf("auditchain: invalid JSON: %w", err)
	}
	b.WriteByte('a')
	count(b, n)
	b.Write(elems.Bytes())
	return nil
}

// DetailDigest returns the SHA-256 of the canonical encoding of a detail.
func DetailDigest(detail json.RawMessage) ([32]byte, error) {
	c, err := Canonical(detail)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(c), nil
}

// Hash returns the hash of a row: it uses r.DetailDigest when set, else the digest of r.Detail.
func Hash(r Row) ([32]byte, error) {
	var digest []byte
	if len(r.DetailDigest) > 0 {
		if len(r.DetailDigest) != sha256.Size {
			return [32]byte{}, errors.New("auditchain: detail digest is not 32 bytes")
		}
		digest = r.DetailDigest
	} else {
		d, err := DetailDigest(r.Detail)
		if err != nil {
			return [32]byte{}, err
		}
		digest = d[:]
	}
	var b bytes.Buffer
	field(&b, version)
	b.Write(r.PrevHash)
	for _, s := range []string{
		r.Scope,
		strconv.FormatInt(r.Seq, 10),
		r.ID,
		r.OccurredAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
		r.ActorType, r.ActorID, r.ActorName, r.KeyID, r.Action,
		r.ResourceType, r.ResourceID, r.Outcome, r.RequestID, r.Reason, r.SupportActionID,
	} {
		field(&b, s)
	}
	b.Write(digest)
	return sha256.Sum256(b.Bytes()), nil
}

// Verify checks a chain row by row against its head and, when given, an expected head hash. It
// stops reading at the first error the sequence yields; the caller reports that error.
func Verify(rows iter.Seq2[Row, error], head *Head, expect []byte) Result {
	var res Result
	var prev []byte
	var next int64
	for r, err := range rows {
		if err != nil {
			break
		}
		if res.Rows == 0 {
			res.First = r.Seq
			switch {
			case head != nil && r.Seq == head.AnchorSeq:
				if !bytes.Equal(r.PrevHash, head.AnchorHash) {
					res.Breaks = append(res.Breaks, Break{r.Seq, ReasonAnchorMismatch})
				}
			case r.Seq == 1:
				if !bytes.Equal(r.PrevHash, make([]byte, sha256.Size)) {
					res.Breaks = append(res.Breaks, Break{r.Seq, ReasonAnchorMismatch})
				}
			default:
				res.LinkUnverified = true
			}
		} else if r.Seq != next {
			res.Breaks = append(res.Breaks, Break{r.Seq, ReasonSeqGap})
		} else if !bytes.Equal(r.PrevHash, prev) {
			res.Breaks = append(res.Breaks, Break{r.Seq, ReasonPrevMismatch})
		}
		if h, err := Hash(r); err != nil || !bytes.Equal(h[:], r.Hash) {
			res.Breaks = append(res.Breaks, Break{r.Seq, ReasonHashMismatch})
		}
		res.Rows++
		res.Last = r.Seq
		res.LastHash = r.Hash
		prev = r.Hash
		next = r.Seq + 1
	}
	if head != nil {
		switch {
		case res.Rows == 0:
			if head.Seq != 0 {
				res.Breaks = append(res.Breaks, Break{head.Seq, ReasonHeadMismatch})
			}
		case res.Last != head.Seq || !bytes.Equal(res.LastHash, head.Hash):
			res.Breaks = append(res.Breaks, Break{res.Last, ReasonHeadMismatch})
		}
	}
	if expect != nil && !bytes.Equal(res.LastHash, expect) {
		res.Breaks = append(res.Breaks, Break{res.Last, ReasonExpectedHeadMismatch})
	}
	return res
}
