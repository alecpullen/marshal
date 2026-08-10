package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/registry"
)

func TestRendererCacheEvicts(t *testing.T) {
	for i := 0; i < 20; i++ {
		_ = getRenderer(60+i*7, 0)
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
	}, 0, false, 80, nil, nil, "", session.ActiveToolCall{})
	b := transcriptHash([]session.TranscriptItem{
		{
			Kind:      session.KindMessage,
			Timestamp: time.Unix(0, 1),
			Message:   &session.Message{Role: session.RoleUser, Content: "goodbye", ContentType: session.ContentTypePlain},
		},
	}, 0, false, 80, nil, nil, "", session.ActiveToolCall{})
	if a == b {
		t.Fatal("hash should differ for different content")
	}
}

func TestTranscriptHashCoversSpinnerAndActiveTool(t *testing.T) {
	base := transcriptHash(nil, 0, true, 80, nil, nil, "⠂", session.ActiveToolCall{})
	if got := transcriptHash(nil, 0, true, 80, nil, nil, "⠒", session.ActiveToolCall{}); got == base {
		t.Fatal("hash must change with the spinner frame — otherwise the live tool row freezes")
	}
	if got := transcriptHash(nil, 0, true, 80, nil, nil, "⠂", session.ActiveToolCall{Name: "agent.run", Args: "investigate"}); got == base {
		t.Fatal("hash must change with the active tool call")
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

func TestRenderReconnectNoticeUsesGutter(t *testing.T) {
	out := renderReconnectNotice("connection lost — retrying in 5s", "⠋", 80)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, " · ") {
		t.Fatalf("reconnect notice missing · gutter:\n%s", out)
	}
	if !strings.Contains(plain, "⠋") || !strings.Contains(plain, "connection lost — retrying in 5s") {
		t.Fatalf("reconnect notice missing spinner or label:\n%s", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("reconnect notice should not add a blank trailing line:\n%q", out)
	}
}

func TestRenderReconnectNoticeEmptyLabelReturnsNothing(t *testing.T) {
	if out := renderReconnectNotice("", "⠋", 80); out != "" {
		t.Fatalf("empty label should render nothing, got %q", out)
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
	// The notice sits on the shared 3-cell gutter, like every other item.
	if !strings.Contains(stripANSI(out), " · Agent turn cancelled.") {
		t.Fatalf("system notice missing dim · gutter:\n%s", out)
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
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(104, 0), false, 80)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, " ▸ ") {
		t.Fatalf("active tool call missing ▸ gutter:\n%s", out)
	}
	if !strings.Contains(plain, "Run command") || !strings.Contains(plain, "4s") {
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

func TestRenderMessageSkillMessages(t *testing.T) {
	t.Run("skill body hidden", func(t *testing.T) {
		msg := session.Message{
			Role: session.RoleSystem,
			Content: "```\n# The following is reference material loaded from a skill file.\n" +
				"skill_name: using-superpowers\n---\n# Using Skills\n\nThe full body.\n```\n",
			ContentType: session.ContentTypeSkillBody,
		}
		if out := renderMessage(msg, 80); out != "" {
			t.Fatalf("skill body must not render in the transcript, got:\n%s", out)
		}
	})

	t.Run("skill tag compact", func(t *testing.T) {
		msg := session.Message{
			Role:        session.RoleSystem,
			Content:     "using-superpowers",
			ContentType: session.ContentTypeSkill,
		}
		plain := stripANSI(renderMessage(msg, 80))
		if !strings.Contains(plain, "skill.load: using-superpowers") {
			t.Fatalf("expected compact skill.load tag, got:\n%s", plain)
		}
	})
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
		result := renderTranscriptItem(item, false, "⠋", width)
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
		result := renderTranscriptItem(item, false, "⠋", width)
		if !strings.Contains(result, "Read file") {
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
		result := renderTranscriptItem(item, false, "⠋", width)
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
	ok := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "read"}, false, 40))
	if !strings.Contains(ok, "·") {
		t.Fatalf("successful tool call should show · gutter: %q", ok)
	}
	if strings.Contains(ok, "done") {
		t.Fatalf("successful tool call should not say 'done': %q", ok)
	}
	bad := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "shell", Error: "boom"}, false, 40))
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
	}, false, 80))
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
	}, false, 80))
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
	}, false, 80))
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
	}, false, 80))
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
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), false, 80)
	stripped := stripANSI(out)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("browser active tool call should not render 🌐:\n%s", out)
	}
	if !strings.Contains(stripped, "Browser navigate") {
		t.Fatalf("missing pretty tool name:\n%s", out)
	}
}

