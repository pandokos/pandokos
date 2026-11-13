package store

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// ConstraintViolation classifies err as a violated constraint: kind is "unique", "check",
// "foreign_key", "restrict" or "not_null"; constraint is the name the database reports.
func ConstraintViolation(err error) (kind, constraint string, ok bool) {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return "", "", false
	}
	switch pe.Code {
	case "23505":
		kind = "unique"
	case "23514":
		kind = "check"
	case "23503":
		kind = "foreign_key"
	case "23001":
		kind = "restrict"
	case "23502":
		kind = "not_null"
	default:
		return "", "", false
	}
	return kind, pe.ConstraintName, true
}

// SQLState returns the SQLSTATE of a database error, or "".
func SQLState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// IsUnavailable reports whether err is about the database being out of reach: a connection
// failure, a timeout, a cancelled context, a shutdown.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return true
	}
	if pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if st := SQLState(err); len(st) >= 2 {
		switch st[:2] {
		case "08", "53", "57":
			return true
		}
	}
	return strings.Contains(err.Error(), "failed to connect") || strings.Contains(err.Error(), "conn closed")
}
