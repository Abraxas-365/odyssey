package pgerr_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/lib/pq"
)

func TestMapSQLStates(t *testing.T) {
	msgs := pgerr.Messages{Unique: "name taken", ForeignKey: "no such parent", Check: "bad value"}
	cases := []struct {
		code    string
		typ     errx.Type
		status  int
		message string // "" = any
	}{
		{"23505", errx.TypeConflict, 409, "name taken"},
		{"23503", errx.TypeNotFound, 404, "no such parent"},
		{"23514", errx.TypeValidation, 400, "bad value"},
		{"40001", errx.TypeConflict, 409, "concurrent modification, retry"},
		{"40P01", errx.TypeConflict, 409, "concurrent modification, retry"},
		{"22021", errx.TypeValidation, 400, ""}, // invalid byte sequence
		{"22P05", errx.TypeValidation, 400, ""}, // untranslatable character (NUL)
		{"22P02", errx.TypeValidation, 400, ""}, // invalid text representation
		{"22007", errx.TypeValidation, 400, ""}, // invalid datetime format
		{"22008", errx.TypeValidation, 400, ""}, // datetime out of range
		{"42P01", errx.TypeInternal, 500, ""},   // undefined table: a bug, not the client's
		{"57014", errx.TypeInternal, 500, ""},   // statement timeout
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			raw := &pq.Error{Code: pq.ErrorCode(tc.code), Message: "driver detail: table core.secret"}
			for _, err := range []error{raw, fmt.Errorf("wrapped: %w", raw)} {
				got := pgerr.MapWith(err, "save thing", msgs)
				var e *errx.Error
				if !errors.As(got, &e) {
					t.Fatalf("not an errx error: %v", got)
				}
				if e.Type != tc.typ || e.HTTPStatus != tc.status {
					t.Fatalf("got %s/%d, want %s/%d", e.Type, e.HTTPStatus, tc.typ, tc.status)
				}
				if tc.message != "" && e.Message != tc.message {
					t.Fatalf("message %q, want %q", e.Message, tc.message)
				}
				if e.HTTPStatus < 500 && e.Message == "driver detail: table core.secret" {
					t.Fatal("driver message leaked to the client")
				}
			}
		})
	}
}

func TestMapPassThroughAndDefaults(t *testing.T) {
	if pgerr.Map(nil, "op") != nil {
		t.Fatal("nil must stay nil")
	}
	existing := errx.NotFound("thing not found")
	if got := pgerr.Map(existing, "op"); got != existing {
		t.Fatalf("errx error was rewrapped: %v", got)
	}
	var e *errx.Error
	if !errors.As(pgerr.Map(&pq.Error{Code: "23505"}, "op"), &e) || e.Type != errx.TypeConflict || e.Message == "" {
		t.Fatalf("default unique message: %+v", e)
	}
	if !errors.As(pgerr.Map(context.DeadlineExceeded, "op"), &e) || e.Type != errx.TypeInternal {
		t.Fatalf("context errors must stay internal: %+v", e)
	}
	plain := errors.New("connection reset")
	if !errors.As(pgerr.Map(plain, "load thing"), &e) || e.Type != errx.TypeInternal || !errors.Is(e, plain) && !errors.Is(errors.Unwrap(e), plain) {
		t.Fatalf("unknown errors are wrapped as internal: %+v", e)
	}
}