func TestRenderActiveToolCallNonBrowserGlyph(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "file.read",
		Args:      "src/main.go",
		StartedAt: time.Unix(100, 0),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), false, 80)
	stripped := stripANSI(out)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("non-browser tool should not have 🌐:\n%s", out)
	}
	if !strings.Contains(stripped, "Read file") {
		t.Fatalf("missing pretty tool name:\n%s", out)
	}
}

func TestRenderQuestionPanelEmpty(t *testing.T) {
	q := &session.PendingQuestion{Questions: []session.Question{}}
	if out := renderQuestionPanel(q, 80); out != "" {
		t.Fatalf("empty question panel should render nothing; got %q", out)
	}
	if out := renderQuestionPanel(nil, 80); out != "" {
		t.Fatalf("nil question panel should render nothing; got %q", out)
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
	out := renderCompletedToolCall(event, false, 80)
	stripped := stripANSI(out)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("browser completed tool call should not render 🌐:\n%s", out)
	}
	if !strings.Contains(stripped, "Browser navigate") {
		t.Fatalf("missing tool name:\n%s", out)
	}
	if strings.Contains(stripped, "done") {
		t.Fatalf("completed tool call should not say 'done':\n%s", out)
	}
}

func TestActiveToolCallHasNoSurfaceBackground(t *testing.T) {
	prev := theme.Current()
	th := theme.LoadFor(false, "xterm-256color")
	theme.Reload(th)
	t.Cleanup(func() { theme.Reload(prev) })

	atc := session.ActiveToolCall{
		Name:      "file.read",
		Args:      "/path/to/file",
		StartedAt: time.Now().Add(-5 * time.Second),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), false, 80)
	// The header line should NOT contain a background SGR (48;5;) — the
	// glyph should use the normal terminal background, not a gray square.
	if strings.Contains(out, "48;5;") {
		t.Fatalf("unexpected background SGR (48;5;) in header line, got:\n%s", out)
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
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), false, 80)
	// In NO_COLOR mode, BGSurface is NoColor{} so lipgloss emits no SGR.
	if strings.Contains(out, "48;5;") {
		t.Fatalf("unexpected background SGR in NO_COLOR mode:\n%s", out)
	}
}

func TestActiveToolCallRenderedInTranscript(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Now().Add(-time.Second),
	}
	out := stripANSI(renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
	if !strings.Contains(out, "Run command") {
		t.Fatalf("active tool call missing pretty name in transcript: %q", out)
	}
	if !strings.Contains(out, "go test") {
		t.Fatalf("active tool call missing command in transcript: %q", out)
	}
}

func TestActiveToolCallRendersOutputTail(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "echo hi",
		Output:    "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8",
		StartedAt: time.Now().Add(-time.Second),
	}
	out := stripANSI(renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
	// Should show last 6 lines (line3 through line8), not line1 or line2.
	if strings.Contains(out, "line1") {
		t.Fatalf("output should not contain line1 (only last 6 lines): %q", out)
	}
	if strings.Contains(out, "line2") {
		t.Fatalf("output should not contain line2 (only last 6 lines): %q", out)
	}
	for _, want := range []string{"line3", "line4", "line5", "line6", "line7", "line8"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
}

func TestActiveToolCallRendersOutputAllWhenUnderSixLines(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "echo hi",
		Output:    "a\nb\nc",
		StartedAt: time.Now().Add(-time.Second),
	}
	out := stripANSI(renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
}

func TestCompletedFileWritePatchRendersDiff(t *testing.T) {
	event := registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "Applied patches to: a.go",
		ResultContent: "--- a.go\n+++ a.go\n@@ -1 +1 @@\n-old\n+new\n",
	}
	out := stripANSI(renderCompletedToolCall(event, false, 80))
	if !strings.Contains(out, "Edit file") {
		t.Fatalf("missing pretty tool name: %q", out)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("missing file stat header: %q", out)
	}
	if !strings.Contains(out, "+1 −1") {
		t.Fatalf("missing stat line: %q", out)
	}
}

