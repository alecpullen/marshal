package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

func TestApprovalSummaryShowsReason(t *testing.T) {
	tc := &session.PendingToolCall{
		Name:   "shell.run",
		Reason: "environment mutation: npm install",
	}
	out := ansi.Strip(approvalSummary(tc, session.SandboxInfo{}, false, 80))
	if !strings.Contains(out, "environment mutation: npm install") {
		t.Fatalf("reason must be rendered:\n%s", out)
	}
}

// Verbatim: reasons are matched by prefix and substring elsewhere in the
// codebase (isEnvironmentConfirm, isGuardrailDeny, isModeElevationApproval),
// so rewording or mid-token truncation would break control flow that reads
// the same strings.
func TestApprovalReasonIsVerbatim(t *testing.T) {
	reason := "mode-elevation: edit requires write access"
	tc := &session.PendingToolCall{Name: "file.write", Reason: reason}
	out := ansi.Strip(approvalSummary(tc, session.SandboxInfo{}, false, 80))
	if !strings.Contains(out, reason) {
		t.Fatalf("reason must appear verbatim, got:\n%s", out)
	}
}

// Risk and Reason are different facts and both must show. riskText's
// either/or is kept only for the compact fallback.
func TestApprovalPanelShowsBothRiskAndReason(t *testing.T) {
	tc := &session.PendingToolCall{
		Name:   "agent.run",
		Risk:   "model-cost-consent",
		Reason: "Subagent will use opus-5 @ anthropic",
	}
	out := ansi.Strip(renderApprovalPanel(tc, session.SandboxInfo{}, false, 80))
	if !strings.Contains(out, "model-cost-consent") {
		t.Errorf("risk missing:\n%s", out)
	}
	if !strings.Contains(out, "Subagent will use opus-5 @ anthropic") {
		t.Errorf("reason missing — this is the sentence the cost prompt exists to show:\n%s", out)
	}
}

func TestApprovalNoReasonRendersUnchanged(t *testing.T) {
	tc := &session.PendingToolCall{Name: "file.read"}
	out := ansi.Strip(approvalSummary(tc, session.SandboxInfo{}, false, 80))
	if strings.Contains(strings.ToLower(out), "reason") {
		t.Fatalf("an empty reason must add no line:\n%s", out)
	}
}

// A long reason must wrap inside the panel rather than overflow it.
func TestApprovalReasonWraps(t *testing.T) {
	tc := &session.PendingToolCall{Name: "shell.run", Reason: strings.Repeat("word ", 80)}
	for i, line := range strings.Split(ansi.Strip(renderApprovalPanel(tc, session.SandboxInfo{}, false, 60)), "\n") {
		if len([]rune(line)) > 60 {
			t.Fatalf("row %d is %d cells wide, want <= 60", i, len([]rune(line)))
		}
	}
}
