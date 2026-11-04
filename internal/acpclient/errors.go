package acpclient

import (
	"encoding/json"
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

// NoticeOf returns the notice an *acpwire.Error's data carries: the banner the
// agent classified a failed run's error as (C9). ok is false for any other
// error, a coded one without a notice included.
func NoticeOf(err error) (acpwire.Notice, bool) {
	var coded *acpwire.Error
	if !errors.As(err, &coded) {
		return acpwire.Notice{}, false
	}
	if len(coded.Data) == 0 {
		return acpwire.Notice{}, false
	}
	var data acpwire.ErrorData
	if uerr := json.Unmarshal(coded.Data, &data); uerr != nil {
		return acpwire.Notice{}, false
	}
	if data.Notice == nil {
		return acpwire.Notice{}, false
	}
	return *data.Notice, true
}
