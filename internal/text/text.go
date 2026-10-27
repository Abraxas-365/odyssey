// Package text validates the free-text fields of request bodies (docs/api.md
// §5): trimmed, bounded in characters, and free of control characters other
// than \n and \t (a NUL would otherwise reach Postgres and fail as a 500).
package text

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Maximum lengths, in characters after trimming.
const (
	MaxName        = 200  // name, title
	MaxDescription = 2000 // description, purpose, justification
	MaxEmail       = 254
	MaxURL         = 2048
	MaxSearch      = 200
)

// Clean trims surrounding white space. Repositories store Clean(value).
func Clean(s string) string { return strings.TrimSpace(s) }

// Required checks a mandatory field: non-empty after trimming, at most max
// characters, no forbidden control characters.
func Required(field, s string, max int) error {
	if strings.TrimSpace(s) == "" {
		return errx.InvalidField(field, field+" is required")
	}
	return Optional(field, s, max)
}

// Optional is Required without the non-empty rule.
func Optional(field, s string, max int) error {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s):
		return errx.InvalidField(field, field+" must be valid UTF-8 text")
	case utf8.RuneCountInString(s) > max:
		return errx.InvalidField(field, field+" must be at most "+itoa(max)+" characters")
	case HasForbiddenControl(s):
		return errx.InvalidField(field, field+" contains control characters that are not allowed")
	}
	return nil
}

// HasForbiddenControl reports a control character other than \n and \t.
func HasForbiddenControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}
