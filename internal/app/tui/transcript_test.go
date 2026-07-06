package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// forceColor makes lipgloss emit ANSI256 SGR codes for the duration of the
// test so color-code assertions are meaningful, then restores the prior
// profile. Requires the test not run in parallel (none in this package do).
func forceColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

func TestRenderUserMessageUsesPromptPrefix(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleUser, Content: "fix the tests", ContentType: session.ContentTypePlain}, 80)
	if !strings.Contains(out, "›") || !strings.Contains(out, "fix the tests") {
		t.Fatalf("user message missing › prefix:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "user") {
		t.Fatalf("user message must not contain a role label:\n%s", out)
	}
}

func TestRenderAgentProseHasNoRoleLabel(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "I found the bug.", ContentType: session.ContentTypeMarkdown}, 80)
	if !strings.Contains(out, "I found the bug.") {
		t.Fatalf("agent prose missing content:\n%s", out)
	}
	for _, label := range []string{"agent", "assistant"} {
		if strings.Contains(strings.ToLower(out), label) {
			t.Fatalf("agent prose must not contain role label %q:\n%s", label, out)
		}
	}
}

func TestRenderAgentProseDoesNotAddBlankTrailingLine(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "I found the bug.\n", ContentType: session.ContentTypeMarkdown}, 80)
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("agent prose should not add a blank trailing line:\n%q", out)
	}
}

func TestRenderThinkingBoxIsCompactInline(t *testing.T) {
	out := renderThinkingBox("checking the auth flow", "⠋", 80)
	if strings.Contains(out, "╭") {
		t.Fatalf("live thinking should be inline, not boxed:\n%s", out)
	}
	if !strings.Contains(out, "⠋ thinking") || !strings.Contains(out, "checking the auth flow") {
		t.Fatalf("live thinking missing spinner or text:\n%s", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("live thinking should not add a blank trailing line:\n%q", out)
	}
}

func TestRenderToolResultUsesBullets(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "shell.run: go test ./...\nFAIL: TestX", ContentType: session.ContentTypeToolResult}, 80)
	if !strings.Contains(out, "⏺") {
		t.Fatalf("tool result missing ⏺ bullet:\n%s", out)
	}
	if !strings.Contains(out, "⎿") {
		t.Fatalf("tool result missing ⎿ continuation:\n%s", out)
	}
	if !strings.Contains(out, "FAIL: TestX") {
		t.Fatalf("tool result missing detail line:\n%s", out)
	}
}

func TestRenderSystemNoticeIsDim(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleSystem, Content: "Agent turn cancelled.", ContentType: session.ContentTypePlain}, 80)
	if !strings.Contains(out, "· Agent turn cancelled.") {
		t.Fatalf("system notice missing dim · prefix:\n%s", out)
	}
}

func TestRenderPlanBlockShowsHeaderAndSteps(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "1. read parser.go\n2. patch it", ContentType: session.ContentTypePlan}, 80)
	if !strings.Contains(out, "⏺ Plan") {
		t.Fatalf("plan block missing header:\n%s", out)
	}
	if !strings.Contains(out, "1. read parser.go") || !strings.Contains(out, "2. patch it") {
		t.Fatalf("plan block missing steps:\n%s", out)
	}
	// No bordered panel around plans anymore.
	if strings.Contains(out, "╭") {
		t.Fatalf("plan block must not be bordered:\n%s", out)
	}
}

func TestRenderDiffBlockColorsWithoutPanel(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "+ added line\n- removed line", ContentType: session.ContentTypeDiff}, 80)
	if !strings.Contains(out, "+ added line") || !strings.Contains(out, "- removed line") {
		t.Fatalf("diff block missing lines:\n%s", out)
	}
	if strings.Contains(out, "╭") {
		t.Fatalf("diff block must not be bordered:\n%s", out)
	}
}

func TestRenderFinalAnswerKeepsResponseTreatment(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true}, 80)
	if !strings.Contains(out, "Response") {
		t.Fatalf("final answer must keep its Response label:\n%s", out)
	}
}

func TestRenderFinalAnswerSalvagedMarker(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true, Salvaged: true, SalvageReason: "truncated"}, 80)
	if !strings.Contains(out, "Response") {
		t.Fatalf("salvaged final answer must keep its Response label:\n%s", out)
	}
	if !strings.Contains(out, "salvaged") {
		t.Fatalf("salvaged final answer missing salvage marker:\n%s", out)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("salvaged final answer missing salvage reason:\n%s", out)
	}
}

func TestRenderFinalAnswerNotSalvagedHasNoMarker(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true}, 80)
	if strings.Contains(out, "salvaged") {
		t.Fatalf("normal final answer must not contain salvage marker:\n%s", out)
	}
}

