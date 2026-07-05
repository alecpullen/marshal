# TUI Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Marshal's two-column dashboard TUI with a single-column, transcript-centric layout: borderless transcript with symbol-bullet messages (`❯`, `⏺`, `⎿`), a bordered input, one slim status line, and the old sidebar's information moved inline or behind `/context` and `/log`.

**Architecture:** Pure rendering change inside `internal/app/tui` plus two command handlers in `internal/commands`. The state-polling architecture (150ms tick, all live state read from `session.State`) is untouched. New renderers and the status line are built first alongside the old code (Tasks 1-3), then the View/geometry swap removes the two-column layout in one task (Task 4), then key handling is simplified (Task 5). `model.go` is split into `model.go` / `view.go` / `transcript.go` / `status.go`.

**Tech Stack:** Go, Bubble Tea, lipgloss, `charmbracelet/x/ansi`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-07-05-tui-simplification-design.md`. Read it before starting.

## Global Constraints

- Build requires CGO: `CGO_ENABLED=1 go build ./cmd/marshal` (tree-sitter dependency).
- Before every commit: `gofmt -w .` and `go vet ./...` clean; run `go test -race ./internal/app/tui/ ./internal/commands/`.
- The TUI is rendering-only — no routing, policy, or prompt logic (CLAUDE.md). No new `tea.Msg` types, channels, or `tea.Program` references; all live state is read from `session.State` on the existing 150ms tick.
- Minimum terminal: 40×10 (`minTerminalWidth`/`minTerminalHeight` keep their current values).
- Every rendered line must fit the terminal width — the fit tests at 40×10, 80×24, 100×30, 120×40 are non-negotiable.
- The approval panel keeps its warning border — it is deliberately the only loud element. The final-answer rendering (cyan left border + `Response` label) is kept unchanged.
- Commit only the files each task names.

## File Structure (end state)

| File | Responsibility |
|---|---|
| `internal/app/tui/model.go` | `Model` struct, options, `Init`/`Update`, key handling, command dispatch, overlay switching, small string helpers |
| `internal/app/tui/view.go` | `View()`, `resize()` geometry, input area, fallback view |
| `internal/app/tui/transcript.go` | every transcript renderer: messages, markdown machinery, live blocks (thinking, approval, active tool call), provider error |
| `internal/app/tui/status.go` | the one-line status bar |
| `internal/app/tui/renderers.go` | **deleted** (contents absorbed into `transcript.go`) |
| `internal/commands/commands.go` | `/context` upgraded, `/log` added |

Test files mirror sources: new tests go in `transcript_test.go`, `status_test.go`, `view_test.go`; obsolete tests are deleted from `model_test.go` in the task that deletes their subject.

---

### Task 1: `/log` command and richer `/context`

**Files:**
- Modify: `internal/commands/commands.go`
- Test: `internal/commands/commands_test.go`

**Interfaces:**
- Consumes: `session.State.AuditLog() []registry.AuditEvent` (fields used: `Timestamp time.Time`, `ToolName string`, `ResultSummary string`), `session.State.ContextPack() contextpack.Pack` (fields: `Sections []Section` with `Title`, `Source`, `EstimatedTokens`; `TokenUsage.EstimatedTokens`, `TokenUsage.MaxTokens`; method `IsEmpty() bool`), `session.State.Messages()`.
- Produces: registered commands `/log` and `/context`; helper `compactTokens(n int) string` in package `commands`. No other task depends on these symbols, only on the commands existing.

- [ ] **Step 1: Write the failing tests**

Add to `internal/commands/commands_test.go` (match the existing tests' setup style — they construct a `session.State` via `session.New(config.Default(), t.TempDir(), ...)` and a registry via `registry.New()`; reuse the file's existing helper if one exists):

```go
func TestLogCommandShowsRecentAuditEvents(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 20; i++ {
		state.LogToolCall(registry.AuditEvent{
			Timestamp:     time.Date(2026, 7, 5, 12, 0, i, 0, time.UTC),
			ToolName:      fmt.Sprintf("tool.%d", i),
			ResultSummary: fmt.Sprintf("result %d", i),
		})
	}

	cmd, ok := cmdReg.Lookup("log")
	if !ok {
		t.Fatal("log command not registered")
	}
	out := cmd.Handler(state, nil)

	if !strings.Contains(out, "tool.19") || !strings.Contains(out, "result 19") {
		t.Fatalf("log output missing newest event:\n%s", out)
	}
	if strings.Contains(out, "tool.4 ") {
		t.Fatalf("log output should only contain the last 15 events:\n%s", out)
	}
}

func TestLogCommandEmpty(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	cmd, _ := cmdReg.Lookup("log")
	if out := cmd.Handler(state, nil); out != "No tool calls yet." {
		t.Fatalf("empty log output = %q", out)
	}
}

func TestContextCommandListsPackSections(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Title: "internal/app/tui/model.go", EstimatedTokens: 8400},
			{Source: "repo-map", EstimatedTokens: 2100},
		},
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 10500, MaxTokens: 32000},
	})

	cmd, _ := cmdReg.Lookup("context")
	out := cmd.Handler(state, nil)

	for _, want := range []string{"10k/32k", "internal/app/tui/model.go", "8k", "repo-map"} {
		if !strings.Contains(out, want) {
			t.Fatalf("context output missing %q:\n%s", want, out)
		}
	}
}
```

Add imports as needed: `fmt`, `strings`, `time`, `marshal/internal/app/config`, `marshal/internal/app/session`, `marshal/internal/contextpack`, `marshal/internal/tools/registry`.

Note: if `contextpack.Pack`/`Section`/`TokenUsage` field names differ from the above, check `internal/contextpack` (the `Pack` type) and adjust the literal — the fields used are the section title/source, per-section `EstimatedTokens`, and pack-level `TokenUsage.EstimatedTokens`/`MaxTokens`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/commands/ -run 'TestLogCommand|TestContextCommand' -v`
Expected: FAIL — "log command not registered" and missing-substring failures for `/context`.

- [ ] **Step 3: Implement**

In `internal/commands/commands.go`:

1. Add the helper at the bottom of the file:

```go
// compactTokens renders a token count the way the TUI does: "842", "18k".
func compactTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}
```

