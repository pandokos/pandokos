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
}

func encodeCursor(sort, name, id string) string {
	b, _ := json.Marshal(cursor{S: sort, N: name, I: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor returns the position a cursor names. An empty cursor is the first page. A cursor
// that does not decode, or that was made for another sort, is invalid_request on cursor.
func decodeCursor(raw, sort string) (after bool, name, id string, err error) {
	if raw == "" {
		return false, "", "", nil
	}
	const msg = "is not a cursor of this collection and sort"
	b, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return false, "", "", invalid("cursor", msg)
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.S != sort || len(c.N) > 200 {
		return false, "", "", invalid("cursor", msg)
	}
	if _, perr := tenant.ParseID(c.I); perr != nil {
		return false, "", "", invalid("cursor", msg)
	}
	return true, c.N, c.I, nil
}