func TestRenderActiveToolCallIsBorderless(t *testing.T) {
	atc := session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Unix(100, 0)}
	out := renderActiveToolCall(atc, "⠋", time.Unix(104, 0), 80)
	if !strings.Contains(out, "shell.run") || !strings.Contains(out, "4s") {
		t.Fatalf("active tool call missing name/elapsed:\n%s", out)
	}
	if !strings.Contains(out, "$ go test ./...") {
		t.Fatalf("command tool call missing $ line:\n%s", out)
	}
	if strings.Contains(out, "╭") {
		t.Fatalf("active tool call must not be bordered:\n%s", out)
	}
}

func TestRenderProviderErrorInline(t *testing.T) {
	out := renderProviderError(errors.New("connection refused"), 80)
	if !strings.Contains(out, "✘ provider: connection refused") {
		t.Fatalf("provider error missing ✘ line:\n%s", out)
	}
}

func TestRenderTranscriptItem(t *testing.T) {
	width := 80

	t.Run("thinking entry collapsed", func(t *testing.T) {
		item := session.TranscriptItem{
			Kind: session.KindThinking,
			Thinking: &session.ThinkingEntry{
				Text:      "Should check the file",
				Duration:  2 * time.Second,
				StartedAt: time.Now(),
			},
		}
		result := renderTranscriptItem(item, false, width)
		if !strings.Contains(result, "thought for 2s") {
			t.Errorf("expected thinking summary, got: %s", result)
		}
	})

	t.Run("audit entry success", func(t *testing.T) {
		item := session.TranscriptItem{
			Kind: session.KindAudit,
			Audit: &registry.AuditEvent{
				ToolName:      "file.read",
				ResultSummary: "file contents here",
			},
		}
		result := renderTranscriptItem(item, false, width)
		if !strings.Contains(result, "file.read done") {
			t.Errorf("expected completed tool call, got: %s", result)
		}
		if strings.Contains(result, "ago") {
			t.Errorf("should not contain elapsed suffix, got: %s", result)
		}
	})

	t.Run("message entry with reasoning", func(t *testing.T) {
		msg := session.Message{
			Role:          session.RoleAssistant,
			Content:       "hello",
			ContentType:   session.ContentTypePlain,
			Reasoning:     "thinking about greeting",
			ThinkDuration: 1 * time.Second,
			CreatedAt:     time.Now(),
		}
		item := session.TranscriptItem{
			Kind:    session.KindMessage,
			Message: &msg,
		}
		result := renderTranscriptItem(item, false, width)
		if !strings.Contains(result, "thought for 1s") {
			t.Errorf("expected thinking summary before message, got: %s", result)
		}
	})
}

func TestTranscriptLinesFitWidth(t *testing.T) {
	long := strings.Repeat("word ", 60)
	messages := []session.Message{
		{Role: session.RoleUser, Content: long, ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: long, ContentType: session.ContentTypeMarkdown},
		{Role: session.RoleAssistant, Content: "summary\n" + long, ContentType: session.ContentTypeToolResult},
		{Role: session.RoleSystem, Content: long, ContentType: session.ContentTypePlain},
	}
	for _, width := range []int{38, 60, 80} {
		for _, msg := range messages {
			out := renderMessage(msg, width)
			for _, line := range strings.Split(out, "\n") {
				if visibleRunes(line) > width {
					t.Fatalf("line exceeds width %d (%d): %q", width, visibleRunes(line), line)
				}
			}
		}
	}
}

func TestWelcomeBannerHasCoralDotAndName(t *testing.T) {
	forceColor(t)
	out := renderWelcomeBanner(80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "● marshal") {
		t.Fatalf("banner missing '● marshal' icon+name: %q", plain)
	}
	if !strings.Contains(plain, "local-first coding agent") {
		t.Fatalf("banner missing tagline: %q", plain)
	}
	// coral 209 must appear as the foreground SGR for the dot/name.
	if !strings.Contains(out, "209") {
		t.Fatalf("banner not styled with coral (209): %q", out)
	}
}

func TestUserMessageUsesChevronPrefix(t *testing.T) {
	out := stripANSI(renderUserMessage("hi there", 40))
	if !strings.HasPrefix(strings.TrimLeft(out, " "), "› ") && !strings.Contains(out, "› ") {
		t.Fatalf("user message should use '›' prefix: %q", out)
	}
}

func TestCompletedToolCallUsesCheckAndCross(t *testing.T) {
	ok := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "read"}, 40))
	if !strings.Contains(ok, "✔") {
		t.Fatalf("successful tool call should show ✔: %q", ok)
	}
	bad := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "shell", Error: "boom"}, 40))
	if !strings.Contains(bad, "✘") {
		t.Fatalf("failed tool call should show ✘: %q", bad)
	}
}

func TestApprovalPanelHasNoBackgroundFill(t *testing.T) {
	forceColor(t)
	tc := &session.PendingToolCall{Name: "shell.run", Command: "ls", Risk: "reads files"}
	out := renderApprovalPanel(tc, 50)
	if strings.Contains(out, ";235m") || strings.Contains(out, "48;5;235") {
		t.Fatalf("approval panel still emits panel background fill:\n%q", out)
	}
}