2. Replace the existing `context` command's `Handler` (currently returns message/char counts only) with:

```go
Handler: func(state *session.State, args []string) string {
	msgs := state.Messages()
	var totalChars int
	for _, m := range msgs {
		totalChars += len(m.Content)
	}
	pack := state.ContextPack()
	var b strings.Builder
	fmt.Fprintf(&b, "Context:\n  Messages: %d (%d chars)\n", len(msgs), totalChars)
	if pack.IsEmpty() {
		b.WriteString("  No context pack built yet.")
		return b.String()
	}
	fmt.Fprintf(&b, "  Pack: %s/%s tokens, %d sections\n",
		compactTokens(pack.TokenUsage.EstimatedTokens),
		compactTokens(pack.TokenUsage.MaxTokens),
		len(pack.Sections),
	)
	for i, section := range pack.Sections {
		title := section.Title
		if title == "" {
			title = section.Source
		}
		fmt.Fprintf(&b, "    %d  %s  %s\n", i+1, title, compactTokens(section.EstimatedTokens))
	}
	return strings.TrimRight(b.String(), "\n")
},
```

3. Add the `/log` command to the `commands` slice (after the `context` entry):

```go
{
	Name:        "log",
	Description: "Show recent tool calls (audit log)",
	Handler: func(state *session.State, args []string) string {
		events := state.AuditLog()
		if len(events) == 0 {
			return "No tool calls yet."
		}
		start := 0
		if len(events) > 15 {
			start = len(events) - 15
		}
		var b strings.Builder
		b.WriteString("Recent tool calls:\n\n")
		for _, e := range events[start:] {
			b.WriteString(fmt.Sprintf("  %s  %-14s  %s\n",
				e.Timestamp.Format("15:04:05"), e.ToolName, e.ResultSummary))
		}
		return strings.TrimRight(b.String(), "\n")
	},
},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/commands/ -v`
Expected: PASS (new tests plus all pre-existing command tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/commands && go vet ./internal/commands/
git add internal/commands/commands.go internal/commands/commands_test.go
git commit -m "feat(commands): add /log and context-pack detail in /context"
```

---

### Task 2: Symbol-bullet transcript renderers (`transcript.go`)

**Files:**
- Create: `internal/app/tui/transcript.go`
- Delete: `internal/app/tui/renderers.go` (contents move or are replaced)
- Modify: `internal/app/tui/model.go` (delete `renderMessage`, `renderCode`, `renderThinkingBox`, `renderThinkingSummary`, `renderApprovalInline`, `renderActiveToolCall`, `renderCommandToolCall`, `renderSimpleToolCall`, `formatElapsed`, `formatThinkDuration`, `formatBoxLine`, `thinkingBoxTailLines` — they move to `transcript.go`)
- Test: create `internal/app/tui/transcript_test.go`; delete obsolete renderer tests from `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `session.Message` (fields `Role`, `Content`, `ContentType`, `Final`), `session.RoleUser`/`RoleSystem`, `session.ContentType*` constants, existing styles/colors in `model.go` (`accentColor`, `dimColor`, `mutedStyle`, `warningColor`, `errorColor`, `successColor`, `violetColor`, `panelTitleStyle`, `thinkingLineStyle`).
- Produces (used by `refreshViewport` today and Task 4's view):
  - `renderMessage(msg session.Message, width int) string` — same name/signature as today, new dispatch.
  - `renderUserMessage(content string, width int) string`
  - `renderAgentMarkdown(content string, width int) string`
  - `renderSystemNotice(content string, width int) string`
  - `renderToolResultLine(content string, width int) string`
  - `renderPlanBlock(content string, width int) string`
  - `renderDiffBlock(content string, width int) string`
  - `renderProviderError(err error, width int) string` (wired into the viewport in Task 4)
  - `renderActiveToolCall(atc session.ActiveToolCall, spinnerFrame string, now time.Time, width int) string` — same signature, restyled (no border).
  - Moved verbatim (unchanged behavior): `splitFencedBlocks`, `parseMarkdownLine`, `renderCodeBlock`, `renderFinalAnswer`, `renderThinkingBox`, `renderThinkingSummary`, `renderApprovalInline`, `formatElapsed`, `formatThinkDuration`, `thinkingBoxTailLines`.
- Deleted symbols (nothing may reference them after this task): `renderPlain`, `renderPlan`, `renderDiff`, `renderToolResult`, `renderMarkdown`, `renderCode`, `roleStyleFor`, `formatBoxLine`, `renderCommandToolCall`, `renderSimpleToolCall`, and the role-label styles `userRoleStyle`, `agentRoleStyle`, `toolRoleStyle`, `outputRoleStyle` (check `grep -rn` for stragglers before deleting).

- [ ] **Step 1: Write the failing tests**

Create `internal/app/tui/transcript_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestRenderUser|TestRenderAgentProse|TestRenderToolResultUsesBullets|TestRenderSystemNotice|TestRenderPlanBlock|TestRenderDiffBlock|TestRenderFinalAnswerKeeps|TestRenderActiveToolCallIsBorderless|TestRenderProviderErrorInline|TestTranscriptLinesFit' -v`
Expected: FAIL to compile — `undefined: renderProviderError`; assertion failures for the rest (old renderers include role labels and borders).

- [ ] **Step 3: Create transcript.go**

Create `internal/app/tui/transcript.go` with package header and imports (`fmt`, `strings`, `time`, `github.com/charmbracelet/lipgloss`, `github.com/charmbracelet/x/ansi`, `marshal/internal/app/session`).

1. **Move verbatim** from `renderers.go`: `mdBlock`, `splitFencedBlocks`, `parseMarkdownLine`, `renderCodeBlock`, `renderFinalAnswer`. **Move verbatim** from `model.go`: `renderThinkingBox`, `renderThinkingSummary`, `renderApprovalInline`, `formatElapsed`, `formatThinkDuration`, `thinkingBoxTailLines`, `riskText`. Delete `renderers.go`.

2. **New dispatch** (replaces `renderMessage` in `model.go`):

```go
// renderMessage formats one transcript entry in the symbol-bullet style:
// user prompts get a ❯ prefix, agent prose renders as plain markdown with
// no role label, tool results render as ⏺/⎿ bullets, system notices are
// dim. Final answers keep the rich-content Response treatment.
func renderMessage(msg session.Message, width int) string {
	if msg.Final {
		return renderFinalAnswer(msg.Content, width)
	}
	if msg.Role == session.RoleUser {
		return renderUserMessage(msg.Content, width)
	}
	if msg.Role == session.RoleSystem {
		return renderSystemNotice(msg.Content, width)
	}
	switch msg.ContentType {
	case session.ContentTypePlan:
		return renderPlanBlock(msg.Content, width)
	case session.ContentTypeDiff:
		return renderDiffBlock(msg.Content, width)
	case session.ContentTypeToolResult:
		return renderToolResultLine(msg.Content, width)
	case session.ContentTypeCode:
		return indentBlock(renderCodeBlock(msg.Content, max(width-4, 1)), "  ") + "\n"
	default: // plain and markdown prose render identically
		return renderAgentMarkdown(msg.Content, width)
	}
}
```

3. **New renderers**:

```go
var promptPrefixStyle = lipgloss.NewStyle().Foreground(accentColor).Bold(true)

func renderUserMessage(content string, width int) string {
	contentWidth := max(width-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(promptPrefixStyle.Render("❯ "))
		} else {
			b.WriteString("  ")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderAgentMarkdown(content string, width int) string {
	contentWidth := max(width-2, 1)
	blocks := splitFencedBlocks(content)
	var b strings.Builder
	for _, block := range blocks {
		switch block.kind {
		case "code":
			b.WriteString(indentBlock(renderCodeBlock(block.text, max(contentWidth-2, 1)), "  "))
			b.WriteString("\n")
		case "prose":
			for _, pLine := range strings.Split(block.text, "\n") {
				style, transformed := parseMarkdownLine(pLine)
				wrapped := ansi.Wrap(transformed, contentWidth, "")
				for _, wl := range strings.Split(wrapped, "\n") {
					b.WriteString(style.Render(wl))
					b.WriteString("\n")
				}
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderSystemNotice(content string, width int) string {
	contentWidth := max(width-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(mutedStyle.Render("· " + line))
		} else {
			b.WriteString(mutedStyle.Render("  " + line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

var toolBulletStyle = lipgloss.NewStyle().Foreground(warningColor)

func renderToolResultLine(content string, width int) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(toolBulletStyle.Render("⏺ "))
	b.WriteString(truncateRunes(strings.TrimSpace(lines[0]), max(width-2, 1)))
	b.WriteString("\n")
	continuation := lines[1:]
	for i, line := range continuation {
		wrapped := ansi.Wrap(line, max(width-4, 1), "")
		for j, wl := range strings.Split(wrapped, "\n") {
			if i == 0 && j == 0 {
				b.WriteString(mutedStyle.Render("  ⎿ " + wl))
			} else {
				b.WriteString(mutedStyle.Render("    " + wl))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderPlanBlock(content string, width int) string {
	var b strings.Builder
	b.WriteString(promptPrefixStyle.Render("⏺ Plan"))
	b.WriteString("\n")
	contentWidth := max(width-4, 1)
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		wrapped := ansi.Wrap(line, contentWidth, "")
		for j, wl := range strings.Split(wrapped, "\n") {
			if j == 0 {
				b.WriteString("  " + wl)
			} else {
				b.WriteString("    " + wl)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderDiffBlock(content string, width int) string {
	addStyle := lipgloss.NewStyle().Foreground(successColor)
	delStyle := lipgloss.NewStyle().Foreground(errorColor)
	contentWidth := max(width-2, 1)
	var b strings.Builder
	for _, line := range strings.Split(content, "\n") {
		var lineStyle lipgloss.Style
		switch {
		case strings.HasPrefix(line, "@@"):
			lineStyle = mutedStyle
		case strings.HasPrefix(line, "+"):
			lineStyle = addStyle
		case strings.HasPrefix(line, "-"):
			lineStyle = delStyle
		default:
			lineStyle = lipgloss.NewStyle()
		}
		wrapped := ansi.Wrap(line, contentWidth, "")
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString("  ")
			b.WriteString(lineStyle.Render(wl))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

var providerErrorStyle = lipgloss.NewStyle().Foreground(errorColor).Bold(true)

func renderProviderError(err error, width int) string {
	contentWidth := max(width-2, 1)
	wrapped := ansi.Wrap("✗ provider: "+err.Error(), contentWidth, "")
	var b strings.Builder
	for _, line := range strings.Split(wrapped, "\n") {
		b.WriteString(providerErrorStyle.Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// indentBlock prefixes every non-empty line of a rendered block.
func indentBlock(block, prefix string) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}
```

4. **Restyled live tool call** (replaces the moved `renderActiveToolCall`/`renderCommandToolCall`/`renderSimpleToolCall`):

```go
// renderActiveToolCall shows the in-flight tool as a spinner line — no
// border, matching the ⏺ bullet style. Command tools get a second $ line.
func renderActiveToolCall(atc session.ActiveToolCall, spinnerFrame string, now time.Time, width int) string {
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	head := fmt.Sprintf("%s %s · %s", spinnerFrame, atc.Name, formatElapsed(elapsed))
	var b strings.Builder
	b.WriteString(toolBulletStyle.Render(truncateRunes(head, max(width-2, 1))))
	b.WriteString("\n")
	if atc.Name == "shell.run" || atc.Name == "test.run" {
		b.WriteString(mutedStyle.Render(truncateRunes("  $ "+atc.Args, max(width-2, 1))))
		b.WriteString("\n")
	} else if atc.Args != "" {
		b.WriteString(mutedStyle.Render(truncateRunes("  "+atc.Args, max(width-2, 1))))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}
```

5. In `model.go`: delete the moved/replaced functions listed in **Files** above, delete `renderPlain`/`renderPlan`/`renderDiff`/`renderToolResult`/`renderMarkdown`/`renderCode`/`roleStyleFor`/`formatBoxLine` and the styles `userRoleStyle`, `agentRoleStyle`, `toolRoleStyle`, `outputRoleStyle`. Run `grep -rn 'roleStyleFor\|renderPlain\|userRoleStyle' internal/` to confirm nothing references them.

6. In `model_test.go`, delete the tests of removed renderers: `TestRenderPlainPreservesExistingBehavior`, `TestRenderMarkdownHandlesHeadings`, `TestRenderMarkdownHandlesBlockquote`, `TestRenderMarkdownHandlesFencedCode`, `TestRenderPlanShowsSteps`, `TestRenderDiffColorizesAdditions`, `TestRenderToolResultShowsSummary`, `TestRenderMessageDispatchesByContentType`, `TestNonFinalAnswerRendersWithAgentLabel`, `TestPolishedTranscriptShowsRolesThinkingAndInput`, `TestShellCommandShowsExpandedPanel`, `TestNonCommandToolShowsSingleLine`. Keep `TestRenderCodeBlockWrapsInBorder` and `TestFinalAnswerRendersWithResponseLabel` (behavior unchanged). If any kept test asserts on role labels ("agent", "user" gutters) in transcript output, update its expected substrings to the new style (❯ / no label).

- [ ] **Step 4: Run the full TUI test suite**

Run: `go test -race ./internal/app/tui/ -v`
Expected: PASS. Layout tests (`TestPolishedView*`, approval tests, etc.) still pass because the view assembly is untouched — only per-message rendering changed. If a layout test asserts an old role label, update that assertion to the new output in this task.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui && go vet ./internal/app/tui/
git add internal/app/tui/transcript.go internal/app/tui/transcript_test.go internal/app/tui/model.go internal/app/tui/model_test.go
git rm internal/app/tui/renderers.go
git commit -m "feat(tui): symbol-bullet transcript renderers, borderless live blocks"
```

---

### Task 3: Status line (`status.go`)

**Files:**
- Create: `internal/app/tui/status.go`
- Test: create `internal/app/tui/status_test.go`

**Interfaces:**
- Consumes: `session.State.ActiveRoute()` (`RouteInfo{Active, Model, Provider, LocalOnly}`), `session.State.Activity()` (`Activity{Kind, Label, StartedAt}`), `session.State.PendingApproval()`, `session.State.ProviderError()`, `session.State.ContextPack()`, `m.forceMode`, `m.spinnerFrame`, `m.lastActivityLabel`/`m.lastActivityDone` (done-flash, `doneDisplayDuration`), `m.now()`, existing `compactTokenCount` helper in `model.go` (keep it; it moves nowhere).
- Produces: `(m Model) renderStatusLine(width int) string` — Task 4's `View` calls this. The old `renderStatusBar`/`renderStateStrip` are NOT touched in this task (deleted in Task 4).

- [ ] **Step 1: Write the failing tests**

Create `internal/app/tui/status_test.go` (the existing `model_test.go` has helpers for building a test state — reuse its pattern of `session.New(config.Default(), t.TempDir(), ...)`):

```go
package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
)

func newStatusTestModel(t *testing.T) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	return m
}

