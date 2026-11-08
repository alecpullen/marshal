package service

import (
	"context"
	"fmt"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// fakeFinder answers the one lookup a callers test makes.
type fakeFinder struct {
	refs   []string
	ok     bool
	calls  int
	asked  []string // "file:line:col" of each lookup
	failIf func()
}

// References is one lookup of a symbol's references.
func (f *fakeFinder) References(ctx context.Context, path string, line, col int) ([]string, bool) {
	if f.failIf != nil {
		f.failIf()
	}
	f.calls++
	f.asked = append(f.asked, fmt.Sprintf("%s:%d:%d", path, line, col))
	return f.refs, f.ok
}

// The callers cases live in the conformance harness now: they read the same
// rows through both arms of the seam (V1). What stays here is the closed
// session's contract, which the client's mirror cannot see.
func TestCallersOnAReplacedSessionFails(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1"), ReferenceFinder: &fakeFinder{ok: true}})
	h.swap(newState(t, "s2"))

	if _, err := old.Callers(context.Background(), "tool:1:c1"); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("Callers on a closed session = %v, want the %q code", err, acpwire.CodeSessionClosed)
	}
}