func TestCompletedFileWritePatchTruncatesLongDiff(t *testing.T) {
	var long strings.Builder
	long.WriteString("--- a.go\n+++ a.go\n@@ -1,20 +1,20 @@\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&long, "-%d\n", i)
		fmt.Fprintf(&long, "+%d\n", i)
	}
	event := registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "Applied patches to: a.go",
		ResultContent: long.String(),
	}
	out := stripANSI(renderCompletedToolCall(event, false, 80))
	if !strings.Contains(out, "a.go") {
		t.Fatalf("missing file stat header: %q", out)
	}
	if !strings.Contains(out, "more lines") {
		t.Fatalf("long diff should be truncated with indicator: %q", out)
	}
}

func TestDisplayToolNameKnown(t *testing.T) {
	if got := DisplayToolName("file.write_patch"); got != "Edit file" {
		t.Fatalf("DisplayToolName(file.write_patch) = %q, want %q", got, "Edit file")
	}
	if got := DisplayToolName("shell.run"); got != "Run command" {
		t.Fatalf("DisplayToolName(shell.run) = %q, want %q", got, "Run command")
	}
}

func TestDisplayToolNameUnknownFallback(t *testing.T) {
	if got := DisplayToolName("custom.thing"); got != "Custom thing" {
		t.Fatalf("DisplayToolName(custom.thing) = %q, want %q", got, "Custom thing")
	}
}

func TestActiveToolCallNoOutputShowsNothing(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "echo hi",
		Output:    "",
		StartedAt: time.Now().Add(-time.Second),
	}
	out := stripANSI(renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
	// Should not contain any output lines beyond the command line.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 3 {
		t.Fatalf("expected at most 3 lines (header, command, blank), got %d: %q", len(lines), out)
	}
}

// unifiedDiffForFile builds one file's worth of unified diff with pairs
// removed/added body lines each.
func unifiedDiffForFile(path string, pairs int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", path, path, pairs, pairs)
	for i := 0; i < pairs; i++ {
		fmt.Fprintf(&b, "-old line %d\n+new line %d\n", i, i)
	}
	return b.String()
}

func TestCompletedToolCallRendersEveryFileInMultiFilePatch(t *testing.T) {
	diff := unifiedDiffForFile("alpha.go", 4) + unifiedDiffForFile("beta.go", 4) + unifiedDiffForFile("gamma.go", 4)
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "applied patch",
		ResultContent: diff,
	}, false, 80))
	for _, path := range []string{"alpha.go", "beta.go", "gamma.go"} {
		if !strings.Contains(out, path) {
			t.Fatalf("diff output missing stat header for %s:\n%s", path, out)
		}
	}
	if !strings.Contains(out, "+4 −4") {
		t.Fatalf("diff output missing +N −M stats:\n%s", out)
	}
	// The flat 20-line cap this replaces would have dropped gamma.go's body.
	if !strings.Contains(out, "new line 3") {
		t.Fatalf("later files lost their body lines:\n%s", out)
	}
}

func TestCompletedToolCallCapsBodyLinesPerFile(t *testing.T) {
	diff := unifiedDiffForFile("big.go", 20) // 41 rendered lines: hunk header + 40 body
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "applied patch",
		ResultContent: diff,
	}, false, 80))
	if !strings.Contains(out, "more lines") {
		t.Fatalf("expected per-file elision of lines:\n%s", out)
	}
}

func TestCompletedToolCallCapsFilesShown(t *testing.T) {
	var diff string
	for i := 0; i < 7; i++ {
		diff += unifiedDiffForFile(fmt.Sprintf("file%d.go", i), 1)
	}
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "applied patch",
		ResultContent: diff,
	}, false, 80))
	if !strings.Contains(out, "… 1 more files") {
		t.Fatalf("expected file-cap elision line:\n%s", out)
	}
	if !strings.Contains(out, "file5.go") {
		t.Fatalf("sixth file should still render:\n%s", out)
	}
	if strings.Contains(out, "file6.go") {
		t.Fatalf("seventh file should be elided:\n%s", out)
	}
}

