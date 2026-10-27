package text_test

import (
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/text"
)

func field(t *testing.T, err error) string {
	t.Helper()
	var e *errx.Error
	if !errx.As(err, &e) || e.Type != errx.TypeValidation {
		t.Fatalf("want a validation error, got %v", err)
	}
	f, _ := e.Details["field"].(string)
	return f
}

func TestRequired(t *testing.T) {
	if err := text.Required("title", "  A title ", 200); err != nil {
		t.Fatal(err)
	}
	if got := field(t, text.Required("title", "  \t ", 200)); got != "title" {
		t.Fatalf("field = %q", got)
	}
}

func TestMaxLengthCountsCharactersAfterTrimming(t *testing.T) {
	if err := text.Optional("name", " "+strings.Repeat("ñ", 200)+" ", 200); err != nil {
		t.Fatalf("200 characters (multi-byte) must pass: %v", err)
	}
	if got := field(t, text.Optional("name", strings.Repeat("ñ", 201), 200)); got != "name" {
		t.Fatalf("field = %q", got)
	}
}

func TestControlCharacters(t *testing.T) {
	for _, ok := range []string{"line1\nline2", "a\tb", "ünï"} {
		if err := text.Optional("description", ok, 100); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"a\x00b", "a\x07b", "a\rb", "\x1b[31m", "a\u0085b"} {
		if got := field(t, text.Optional("description", bad, 100)); got != "description" {
			t.Errorf("%q accepted or wrong field %q", bad, got)
		}
	}
	if got := field(t, text.Optional("note", "bad\xffutf8", 100)); got != "note" {
		t.Fatalf("invalid UTF-8 field = %q", got)
	}
}
