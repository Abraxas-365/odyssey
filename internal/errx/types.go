package errx

// Type categorizes an error into a small, closed set of semantic kinds.
// Each kind maps to exactly one HTTP status in typeToHTTPStatus — the
// transport layer never has to re-derive a status code from a message
// string or an error type switch of its own.
type Type string

const (
	TypeInternal      Type = "INTERNAL"
	TypeValidation    Type = "VALIDATION"
	TypeAuthorization Type = "AUTHORIZATION"
	TypeNotFound      Type = "NOT_FOUND"
	TypeConflict      Type = "CONFLICT"
	TypeBusiness      Type = "BUSINESS"
	TypeExternal      Type = "EXTERNAL"
)

func (t Type) String() string { return string(t) }

// Codes that differ from the Type's name (docs/api.md §2).
const (
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeForbidden            = "FORBIDDEN"
	CodeMethodNotAllowed     = "METHOD_NOT_ALLOWED"
	CodePayloadTooLarge      = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedMediaType = "UNSUPPORTED_MEDIA_TYPE"
)
