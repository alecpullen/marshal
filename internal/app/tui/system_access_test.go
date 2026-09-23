// internal/app/tui/system_access_test.go — TUI surfaces for the per-session
// system-access modifier: the /system command, the status badge, the
// agent-requested elevation, and the exit-postmortem grant.
package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/postmortempanel"
	"marshal/internal/commands"
	"marshal/internal/tools/policy"
)

// TestSystemCommandTogglesFlagAndNotes pins the /system command: it flips the
// session flag and confirms the new state in the transcript.
func TestSystemCommandTogglesFlagAndNotes(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)

	if state.SystemAccess() {
		t.Fatal("SystemAccess() = true on a fresh session, want false")
	}

	updated, _ := m.dispatchCommand("/system")
	m = asModel(t, updated)
	if !state.SystemAccess() {
		t.Fatal("/system did not enable system access")
	}
	if got := lastMessageContent(state.Messages()); !strings.Contains(got, "System access enabled") {
		t.Fatalf("transcript note after enabling = %q, want the enabled note", got)
	}

	updated, _ = m.dispatchCommand("/system")
	m = asModel(t, updated)
	if state.SystemAccess() {
		t.Fatal("/system did not disable system access")
	}
	if got := lastMessageContent(state.Messages()); !strings.Contains(got, "System access disabled") {
		t.Fatalf("transcript note after disabling = %q, want the disabled note", got)
	}
}

func lastMessageContent(msgs []session.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Content
}

// TestStatusBadgeSystemSuffix pins the status-bar disclosure: the mode cue
// gains a " · system" suffix only while the flag is on.
func TestStatusBadgeSystemSuffix(t *testing.T) {
	m := newStatusTestModel(t)

	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "· system") {
		t.Fatalf("status line shows the system suffix with the flag off:\n%s", line)
	}

	m.state.SetSystemAccess(true)
	line = stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "· system") {
		t.Fatalf("status line missing the system suffix with the flag on:\n%s", line)
	}
}

// TestModePickerExcludesSystemModifier pins that the modifier is not offered
// as a rung on the mode ladder — it must not be confusable with a mode.
func TestModePickerExcludesSystemModifier(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	model := New(state)
	for _, item := range model.modePickerItems() {
		if item.Value == "system" {
			t.Fatal("modePickerItems must not offer the system modifier as a mode")
		}
	}
}

// TestModeElevationSystemRequestGrantsFlag pins the agent-requested
// elevation: the picker discloses the grant, and approving applies it.
func TestModeElevationSystemRequestGrantsFlag(t *testing.T) {
	m := newViewTestModel(t, 200, 30)
	m.state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call_mode_sys",
		Name:         "mode.request",
		Args:         `{"mode":"edit","system":true}`,
		Reason:       "mode-elevation: agent requests system access",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	m.refreshViewport()

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x'})
	m = updated.(Model)
	if !m.dock.IsOpen() {
		t.Fatal("mode-elevation picker did not open in the dock")
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "system access") {
		t.Fatalf("picker does not disclose the system-access request:\n%s", view)
	}

	updated, _ = m.Update(picker.PickedMsg{Value: "edit"})
	m = updated.(Model)
	if !m.state.SystemAccess() {
		t.Fatal("approving a system mode.request did not grant system access")
	}
}

// TestModeElevationPlainRequestDoesNotGrantSystem pins that an ordinary
// mode.request leaves the flag alone.
func TestModeElevationPlainRequestDoesNotGrantSystem(t *testing.T) {
	m := newViewTestModel(t, 200, 30)
	m.state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call_mode_plain",
		Name:         "mode.request",
		Args:         `{"mode":"edit"}`,
		Reason:       "mode-elevation: agent requests an editing mode",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	m.refreshViewport()

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x'})
	m = updated.(Model)
	updated, _ = m.Update(picker.PickedMsg{Value: "edit"})
	m = updated.(Model)
	if m.state.SystemAccess() {
		t.Fatal("a plain mode.request granted system access")
	}
}

// TestPostmortemAgentPassGrantsSystemAccess pins the motivating case: the
// exit postmortem's agent pass writes a report at an absolute path outside
// the workspace, so the grant must be in place before the turn starts — and
// plan/default must be elevated so the pass is not mode-denied.
func TestPostmortemAgentPassGrantsSystemAccess(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	state := postmortemTestState(t, "sess-sysgrant")
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.Config.Postmortem.OnExit = "prompt"
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := New(state, WithRunner(context.Background(), runner))
	m.resize(80, 24)
	m.approvalMode = policy.ModeDefault

	if cmd := m.beginShutdown(true); cmd != nil {
		t.Fatalf("prompt path returned a cmd (%T), want nil", cmd)
	}
	updated, _ := m.Update(postmortempanel.DoneMsg{Result: postmortempanel.ResultWithAgent})
	m = asModel(t, updated)

	if !state.SystemAccess() {
		t.Fatal("system access not granted for the postmortem agent pass")
	}
	if m.approvalMode != policy.ModeEdit {
		t.Fatalf("approvalMode = %q, want edit so the pass is not mode-denied", m.approvalMode)
	}
}

// TestPostmortemAgentPassRefusedStartClearsSystemAccess pins the cleanup
// half: a refused start must not leave the grant behind.
func TestPostmortemAgentPassRefusedStartClearsSystemAccess(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	state := postmortemTestState(t, "sess-sysrefused")
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.Config.Postmortem.OnExit = "prompt"
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := New(state, WithRunner(context.Background(), runner))
	m.resize(80, 24)

	if cmd := m.beginShutdown(true); cmd != nil {
		t.Fatalf("prompt path returned a cmd (%T), want nil", cmd)
	}
	state.BeginQuiesce() // BeginWork now refuses, so the start fails.
	updated, _ := m.Update(postmortempanel.DoneMsg{Result: postmortempanel.ResultWithAgent})
	m = asModel(t, updated)

	if state.SystemAccess() {
		t.Fatal("system access survived a refused postmortem agent start")
	}
	if m.postmortemAgentPending {
		t.Fatal("postmortemAgentPending survived a refused start")
	}
}
