package admin

import (
	"encoding/base64"
	"encoding/json"

	"github.com/pandokos/pandokos/internal/tenant"
)

type cursor struct {
	S string `json:"s"`
	N string `json:"n,omitempty"`
	I string `json:"i"`
	// F, T and P are the window and the provider filter of a usage list, which a cursor carries
	// because the default window moves with the clock.
	F string `json:"f,omitempty"`
	T string `json:"t,omitempty"`
	P string `json:"p,omitempty"`
	// L is the limit_state filter of a usage list.
	L string `json:"l,omitempty"`
}

func encodeCursor(sort, name, id string) string {
	return encodeFullCursor(cursor{S: sort, N: name, I: id})
}

func encodeFullCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor returns the position a cursor names. An empty cursor is the first page. A cursor
// that does not decode, or that was made for another sort, is invalid_request on cursor.
func decodeCursor(raw, sort string) (after bool, name, id string, err error) {
	if raw == "" {
		return false, "", "", nil
	}
	c, err := decodeFullCursor(raw, sort)
	if err != nil {
		return false, "", "", err
	}
	return true, c.N, c.I, nil
}

// decodeFullCursor decodes a non-empty cursor made for the sort, with every member it carries.
func decodeFullCursor(raw, sort string) (cursor, error) {
	const msg = "is not a cursor of this collection and sort"
	b, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return cursor{}, invalid("cursor", msg)
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.S != sort || len(c.N) > 200 || len(c.F) > 64 || len(c.T) > 64 || len(c.P) > 64 {
		return cursor{}, invalid("cursor", msg)
	}
	if _, perr := tenant.ParseID(c.I); perr != nil {
		return cursor{}, invalid("cursor", msg)
	}
	return c, nil
}