// TestRenderThinkingSummaryWithoutText covers thinking phases from models
// that expose no reasoning: the marker must still render, and there is
// nothing to expand.
func TestRenderThinkingSummaryWithoutText(t *testing.T) {
	collapsed := stripANSI(renderThinkingSummary("", 6*time.Second, false, 80))
	if !strings.Contains(collapsed, "thought for") {
		t.Errorf("collapsed render missing marker: %q", collapsed)
	}

	expanded := stripANSI(renderThinkingSummary("", 6*time.Second, true, 80))
	if !strings.Contains(expanded, "thought for") {
		t.Errorf("expanded render missing marker: %q", expanded)
	}
	if got := strings.Count(strings.TrimRight(expanded, "\n"), "\n") + 1; got != 1 {
		t.Errorf("expanded render = %d lines, want 1 (nothing to expand): %q", got, expanded)
	}
}

func TestCompletedToolCallHidesResultContentWhenCollapsed(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "subagent completed",
		ResultContent: "full subagent report body",
	}, false, 80))
	if strings.Contains(out, "full subagent report body") {
		t.Fatalf("collapsed row must not render result content:\n%s", out)
	}
}

func TestCompletedToolCallExpandsResultContent(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "subagent completed",
		ResultContent: "full subagent report body",
	}, true, 80))
	if !strings.Contains(out, "full subagent report body") {
		t.Fatalf("expanded row must render result content:\n%s", out)
	}
}

func TestCompletedFailedDiffToolShowsFailureDetailsWhenExpanded(t *testing.T) {
	exitCode := 2
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:        "file.write_patch",
		Error:           "patch rejected",
		Args:            []byte(`{"path":"a.go"}`),
		CommandExitCode: &exitCode,
		ResultContent:   "patch output",
	}, true, 80))
	if !strings.Contains(out, "exit code: 2") || !strings.Contains(out, `args: {"path":"a.go"}`) {
		t.Fatalf("expanded failed diff tool must show failure details:\n%s", out)
	}
}

func TestCompletedToolCallCapsExpandedResultContent(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("report line %d", i))
	}
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "subagent completed",
		ResultContent: strings.Join(lines, "\n"),
	}, true, 80))
	if !strings.Contains(out, "… 8 more lines") {
		t.Fatalf("expanded content must be capped with an elision marker:\n%s", out)
	}
	if strings.Contains(out, "report line 19") {
		t.Fatalf("expanded content exceeded the cap:\n%s", out)
	}
}

func TestCompletedToolCallFitsWidthWithWideRunes(t *testing.T) {
	out := renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "file.read",
		ResultSummary: "読み込み完了 — これは幅を超えるとても長い日本語のサマリーです",
	}, false, 40)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d cells wide, budget 40: %q", w, line)
		}
	}
}

func TestActiveToolCallFitsWidthWithWideRunes(t *testing.T) {
	out := renderActiveToolCall(session.ActiveToolCall{
		Name:      "agent.run",
		Args:      "調査: これはとても長いサブエージェントの説明文で幅を超えます",
		StartedAt: time.Now(),
	}, session.SandboxInfo{}, false, "⠂", time.Now(), false, 40)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d cells wide, budget 40: %q", w, line)
		}
	}
}

func TestCompletedToolCallUsesCategoryGlyph(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "file.read", ResultSummary: "ok"}, false, 40))
	if !strings.Contains(out, "≡") {
		t.Fatalf("file.read row should use the ≡ gutter: %q", out)
	}
	bad := stripANSI(renderCompletedToolCall(registry.AuditEvent{ToolName: "file.read", Error: "boom"}, false, 40))
	if !strings.Contains(bad, "✗") {
		t.Fatalf("error state must win over the category glyph: %q", bad)
	}
	if strings.Contains(bad, "≡") {
		t.Fatalf("error row must not show the category glyph: %q", bad)
	}
}

func TestToolResultLineWrapsLongFirstLine(t *testing.T) {
	long := strings.Repeat("abcdefghij", 10) // 100 chars
	out := stripANSI(renderToolResultLine(long, 30))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapping to produce multiple lines, got %d:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > 30 {
			t.Fatalf("line is %d cells wide, budget 30: %q", w, line)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "abcdefghijabcdefghij") {
		t.Fatalf("wrapped output is missing content (was it truncated?):\n%s", out)
	}
}

func TestActiveToolCallWrapsLongArgs(t *testing.T) {
	long := strings.Repeat("path-segment-", 10) // 130 chars
	out := stripANSI(renderActiveToolCall(session.ActiveToolCall{
		Name:      "agent.run",
		Args:      long,
		StartedAt: time.Now(),
	}, session.SandboxInfo{}, false, "⠂", time.Now(), false, 30))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected wrapping to produce multiple lines, got %d:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > 30 {
			t.Fatalf("line is %d cells wide, budget 30: %q", w, line)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "path-segment-") {
		t.Fatalf("wrapped output is missing content:\n%s", out)
	}
}

