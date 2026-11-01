package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/liveregion"
	"marshal/internal/app/tui/theme"
)

func TestWrapOverwideLinesWrapsLongCodeLine(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := wrapOverwideLines("  "+long, 80)
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Fatalf("line width = %d, want <= 80: %q", w, line)
		}
	}
	// Continuation lines keep the original indent so wrapped code still
	// reads as indented code.
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("line %d lost its indent: %q", i, line)
		}
	}
}

func TestWrapOverwideLinesLeavesShortLinesAlone(t *testing.T) {
	in := "short line\nanother"
	if out := wrapOverwideLines(in, 80); out != in {
		t.Fatalf("short lines were modified: %q", out)
	}
}

// glamour's WithWordWrap does not cover fenced code blocks; the
// post-processing in renderMarkdownWithMargin must catch them so no
// transcript line is wider than the viewport.
func TestRenderMarkdownWrapsCodeBlockLines(t *testing.T) {
	md := "before\n\n```go\n" + strings.Repeat("y", 200) + "\n```\n"
	out, ok := renderMarkdown(md, 80)
	if !ok {
		t.Skip("glamour renderer unavailable")
	}
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Fatalf("rendered line width = %d, want <= 80", w)
		}
	}
}

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

func TestRenderUserMessageUsesPromptPrefix(t *testing.T) {
	out := renderMessage(wireMessage(session.Message{Role: session.RoleUser, Content: "fix the tests", ContentType: session.ContentTypePlain}), 80)
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
	out := stripANSI(renderMessage(wireMessage(session.Message{Role: session.RoleAssistant, Content: "I found the bug.", ContentType: session.ContentTypeMarkdown}), 80))
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
	out := renderMessage(wireMessage(session.Message{Role: session.RoleAssistant, Content: "I found the bug.\n", ContentType: session.ContentTypeMarkdown}), 80)
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
	out := renderThinkingBox("checking the auth flow", "⠋", 0, regionView{}, 80)
	plain := stripANSI(out)
	if strings.Contains(plain, "╭") {
		t.Fatalf("live thinking should be inline, not boxed:\n%s", out)
	}
	if !strings.Contains(plain, "⠋ thinking") || !strings.Contains(plain, "checking the auth flow") {
		t.Fatalf("live thinking missing spinner or text:\n%s", out)
	}
	if !strings.Contains(plain, " ▍ ") {
		t.Fatalf("live thinking tail lines missing ▍ gutter:\n%s", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("live thinking should not add a blank trailing newline:\n%q", out)
	}
}

func TestRenderToolResultUsesGutter(t *testing.T) {
	out := renderMessage(wireMessage(session.Message{Role: session.RoleAssistant, Content: "shell.run: go test ./...\nFAIL: TestX", ContentType: session.ContentTypeToolResult}), 80)
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
	out := renderMessage(wireMessage(session.Message{Role: session.RoleSystem, Content: "Agent turn cancelled.", ContentType: session.ContentTypePlain}), 80)
	// The notice sits on the shared 3-cell gutter, like every other item.
	if !strings.Contains(stripANSI(out), " · Agent turn cancelled.") {
		t.Fatalf("system notice missing dim · gutter:\n%s", out)
	}
}

func TestRenderPlanBlockUsesGutter(t *testing.T) {
	out := renderMessage(wireMessage(session.Message{Role: session.RoleAssistant, Content: "1. read parser.go\n2. patch it", ContentType: session.ContentTypePlan}), 80)
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
	out := renderMessage(wireMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true}), 80)
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
	out := renderMessage(wireMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true, Salvaged: true, SalvageReason: "truncated"}), 80)
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
	out := renderMessage(wireMessage(session.Message{Role: session.RoleAssistant, Content: "All done.", ContentType: session.ContentTypeMarkdown, Final: true}), 80)
	if strings.Contains(out, "salvaged") {
		t.Fatalf("normal final answer must not contain salvage marker:\n%s", out)
	}
}

func TestRenderActiveToolCallUsesGutter(t *testing.T) {
	atc := session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Unix(100, 0)}
	out := renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Unix(104, 0), false, 80)
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