func TestStatusLineShowsRouteAndContext(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder:14b", Provider: "ollama", LocalOnly: true})
	m.state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 18000, MaxTokens: 32000},
		Sections:   []contextpack.Section{{Title: "x", EstimatedTokens: 18000}},
	})

	line := m.renderStatusLine(100)
	for _, want := range []string{"auto", "qwen2.5-coder:14b @ ollama", "local", "ctx 18k/32k"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q:\n%s", want, line)
		}
	}
}

func TestStatusLineShowsToolActivityWithElapsed(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(100, 0)})

	line := m.renderStatusLine(100)
	if !strings.Contains(line, "⠋") || !strings.Contains(line, "shell.run: go test") || !strings.Contains(line, "4s") {
		t.Fatalf("status line missing tool activity:\n%s", line)
	}
}

func TestStatusLineShowsApprovalState(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetPendingApproval(&session.PendingToolCall{Name: "shell.run", Command: "rm -rf build", ResponseChan: make(chan session.UserApprovalDecision, 1)})
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "⚠ approval") {
		t.Fatalf("status line missing approval state:\n%s", line)
	}
}

func TestStatusLineShowsProviderError(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetProviderError(errors.New("connection refused"))
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "✗ error") {
		t.Fatalf("status line missing error state:\n%s", line)
	}
}

