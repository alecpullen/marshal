package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// The welcome banner is startup identity: it must render on a fresh boot
// even when boot-time system items (the autoloaded-skill body, startup
// notices) are already in the transcript. Regression: autoloading
// using-skills by default put a system message in every fresh session,
// and the old len(items)==0 gate hid the banner on every first run.
func TestWelcomeBannerSurvivesBootSystemItems(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleSystem, "skill body here", session.ContentTypePlain)
	m.resize(220, 80)
	m.refreshViewport()
	plain := stripANSI(m.viewString())
	if !strings.Contains(plain, "Type a question") {
		t.Fatal("welcome banner must render alongside boot-time system items")
	}

	// A real conversation turn still suppresses it (resumed sessions don't
	// get the banner).
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m2 := m
	m2.refreshViewport()
	plain2 := stripANSI(m2.viewString())
	if strings.Contains(plain2, "Type a question") {
		t.Fatal("welcome banner must not render once the conversation has turns")
	}
}

// TestWelcomeBannerReadsTheWire pins decision 10's rule: the banner shows
// until the tree holds a user turn or assistant text. A boot-time system item
// and a beat that ran a tool without narrating keep it; an assistant message
// on its own ends it.
func TestWelcomeBannerReadsTheWire(t *testing.T) {
	m := newTestModel(t)
	m.resize(220, 80)

	m.state.AddMessage(session.RoleSystem, "skill body here", session.ContentTypePlain)
	m.invalidateTranscript()
	m.refreshViewport()
	if plain := stripANSI(m.viewString()); !strings.Contains(plain, "Type a question") {
		t.Fatal("a boot-time system item must not end the welcome banner")
	}

	step := m.state.BeginStep(session.Actor{})
	m.state.LogToolCall(registry.AuditEvent{
		ToolName: "file.read",
		Args:     json.RawMessage(`{"path":"main.go"}`),
	})
	m.state.EndStep(step)
	m.invalidateTranscript()
	m.refreshViewport()
	if plain := stripANSI(m.viewString()); !strings.Contains(plain, "Type a question") {
		t.Fatal("a beat with no narration must not end the welcome banner")
	}

	m.state.AddMessage(session.RoleAssistant, "Here is the file.", session.ContentTypePlain)
	m.invalidateTranscript()
	m.refreshViewport()
	if plain := stripANSI(m.viewString()); strings.Contains(plain, "Type a question") {
		t.Fatal("an assistant message must end the welcome banner")
	}
}