func TestActiveToolCallWrapsLongOutput(t *testing.T) {
	long := strings.Repeat("outputline", 10) // 100 chars on one line
	out := stripANSI(renderActiveToolCall(session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "echo test",
		Output:    long,
		StartedAt: time.Now(),
	}, session.SandboxInfo{}, false, "⠂", time.Now(), false, 30))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > 30 {
			t.Fatalf("line is %d cells wide, budget 30: %q", w, line)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "outputline") {
		t.Fatalf("wrapped output is missing content:\n%s", out)
	}
}

func TestCompletedToolCallWrapsLongSummary(t *testing.T) {
	long := strings.Repeat("summary-part-", 10) // 130 chars
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName:      "file.read",
		ResultSummary: long,
	}, false, 30))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapping to produce multiple lines, got %d:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > 30 {
			t.Fatalf("line is %d cells wide, budget 30: %q", w, line)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "summary-part-") {
		t.Fatalf("wrapped output is missing content:\n%s", out)
	}
}

func TestCompletedToolCallWrapsLongError(t *testing.T) {
	long := strings.Repeat("error-detail-", 10) // 130 chars
	out := stripANSI(renderCompletedToolCall(registry.AuditEvent{
		ToolName: "shell.run",
		Error:    long,
	}, false, 30))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapping to produce multiple lines, got %d:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > 30 {
			t.Fatalf("line is %d cells wide, budget 30: %q", w, line)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "error-detail-") {
		t.Fatalf("wrapped output is missing content:\n%s", out)
	}
}

func TestRenderCompletedToolCallExpandedShowsFullDiff(t *testing.T) {
	var diff strings.Builder
	diff.WriteString("--- a/file.go\n+++ b/file.go\n")
	for i := 0; i < maxDiffLinesPerFile+5; i++ {
		diff.WriteString(fmt.Sprintf("+line %d\n", i))
	}
	event := registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultContent: diff.String(),
	}

	collapsed := renderCompletedToolCall(event, false, 80)
	if !strings.Contains(collapsed, "more lines") {
		t.Fatal("expected collapsed diff to elide lines")
	}

	expanded := renderCompletedToolCall(event, true, 80)
	if strings.Contains(expanded, "more lines") {
		t.Fatalf("expected expanded diff to show every line, got: %s", expanded)
	}
	if strings.Count(expanded, "+line ") != maxDiffLinesPerFile+5 {
		t.Fatalf("expected all %d added lines, got %d", maxDiffLinesPerFile+5, strings.Count(expanded, "+line "))
	}
}

func TestBlockRenderersEndWithSingleNewline(t *testing.T) {
	outs := map[string]string{
		"user message":   renderUserMessage("hello", 80),
		"system notice":  renderSystemNotice("note", 80),
		"tool result":    renderToolResultLine("line1\nline2", 80),
		"plan block":     renderPlanBlock("step 1", 80),
		"provider error": renderProviderError(errors.New("boom"), 80),
		"queued":         renderQueuedMessages([]string{"q1"}, 80),
		"active tool": renderActiveToolCall(
			session.ActiveToolCall{Name: "shell.run", Args: "go test", StartedAt: time.Unix(100, 0)},
			session.SandboxInfo{}, false, "⠻", time.Unix(104, 0), false, 80),
	}
	for name, out := range outs {
		if !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") {
			t.Errorf("%s must end with exactly one newline: %q", name, out)
		}
	}
}

func TestRenderActiveToolCallExpandedShowsFullOutput(t *testing.T) {
	var out strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&out, "output line %d\n", i)
	}
	atc := session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "echo hi",
		StartedAt: time.Unix(100, 0),
		Output:    out.String(),
	}

	collapsed := renderActiveToolCall(atc, session.SandboxInfo{}, false, "", time.Unix(101, 0), false, 80)
	if strings.Contains(collapsed, "output line 0") {
		t.Fatal("expected collapsed tail to omit early lines")
	}
	if !strings.Contains(collapsed, "output line 9") {
		t.Fatal("expected collapsed tail to include the last line")
	}

	expanded := renderActiveToolCall(atc, session.SandboxInfo{}, false, "", time.Unix(101, 0), true, 80)
	if !strings.Contains(expanded, "output line 0") {
		t.Fatal("expected expanded output to include the first line")
	}
}

