package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
)

func TestRenderUserMessageUsesPromptPrefix(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleUser, Content: "fix the tests", ContentType: session.ContentTypePlain}, 80)
	if !strings.Contains(out, "❯") || !strings.Contains(out, "fix the tests") {
		t.Fatalf("user message missing ❯ prefix:\n%s", out)
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
	if !strings.Contains(out, "✗ provider: connection refused") {
		t.Fatalf("provider error missing ✗ line:\n%s", out)
	}
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
