package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestPrivacyFrameTogglesRemoteProviders(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(privacyFrame(s))
	ps.SetSize(60, 20)
	if ps.top().list.CursorRow().title != "Remote providers allowed" {
		t.Fatalf("first row should be Remote providers allowed, got %q", ps.top().list.CursorRow().title)
	}
	before := s.cfg.Privacy.RemoteProvidersAllowed
	ps.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.cfg.Privacy.RemoteProvidersAllowed == before {
		t.Fatal("space should toggle the working copy")
	}
}

func TestWebFrameDurationValidation(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(webFrame(s))
	ps.SetSize(60, 20)
	// move to "Fetch timeout" row
	for ps.top().list.CursorRow().title != "Fetch timeout" {
		ps.Update(kp("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ps.top().list.input.SetValue("45s")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Web.FetchTimeout.String() != "45s" {
		t.Fatalf("expected 45s, got %v", s.cfg.Web.FetchTimeout)
	}
}

func TestSliceOpts(t *testing.T) {
	items := []string{"a", "b", "c"}
	opts := sliceOpts(&items)

	opts.moveUp("1")
	if items[0] != "b" || items[1] != "a" {
		t.Fatalf("moveUp: %v", items)
	}
	opts.moveDown("0")
	if items[0] != "a" || items[1] != "b" {
		t.Fatalf("moveDown: %v", items)
	}
	if got := opts.yank("2"); got != "c" {
		t.Fatalf("yank: %v", got)
	}
	if got := opts.yank("9"); got != nil {
		t.Fatalf("yank out of range: %v", got)
	}
	if err := opts.paste("1", "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "x", "c"}
	if len(items) != 4 || items[2] != "x" {
		t.Fatalf("paste: %v, want %v", items, want)
	}
	if err := opts.paste("0", 42); err == nil {
		t.Fatal("paste of wrong type should fail")
	}
}

func TestSDDSectionRegistered(t *testing.T) {
	registry := BuildRegistry(config.Default())
	for _, key := range []string{"sdd.auto_worktree", "sdd.max_fix_rounds", "sdd.plans_dir"} {
		if _, ok := registry.Lookup(key); !ok {
			t.Errorf("registry missing %s", key)
		}
	}
}

func TestUnsafePassthroughExposedAndRiskyFieldsFlagged(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(sandboxFrame(s))
	ps.SetSize(60, 20)

	// Navigate to the "Allow passthrough backend" row (last row)
	for ps.top().list.CursorRow().title != "Allow passthrough backend" {
		ps.Update(kp("j"))
	}
	row := ps.top().list.CursorRow()
	if row == nil {
		t.Fatal("expected a row for unsafe_passthrough")
	}
	if row.id != "sandbox.unsafe_passthrough" {
		t.Fatalf("expected id sandbox.unsafe_passthrough, got %q", row.id)
	}
	if !row.warn {
		t.Fatal("expected warn=true for unsafe_passthrough")
	}
	if row.kind != kindToggle {
		t.Fatalf("expected kindToggle, got %v", row.kind)
	}

	// Verify it toggles the working copy
	before := s.cfg.Tools.Shell.Sandbox.UnsafePassthrough
	ps.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.cfg.Tools.Shell.Sandbox.UnsafePassthrough == before {
		t.Fatal("space should toggle UnsafePassthrough")
	}

	// Verify the warning indicator appears in the view
	view := ps.top().list.View()
	if !strings.Contains(view, "⚠") {
		t.Fatalf("expected warning indicator in view for risky field, got:\n%s", view)
	}
}

func TestDiagnosticsFrameIsMapAtRoot(t *testing.T) {
	s := newState(config.Default())
	s.cfg.Diagnostics.Commands = map[string]string{"lint": "go vet ./..."}
	ps := newPaneStack(diagnosticsFrame(s))
	ps.SetSize(60, 20)
	view := ps.top().list.View()
	if !strings.Contains(view, "lint") {
		t.Fatalf("diagnostics root should list command keys directly, got:\n%s", view)
	}
}
