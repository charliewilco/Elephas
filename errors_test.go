package elephas

import (
	"errors"
	"testing"
)

func TestErrorImplementsUnwrap(t *testing.T) {
	inner := errors.New("boom")
	err := WrapError(ErrorCodeConflict, "failed to save", inner, map[string]any{"resource": "entity"})

	if got := errors.Unwrap(err); got != inner {
		t.Fatalf("expected unwrap to return inner error, got %#v", got)
	}
}

func TestNilErrorBehavesGracefully(t *testing.T) {
	var err *Error

	if got := err.Error(); got != "<nil>" {
		t.Fatalf("expected nil error string, got %q", got)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("expected unwrap on nil to return nil")
	}
}
