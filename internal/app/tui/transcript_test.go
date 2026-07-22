package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestRendererCacheEvicts(t *testing.T) {
	for i := 0; i < 20; i++ {
		_ = getRenderer(60 + i*7)
	}
	mdMu.Lock()
	size := len(mdRenderers)
	mdMu.Unlock()
	if size > maxRenderers {
		t.Fatalf("cache exceeded bound: %d", size)
	}
}

func TestTranscriptHashDistinguishesContent(t *testing.T) {
	a := transcriptHash([]session.TranscriptItem{
		{
			Kind:      session.KindMessage,
			Timestamp: time.Unix(0, 1),
			Message:   &session.Message{Role: session.RoleUser, Content: "hello", ContentType: session.ContentTypePlain},
		},
	}, 0, false, 80, nil, nil)
	b := transcriptHash([]session.TranscriptItem{
		{
			Kind:      session.KindMessage,
			Timestamp: time.Unix(0, 1),
			Message:   &session.Message{Role: session.RoleUser, Content: "goodbye", ContentType: session.ContentTypePlain},
		},
	}, 0, false, 80, nil, nil)
	if a == b {
		t.Fatal("hash should differ for different content")
	}
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
	out := stripANSI(renderMessage(session.Message{Role: session.RoleAssistant, Content: "I found the bug.", ContentType: session.ContentTypeMarkdown}, 80))
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
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(104, 0), 80)
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
	out := renderWelcomeBanner(80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "● marshal") {
		t.Fatalf("banner missing '● marshal' icon+name: %q", plain)
	}
	if !strings.Contains(plain, "local-first coding agent") {
		t.Fatalf("banner missing tagline: %q", plain)
	}
	// The dot/name must be styled (not terminal default colour).
	if out == stripANSI(out) {
		t.Fatalf("banner not styled with accent colour:\n%q", out)
	}
}

func TestUserMessageUsesChevronPrefix(t *testing.T) {
	out := strings.TrimLeft(stripANSI(renderUserMessage("hi there", 40)), " ")
	if !strings.HasPrefix(out, "› ") {
		t.Fatalf("user message should start with '› ' prefix: %q", out)
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

func TestRenderCompletedToolCallShowsHookBlockedIndicator(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName: "file.write_patch",
		Error:    "blocked by pre_tool_use hook: lint gate",
		Hooks: []registry.HookMetadata{{
			Event:    "pre_tool_use",
			Decision: "block",
			Reason:   "lint gate",
		}},
	}, 80))
	if !strings.Contains(out, "hook blocked") {
		t.Fatalf("rendered tool call missing hook blocked indicator: %q", out)
	}
}

func TestRenderCompletedToolCallShowsHookRewroteIndicator(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ResultSummary: "ok",
		Hooks: []registry.HookMetadata{{
			Event:   "pre_tool_use",
			Rewrote: true,
		}},
	}, 80))
	if !strings.Contains(out, "hook rewrote") {
		t.Fatalf("rendered tool call missing hook rewrote indicator: %q", out)
	}
}

func TestRenderCompletedToolCallShowsHookFailedOpenIndicator(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ResultSummary: "ok",
		Hooks: []registry.HookMetadata{{
			Event:      "pre_tool_use",
			FailedOpen: true,
		}},
	}, 80))
	if !strings.Contains(out, "hook failed-open") {
		t.Fatalf("rendered tool call missing hook failed-open indicator: %q", out)
	}
}

func TestRenderCompletedToolCallShowsHookCountIndicator(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ResultSummary: "ok",
		Hooks: []registry.HookMetadata{
			{Event: "pre_tool_use"},
			{Event: "pre_tool_use"},
		},
	}, 80))
	if !strings.Contains(out, "hooks 2") {
		t.Fatalf("rendered tool call missing hooks 2 indicator: %q", out)
	}
}

func TestApprovalPanelHasNoBackgroundFill(t *testing.T) {
	tc := &session.PendingToolCall{Name: "shell.run", Command: "ls", Risk: "reads files"}
	out := renderApprovalPanel(tc, session.SandboxInfo{Backend: "restricted"}, false, 50)
	if strings.Contains(out, ";235m") || strings.Contains(out, "48;5;235") {
		t.Fatalf("approval panel still emits panel background fill:\n%q", out)
	}
}

func TestAgentMarkdownRendersRichBlocksWithinWidth(t *testing.T) {
	content := "# Fix summary\n\nThe bug was in `parse`:\n\n```go\nfunc parse(s string) error {\n\treturn errors.New(\"a deliberately long line to exercise wrapping behavior in code blocks\")\n}\n```\n\n- first point\n- second point\n\n| col | val |\n|-----|-----|\n| a   | 1   |\n"
	width := 60
	out := renderAgentMarkdown(content, width)
	plain := stripANSI(out)
	for i, line := range strings.Split(plain, "\n") {
		if got := visibleRunes(line); got > width {
			t.Errorf("line %d width %d exceeds %d: %q", i, got, width, line)
		}
	}
	for _, want := range []string{"Fix summary", "first point", "second point", "col", "val"} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered markdown missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("rendered markdown should carry ANSI styling")
	}
	t.Logf("rendered:\n%s", out)
}

func TestRenderActiveToolCallBrowserGlyph(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "browser.navigate",
		Args:      "https://example.com",
		StartedAt: time.Unix(100, 0),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), 80)
	stripped := stripANSI(out)
	if !strings.Contains(stripped, "🌐") {
		t.Fatalf("browser active tool call missing 🌐 glyph:\n%s", out)
	}
	if !strings.Contains(stripped, "browser.navigate") {
		t.Fatalf("missing tool name:\n%s", out)
	}
}

func TestRenderActiveToolCallNonBrowserGlyph(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "file.read",
		Args:      "src/main.go",
		StartedAt: time.Unix(100, 0),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), 80)
	stripped := stripANSI(out)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("non-browser tool should not have 🌐:\n%s", out)
	}
	if !strings.Contains(stripped, "file.read") {
		t.Fatalf("missing tool name:\n%s", out)
	}
}

func TestWelcomeBannerIsCenteredHero(t *testing.T) {
	out := renderWelcomeBanner(60)
	plain := stripANSI(out)
	if !strings.Contains(plain, "marshal") {
		t.Fatalf("welcome banner missing brand:\n%s", plain)
	}
	if !strings.Contains(plain, "▍") {
		t.Fatalf("welcome banner should be a gutter-framed card:\n%s", plain)
	}
	if !strings.Contains(plain, "Type a question") {
		t.Fatalf("welcome banner missing call-to-action:\n%s", plain)
	}
}

func TestRenderCompletedToolCallBrowserGlyph(t *testing.T) {
	event := registry.AuditEvent{
		ToolName:      "browser.navigate",
		ResultSummary: "Navigated to https://example.com",
	}
	out := renderCompletedToolCall(event, 80)
	stripped := stripANSI(out)
	if !strings.Contains(stripped, "browser.navigate") {
		t.Fatalf("missing tool name:\n%s", out)
	}
	if !strings.Contains(stripped, "done") {
		t.Fatalf("missing 'done':\n%s", out)
	}
}