func TestRenderNoticeInline(t *testing.T) {
	out := renderNotice(session.Notice{
		Category: session.NoticeProvider,
		Severity: session.SeverityError,
		Message:  "connection refused",
	}, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "✗") {
		t.Fatalf("provider error missing ✗ gutter:\n%s", out)
	}
	if !strings.Contains(plain, "provider") || !strings.Contains(plain, "connection refused") {
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
		if out := renderMessage(wireMessage(msg), 80); out != "" {
			t.Fatalf("skill body must not render in the transcript, got:\n%s", out)
		}
	})

	t.Run("skill tag compact", func(t *testing.T) {
		msg := session.Message{
			Role:        session.RoleSystem,
			Content:     "using-superpowers",
			ContentType: session.ContentTypeSkill,
		}
		plain := stripANSI(renderMessage(wireMessage(msg), 80))
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
		result := renderItem(wireItem(item), nil, false, width)
		if !strings.Contains(result, "thought for 2s") {
			t.Errorf("expected thinking summary, got: %s", result)
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
		result := renderItem(wireItem(item), nil, false, width)
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
			out := renderMessage(wireMessage(msg), width)
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
	out := renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), false, 80)
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
	out := renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), false, 80)
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
	out := renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Now(), false, 80)
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
	out := renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Now(), false, 80)
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
	out := stripANSI(renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
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
	out := stripANSI(renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
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
	out := stripANSI(renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
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
	out := stripANSI(renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "⠋", time.Now(), false, 80))
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

func TestActiveToolCallFitsWidthWithWideRunes(t *testing.T) {
	out := renderActiveToolCall(wireRunning(session.ActiveToolCall{
		Name:      "agent.run",
		Args:      "調査: これはとても長いサブエージェントの説明文で幅を超えます",
		StartedAt: time.Now(),
	}), session.SandboxInfo{}, false, "⠂", time.Now(), false, 40)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d cells wide, budget 40: %q", w, line)
		}
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
	out := stripANSI(renderActiveToolCall(wireRunning(session.ActiveToolCall{
		Name:      "agent.run",
		Args:      long,
		StartedAt: time.Now(),
	}), session.SandboxInfo{}, false, "⠂", time.Now(), false, 30))
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
	out := stripANSI(renderActiveToolCall(wireRunning(session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "echo test",
		Output:    long,
		StartedAt: time.Now(),
	}), session.SandboxInfo{}, false, "⠂", time.Now(), false, 30))
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

func TestBlockRenderersEndWithSingleNewline(t *testing.T) {
	outs := map[string]string{
		"user message":   renderUserMessage("hello", 80),
		"system notice":  renderSystemNotice("note", 80),
		"tool result":    renderToolResultLine("line1\nline2", 80),
		"plan block":     renderPlanBlock("step 1", 80),
		"provider error": renderNotice(session.Notice{Category: session.NoticeProvider, Severity: session.SeverityError, Message: "boom"}, 80),
		"queued":         renderQueuedMessages([]string{"q1"}, 80),
		"active tool": renderActiveToolCall(
			wireRunning(session.ActiveToolCall{Name: "shell.run", Args: "go test", StartedAt: time.Unix(100, 0)}),
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

	collapsed := renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "", time.Unix(101, 0), false, 80)
	if strings.Contains(collapsed, "output line 0") {
		t.Fatal("expected collapsed tail to omit early lines")
	}
	if !strings.Contains(collapsed, "output line 9") {
		t.Fatal("expected collapsed tail to include the last line")
	}

	expanded := renderActiveToolCall(wireRunning(atc), session.SandboxInfo{}, false, "", time.Unix(101, 0), true, 80)
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

// TestMarkdownEmitsRichStyling pins the actual SGR codes for bold, italic,
// and heading colour. The pre-existing guard only asserted that *some* escape
// sequence was present, which passes even when every one of these degrades to
// plain text — the failure mode this package kept shipping.
func TestMarkdownEmitsRichStyling(t *testing.T) {
	out := renderAgentMarkdown("# Title\n\n## Section\n\n### Sub\n\nSome **bold** and *italic* text.\n", 100)
	for _, c := range []struct{ name, sgr string }{
		{"bold", ";1m"},
		{"italic", ";3m"},
		{"H1 accent colour", "38;5;209"},
	} {
		if !strings.Contains(out, c.sgr) {
			t.Errorf("markdown output lost %s (%q):\n%q", c.name, c.sgr, out)
		}
	}
	plain := stripANSI(out)
	for _, marker := range []string{"## Section", "### Sub"} {
		if strings.Contains(plain, marker) {
			t.Errorf("heading prefix leaked into rendered markdown:\n%q", plain)
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

func TestThinkingBoxHeightIsStable(t *testing.T) {
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	t.Cleanup(func() { theme.Reload(theme.LoadFor(false, "xterm-256color")) })
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString(fmt.Sprintf("a fairly long line of reasoning number %d that wraps\n", i))
		out := renderThinkingBox(sb.String(), "*", 12*time.Second, regionView{}, 60)
		if got := strings.Count(out, "\n"); got > liveregion.ThinkingRows {
			t.Fatalf("thinking box = %d rows at step %d, cap is %d", got, i, liveregion.ThinkingRows)
		}
	}
}

func TestThinkingBoxEmptyReasoningRendersNothing(t *testing.T) {
	if out := renderThinkingBox("   \n  ", "*", time.Second, regionView{}, 60); out != "" {
		t.Fatalf("empty reasoning must render nothing, got %q", out)
	}
}

// renderThinkingSummary is history and must stay unbounded and flat.
func TestThinkingSummaryStaysUnboundedAndFlat(t *testing.T) {
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	t.Cleanup(func() { theme.Reload(theme.LoadFor(false, "xterm-256color")) })
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString(fmt.Sprintf("line %d\n", i))
	}
	out := renderThinkingSummary(sb.String(), 4*time.Second, true, 60)
	if got := strings.Count(out, "\n"); got <= liveregion.ThinkingRows {
		t.Fatalf("expanded thinking summary must stay unbounded, got %d rows", got)
	}
	if strings.Contains(out, "48;5;") {
		t.Fatal("thinking summary is history and must not be tinted")
	}
}

// The four job-exit row tests (success, failure, expansion, no tint) lived
// here. A job exit is no longer a row: its card is the shell.run that started
// it, settled in place, and the moment it finished is a completion marker.
// The card's own coverage is TestJobCardIsOneLineAndSettles in
// cards_render_test.go.

func TestCompactionMarkerRendersAsOneLine(t *testing.T) {
	out := renderMessage(wireMessage(session.Message{
		Role:        session.RoleSystem,
		ContentType: session.ContentTypeCompaction,
		Content:     "compacted 62 messages to 5 · 118k → 9k · generation 3",
	}), 80)

	if out == "" {
		t.Fatal("compaction marker rendered empty")
	}
	if !strings.Contains(out, "compacted") {
		t.Errorf("marker text missing from %q", out)
	}
	// One line of content: it is a boundary marker, not prose.
	if n := strings.Count(strings.TrimRight(out, "\n"), "\n"); n != 0 {
		t.Errorf("marker spans %d extra lines, want a single line: %q", n+1, out)
	}
}

// The durable subagent report is model context, not transcript content.
// Rendering it put a turn separator and a ❯ prompt in the transcript for
// something the user never typed.
func TestSubagentReportRendersNothing(t *testing.T) {
	msg := session.Message{
		Role:        session.RoleUser,
		Content:     "[subagent 1 finished] found two issues",
		ContentType: session.ContentTypeSubagentReport,
	}
	if out := renderMessage(wireMessage(msg), 80); out != "" {
		t.Fatalf("subagent report must render nothing, got %q", out)
	}
}

func TestSubagentReportIsNotAUserTurn(t *testing.T) {
	item := session.TranscriptItem{
		Kind: session.KindMessage,
		Message: &session.Message{
			Role:        session.RoleUser,
			Content:     "[subagent 1 finished] found two issues",
			ContentType: session.ContentTypeSubagentReport,
		},
	}
	if isUserTurn(item) {
		t.Fatal("a subagent report must not count as a user turn, or it emits a turn separator")
	}
}

// The durable watch report is model context, not transcript content, and
// must not count as a user turn (mirrors the subagent report behavior).
func TestWatchReportRendersNothing(t *testing.T) {
	msg := session.Message{
		Role:        session.RoleUser,
		Content:     "[watch build fired] kind=command",
		ContentType: session.ContentTypeWatchReport,
	}
	if out := renderMessage(wireMessage(msg), 80); out != "" {
		t.Fatalf("watch report must render nothing, got %q", out)
	}
}

func TestWatchReportIsNotAUserTurn(t *testing.T) {
	item := session.TranscriptItem{
		Kind: session.KindMessage,
		Message: &session.Message{
			Role:        session.RoleUser,
			Content:     "[watch build fired] kind=command",
			ContentType: session.ContentTypeWatchReport,
		},
	}
	if isUserTurn(item) {
		t.Fatal("a watch report must not count as a user turn, or it emits a turn separator")
	}
}

func TestRealUserMessageStillIsAUserTurn(t *testing.T) {
	item := session.TranscriptItem{
		Kind: session.KindMessage,
		Message: &session.Message{
			Role:        session.RoleUser,
			Content:     "fix the wheel handler",
			ContentType: session.ContentTypePlain,
		},
	}
	if !isUserTurn(item) {
		t.Fatal("an ordinary user message must still be a user turn")
	}
}

// An expanded message draws the whole reasoning, which the wire cut: until
// the detail lands the row still shows the cut, and once it lands the tail is
// there.
func TestExpandedMessageReadsItsWholeReasoning(t *testing.T) {
	m := newTestModel(t)
	var reasoning strings.Builder
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&reasoning, "thought %04d\n", i)
	}
	reasoning.WriteString("REASONING END")
	m.state.BeginStreaming()
	m.state.AppendThinking(reasoning.String())
	m.state.AddMessage(session.RoleAssistant, "Done.", session.ContentTypeMarkdown)
	m.invalidateTranscript()
	m.refreshViewport()

	content := func() string { return stripANSI(strings.Join(m.contentLines, "\n")) }

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = asModel(t, updated)
	if !m.expandAll {
		t.Fatal("ctrl+g did not turn expand-all on")
	}
	if strings.Contains(content(), "REASONING END") {
		t.Fatalf("the undrained expansion must draw the wire's cut:\n%s", content())
	}

	m = drainCmds(m, cmd)
	if got := content(); !strings.Contains(got, "REASONING END") {
		t.Fatalf("the drained expansion must read the whole reasoning:\n%s", got)
	}
}
