package tui

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
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
	provider := &agenttest.ScriptedProvider{
		Responses:     []string{"I will inspect the fixture.", "done"},
		ToolCalls:     [][]schema.ToolCall{{{ID: "view-call", Name: "view.noop", Args: json.RawMessage(`{}`)}}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
	}
	var toolExecutions int
	tools := registry.New()
	if err := tools.Register(registry.Tool{Name: "view.noop", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		toolExecutions++
		return registry.ToolResult{Summary: "done"}, nil
	}}); err != nil {
		t.Fatalf("register view fixture tool: %v", err)
	}
	runner := agent.NewRunner(provider, tools, policy.NewEngine(&config.Config{}, nil), m.state, "view-test")
	runner.NativeTools = true
	runner.SetForceClass(string(agent.ClassQuestion))
	if err := runner.Run(context.Background(), "inspect this fixture"); err != nil {
		t.Fatalf("seed runner state: %v", err)
	}
	m.runner = runner
	providerCalls, auditCount, executedTools := provider.Calls, len(m.state.AuditLog()), toolExecutions
	if providerCalls != 2 || auditCount != 1 || executedTools != 1 {
		t.Fatalf("seed fixture counts: provider=%d audit=%d tools=%d", providerCalls, auditCount, executedTools)
	}
	if err := m.state.SetTodos([]db.TodoItem{{Content: "keep this task", Status: "pending"}}); err != nil {
		t.Fatalf("set todo fixture: %v", err)
	}
	todos := m.state.Todos()
	m.state.SetRunningJobsCount(3)
	m.busy = true
	m.input.SetValue("unfinished draft")
	m.state.PushSteering("queued follow up")
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Now()})
	pending := &session.PendingToolCall{ID: "approval-1", Name: "shell.run", Args: "git status", ResponseChan: make(chan session.UserApprovalDecision, 1)}
	m.state.SetPendingApproval(pending)
	m.state.AddMessage(session.RoleAssistant, "streaming partial answer", session.ContentTypePlain)
	m.detailExpanded = true
	m.itemExpanded[itemKey{viewID: "audit:expanded", kind: session.KindAudit}] = true
	m.setTranscriptView(config.TranscriptNotebook)
	m.setTranscriptView(config.TranscriptLegacy)
	m.setTranscriptView(config.TranscriptNotebook)
	if provider.Calls != providerCalls || len(m.state.AuditLog()) != auditCount || toolExecutions != executedTools {
		t.Fatalf("view switch caused execution side effects: provider=%d/%d audit=%d/%d tools=%d/%d", provider.Calls, providerCalls, len(m.state.AuditLog()), auditCount, toolExecutions, executedTools)
	}
	if !reflect.DeepEqual(m.state.Todos(), todos) || m.state.RunningJobsCount() != 3 {
		t.Fatalf("view switch changed todo/job state: todos=%+v jobs=%d", m.state.Todos(), m.state.RunningJobsCount())
	}
	if m.input.Value() != "unfinished draft" || len(m.state.SteeringQueue()) != 1 || !m.busy {
		t.Fatal("view switch disturbed draft, queue, or running turn")
	}
	if got := m.state.PendingApproval(); got == nil || got.ID != "approval-1" {
		t.Fatalf("pending approval changed: %+v", got)
	}
	select {
	case decision := <-pending.ResponseChan:
		t.Fatalf("view switch answered approval channel: %+v", decision)
	default:
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
