package tui

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func TestTranscriptViewOverrideAndSelectionGuard(t *testing.T) {
	m := newTestModel(t)
	if got := m.effectiveTranscriptView(); got != config.TranscriptLegacy {
		t.Fatalf("default view = %q", got)
	}
	m.selection.dragging = true
	m.setTranscriptView(config.TranscriptNotebook)
	if !m.selection.dragging || !m.pendingTranscriptViewIs(config.TranscriptNotebook) || m.notebookView {
		t.Fatal("switch changed the active view or selection during a drag")
	}
	m.clearSelection()
	if m.selection.dragging || !m.notebookView {
		t.Fatal("pending view did not apply after selection cleared")
	}
	m.transcriptViewOverride = transcriptViewPtr(config.TranscriptNotebook)
	if got := m.effectiveTranscriptView(); got != config.TranscriptNotebook {
		t.Fatalf("override = %q", got)
	}
	m.transcriptViewOverride = nil
	m.setTranscriptView(m.configuredTranscriptView())
	if got := m.effectiveTranscriptView(); got != m.configuredTranscriptView() {
		t.Fatalf("reset did not follow configured default: %q", got)
	}
}

func TestTranscriptViewActionSnapshotTracksOverrideResetAndConfiguredUpdate(t *testing.T) {
	m := newTestModel(t)
	_, _ = m.runAction(ActionTranscriptNotebook)
	if ctx := m.actionSnapshot(); !ctx.TranscriptOverridden || ctx.TranscriptView != config.TranscriptNotebook {
		t.Fatalf("after override: %+v", ctx)
	}
	updated := m.state.Config
	updated.TUI.TranscriptView = config.TranscriptNotebook
	m.applyNewConfig(updated)
	if m.transcriptViewOverride == nil || m.effectiveTranscriptView() != config.TranscriptNotebook {
		t.Fatal("unrelated configured update cleared the override")
	}
	_, _ = m.runAction(ActionTranscriptConfigured)
	ctx := m.actionSnapshot()
	if ctx.TranscriptOverridden || ctx.TranscriptView != config.TranscriptNotebook {
		t.Fatalf("reset action snapshot is stale: %+v", ctx)
	}
}

func TestTranscriptViewSwitchPreservesLiveWorkAndChildScope(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.input.SetValue("unfinished draft")
	m.state.PushSteering("queued follow up")
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Now()})
	pending := &session.PendingToolCall{ID: "approval-1", Name: "shell.run", Args: "git status"}
	m.state.SetPendingApproval(pending)
	m.state.AddMessage(session.RoleAssistant, "streaming partial answer", session.ContentTypePlain)
	m.detailExpanded = true
	m.itemExpanded[itemKey{viewID: "audit:expanded", kind: session.KindAudit}] = true
	m.setTranscriptView(config.TranscriptNotebook)
	if m.input.Value() != "unfinished draft" || len(m.state.SteeringQueue()) != 1 || !m.busy {
		t.Fatal("view switch disturbed draft, queue, or running turn")
	}
	if got := m.state.PendingApproval(); got == nil || got.ID != "approval-1" {
		t.Fatalf("pending approval changed: %+v", got)
	}
	if got, ok := m.state.ActiveToolCall(); !ok || got.Name != "shell.run" {
		t.Fatalf("active call changed: %+v (%v)", got, ok)
	}
	if !m.detailExpanded || !m.itemExpanded[itemKey{viewID: "audit:expanded", kind: session.KindAudit}] {
		t.Fatal("expanded output state was lost")
	}

	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child before switch", session.ContentTypePlain)
	view := m.state.RegisterSubagent("live child", child)
	m.drillIntoSubagent(view)
	m.setTranscriptView(config.TranscriptLegacy)
	child.AddMessage(session.RoleAssistant, "late child result", session.ContentTypePlain)
	m.setTranscriptView(config.TranscriptNotebook)
	source, _ := m.conversationSource()
	if source != child {
		t.Fatal("view transition changed the drilled conversation scope")
	}
	if !m.popDrill() {
		t.Fatal("drill-in did not return to parent")
	}
	m.setTranscriptView(config.TranscriptLegacy)
	source, _ = m.conversationSource()
	if source != m.state {
		t.Fatal("returning from child did not restore parent conversation")
	}
}

func (m Model) pendingTranscriptViewIs(v config.TranscriptView) bool {
	return m.pendingTranscriptView != nil && *m.pendingTranscriptView == v
}
