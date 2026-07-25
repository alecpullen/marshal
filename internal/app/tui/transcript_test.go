package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/theme"
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
	if !strings.Contains(out, "❯") || !strings.Contains(out, "fix the tests") {
		t.Fatalf("user message missing ❯ prefix:\n%s", out)
	}
	if strings.Contains(stripANSI(out), "›") {
		t.Fatalf("user message still uses old › prefix:\n%s", out)
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

func TestRenderThinkingBoxUsesGutter(t *testing.T) {
	out := renderThinkingBox("checking the auth flow", "⠋", 80)
	plain := stripANSI(out)
	if strings.Contains(plain, "╭") {
		t.Fatalf("live thinking should be inline, not boxed:\n%s", out)
	}
	if !strings.HasPrefix(plain, " · ") {
		t.Fatalf("live thinking header missing · gutter:\n%s", out)
	}
	if !strings.Contains(plain, "⠋ thinking") || !strings.Contains(plain, "checking the auth flow") {
		t.Fatalf("live thinking missing spinner or text:\n%s", out)
	}
	if !strings.Contains(plain, " ▍ ") {
		t.Fatalf("live thinking tail lines missing ▍ gutter:\n%s", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("live thinking should not add a blank trailing line:\n%q", out)
	}
}

func TestRenderToolResultUsesGutter(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "shell.run: go test ./...\nFAIL: TestX", ContentType: session.ContentTypeToolResult}, 80)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, " · ") {
		t.Fatalf("tool result missing · gutter:\n%s", out)
	}
	if strings.Contains(plain, "⏺") || strings.Contains(plain, "⎿") {
		t.Fatalf("tool result still uses retired bullets:\n%s", out)
	}
	if !strings.Contains(plain, "FAIL: TestX") {
		t.Fatalf("tool result missing detail line:\n%s", out)
	}
}

func TestRenderSystemNoticeIsDim(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleSystem, Content: "Agent turn cancelled.", ContentType: session.ContentTypePlain}, 80)
	if !strings.Contains(out, "· Agent turn cancelled.") {
		t.Fatalf("system notice missing dim · prefix:\n%s", out)
	}
}

func TestRenderPlanBlockUsesGutter(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "1. read parser.go\n2. patch it", ContentType: session.ContentTypePlan}, 80)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, " · plan") {
		t.Fatalf("plan block missing · plan gutter header:\n%s", out)
	}
	if !strings.Contains(plain, "1. read parser.go") || !strings.Contains(plain, "2. patch it") {
		t.Fatalf("plan block missing steps:\n%s", out)
	}
	if strings.Contains(plain, "⏺") {
		t.Fatalf("plan block must not use retired ⏺ header:\n%s", out)
	}
	if strings.Contains(plain, "╭") {
		t.Fatalf("plan block must not be bordered:\n%s", out)
	}
}

func TestRenderQueuedMessagesHaveGutter(t *testing.T) {
	out := renderQueuedMessages([]string{"follow up about tests", "and docs"}, 80)
	plain := stripANSI(out)
	if strings.Contains(plain, "Queued") {
		t.Fatalf("queued rendering must not keep old header:\n%s", out)
	}
	for _, want := range []string{"· queued: follow up", "· queued: and docs"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("queued output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderFinalAnswerUsesGutter(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true}, 80)
	plain := stripANSI(out)
	if strings.Contains(plain, "Response") {
		t.Fatalf("final answer must not show Response label:\n%s", plain)
	}
	if !strings.Contains(plain, "▍") {
		t.Fatalf("final answer missing ▍ gutter:\n%s", plain)
	}
	if !strings.Contains(plain, "All done.") {
		t.Fatalf("final answer missing content:\n%s", plain)
	}
}

func TestRenderFinalAnswerSalvagedNote(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true, Salvaged: true, SalvageReason: "truncated"}, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "salvaged") {
		t.Fatalf("salvaged final answer missing salvage marker:\n%s", plain)
	}
	if !strings.Contains(plain, "truncated") {
		t.Fatalf("salvaged final answer missing salvage reason:\n%s", plain)
	}
	if strings.Contains(plain, "Response") {
		t.Fatalf("salvaged final answer must not show Response label:\n%s", plain)
	}
}

func TestRenderFinalAnswerNotSalvagedHasNoMarker(t *testing.T) {
	out := renderMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true}, 80)
	if strings.Contains(out, "salvaged") {
		t.Fatalf("normal final answer must not contain salvage marker:\n%s", out)
	}
}

func TestRenderActiveToolCallUsesGutter(t *testing.T) {
	atc := session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Unix(100, 0)}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(104, 0), 80)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, " · ") {
		t.Fatalf("active tool call missing · gutter:\n%s", out)
	}
	if !strings.Contains(plain, "shell.run") || !strings.Contains(plain, "4s") {
		t.Fatalf("active tool call missing name/elapsed:\n%s", out)
	}
	if !strings.Contains(plain, "$ go test ./...") {
		t.Fatalf("command tool call missing $ line:\n%s", out)
	}
	if strings.Contains(plain, "╭") {
		t.Fatalf("active tool call must not be bordered:\n%s", out)
	}
	if strings.Contains(plain, "🌐") {
		t.Fatalf("active tool call should not use browser glyph:\n%s", out)
	}
}

