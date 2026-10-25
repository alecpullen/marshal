package acpclient

import (
	"errors"

	"marshal/internal/acpwire"
)

// IsCode reports whether errors.As finds an *acpwire.Error with code in err.
//
// Callers branch on it instead of matching error text, so a coded failure keeps
// its meaning through every wrapper between the agent side and the TUI.
func IsCode(err error, code acpwire.ErrorCode) bool {
	found, ok := CodeOf(err)
	return ok && found == code
}

// CodeOf returns the code of the *acpwire.Error that errors.As finds in err.
//
// It returns "", false when it finds none, a nil err included.
func CodeOf(err error) (acpwire.ErrorCode, bool) {
	var coded *acpwire.Error
	if !errors.As(err, &coded) {
		return "", false
	}
	return coded.Code, true
}
