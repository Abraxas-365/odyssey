package httpx

import (
	"regexp"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// requestIDPattern is what a client-sent X-Request-ID may look like besides a
// UUID (docs/api.md §10): short and free of anything a log line could be
// forged with.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidRequestID reports whether a client-sent X-Request-ID is accepted: a
// UUID, or up to 64 characters of [A-Za-z0-9-_].
func ValidRequestID(s string) bool {
	if requestIDPattern.MatchString(s) {
		return true
	}
	_, err := uuid.Parse(s)
	return err == nil && len(s) == 36
}

// RequestID gives every response an X-Request-ID: the client's when valid,
// else a new UUID.
func RequestID(c *fiber.Ctx) error {
	id := c.Get(fiber.HeaderXRequestID)
	if !ValidRequestID(id) {
		id = uuid.NewString()
	}
	c.Set(fiber.HeaderXRequestID, id)
	return c.Next()
}

// ensureRequestID returns the response's request id, creating one when the
// request never went through RequestID (errors Fiber raises while reading it).
func ensureRequestID(c *fiber.Ctx) string {
	if id := c.GetRespHeader(fiber.HeaderXRequestID); id != "" {
		return id
	}
	id := c.Get(fiber.HeaderXRequestID)
	if !ValidRequestID(id) {
		id = uuid.NewString()
	}
	c.Set(fiber.HeaderXRequestID, id)
	return id
}

// SecurityHeaders sets the headers of docs/api.md §10. Cache-Control is set
// only when the handler has not (file downloads keep their own).
func SecurityHeaders(c *fiber.Ctx) {
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	c.Set(fiber.HeaderReferrerPolicy, "no-referrer")
	if c.GetRespHeader(fiber.HeaderCacheControl) == "" {
		c.Set(fiber.HeaderCacheControl, "no-store")
	}
	if c.Secure() || c.Get(fiber.HeaderXForwardedProto) == "https" {
		c.Set(fiber.HeaderStrictTransportSecurity, "max-age=31536000; includeSubDomains")
	}
}

// Headers is the middleware form of SecurityHeaders.
func Headers(c *fiber.Ctx) error {
	SecurityHeaders(c)
	return c.Next()
}

// RejectContentEncoding answers 415 to any request carrying a Content-Encoding
// other than "identity". Fiber's c.Body() inflates gzip/deflate/br bodies
// without a cap (after the size check), so a tiny compressed body could
// expand without bound; the API accepts no compressed request bodies at all.
// Install it globally, before any handler reads the body.
func RejectContentEncoding(c *fiber.Ctx) error {
	for _, v := range c.Request().Header.PeekAll(fiber.HeaderContentEncoding) {
		enc := strings.ToLower(strings.TrimSpace(string(v)))
		if enc != "" && enc != "identity" {
			return errx.UnsupportedMediaType("Content-Encoding is not supported (send the body uncompressed)")
		}
	}
	return c.Next()
}
