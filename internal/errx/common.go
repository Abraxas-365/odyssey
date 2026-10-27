package errx

// Common error constructors for convenience. Prefer these over calling
// New directly — they keep call sites reading as intent ("this input is
// invalid") rather than a status code lookup.

func Internal(message string) *Error   { return New(message, TypeInternal) }
func Validation(message string) *Error { return New(message, TypeValidation) }
func NotFound(message string) *Error   { return New(message, TypeNotFound) }

// Unauthorized: the caller has no valid credentials (or none at all).
func Unauthorized(message string) *Error {
	err := New(message, TypeAuthorization)
	err.Code = CodeUnauthorized
	return err
}

// Forbidden: the caller IS authenticated but lacks permission for this
// action. Kept distinct from Unauthorized so clients can tell "log in
// again" apart from "you don't have access".
func Forbidden(message string) *Error {
	err := New(message, TypeAuthorization)
	err.Code = CodeForbidden
	err.HTTPStatus = 403
	return err
}

// MethodNotAllowed: the route exists but not for this method (405).
func MethodNotAllowed(message string) *Error { return statusError(message, CodeMethodNotAllowed, 405) }

// PayloadTooLarge: the body is over the route's limit (413).
func PayloadTooLarge(message string) *Error { return statusError(message, CodePayloadTooLarge, 413) }

// UnsupportedMediaType: the body's content type is not accepted here (415).
func UnsupportedMediaType(message string) *Error {
	return statusError(message, CodeUnsupportedMediaType, 415)
}

func statusError(message, code string, status int) *Error {
	err := New(message, TypeValidation)
	err.Code = code
	err.HTTPStatus = status
	return err
}

// WithCode sets a more specific code than the type's default.
func (e *Error) WithCode(code string) *Error {
	e.Code = code
	return e
}

// WithField names the request field (JSON path, "items[2].url") a validation
// error is about; it travels as details.field.
func (e *Error) WithField(field string) *Error { return e.WithDetail("field", field) }

// InvalidField is a validation error about one request field.
func InvalidField(field, message string) *Error { return Validation(message).WithField(field) }

func Conflict(message string) *Error { return New(message, TypeConflict) }
func Business(message string) *Error { return New(message, TypeBusiness) }
func External(message string) *Error { return New(message, TypeExternal) }

// FromStatus builds the error for an HTTP status that did not come from the
// domain (Fiber's own 404/405/413/..., fasthttp read errors), so every
// response uses the one error shape.
func FromStatus(status int, message string) *Error {
	switch status {
	case 400:
		return Validation(message)
	case 401:
		return Unauthorized(message)
	case 403:
		return Forbidden(message)
	case 404:
		return NotFound(message)
	case 405:
		return MethodNotAllowed(message)
	case 409:
		return Conflict(message)
	case 413:
		return PayloadTooLarge(message)
	case 415:
		return UnsupportedMediaType(message)
	case 422:
		return Business(message)
	case 502, 503, 504:
		err := External(message)
		err.HTTPStatus = status
		return err
	}
	if status >= 500 {
		err := Internal(message)
		err.HTTPStatus = status
		return err
	}
	return statusError(message, string(TypeValidation), status)
}
