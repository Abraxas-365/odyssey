// Package httpx holds Fiber-specific helpers: request parsing (JSON bodies,
// pagination, filters), IAMKit authentication, the headers every response
// carries and the error handler that turns every error into the one error
// body of docs/api.md §3.
package httpx

import (
	"errors"
	"log/slog"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/gofiber/fiber/v2"
)

// ErrorHandler answers every error with {"error": {...}} (docs/api.md §3):
// *errx.Error as typed, Fiber's own errors (404, 405, 413...) mapped onto the
// same shape, anything else as a generic 500. 5xx bodies never carry the
// cause; it goes to the log together with the request id.
func ErrorHandler(c *fiber.Ctx, err error) error {
	var e *errx.Error
	var fe *fiber.Error
	switch {
	case errors.As(err, &e):
	case errors.As(err, &fe):
		e = errx.FromStatus(fe.Code, fiberMessage(fe))
		err = fe
	default:
		e = errx.Internal(errx.GenericMessage)
	}
	id := ensureRequestID(c)
	SecurityHeaders(c)
	if e.HTTPStatus >= 500 {
		slog.Error("request failed", "request_id", id, "method", c.Method(), "path", c.Path(),
			"status", e.HTTPStatus, "code", e.Code, "err", err)
	}
	if e.HTTPStatus == fiber.StatusUnauthorized {
		c.Set(fiber.HeaderWWWAuthenticate, "Bearer")
	}
	return c.Status(e.HTTPStatus).JSON(fiber.Map{"error": e.ToResponse(id)})
}

// fiberMessage is the client text for an error Fiber raised itself.
func fiberMessage(e *fiber.Error) string {
	switch e.Code {
	case fiber.StatusNotFound:
		return "route not found"
	case fiber.StatusMethodNotAllowed:
		return "method not allowed"
	case fiber.StatusRequestEntityTooLarge:
		return "request body is too large"
	case fiber.StatusUnsupportedMediaType:
		return "unsupported media type"
	case fiber.StatusRequestTimeout:
		return "request timed out"
	case fiber.StatusBadRequest:
		return "malformed request"
	}
	return e.Message
}
