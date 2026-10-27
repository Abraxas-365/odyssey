package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"reflect"
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/gofiber/fiber/v2"
)

// MaxJSONBody is the default body limit (docs/api.md §9); the server rejects
// larger bodies before buffering them, DecodeJSON checks again.
const MaxJSONBody = 1 << 20

// DecodeJSON reads the request body into into, strictly:
//   - the body must be present and the Content-Type application/json (415);
//   - at most MaxJSONBody bytes (413);
//   - unknown fields are an error naming the field (400);
//   - exactly one JSON value, nothing after it (400).
func DecodeJSON(c *fiber.Ctx, into any) error {
	body := c.Body()
	if len(body) == 0 {
		return errx.Validation("request body is required")
	}
	if !IsJSON(c) {
		return errx.UnsupportedMediaType("Content-Type must be application/json")
	}
	if len(body) > MaxJSONBody {
		return errx.PayloadTooLarge("request body is too large (max 1 MiB)")
	}
	return decodeStrict(body, into)
}

// DecodePatch is DecodeJSON for PATCH bodies. Only the fields listed in
// nullable (JSON paths, "treatment.due_date" for nested ones) accept an
// explicit null, which clears them; null on any other field is a 400 instead
// of being read as "absent" (docs/api.md §6).
func DecodePatch(c *fiber.Ctx, into any, nullable ...string) error {
	if err := DecodeJSON(c, into); err != nil {
		return err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(c.Body(), &top); err != nil || top == nil {
		return errx.Validation("request body must be a JSON object") // array, string, number or null
	}
	return rejectNulls(top, "", nullable)
}

func rejectNulls(object map[string]json.RawMessage, prefix string, nullable []string) error {
	for name, raw := range object {
		path := prefix + name
		raw = bytes.TrimSpace(raw)
		switch {
		case bytes.Equal(raw, []byte("null")):
			if !slices.Contains(nullable, path) {
				return errx.InvalidField(path, path+" cannot be null")
			}
		case len(raw) > 0 && raw[0] == '{':
			var inner map[string]json.RawMessage
			if json.Unmarshal(raw, &inner) == nil {
				if err := rejectNulls(inner, path+".", nullable); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// IsJSON reports an application/json Content-Type (any charset parameter).
func IsJSON(c *fiber.Ctx) bool {
	mt, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	return err == nil && mt == fiber.MIMEApplicationJSON
}

// IsMultipart reports a multipart/form-data Content-Type.
func IsMultipart(c *fiber.Ctx) bool {
	mt, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	return err == nil && mt == fiber.MIMEMultipartForm
}

func decodeStrict(body []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return jsonError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errx.Validation("request body must contain a single JSON value")
	}
	return nil
}

func jsonError(err error) error {
	var e *errx.Error
	if errors.As(err, &e) {
		return e
	}
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return errx.Validation("request body is not valid JSON")
	case errors.As(err, &typ):
		field := typ.Field
		if field == "" {
			return errx.Validation("request body must be a JSON " + jsonKind(typ.Type))
		}
		return errx.InvalidField(field, field+" must be "+article(jsonKind(typ.Type)))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		name := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return errx.InvalidField(name, "unknown field "+`"`+name+`"`)
	}
	return errx.Validation("request body is invalid")
}

// jsonKind names the JSON type a Go type reads from.
func jsonKind(t reflect.Type) string {
	if t == nil {
		return "value"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	case reflect.Pointer:
		return jsonKind(t.Elem())
	}
	return "value"
}

func article(kind string) string {
	if kind == "array" || kind == "integer" || kind == "object" {
		return "an " + kind
	}
	return "a " + kind
}