func TestThinkingSummaryShowsDisclosureGlyphOnlyWhenExpandable(t *testing.T) {
	noReasoning := renderThinkingSummary("", time.Second, false, 80)
	if strings.Contains(noReasoning, glyph.DisclosureCollapsed) {
		t.Fatal("a thinking phase with no reasoning has nothing to expand")
	}

	collapsed := renderThinkingSummary("some reasoning", time.Second, false, 80)
	if !strings.Contains(collapsed, glyph.DisclosureCollapsed) {
		t.Fatal("expected the collapsed disclosure glyph")
	}

	expanded := renderThinkingSummary("some reasoning", time.Second, true, 80)
	if !strings.Contains(expanded, glyph.DisclosureExpanded) {
		t.Fatal("expected the expanded disclosure glyph")
	}
}

func TestCompletedToolCallShowsDisclosureGlyphWhenResultContentPresent(t *testing.T) {
	withResult := registry.AuditEvent{ToolName: "file.read", ResultContent: "some content"}
	collapsed := renderCompletedToolCall(withResult, false, 80)
	if !strings.Contains(collapsed, glyph.DisclosureCollapsed) {
		t.Fatal("expected the collapsed disclosure glyph when ResultContent is present")
	}
	expanded := renderCompletedToolCall(withResult, true, 80)
	if !strings.Contains(expanded, glyph.DisclosureExpanded) {
		t.Fatal("expected the expanded disclosure glyph when ResultContent is present")
	}

	withoutResult := registry.AuditEvent{ToolName: "file.read"}
	rendered := renderCompletedToolCall(withoutResult, false, 80)
	if strings.Contains(rendered, glyph.DisclosureCollapsed) || strings.Contains(rendered, glyph.DisclosureExpanded) {
		t.Fatal("expected no disclosure glyph when there is nothing to expand")
	}
}

// TestMarkdownEmitsRichStyling pins the actual SGR codes for bold, italic,
// and heading colour. The pre-existing guard only asserted that *some* escape
// sequence was present, which passes even when every one of these degrades to
// plain text — the failure mode this package kept shipping.
func TestMarkdownEmitsRichStyling(t *testing.T) {
	out := renderAgentMarkdown("# Title\n\nSome **bold** and *italic* text.\n", 100)
	for _, c := range []struct{ name, sgr string }{
		{"bold", ";1m"},
		{"italic", ";3m"},
		{"H1 accent colour", "38;5;209"},
	} {
		if !strings.Contains(out, c.sgr) {
			t.Errorf("markdown output lost %s (%q):\n%q", c.name, c.sgr, out)
		}
	}
}

// TestIndentedMarkdownStillRenders is the regression guard for the root cause
// of raw "#" and "**" reaching the transcript: CommonMark reads a line
// indented four or more spaces as a code block, and expandTabs turns one
// leading tab into tabStop spaces before glamour ever sees the content.
func TestIndentedMarkdownStillRenders(t *testing.T) {
	cases := map[string]string{
		"flush left": "# Title\n\n**bold** text\n",
		"tab":        "\t# Title\n\n\t**bold** text\n",
		"four space": "    # Title\n\n    **bold** text\n",
		"two space":  "  # Title\n\n  **bold** text\n",
	}
	for name, src := range cases {
		out := renderAgentMarkdown(expandTabs(src), 100)
		if strings.Contains(out, "**") || strings.Contains(out, "# Title") {
			t.Errorf("%s: raw markdown leaked to the transcript:\n%q", name, out)
		}
	}
}

// TestDedentPreservesRelativeIndent guards the other direction: the dedent
// must not flatten nested structure, only the flat outer indent.
func TestDedentPreservesRelativeIndent(t *testing.T) {
	got := dedentMarkdown("    - outer\n      - nested\n")
	if want := "- outer\n  - nested\n"; got != want {
		t.Errorf("dedentMarkdown() = %q, want %q", got, want)
	}
	src := "# Flush\n\n    indented code\n"
	if got := dedentMarkdown(src); got != src {
		t.Errorf("dedentMarkdown() altered a flush-left document: %q", got)
	}
}

