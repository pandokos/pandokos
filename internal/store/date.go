package store

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Date returns t as a value of the SQL type date, the civil date of t as it is written: callers
// outside this package do not import pgx.
func Date(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }
