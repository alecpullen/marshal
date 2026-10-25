package inproc

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// newState builds a state the way the TUI tests do.
func newState(t *testing.T, id string) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: id})
}

func TestSessionIDIsTheStateID(t *testing.T) {
	_, sess := New(Deps{State: newState(t, "s1")})
	if got := sess.ID(); got != "s1" {
		t.Fatalf("ID() = %q, want s1", got)
	}
}

func TestSwapClosesTheOldSession(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1")})
	fresh := h.Swap(newState(t, "s2"))

	if got := fresh.ID(); got != "s2" {
		t.Fatalf("new session ID() = %q, want s2", got)
	}
	if h.current != fresh {
		t.Fatalf("host current is not the session Swap returned")
	}
	if !old.closed.Load() {
		t.Errorf("old session is not closed after Swap")
	}
	if fresh.closed.Load() {
		t.Errorf("new session is closed after Swap")
	}
	if got := old.ID(); got != "s1" {
		t.Errorf("closed session ID() = %q, want its final value s1", got)
	}
}

func TestHostSharesTheFilesystem(t *testing.T) {
	h, _ := New(Deps{State: newState(t, "s1")})
	if !h.SharedFilesystem() {
		t.Fatalf("SharedFilesystem() = false, want true for the in-process adapter")
	}
}

// TestAdapterDoesNotImportApp keeps the adapter out of the app package: TUI
// tests import this package, and app imports tui, so an import back into app
// would be a cycle.
func TestAdapterDoesNotImportApp(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, perr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, spec := range f.Imports {
			imp, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				t.Fatalf("unquote import in %s: %v", name, uerr)
			}
			if imp == "marshal/internal/app" || imp == "marshal/internal/app/tui" ||
				strings.HasPrefix(imp, "marshal/internal/app/tui/") {
				t.Errorf("%s imports %q: the adapter must not import the app or the TUI", name, imp)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no non-test Go files in the package directory")
	}
}
