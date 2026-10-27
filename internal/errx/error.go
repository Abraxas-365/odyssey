package errx

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Error is the one error type that should reach a transport boundary
// (HTTP handler, gRPC interceptor, CLI exit). Construct it only through
// this package's functions (New, Wrap, or the semantic helpers in
// common.go) so Code/Type/HTTPStatus always stay consistent with each other.
type Error struct {
	Code       string                 `json:"code"`
	Message    string                 `json:"message"`
	Type       Type                   `json:"type"`
	HTTPStatus int                    `json:"http_status"`
	Details    map[string]interface{} `json:"details,omitempty"`
	Err        error                  `json:"-"`
	// Op is the operation context added by Wrap on an already-typed error.
	// It is for logs only (Error()); it never reaches a response body.
	Op string `json:"-"`
}

func (e *Error) Error() string {
	if e.Err != nil {
		if e.Op != "" {
			return fmt.Sprintf("[%s] %s (%s): %v", e.Code, e.Message, e.Op, e.Err)
		}
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) WithDetail(key string, value interface{}) *Error {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// MarshalJSON implements json.Marshaler.
func (e *Error) MarshalJSON() ([]byte, error) {
	type Alias Error
	return json.Marshal((*Alias)(e))
}

func New(message string, errType Type) *Error {
	return &Error{
		Code:       string(errType),
		Message:    message,
		Type:       errType,
		HTTPStatus: typeToHTTPStatus(errType),
		Details:    make(map[string]interface{}),
	}
}

// Wrap wraps an existing error with additional context. If err is already
// an *Error, its Code/Type/HTTPStatus/Message/Details are preserved and
// message is kept only as Op (log context): the client-facing text of a typed
// error is never replaced by an operation name. Re-wrapping a NotFound never
// turns it into an Internal by accident.
func Wrap(err error, message string, errType Type) *Error {
	if err == nil {
		return nil
	}
	var existingErr *Error
	if errors.As(err, &existingErr) {
		return &Error{
			Code:       existingErr.Code,
			Message:    existingErr.Message,
			Type:       existingErr.Type,
			HTTPStatus: existingErr.HTTPStatus,
			Details:    existingErr.Details,
			Err:        err,
			Op:         message,
		}
	}
	return &Error{
		Code:       string(errType),
		Message:    message,
		Type:       errType,
		HTTPStatus: typeToHTTPStatus(errType),
		Details:    make(map[string]interface{}),
		Err:        err,
	}
}

func Wrapf(err error, errType Type, format string, args ...interface{}) *Error {
	return Wrap(err, fmt.Sprintf(format, args...), errType)
}

func Is(err, target error) bool             { return errors.Is(err, target) }
func As(err error, target interface{}) bool { return errors.As(err, target) }

// IsType reports whether err is (or wraps) an *Error of type t.
func IsType(err error, t Type) bool {
	var e *Error
	return errors.As(err, &e) && e.Type == t
}

func typeToHTTPStatus(t Type) int {
	switch t {
	case TypeValidation:
		return 400
	case TypeAuthorization:
		return 401
	case TypeNotFound:
		return 404
	case TypeConflict:
		return 409
	case TypeBusiness:
		return 422
	case TypeExternal:
		return 502
	case TypeInternal:
		return 500
	default:
		return 500
	}
}