func TestStatusLineFitsWidth(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "a-very-long-model-name:70b-instruct-q4", Provider: "ollama", LocalOnly: true})
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("x", 80), StartedAt: time.Unix(100, 0)})
	m.spinnerFrame = "⠋"
	for _, width := range []int{40, 60, 80, 120} {
		line := m.renderStatusLine(width)
		for _, l := range strings.Split(line, "\n") {
			if visibleRunes(l) > width {
				t.Fatalf("status line exceeds width %d (%d): %q", width, visibleRunes(l), l)
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run TestStatusLine -v`
Expected: FAIL to compile — `m.renderStatusLine undefined`.

- [ ] **Step 3: Implement status.go**

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"marshal/internal/app/session"
)

// renderStatusLine is the single row of persistent chrome below the input:
// left cluster identifies the session (mode · model @ provider · locality
// · ctx usage), right cluster shows what the agent is doing right now.
func (m Model) renderStatusLine(width int) string {
	left := strings.Join(m.statusLeftSegments(), " · ")
	right := m.statusRightSegment()

	gap := width - visibleRunes(left) - visibleRunes(right) - 2
	if gap < 1 {
		// Not enough room: prioritise the activity cluster.
		left = truncateRunes(left, max(width-visibleRunes(right)-3, 0))
		gap = max(width-visibleRunes(left)-visibleRunes(right)-2, 1)
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	return statusBarBg.Width(max(width, 1)).MaxWidth(max(width, 1)).Render(truncateRunes(line, width))
}

func (m Model) statusLeftSegments() []string {
	mode := m.forceMode
	if mode == "" {
		mode = "auto"
	}
	segments := []string{mode}

	route := m.state.ActiveRoute()
	if route.Active {
		segments = append(segments, fmt.Sprintf("%s @ %s", route.Model, route.Provider))
		if route.LocalOnly {
			segments = append(segments, "local")
		}
	} else {
		segments = append(segments, "no model")
		if !m.state.Config.Privacy.RemoteProvidersAllowed {
			segments = append(segments, "local")
		}
	}

	if pack := m.state.ContextPack(); !pack.IsEmpty() {
		segments = append(segments, fmt.Sprintf("ctx %s/%s",
			compactTokenCount(pack.TokenUsage.EstimatedTokens),
			compactTokenCount(pack.TokenUsage.MaxTokens)))
	}
	return segments
}

var (
	statusWarnStyle  = lipgloss.NewStyle().Foreground(warningColor).Bold(true)
	statusErrStyle   = lipgloss.NewStyle().Foreground(errorColor).Bold(true)
	statusOkStyle    = lipgloss.NewStyle().Foreground(successColor)
	statusBusyStyle  = lipgloss.NewStyle().Foreground(accentColor)
)

func (m Model) statusRightSegment() string {
	if m.state.PendingApproval() != nil {
		return statusWarnStyle.Render("⚠ approval")
	}
	activity := m.state.Activity()
	switch activity.Kind {
	case session.ActivityThinking:
		return statusBusyStyle.Render(fmt.Sprintf("%s thinking", m.spinnerFrame))
	case session.ActivityTool, session.ActivityApproval:
		elapsed := m.now().Sub(activity.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		return statusBusyStyle.Render(fmt.Sprintf("%s %s · %s", m.spinnerFrame, activity.Label, formatElapsed(elapsed)))
	}
	if m.state.ProviderError() != nil {
		return statusErrStyle.Render("✗ error")
	}
	if m.lastActivityLabel != "" && time.Since(m.lastActivityDone) < doneDisplayDuration {
		return statusOkStyle.Render("✓ " + m.lastActivityLabel)
	}
	return ""
}
```

Note: `statusBarBg` already exists in `model.go` and is kept. Do not modify `renderStatusBar`/`renderStateStrip` yet.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/app/tui/ -v`
Expected: PASS (new status tests plus everything existing — nothing calls `renderStatusLine` from the view yet).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui && go vet ./internal/app/tui/
git add internal/app/tui/status.go internal/app/tui/status_test.go
git commit -m "feat(tui): one-line status bar with route, context, and activity clusters"
```

---

### Task 4: Single-column View and geometry (`view.go`)

This is the swap task: the two-column dashboard, top bar, sidebar, state strip, old status bar, and key-help line all go away at once. Expect a large test diff — obsolete tests are deleted here, and new view tests added.

**Files:**
- Create: `internal/app/tui/view.go`
- Modify: `internal/app/tui/model.go` (delete old view code, `resize`, geometry fields/constants; extend `refreshViewport` with the inline provider error)
- Test: create `internal/app/tui/view_test.go`; prune `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `renderMessage`/live-block renderers and `renderProviderError` (Task 2), `renderStatusLine` (Task 3), existing overlay models, `renderThinkingBox`/`renderThinkingSummary`/`renderApprovalInline`/`renderActiveToolCall`.
- Produces: `View()`, `resize(width, height int)` (new geometry), `renderInputArea()` (full-width, no help line), `fallbackView()` — same names as today, new bodies, all in `view.go`.
- Deleted symbols (nothing may reference them after this task): `renderChatPanel`, `renderRightInfoPanel`, `renderSidebarTabs`, `renderPlanTab`, `renderContextTab`, `renderLogTab`, `renderModeStrip`, `renderStateStrip`, `renderStatusBar`, `renderKeyHelp`, `renderPanel`, `padPillHeight`, `pillHeight`, `activePillStyle`, `inactivePillStyle`, `statusBarBrand`, `statusBarBusy`, `panelTitleStyle` (check: `renderApprovalInline` uses `panelTitleStyle` — keep it if so), `panelSoftColor`; Model fields `activeTab`, `leftWidth`, `rightWidth`, `contentHeight`, `chatHeight`, `stateStripActive`; constants `minPanelWidth`, `totalHorizontalBorderGutter`, `verticalOverhead`, `chatBelowViewportRows`, `stateStripRows`.
- New constants: `inputBoxRows = 3` (bordered input), `statusLineRows = 1`.

- [ ] **Step 1: Write the failing view tests**

Create `internal/app/tui/view_test.go`:

```go
package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func newViewTestModel(t *testing.T, width, height int) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(width, height)
	m.refreshViewport()
	return m
}

func TestViewIsSingleColumn(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()
	view := m.View()

	for _, gone := range []string{"inspector", "1 Plan", "2 Context", "3 Log", "live transcript", "● ● ●", "MARSHAL"} {
		if strings.Contains(view, gone) {
			t.Fatalf("view still contains removed chrome %q", gone)
		}
	}
	if !strings.Contains(view, "❯") {
		t.Fatal("view missing input prompt / transcript")
	}
}

func TestViewContainsStatusLine(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})
	view := m.View()
	if !strings.Contains(view, "qwen @ ollama") {
		t.Fatalf("view missing status line route info:\n%s", view)
	}
}

