// Package pgerr maps Postgres driver errors into errx once, so repositories
// never leak raw driver errors and every package answers the same SQLSTATE
// the same way.
package pgerr

import (
	"context"
	"errors"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/lib/pq"
)

// Messages are the client-facing texts a repository wants for the
// constraint violations it can cause. Empty fields use generic texts.
type Messages struct {
	Unique     string // 23505 unique_violation → Conflict
	ForeignKey string // 23503 foreign_key_violation → NotFound
	Check      string // 23514 check_violation → Validation
}

// Map turns err into an errx error; nil stays nil and an *errx.Error passes
// through unchanged. op names the failed operation for the log (it never
// reaches a client: 5xx bodies are generic).
func Map(err error, op string) error { return MapWith(err, op, Messages{}) }

// MapWith is Map with repository-specific messages.
//
//	23505                     → Conflict
//	23503                     → NotFound
//	23514                     → Validation
//	40001, 40P01              → Conflict "concurrent modification, retry"
//	22021, 22P05, 22P02, 22007, 22008, 22003, 22001
//	                          → Validation (bad byte sequence, NUL, bad
//	                            text representation, bad date, out of range)
//	anything else             → Internal (wrapped, logged by the error handler)
func MapWith(err error, op string, m Messages) error {
	if err == nil {
		return nil
	}
	var e *errx.Error
	if errors.As(err, &e) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errx.Wrap(err, op, errx.TypeInternal)
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code {
		case "23505":
			return errx.Conflict(or(m.Unique, "a resource with these values already exists"))
		case "23503":
			return errx.NotFound(or(m.ForeignKey, "a referenced resource does not exist"))
		case "23514":
			return errx.Validation(or(m.Check, "a value is not allowed"))
		case "40001", "40P01":
			return errx.Conflict("concurrent modification, retry")
		case "22021", "22P05":
			return errx.Validation("text contains characters that are not allowed")
		case "22P02", "22007", "22008", "22003", "22001":
			return errx.Validation("a value has an invalid format or is out of range")
		}
	}
	return errx.Wrap(err, op, errx.TypeInternal)
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