func TestRenderProviderErrorInline(t *testing.T) {
	out := renderProviderError(errors.New("connection refused"), 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "✗") {
		t.Fatalf("provider error missing ✗ gutter:\n%s", out)
	}
	if !strings.Contains(plain, "provider: connection refused") {
		t.Fatalf("provider error missing error text:\n%s", out)
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
		if !strings.Contains(result, "file.read") {
			t.Errorf("expected completed tool call, got: %s", result)
		}
		if strings.Contains(result, "done") {
			t.Errorf("should not contain 'done', got: %s", result)
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

func TestUserMessageUsesCoralGutter(t *testing.T) {
	out := strings.TrimLeft(stripANSI(renderUserMessage("hi there", 40)), " ")
	if !strings.HasPrefix(out, "❯ ") {
		t.Fatalf("user message should start with gutter '❯ ': %q", out)
	}
}

func TestCompletedToolCallUsesGutterGlyphs(t *testing.T) {
	ok := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "read"}, 40))
	if !strings.Contains(ok, "·") {
		t.Fatalf("successful tool call should show · gutter: %q", ok)
	}
	if strings.Contains(ok, "done") {
		t.Fatalf("successful tool call should not say 'done': %q", ok)
	}
	bad := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "shell", Error: "boom"}, 40))
	if !strings.Contains(bad, "✗") {
		t.Fatalf("failed tool call should show ✗ gutter: %q", bad)
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

func TestRenderActiveToolCallBrowserGlyphRemoved(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "browser.navigate",
		Args:      "https://example.com",
		StartedAt: time.Unix(100, 0),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), 80)
	stripped := stripANSI(out)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("browser active tool call should not render 🌐:\n%s", out)
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

func TestWelcomeBannerIsPlainLines(t *testing.T) {
	out := renderWelcomeBanner(60)
	plain := stripANSI(out)
	if !strings.Contains(plain, "marshal") || !strings.Contains(plain, "●") {
		t.Fatalf("welcome banner missing brand:\n%s", plain)
	}
	for _, glyph := range []string{"╭", "╰", "│"} {
		if strings.Contains(plain, glyph) {
			t.Fatalf("welcome banner should be borderless (%q):\n%s", glyph, plain)
		}
	}
	if !strings.Contains(plain, "/") {
		t.Fatalf("welcome banner missing call-to-action:\n%s", plain)
	}
}

func TestRenderCodeBlockIsSurfaceTinted(t *testing.T) {
	out := renderCodeBlock("func main() {}", 40)
	plain := stripANSI(out)
	if strings.Contains(plain, "╭") || strings.Contains(plain, "╰") || strings.Contains(plain, "│") {
		t.Fatalf("code block still uses border glyphs:\n%s", plain)
	}
	if !strings.Contains(plain, "func main() {}") {
		t.Fatalf("code block missing content:\n%s", plain)
	}
	if visibleRunes(out) > 40 {
		t.Fatalf("code block exceeds width: %d > 40", visibleRunes(out))
	}
}

func TestRenderCompletedToolCallBrowserGlyphRemoved(t *testing.T) {
	event := registry.AuditEvent{
		ToolName:      "browser.navigate",
		ResultSummary: "Navigated to https://example.com",
	}
	out := renderCompletedToolCall(event, 80)
	stripped := stripANSI(out)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("browser completed tool call should not render 🌐:\n%s", out)
	}
	if !strings.Contains(stripped, "browser.navigate") {
		t.Fatalf("missing tool name:\n%s", out)
	}
	if strings.Contains(stripped, "done") {
		t.Fatalf("completed tool call should not say 'done':\n%s", out)
	}
}

func TestActiveToolCallHasSurfaceBackground(t *testing.T) {
	prev := theme.Current()
	th := theme.LoadFor(false, "xterm-256color")
	theme.Reload(th)
	t.Cleanup(func() { theme.Reload(prev) })

	atc := session.ActiveToolCall{
		Name:      "file.read",
		Args:      "/path/to/file",
		StartedAt: time.Now().Add(-5 * time.Second),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), 80)
	// The header line should contain a 256-color background SGR (48;5;).
	if !strings.Contains(out, "48;5;") {
		t.Fatalf("expected background SGR (48;5;) in header line, got:\n%s", out)
	}
}

func TestActiveToolCallNoSurfaceBackgroundInMono(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	th := theme.Load()
	theme.Reload(th)
	t.Cleanup(func() { theme.Reload(theme.LoadFor(false, "xterm-256color")) })

	atc := session.ActiveToolCall{
		Name:      "file.read",
		Args:      "/path/to/file",
		StartedAt: time.Now().Add(-5 * time.Second),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), 80)
	// In NO_COLOR mode, BGSurface is NoColor{} so lipgloss emits no SGR.
	if strings.Contains(out, "48;5;") {
		t.Fatalf("unexpected background SGR in NO_COLOR mode:\n%s", out)
	}
}
