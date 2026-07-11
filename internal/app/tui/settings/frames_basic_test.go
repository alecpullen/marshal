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