// TestSubagentCardShowsProviderModel verifies that a subagent card renders
// the resolved model and provider (e.g. "qwen2.5-coder:14b @ ollama") on
// both running and completed cards, so the user can see what model actually
// ran a subagent. A card with no metadata must not render a bare separator.
func TestSubagentCardShowsProviderModel(t *testing.T) {
	running := session.SubagentView{
		Label:     "explore repo",
		Status:    session.SubagentRunning,
		Model:     "qwen2.5-coder:14b",
		Provider:  "ollama",
		StartedAt: time.Now().Add(-5 * time.Second),
	}
	got := stripANSI(renderSubagentCard(running, false, "⠋", 100))
	if !strings.Contains(got, "qwen2.5-coder:14b @ ollama") {
		t.Errorf("running card missing model @ provider:\n%s", got)
	}

	done := session.SubagentView{
		Label:     "explore repo",
		Status:    session.SubagentDone,
		Model:     "qwen2.5-coder:14b",
		Provider:  "ollama",
		StartedAt: time.Now().Add(-2 * time.Minute),
		EndedAt:   time.Now(),
	}
	gotDone := stripANSI(renderSubagentCard(done, false, "⠋", 100))
	if !strings.Contains(gotDone, "qwen2.5-coder:14b @ ollama") {
		t.Errorf("completed card missing model @ provider:\n%s", gotDone)
	}

	// A childless, metadata-less card must not render a dangling separator.
	plain := session.SubagentView{Label: "no meta", Status: session.SubagentDone}
	gotPlain := stripANSI(renderSubagentCard(plain, false, "⠋", 100))
	if strings.Contains(gotPlain, " · ") {
		t.Errorf("metadata-less card rendered dangling separators:\n%q", gotPlain)
	}
}

func TestRunningSubagentCardShowsCurrentTool(t *testing.T) {
	v := session.SubagentView{
		Label:       "sdd_implementer · task 3 — Add retry helper",
		Status:      session.SubagentRunning,
		ToolCalls:   9,
		CurrentTool: "editing internal/retry/retry.go",
		StartedAt:   time.Now().Add(-72 * time.Second),
	}
	got := stripANSI(renderSubagentCard(v, false, "⠋", 100))
	if !strings.Contains(got, "editing internal/retry/retry.go") {
		t.Errorf("a running card must say what the subagent is doing, not just count calls:\n%s", got)
	}
}

func TestFinishedSubagentCardOmitsCurrentTool(t *testing.T) {
	v := session.SubagentView{
		Label:       "sdd_implementer · task 3",
		Status:      session.SubagentDone,
		ToolCalls:   9,
		CurrentTool: "editing internal/retry/retry.go",
		StartedAt:   time.Now().Add(-2 * time.Minute),
		EndedAt:     time.Now(),
	}
	got := stripANSI(renderSubagentCard(v, false, "⠋", 100))
	if strings.Contains(got, "editing") {
		t.Errorf("a finished card must not claim in-flight work:\n%s", got)
	}
}

func TestRenderSubagentCardRunningHasOneGlyph(t *testing.T) {
	child := newChildState(t)
	running := session.SubagentView{
		ID:        1,
		Label:     "explore repo",
		Status:    session.SubagentRunning,
		StartedAt: time.Now().Add(-5 * time.Second),
		Child:     child,
	}
	out := stripANSI(renderSubagentCard(running, false, "⠋", 80))
	// The running card should have a spinner glyph in the gutter and NO
	// leading agent glyph in the head line.
	glyphCount := strings.Count(out, "⧉")
	if glyphCount != 0 {
		t.Errorf("running card should not contain agent glyph ⧉, found %d:\n%s", glyphCount, out)
	}
	// It should contain the spinner frame.
	if !strings.Contains(out, "⠋") {
		t.Errorf("running card should contain spinner frame ⠋:\n%s", out)
	}
}

func TestRenderSubagentCardDoneHasOneGlyph(t *testing.T) {
	done := session.SubagentView{
		ID:     2,
		Label:  "explore repo",
		Status: session.SubagentDone,
		Child:  newChildState(t),
	}
	out := stripANSI(renderSubagentCard(done, false, "⠋", 80))
	glyphCount := strings.Count(out, "⧉")
	if glyphCount != 0 {
		t.Errorf("done card should not contain agent glyph ⧉, found %d:\n%s", glyphCount, out)
	}
}
