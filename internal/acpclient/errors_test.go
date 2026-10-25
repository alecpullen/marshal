package acpclient

import (
	"errors"
	"fmt"
	"testing"

	"marshal/internal/acpwire"
)

// TestIsCodeSeesThroughWrapping checks the code survives a wrapper, which is
// how a coded failure reaches the TUI (C2).
func TestIsCodeSeesThroughWrapping(t *testing.T) {
	err := fmt.Errorf("wrap: %w", &acpwire.Error{Code: acpwire.CodeBusy, Message: "busy"})

	if !IsCode(err, acpwire.CodeBusy) {
		t.Errorf("IsCode(%v, %q) = false, want true", err, acpwire.CodeBusy)
	}
	if IsCode(err, acpwire.CodeQuiescing) {
		t.Errorf("IsCode(%v, %q) = true, want false", err, acpwire.CodeQuiescing)
	}
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("CodeOf(%v) reported no code, want %q", err, acpwire.CodeBusy)
	}
	if code != acpwire.CodeBusy {
		t.Errorf("CodeOf(%v) = %q, want %q", err, code, acpwire.CodeBusy)
	}
}

// TestCodeOfAnUncodedError checks an error without a code reports none, a nil
// error included.
func TestCodeOfAnUncodedError(t *testing.T) {
	for _, err := range []error{errors.New("x"), nil} {
		code, ok := CodeOf(err)
		if ok || code != "" {
			t.Errorf("CodeOf(%v) = %q, %t, want \"\", false", err, code, ok)
		}
	}
}
