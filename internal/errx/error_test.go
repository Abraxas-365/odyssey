package errx_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

func TestWrapKeepsTheClientMessageOfTypedErrors(t *testing.T) {
	inner := errx.NotFound("risk not found")
	wrapped := errx.Wrap(inner, "find risk for update", errx.TypeInternal)

	if wrapped.Message != "risk not found" || wrapped.HTTPStatus != 404 || wrapped.Type != errx.TypeNotFound {
		t.Fatalf("typed error changed: %+v", wrapped)
	}
	resp := wrapped.ToResponse("rid")
	if strings.Contains(resp.Message, "find risk") {
		t.Fatalf("op leaked in the response: %q", resp.Message)
	}
	if !strings.Contains(wrapped.Error(), "find risk for update") {
		t.Fatalf("op missing from the log text: %q", wrapped.Error())
	}
	if !errors.Is(wrapped, inner) {
		t.Fatal("cause chain lost")
	}
}

func TestWrapOfUntypedErrorUsesTheMessage(t *testing.T) {
	w := errx.Wrap(errors.New("boom"), "do thing", errx.TypeInternal)
	if w.Message != "do thing" || w.HTTPStatus != 500 {
		t.Fatalf("%+v", w)
	}
}