func TestViewFitsTerminalSizesSingleColumn(t *testing.T) {
	sizes := [][2]int{{40, 10}, {80, 24}, {100, 30}, {120, 40}}
	for _, size := range sizes {
		m := newViewTestModel(t, size[0], size[1])
		m.state.AddMessage(session.RoleUser, strings.Repeat("wide input ", 30), session.ContentTypePlain)
		m.state.AddMessage(session.RoleAssistant, strings.Repeat("wide answer ", 30), session.ContentTypeMarkdown)
		m.refreshViewport()
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > size[1] {
			t.Fatalf("view has %d lines for height %d", len(lines), size[1])
		}
		for _, line := range lines {
			if visibleRunes(line) > size[0] {
				t.Fatalf("line exceeds width %d (%d): %q", size[0], visibleRunes(line), line)
			}
		}
	}
}

func TestProviderErrorShowsInlineNotFullScreen(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.state.SetProviderError(errors.New("connection refused"))
	m.lastMessageCount = -1
	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "✗ provider: connection refused") {
		t.Fatalf("provider error not rendered inline:\n%s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Fatal("provider error must not hide the transcript")
	}
}

func TestResizeComputesSingleColumnGeometry(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if m.viewport.Width != 98 {
		t.Fatalf("viewport.Width = %d, want 98 (width-2)", m.viewport.Width)
	}
	if m.viewport.Height != 30-inputBoxRows-statusLineRows {
		t.Fatalf("viewport.Height = %d, want %d", m.viewport.Height, 30-inputBoxRows-statusLineRows)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestViewIsSingleColumn|TestViewContainsStatusLine|TestViewFitsTerminalSizesSingleColumn|TestProviderErrorShowsInlineNot|TestResizeComputesSingleColumn' -v`
Expected: FAIL to compile (`undefined: inputBoxRows`) and chrome assertions fail against the old two-column view.

- [ ] **Step 3: Create view.go and rewrite geometry**

Create `internal/app/tui/view.go`:

```go
package tui

import (
	"github.com/charmbracelet/lipgloss"
)

const (
	inputBoxRows   = 3 // bordered input: top border + text row + bottom border
	statusLineRows = 1
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}
	if m.settingsOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModel.View())
	}
	if m.memoryOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.memoryModel.View())
	}

	transcript := lipgloss.NewStyle().Padding(0, 1).Render(m.viewport.View())
	return lipgloss.JoinVertical(
		lipgloss.Left,
		transcript,
		m.renderInputArea(),
		m.renderStatusLine(m.width),
	)
}

func (m Model) renderInputArea() string {
	inputStyle := lipgloss.NewStyle().
		Width(max(m.width-2, 1)).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Padding(0, 1)
	inputLine := lipgloss.JoinHorizontal(
		lipgloss.Top,
		promptPrefixStyle.Render("❯ "),
		m.input.View(),
	)
	return inputStyle.Render(inputLine)
}

func (m Model) fallbackView() string {
	if m.settingsOpen {
		return m.settingsModel.View()
	}
	if m.memoryOpen {
		return m.memoryModel.View()
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		mutedStyle.Render("Marshal — waiting for terminal resize..."),
	)
}
```

Replace `resize` in `model.go` (delete the old body entirely):

```go
func (m *Model) resize(width, height int) {
	if width < minTerminalWidth {
		width = minTerminalWidth
	}
	if height < minTerminalHeight {
		height = minTerminalHeight
	}
	m.width = width
	m.height = height

	// Transcript viewport: full width minus 1 column of padding each side,
	// full height minus the bordered input box and the status line.
	m.viewport.Width = max(width-2, 1)
	m.viewport.Height = max(height-inputBoxRows-statusLineRows, 1)

	// Input interior: width minus border (2), padding (2), and prompt (2).
	m.input.Width = max(width-8, 1)
}
```

- [ ] **Step 4: Wire the inline provider error and delete dead code**

1. In `refreshViewport` (`model.go`), add error tracking. Change the dirty-check block to include errors, and append the error block after the live blocks:

```go
func (m *Model) refreshViewport() {
	messages := m.state.Messages()
	inProgress := m.state.InProgress()
	streamLen := len(inProgress.Reasoning)
	hasApproval := m.state.PendingApproval() != nil
	hasError := m.state.ProviderError() != nil
	if len(messages) == m.lastMessageCount && streamLen == m.lastStreamLen && !m.busy &&
		hasApproval == m.lastHadApproval && hasError == m.lastHadError {
		return
	}
	m.lastMessageCount = len(messages)
	m.lastStreamLen = streamLen
	m.lastHadApproval = hasApproval
	m.lastHadError = hasError

	var b strings.Builder
	if len(messages) == 0 {
		b.WriteString("  No messages yet.\n")
	}
	for _, message := range messages {
		if message.Reasoning != "" {
			b.WriteString(renderThinkingSummary(message.Reasoning, message.ThinkDuration, m.thinkingExpanded, m.viewport.Width))
		}
		b.WriteString(renderMessage(message, m.viewport.Width))
	}
	if inProgress.Active {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.viewport.Width))
	}
	if tc := m.state.PendingApproval(); tc != nil {
		b.WriteString(renderApprovalInline(tc, m.viewport.Width))
	}
	if atc, ok := m.state.ActiveToolCall(); ok {
		b.WriteString(renderActiveToolCall(atc, m.spinnerFrame, m.now(), m.viewport.Width))
	}
	if err := m.state.ProviderError(); err != nil {
		b.WriteString(renderProviderError(err, m.viewport.Width))
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}
```

Add the `lastHadError bool` field to `Model` next to `lastHadApproval`.

2. Delete from `model.go`: the old `View`, `renderChatPanel`, `renderRightInfoPanel`, `renderSidebarTabs`, `renderPlanTab`, `renderContextTab`, `renderLogTab`, `renderModeStrip`, `renderStateStrip`, `renderStatusBar`, `renderKeyHelp`, `renderPanel`, `padPillHeight`, `pillHeight`, `renderInputArea`, `fallbackView` (moved to `view.go`), styles `activePillStyle`, `inactivePillStyle`, `statusBarBrand`, `statusBarBusy`, `panelSoftColor`; Model fields `activeTab`, `leftWidth`, `rightWidth`, `contentHeight`, `chatHeight`, `stateStripActive`; constants `minPanelWidth`, `totalHorizontalBorderGutter`, `verticalOverhead`, `chatBelowViewportRows`, `stateStripRows`. Keep `panelTitleStyle` if `renderApprovalInline` still uses it (it does). Keep `compactTokenCount` (used by `status.go`). In `Update`, remove the tab-key branches that reference `m.activeTab` **only enough to compile** — full key cleanup is Task 5; for now replace the bodies of the Tab/Shift+Tab/Ctrl+P/Ctrl+X/Ctrl+T branches with `return m, nil` if deleting them wholesale would pull Task 5 work forward, or simply delete those branches now (preferred; they are dead once `activeTab` is gone).

3. Run `grep -n 'activeTab\|leftWidth\|rightWidth\|chatHeight\|contentHeight\|stateStrip\|renderPanel\|renderStatusBar\|renderKeyHelp' internal/app/tui/*.go` — must return nothing outside tests.

4. Prune `model_test.go` — delete these obsolete tests (their subjects no longer exist): `TestPolishedViewContainsCurrentLayoutChrome`, `TestPolishedStatusBarShowsRouteWhenActive`, `TestPolishedRightPanelTracksActiveTab`, `TestPolishedViewFitsCommonTerminalSizes`, `TestPolishedTranscriptReflowsAfterResize`, `TestViewShowsProviderErrorWhenSet`, `TestViewOmitsProviderErrorSectionByDefault`, `TestModelLayoutStateInit`, `TestFocusAndTabNavigation` (rewritten in Task 5), `TestResizeComputesGeometry`, `TestAltScreenViewLayout`, `TestAltScreenViewFits80x24`, `TestStatusBarFitsTerminalWidth`, `TestViewFitsTerminalSizes`, `TestProviderErrorVisibleInAltScreen`, `TestPolishedSidebarTabsAndContextSummary`, `TestRenderSidebarTabsSingleRowAcrossActiveIndex`, `TestPolishedApprovalStateShowsCommandReasonRiskAndActions`, `TestPolishedProviderErrorUsesCompactBanner`, `TestPolishedProviderErrorBannerFitsCommonTerminalSizes`, `TestStatusBarShowsSpinnerAndThinkingWhenBusy`, `TestStatusBarShowsToolLabel`, `TestStatusBarShowsDoneBadgeAfterActivity`, `TestStatusBarDoneBadgeExpiresAfterDuration`, `TestPlanTabShowsPlanItemsAndSpinner`, `TestPlanTabShowsNoActivePlanWhenIdleAndEmpty`, `TestPolishedCurrentLayoutFullSurface`, `TestStateStripShowsThinking`, `TestStateStripShowsApproval`, `TestStateStripHiddenWhenIdle`. Adapt (keep, fix assertions/geometry references): `TestPolishedViewPreservesPendingApprovalContent` (approval text now inline in viewport — assert `View()` contains the command), `TestChatMessagesWrapWithinViewportWidth`, `TestApprovalBannerHasSingleBorder`, `TestTUIApprovalBannerAndKeypresses`, `TestThinkingBoxRendersWhileStreaming`, and any test calling `m.resize(...)` then asserting on removed fields.

- [ ] **Step 5: Run the full suite**

Run: `go test -race ./internal/app/tui/ -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 6: Smoke-run the TUI**

Run: `CGO_ENABLED=1 go run ./cmd/marshal` in a scratch directory (or this repo). Verify by eye: single column, bordered input at bottom, status line below it, no sidebar/top bar. Type a message (provider may error — the error must appear inline, not replace the screen). Quit with Ctrl+C.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/app/tui && go vet ./internal/app/tui/
git add internal/app/tui/view.go internal/app/tui/view_test.go internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): single-column layout — remove sidebar, top bar, state strip, old status bar"
```

---

### Task 5: Key handling and focus simplification

**Files:**
- Modify: `internal/app/tui/model.go` (the `tea.KeyMsg` handling in `Update`, `dispatchCommand`'s `stop` case)
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: everything already in place; adds helper `(m *Model) cancelTurn() bool` used by both Esc and `/stop`.
- Behavior contract after this task:
  - Input is **always focused**; the `inputFocused` field, `m.input.Blur()` paths outside approval-editing, and the whole unfocused key branch are deleted.
  - Esc: cancels an in-flight agent turn (like `/stop`); no-op when idle. (During approval, Esc still denies — that branch is untouched.)
  - Ctrl+C: quits (unchanged). The unfocused-Esc quit path is gone.
  - PgUp/PgDown scroll the transcript viewport; mouse messages route to the viewport.
  - Removed keys: Tab/Shift+Tab cycling, Ctrl+P/Ctrl+X/Ctrl+T tab hotkeys, `1`/`2`/`3` tab digits, unfocused `r` rollback, unfocused Enter-to-focus. Kept: Ctrl+O settings, Ctrl+K memory, Ctrl+G thinking toggle, Ctrl+R rollback (works while typing, no conflict), approval keys Enter/d/e/a/`r`/Esc.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/tui/model_test.go` (reuse its existing fake-runner/state helpers; the file already has a pattern for dispatching `tea.KeyMsg`):

```go
func TestEscCancelsInFlightTurn(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)
	m.busy = true
	cancelled := false
	m.agentCancel = func() { cancelled = true }

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)

	if !cancelled {
		t.Fatal("Esc should cancel the in-flight agent turn")
	}
	if m.agentCancel != nil {
		t.Fatal("agentCancel should be cleared after Esc")
	}
}

func TestEscWhenIdleDoesNotQuit(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("Esc when idle must be a no-op (no quit command)")
	}
	select {
	case <-state.Done():
		t.Fatal("Esc must not shut the session down")
	default:
	}
}

