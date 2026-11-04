package acpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// TestNoticeOfReadsTheErrorData checks a failed run's banner comes back from
// the coded error's data, through the wrappers a caller's error collects (C9).
func TestNoticeOfReadsTheErrorData(t *testing.T) {
	notice := acpwire.Notice{
		Category: acpwire.NoticeProvider,
		Severity: acpwire.NoticeError,
		Message:  "boom",
		Hint:     "run /connect",
		Source:   "turn",
		SetAt:    1700000000000,
	}
	withNotice, err := json.Marshal(acpwire.ErrorData{Notice: &notice})
	if err != nil {
		t.Fatalf("marshal ErrorData: %v", err)
	}
	withoutNotice, err := json.Marshal(acpwire.ErrorData{})
	if err != nil {
		t.Fatalf("marshal empty ErrorData: %v", err)
	}

	wrapped := fmt.Errorf("wrap: %w", &acpwire.Error{
		Code:    acpwire.CodeProviderError,
		Message: "boom",
		Data:    withNotice,
	})
	got, ok := NoticeOf(wrapped)
	if !ok {
		t.Fatalf("NoticeOf(%v) reported no notice, want one", wrapped)
	}
	if !reflect.DeepEqual(got, notice) {
		t.Errorf("NoticeOf(%v) = %+v, want %+v", wrapped, got, notice)
	}

	cases := map[string]error{
		"a coded error without data": &acpwire.Error{Code: acpwire.CodeBusy, Message: "busy"},
		"data carrying no notice":    &acpwire.Error{Code: acpwire.CodeProviderError, Data: withoutNotice},
		"an uncoded error":           errors.New("x"),
		"a nil error":                nil,
	}
	for name, e := range cases {
		if n, ok := NoticeOf(e); ok {
			t.Errorf("%s: NoticeOf(%v) = %+v, true, want false", name, e, n)
		}
	}
}