func TestTypingIsAlwaysCaptured(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	// Digits used to switch sidebar tabs; now they must reach the input.
	for _, r := range "123r" {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	if got := m.input.Value(); got != "123r" {
		t.Fatalf("input value = %q, want %q", got, "123r")
	}
}

func TestPageKeysScrollViewport(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 100; i++ {
		state.AddMessage(session.RoleUser, fmt.Sprintf("message %d", i), session.ContentTypePlain)
	}
	m := New(state)
	m.resize(80, 24)
	m.refreshViewport() // GotoBottom
	bottom := m.viewport.YOffset

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(Model)
	if m.viewport.YOffset >= bottom {
		t.Fatalf("PgUp did not scroll up: offset %d -> %d", bottom, m.viewport.YOffset)
	}
	if m.input.Value() != "" {
		t.Fatalf("PgUp leaked into the input: %q", m.input.Value())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestEscCancels|TestEscWhenIdle|TestTypingIsAlways|TestPageKeysScroll' -v`
Expected: FAIL — Esc currently blurs the input (no cancel); digits currently switch tabs when unfocused (this test may pass if focused — verify at least Esc tests fail); PgUp currently reaches the input, not the viewport.

- [ ] **Step 3: Implement**

In `model.go`:

1. Add the helper (near `dispatchCommand`):

```go
// cancelTurn cancels the in-flight agent turn, if any. Shared by Esc and
// the /stop command.
func (m *Model) cancelTurn() bool {
	if m.agentCancel == nil {
		return false
	}
	m.agentCancel()
	m.agentCancel = nil
	m.state.AddMessage(session.RoleSystem, "Agent turn cancelled.", session.ContentTypePlain)
	m.refreshViewport()
	return true
}
```

Update `dispatchCommand`'s `stop` case to use it:

```go
	case "stop":
		if !m.cancelTurn() {
			m.refreshViewport()
		}
		return m, nil
```

2. Rewrite the non-approval, non-overlay `tea.KeyMsg` handling. After the existing overlay and `tc != nil` approval branches, the `else` branch (currently split into focused/unfocused) becomes a single block:

```go
			// Global hotkeys — input is always focused.
			switch msg.Type {
			case tea.KeyEsc:
				m.cancelTurn()
				return m, nil
			case tea.KeyCtrlO:
				m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
				m.settingsModel.SetSize(m.width, m.height)
				m.settingsOpen = true
				return m, nil
			case tea.KeyCtrlK:
				if m.memoryDB == nil {
					return m, nil
				}
				m.memoryModel = memory.New(m.memoryDB, m.memoryProject)
				m.memoryModel.SetSize(m.width, m.height)
				m.memoryOpen = true
				return m, nil
			case tea.KeyCtrlG:
				m.thinkingExpanded = !m.thinkingExpanded
				m.lastMessageCount = -1
				m.refreshViewport()
				return m, nil
			case tea.KeyCtrlR:
				if m.state.HasBackup() {
					_ = m.state.RollbackBackup()
					m.state.LogToolCall(registry.AuditEvent{
						Timestamp:     time.Now(),
						ToolName:      "rollback",
						ResultSummary: "Rollback applied successfully",
					})
					m.refreshViewport()
				}
				return m, nil
			case tea.KeyPgUp, tea.KeyPgDown:
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(msg)
				return m, vpCmd
			case tea.KeyEnter:
				value := strings.TrimSpace(m.input.Value())
				if value == "" {
					return m, nil
				}
				m.input.Reset()
				if strings.HasPrefix(value, "/") {
					return m.dispatchCommand(value)
				}
				if m.busy {
					return m, nil
				}
				if m.runner == nil {
					m.state.AddMessage(session.RoleUser, value, session.ContentTypePlain)
					m.refreshViewport()
					return m, nil
				}
				m.busy = true
				agentCtx, cancel := context.WithCancel(m.ctx)
				m.agentCancel = cancel
				return m, tea.Batch(runAgentCmd(agentCtx, m.runner, value), tickCmd())
			}
			// Everything else is typing.
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
```

Also add a `tea.MouseMsg` case to the top-level `switch msg := msg.(type)` so wheel scrolling reaches the viewport:

```go
	case tea.MouseMsg:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd
```

3. Delete: the `inputFocused` Model field and every reference; the whole former unfocused branch (Esc-quit, Enter-focus, `1/2/3`, bare `r` rollback, viewport passthrough); Tab/Shift+Tab and Ctrl+P/Ctrl+X/Ctrl+T branches (if not already removed in Task 4); the `m.input.Blur()` / `m.inputFocused = false` lines inside the approval-editing Esc/Enter cases (keep the input focused; just `Reset()` and restore the placeholder).

4. Run `grep -n 'inputFocused' internal/app/tui/` — must return nothing.

5. Prune obsolete key tests from `model_test.go`: `TestQuitKeyRequestsShutdown` (was Esc-quit — rewrite it to assert **Ctrl+C** quits, keeping the name or renaming to `TestCtrlCQuits`), `TestTUIRollbackFlow` (used unfocused `r` — rewrite to use Ctrl+R). `TestGlobalKeysDoNotLeakDuringApproval` and `TestEscDuringApprovalDenies` must still pass unchanged.

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./internal/app/tui/ -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui && go vet ./internal/app/tui/
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): always-focused input, Esc cancels turn, page keys scroll transcript"
```

---

### Task 6: Docs and final verification

**Files:**
- Modify: `docs/06-tui-design.md` (supersession note)
- Modify: `docs/superpowers/specs/2026-07-05-tui-simplification-design.md` (status → Implemented)

- [ ] **Step 1: Add a supersession note to docs/06-tui-design.md**

At the top of `docs/06-tui-design.md`, immediately after the `# 06. ...` heading, insert:

```markdown
> **2026-07-05:** The dashboard layout described below is superseded by the
> single-column transcript design in
> `docs/superpowers/specs/2026-07-05-tui-simplification-design.md`
> (borderless transcript, symbol-bullet messages, one status line,
> `/context` + `/log` instead of the sidebar).
```

Change the spec's `**Status**: Approved` line to `**Status**: Implemented`.

- [ ] **Step 2: Full verification**

Run each; all must pass:

```bash
gofmt -l .            # expect: no output
go vet ./...          # expect: no output
CGO_ENABLED=1 go build ./cmd/marshal
go test ./...
go test -race ./internal/app/tui/ ./internal/commands/
```

- [ ] **Step 3: Final smoke-run**

Run `CGO_ENABLED=1 go run ./cmd/marshal` and walk the spec's surface: submit a message, `/help`, `/context`, `/log`, Ctrl+O settings opens/closes, Ctrl+K memory opens/closes, Esc during a running turn cancels it, resize the terminal window (layout must reflow), Ctrl+C quits.

- [ ] **Step 4: Commit**

```bash
git add docs/06-tui-design.md docs/superpowers/specs/2026-07-05-tui-simplification-design.md
git commit -m "docs: mark TUI simplification implemented, supersede dashboard layout doc"
```

---

## Self-review notes

- **Spec coverage:** Layout/chrome removal → Task 4; symbol-bullet transcript + plan block + diff/tool restyle + live-block restyle → Task 2; status line → Task 3; `/context`+`/log` → Task 1; key/focus changes (always-focused input, Esc-cancel, removed tab keys, `r` removal, PgUp/PgDn scrolling) → Task 5; inline provider error → Tasks 2 (renderer) + 4 (wiring); file split (`model.go`/`view.go`/`transcript.go`/`status.go`) → Tasks 2-4; docs → Task 6.
- **Spec deviations (deliberate, small):** Ctrl+R rollback is kept (the spec removes only the bare `r` key, which conflicts with typing; Ctrl+R does not). The approval-branch `r` rollback key is kept — it belongs to the inline-approval spec's keyset and only applies while an approval is pending.
- **Known-fragile points for the executor:** `model_test.go` is 2,162 lines; the deletion lists in Tasks 2/4/5 name every test to remove — if a named test doesn't exist verbatim, find its renamed equivalent by grepping the asserted substrings. `contextpack` literal field names in Task 1/3 tests should be checked against `internal/contextpack` before running.
