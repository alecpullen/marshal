# TUI Single-Column Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the Marshal TUI as a clean single stacked column with no background fills — borderless transcript, one bordered input box, a borderless status row — using a warm-sunset palette and universal-Unicode iconography.

**Architecture:** Purely presentational refactor of `internal/app/tui`. Every panel is styled by border + foreground color only; the terminal background shows through. This deletes the `fillRowsToWidth` padding machinery (and the whole class of "fill doesn't reach the border" bugs) and the panel-background color variables. No model logic, `Update`, `session.State`, or key handling changes.

**Tech Stack:** Go, Bubble Tea, Lipgloss (`github.com/charmbracelet/lipgloss`), `github.com/charmbracelet/x/ansi`. Tests are standard `go test` in-package (`package tui`).

## Global Constraints

- Build requires `CGO_ENABLED=1` (tree-sitter dep). Build command: `CGO_ENABLED=1 go build ./cmd/marshal`.
- No background fills anywhere in the TUI: no `Background(...)` on panels, boxes, the textarea, the status row, or the swarm panel.
- Icons are universal Unicode only (no Nerd Font): `● ⚙ ✔ ✘ ⚠ ❯ › ▸ ▾ ─ ·`.
- Palette (256-color codes), exact values:
  - coral `209` — marshal, focused input border, `❯` prompt
  - gold `214` — tool calls
  - teal `43` — success
  - orange `172` — warning / risk
  - red `203` — error
  - mauve `245` — blurred input border
  - warm gray `246` — user (`›`)
  - dim gray `244` — meta / separators
- Run `gofmt -w .` and `go vet ./...` before the final commit.
- Format each commit message as `style(tui): ...` or `refactor(tui): ...`.

---

## File Structure

- `internal/app/tui/model.go` — palette/style variable block (rewrite), textarea style setup in `New()` (remove backgrounds), layout row constants + resize geometry.
- `internal/app/tui/view.go` — remove fill machinery; borderless transcript + header; input box without background.
- `internal/app/tui/transcript.go` — per-message icon/role rendering, header banner, approval panel (drop panel backgrounds).
- `internal/app/tui/status.go` — status row without background fill; new colors.
- `internal/app/tui/swarm_panel.go` — borderless swarm panel.
- Test files updated alongside each: `view_test.go`, `status_test.go`, `transcript_test.go`, `swarm_panel_test.go`, `model_test.go`.

**Note on compile-safety:** Go does not error on unused *package-level* variables. Early tasks retune/keep the old background vars (`panelBgColor`, `statusBarBgColor`, `panelBorderColor`, `inputFillStyle`, `transcriptFrameStyle`) defined so the package always compiles; Task 6 deletes whatever is then unused.

---

### Task 1: Warm-sunset palette & header banner

**Files:**
- Modify: `internal/app/tui/model.go:790-855` (color/style var block), `internal/app/tui/model.go:149-162` (textarea styles in `New()`)
- Modify: `internal/app/tui/transcript.go:530-535` (`renderWelcomeBanner`)
- Test: `internal/app/tui/transcript_test.go`

**Interfaces:**
- Produces color vars consumed by every later task: `coralColor`, `goldColor`, `tealColor`, `mauveColor`, `userColor` (all `lipgloss.Color`); retuned `accentColor=209`, `successColor=43`, `warningColor=172`, `errorColor=203`, `dimColor=244` (unchanged).
- Produces `renderWelcomeBanner(width int) string` → header line beginning with a coral `●` then `marshal`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/transcript_test.go`:

```go
func TestWelcomeBannerHasCoralDotAndName(t *testing.T) {
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestWelcomeBannerHasCoralDotAndName -v`
Expected: FAIL (banner currently renders `marshal` in `accentColor` 38, no `●`).

- [ ] **Step 3: Rewrite the palette/style block**

In `internal/app/tui/model.go`, replace the `var (` block that starts at line 790 (`panelBorderColor = ...`) through `inputFillStyle = ...` with:

```go
var (
	// Warm Sunset palette (256-color).
	coralColor  = lipgloss.Color("209") // marshal, focused border, prompt
	goldColor   = lipgloss.Color("214") // tool calls
	tealColor   = lipgloss.Color("43")  // success
	orangeColor = lipgloss.Color("172") // warning / risk
	mauveColor  = lipgloss.Color("245") // blurred border
	userColor   = lipgloss.Color("246") // user prompt

	// accentColor is the primary accent (coral). Retained name because it is
	// referenced widely; successColor/warningColor/errorColor are retuned to
	// the warm palette.
	accentColor  = coralColor
	violetColor  = lipgloss.Color("175") // markdown headings (warm magenta)
	dimColor     = lipgloss.Color("244")
	successColor = tealColor
	warningColor = orangeColor
	errorColor   = lipgloss.Color("203")

	// Legacy background vars: retained (unused after later tasks) so the
	// package keeps compiling during the incremental refactor. Removed in the
	// cleanup task once nothing references them.
	panelBorderColor = lipgloss.Color("240")
	panelBgColor     = lipgloss.Color("235")
	statusBarBgColor = lipgloss.Color("237")

	mutedStyle      = lipgloss.NewStyle().Foreground(dimColor)
	panelTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")).
			Bold(true)
	thinkingLineStyle = lipgloss.NewStyle().
				Foreground(dimColor).
				Italic(true)
	inputPromptStyle = lipgloss.NewStyle().
				Foreground(coralColor).
				Bold(true)

	codeBorderStyle = lipgloss.NewStyle().
			Foreground(dimColor).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dimColor)
	toolNameStyle = lipgloss.NewStyle().
			Foreground(goldColor)
	keyHintStyle = lipgloss.NewStyle().
			Foreground(coralColor).
			Bold(true)
	riskLabelStyle = lipgloss.NewStyle().
			Foreground(warningColor).
			Bold(true)
	dimSeparator = " · "

	transcriptFrameStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(panelBorderColor)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(coralColor).
			Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	inputFillStyle = lipgloss.NewStyle()
)
```

- [ ] **Step 4: Remove textarea backgrounds in `New()`**

In `internal/app/tui/model.go`, replace lines 149-162 with:

```go
	input.FocusedStyle.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	input.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(dimColor)
	input.BlurredStyle.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	input.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(dimColor)

	// Cursor lives on the embedded cursor.Model. Style paints the visible
	// (reverse) block; TextStyle paints the cell mid-blink. Foreground only —
	// no panel background to bleed.
	input.Cursor.Style = lipgloss.NewStyle().Foreground(coralColor)
	input.Cursor.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
```

- [ ] **Step 5: Update the header banner**

In `internal/app/tui/transcript.go`, replace `renderWelcomeBanner` (lines 530-535) with:

```go
func renderWelcomeBanner(width int) string {
	dot := lipgloss.NewStyle().Foreground(coralColor).Render("●")
	title := lipgloss.NewStyle().Foreground(coralColor).Bold(true).Render("marshal")
	desc := mutedStyle.Render("local-first coding agent")
	return "  " + dot + " " + title + dimSeparator + desc + "\n\n"
}
```

- [ ] **Step 6: Run the test and the package build**

Run: `go test ./internal/app/tui/ -run TestWelcomeBannerHasCoralDotAndName -v`
Expected: PASS
Run: `CGO_ENABLED=1 go build ./...`
Expected: builds (legacy bg vars still defined; some tests may now fail — fixed in later tasks).

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/model.go internal/app/tui/transcript.go internal/app/tui/transcript_test.go
git commit -m "style(tui): warm-sunset palette and icon header banner"
```

---

### Task 2: Delete fill machinery, borderless-background input box

**Files:**
- Modify: `internal/app/tui/view.go:55-90` (`renderInputArea`), `:92-139` (activity strip / suggestions), `:153-186` (remove `contentWidth`, `fillRowsToWidth`)
- Modify: `internal/app/tui/model.go` — `inputBoxStyle` border color by focus (done via render site)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: `inputBoxStyle`, `coralColor`, `mauveColor`, `inputPromptStyle` (Task 1).
- Produces: `renderInputArea()` returns the input box with **no** trailing background fill; helpers `fillRowsToWidth`/`contentWidth` no longer exist.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/view_test.go`:

```go
func TestInputAreaHasNoBackgroundFill(t *testing.T) {
	m := newViewTestModel(t, 60, 20)
	out := m.renderInputArea()
	// panelBg 235 must never be emitted as a fill anymore.
	if strings.Contains(out, "48;5;235") || strings.Contains(out, ";235m") {
		t.Fatalf("input area still emits panel background fill:\n%q", out)
	}
	if !strings.Contains(stripANSI(out), "❯") {
		t.Fatalf("input area missing prompt:\n%q", stripANSI(out))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestInputAreaHasNoBackgroundFill -v`
Expected: FAIL (current `fillRowsToWidth` + `inputFillStyle`/`panelBgColor` emit `235` fill).

- [ ] **Step 3: Rewrite `renderInputArea` and helpers**

In `internal/app/tui/view.go`, replace `renderInputArea` (lines 55-90) with:

```go
func (m Model) renderInputArea() string {
	inputInnerWidth := max(m.width-4, 1)

	rows := make([]string, 0, 4)

	if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			editLine := lipgloss.JoinHorizontal(
				lipgloss.Top,
				inputPromptStyle.Render("❯ "),
				m.input.View(),
			)
			rows = append(rows, editLine)
		} else {
			rows = append(rows, renderApprovalPanel(tc, inputInnerWidth))
		}
	} else {
		rows = append(rows, m.renderActivityStrip())
		if len(m.commandSuggestions) > 0 {
			rows = append(rows, m.renderCommandSuggestions())
		}
		inputLine := lipgloss.JoinHorizontal(
			lipgloss.Top,
			inputPromptStyle.Render("❯ "),
			m.input.View(),
		)
		rows = append(rows, inputLine)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	border := coralColor
	if !m.input.Focused() {
		border = mauveColor
	}
	return inputBoxStyle.Copy().BorderForeground(border).Width(inputInnerWidth).Render(content)
}
```

- [ ] **Step 4: Delete the fill helpers and simplify strips**

In `internal/app/tui/view.go`:
- Delete `contentWidth` (lines 153-159) and `fillRowsToWidth` (lines 161-186) entirely.
- In `renderActivityStrip` (lines 92-110), replace the returned style block (the `lipgloss.NewStyle().Width(available).Background(panelBgColor).Render(...)`) with:

```go
	return statusBusyStyle.Render(truncateRunes(label, available))
```

- In `renderCommandSuggestions` (lines 112-139), remove `.Background(panelBgColor)` from both the selected/unselected item styling and the final wrapping style. The selected item becomes `promptPrefixStyle.Render(item)` and the unselected becomes `mutedStyle.Render(item)`; return `line` directly:

```go
	for i, cmd := range m.commandSuggestions {
		name := "/" + cmd.Name
		if cmd.Args != "" {
			name += " " + cmd.Args
		}
		item := name
		if cmd.Description != "" {
			item += " - " + cmd.Description
		}
		item = truncateRunes(item, itemWidth)
		if i == m.commandSuggestionIndex {
			item = promptPrefixStyle.Render(item)
		} else {
			item = mutedStyle.Render(item)
		}
		parts = append(parts, item)
	}
	return strings.Join(parts, "  ")
```

- [ ] **Step 5: Run the test and package tests**

Run: `go test ./internal/app/tui/ -run TestInputAreaHasNoBackgroundFill -v`
Expected: PASS
Run: `CGO_ENABLED=1 go build ./...`
Expected: builds (the `ansi` import in `view.go` may now be unused — if the build reports `"github.com/charmbracelet/x/ansi" imported and not used`, remove that import line from `view.go`).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/view.go internal/app/tui/view_test.go
git commit -m "refactor(tui): drop input-box background fill machinery"
```

---

### Task 3: Borderless transcript + header line, updated geometry

**Files:**
- Modify: `internal/app/tui/view.go:21-53` (row constants, `renderTranscriptFrame`)
- Modify: `internal/app/tui/model.go:198-204` (resize geometry), `:500-507` (`updateViewportHeight`)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: viewport rendering (unchanged), `renderWelcomeBanner` (Task 1).
- Produces: `renderTranscriptFrame()` returns the plain viewport view with **no** rounded border; `transcriptFrameRows = 0`; `m.viewport.Width = m.width - 2`.

- [ ] **Step 1: Update the failing tests**

In `internal/app/tui/view_test.go`, replace `TestTranscriptHasSubtleFrame` (lines 47-56) with:

```go
func TestTranscriptIsBorderless(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()
	transcript := m.renderTranscriptFrame()
	if strings.Contains(transcript, "╭") || strings.Contains(transcript, "╰") {
		t.Fatalf("transcript should have no rounded border:\n%s", transcript)
	}
}
```

And update `TestResizeComputesSingleColumnGeometry` (lines 125-134): change the expected viewport width from `96` to `98` and its message:

```go
	if m.viewport.Width != 98 {
		t.Fatalf("viewport.Width = %d, want 98 (width-2, borderless transcript)", m.viewport.Width)
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestTranscriptIsBorderless|TestResizeComputesSingleColumnGeometry' -v`
Expected: FAIL (transcript still bordered; width still 96).

- [ ] **Step 3: Make the transcript borderless**

In `internal/app/tui/view.go`, change the row constants block (lines 21-27) so `transcriptFrameRows` is 0:

```go
const (
	inputBorderRows       = 2
	activityStripRows     = 1
	commandSuggestionRows = 1
	transcriptFrameRows   = 0
	statusLineRows        = 1
)
```

Replace `renderTranscriptFrame` (lines 48-53) with:

```go
func (m Model) renderTranscriptFrame() string {
	return lipgloss.NewStyle().
		Width(max(m.width-2, 1)).
		Height(max(m.viewport.Height, 1)).
		Render(m.viewport.View())
}
```

- [ ] **Step 4: Update resize geometry**

In `internal/app/tui/model.go`, change line 199 from `max(width-4, 1)` to:

```go
	m.viewport.Width = max(width-2, 1)
```

(Lines 200 and 501 already subtract `transcriptFrameRows`, which is now 0, so no other change is needed there.)

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/app/tui/ -run 'TestTranscriptIsBorderless|TestResizeComputesSingleColumnGeometry|TestViewFitsTerminalSizesSingleColumn' -v`
Expected: PASS
Run: `CGO_ENABLED=1 go build ./...`
Expected: builds.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/view.go internal/app/tui/view_test.go internal/app/tui/model.go
git commit -m "refactor(tui): borderless transcript scrollback with header line"
```

---

### Task 4: Transcript message icons & colors, approval panel de-fill

**Files:**
- Modify: `internal/app/tui/transcript.go` — `renderUserMessage` (243-258), `toolBulletStyle` (334), `renderCompletedToolCall` (448-468), `renderProviderError` (412-424), `renderApprovalPanel` (495-528)
- Test: `internal/app/tui/transcript_test.go`

**Interfaces:**
- Consumes: `coralColor`, `goldColor`, `tealColor`, `errorColor`, `userColor` (Task 1).
- Produces: user lines prefixed `›` in warm gray; tool bullets gold; completed tool `✔`/`✘`; provider error `✘`; approval panel with no `panelBgColor` backgrounds.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/tui/transcript_test.go`:

```go
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
	tc := &session.PendingToolCall{Name: "shell.run", Command: "ls", Risk: "reads files"}
	out := renderApprovalPanel(tc, 50)
	if strings.Contains(out, ";235m") || strings.Contains(out, "48;5;235") {
		t.Fatalf("approval panel still emits panel background fill:\n%q", out)
	}
}
```

Ensure the test file imports `marshal/internal/tools/registry` and `marshal/internal/app/session` (add if missing).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestUserMessageUsesChevronPrefix|TestCompletedToolCallUsesCheckAndCross|TestApprovalPanelHasNoBackgroundFill' -v`
Expected: FAIL (`❯` prefix, `✓`/`failed` text, and `235` backgrounds still present).

- [ ] **Step 3: Update user message prefix**

In `internal/app/tui/transcript.go`, in `renderUserMessage` (lines 243-258) change the first-line prefix from the `❯` prompt to a warm-gray `›`. Replace the loop body's prefix branch:

```go
	userPrefix := lipgloss.NewStyle().Foreground(userColor).Bold(true).Render("› ")
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(userPrefix)
		} else {
			b.WriteString("  ")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
```

- [ ] **Step 4: Update tool bullet + completed tool glyphs**

In `internal/app/tui/transcript.go`:
- Change `toolBulletStyle` (line 334) to gold:

```go
var toolBulletStyle = lipgloss.NewStyle().Foreground(goldColor)
```

- In `renderCompletedToolCall` (lines 448-468), replace the head-building lines so success uses `✔` (teal) and failure `✘` (red):

```go
	glyph := "✔"
	style := statusOkStyle
	state := "done"
	if event.Error != "" {
		glyph = "✘"
		style = statusErrStyle
		state = "failed"
	}
	head := fmt.Sprintf("%s %s %s", glyph, event.ToolName, state)
```

- [ ] **Step 5: Update provider-error glyph**

In `renderProviderError` (lines 414-424), change the prefix from `✗` to `✘`:

```go
	wrapped := ansi.Wrap("✘ provider: "+err.Error(), contentWidth, "")
```

Then update the assertion string in `view_test.go`'s `TestProviderErrorShowsInlineNotFullScreen` (line 117) from `"✗ provider: connection refused"` to `"✘ provider: connection refused"`.

- [ ] **Step 6: De-fill the approval panel**

In `internal/app/tui/transcript.go`, rewrite `renderApprovalPanel` (lines 495-528) removing every `.Background(panelBgColor)` / `.Copy().Background(panelBgColor)`:

```go
func renderApprovalPanel(tc *session.PendingToolCall, width int) string {
	innerWidth := max(width-2, 1)

	titleStyle := panelTitleStyle.Copy().Foreground(warningColor)
	muted := mutedStyle
	text := lipgloss.NewStyle()
	key := keyHintStyle

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚠ Approval needed"))
	b.WriteString("\n")

	if tc.Name == "shell.run" {
		b.WriteString(muted.Render("Agent wants to run:"))
		b.WriteString("\n")
		b.WriteString(text.Render(truncateRunes(tc.Command, innerWidth)))
	} else {
		b.WriteString(muted.Render("Agent wants to call tool: ") + toolNameStyle.Render(tc.Name))
		b.WriteString("\n")
		if tc.Schema != "" {
			b.WriteString(muted.Render("Description: ") + text.Render(truncateRunes(tc.Schema, innerWidth)))
			b.WriteString("\n")
		}
		b.WriteString(muted.Render("Arguments: "))
		b.WriteString(text.Render(truncateRunes(tc.Args, innerWidth)))
	}
	b.WriteString("\n\n")
	b.WriteString(riskLabelStyle.Render("Risk: "))
	b.WriteString(text.Render(truncateRunes(riskText(tc), innerWidth)))
	b.WriteString("\n\n")
	helpLine := key.Render("Enter") + muted.Render(" approve ") + key.Render("d") + muted.Render(" deny ") + key.Render("e") + muted.Render(" edit ") + key.Render("a") + muted.Render(" always")
	b.WriteString(helpLine)
	return b.String()
}
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/app/tui/ -run 'TestUserMessageUsesChevronPrefix|TestCompletedToolCallUsesCheckAndCross|TestApprovalPanelHasNoBackgroundFill|TestProviderErrorShowsInlineNotFullScreen' -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/transcript.go internal/app/tui/transcript_test.go internal/app/tui/view_test.go
git commit -m "style(tui): icon-prefixed transcript roles and de-filled approval panel"
```

---

### Task 5: Status row & swarm panel — borderless, warm colors

**Files:**
- Modify: `internal/app/tui/status.go:25-38` (`renderStatusLine`), `:79-82` (status styles)
- Modify: `internal/app/tui/swarm_panel.go:25-53` (`renderSwarmPanel`)
- Test: `internal/app/tui/status_test.go`, `internal/app/tui/swarm_panel_test.go`

**Interfaces:**
- Consumes: `statusBarStyle` (now background-free, Task 1), `coralColor`, `tealColor`, `warningColor`, `errorColor`.
- Produces: status row rendered with foreground styling only (no `statusBarBgColor`); swarm panel rendered with no `inputBoxStyle` border/background.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/tui/status_test.go`:

```go
func TestStatusLineHasNoBackgroundFill(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
	out := m.renderStatusLine(80)
	if strings.Contains(out, "48;5;237") || strings.Contains(out, ";237m") {
		t.Fatalf("status line still emits statusBar background fill:\n%q", out)
	}
	if !strings.Contains(stripANSI(out), "qwen @ ollama") {
		t.Fatalf("status line missing route:\n%q", stripANSI(out))
	}
}
```

Add to `internal/app/tui/swarm_panel_test.go`:

```go
func TestSwarmPanelIsBorderless(t *testing.T) {
	p := session.SwarmProgress{Active: true, Goal: "build", Roles: []session.SwarmRole{{Name: "coder", Status: session.SwarmRoleActive}}}
	out := renderSwarmPanel(p, "⠋", 60)
	if strings.Contains(out, "╭") || strings.Contains(out, "╰") {
		t.Fatalf("swarm panel should have no border:\n%s", out)
	}
}
```

(If `newViewTestModel` is defined only in `view_test.go`, it is in-package and usable from `status_test.go` directly. Confirm `status_test.go` imports `strings` and `marshal/internal/app/session`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestStatusLineHasNoBackgroundFill|TestSwarmPanelIsBorderless' -v`
Expected: FAIL (status still fills `237`; swarm panel still uses `inputBoxStyle` border).

- [ ] **Step 3: Confirm status styles are background-free**

In `internal/app/tui/status.go`, the busy/ok/warn/err styles (lines 79-82) are already foreground-only — leave them. `statusBarStyle` lost its background in Task 1, so `renderStatusLine` (lines 25-38) needs no structural change. Verify line 37 still reads:

```go
	return statusBarStyle.Width(max(width, 1)).MaxWidth(max(width, 1)).Render(ansi.Cut(line, 0, width))
```

No edit required here unless the test still sees `237` — if so, grep for a stray `Background(statusBarBgColor)` and remove it.

- [ ] **Step 4: Make the swarm panel borderless**

In `internal/app/tui/swarm_panel.go`, change the final return (line 52) from the `inputBoxStyle`-wrapped render to a plain indented block:

```go
	return indentBlock(b.String(), "  ")
```

(`indentBlock` is defined in `transcript.go`, same package.)

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/app/tui/ -run 'TestStatusLineHasNoBackgroundFill|TestSwarmPanelIsBorderless' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/status.go internal/app/tui/status_test.go internal/app/tui/swarm_panel.go internal/app/tui/swarm_panel_test.go
git commit -m "style(tui): borderless status row and swarm panel"
```

---

### Task 6: Remove dead background vars & full verification

**Files:**
- Modify: `internal/app/tui/model.go` (delete now-unused vars), plus any file still referencing them
- Test: entire `./internal/app/tui/` suite + full build

**Interfaces:**
- Consumes: everything above.
- Produces: no references to `panelBgColor`, `statusBarBgColor`, `panelBorderColor`, `inputFillStyle`, `transcriptFrameStyle` (delete whichever are unused).

- [ ] **Step 1: Find remaining references**

Run: `grep -rn "panelBgColor\|statusBarBgColor\|panelBorderColor\|inputFillStyle\|transcriptFrameStyle" internal/app/tui/`
Expected: only the `var` definitions remain (no usages in non-test code). If a usage remains, replace it with the borderless/foreground-only equivalent before deleting.

- [ ] **Step 2: Delete the dead variable definitions**

In `internal/app/tui/model.go`, remove the definitions of `panelBorderColor`, `panelBgColor`, `statusBarBgColor`, `inputFillStyle`, and `transcriptFrameStyle` from the `var (` block (added/retained in Task 1). Keep `mutedStyle`, `codeBorderStyle`, etc.

- [ ] **Step 3: Build and vet**

Run: `CGO_ENABLED=1 go build ./...`
Expected: builds clean (no "declared and not used" — package-level vars are exempt, but we removed them anyway).
Run: `go vet ./...`
Expected: no errors.

- [ ] **Step 4: Run the full package test suite**

Run: `go test ./internal/app/tui/...`
Expected: PASS. If a legacy test still asserts old chrome (e.g. a `panelBgColor` background or the transcript border), update its assertion to the borderless/foreground-only expectation and re-run.

- [ ] **Step 5: Run the whole repo test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Manual smoke check (optional but recommended)**

Run: `CGO_ENABLED=1 go run ./cmd/marshal` in a real terminal; confirm: coral `●` header, borderless transcript, single coral-bordered input box that fills evenly to the border, borderless dim status row. Quit with the app's normal exit key.

- [ ] **Step 7: Commit**

```bash
gofmt -w .
git add -A internal/app/tui/
git commit -m "refactor(tui): remove dead panel-background variables"
```

---

## Self-Review

**Spec coverage:**
- Borders & text only, no fills → Tasks 1 (styles), 2 (input), 4 (approval), 5 (status/swarm), 6 (cleanup). ✓
- Borderless transcript scrollback + header line → Task 3 + Task 1 banner. ✓
- Warm Sunset palette (exact codes) → Task 1 palette block. ✓
- Universal Unicode icons (● ⚙ ✔ ✘ ⚠ ❯ ›) → Task 1 (●), Task 4 (› ✔ ✘), existing ⚙/⚠ retained. ✓
- Input is the only bordered box, coral focused / mauve blurred → Task 2. ✓
- Status row: left cluster + right activity, no fill → Task 5 (structure in `status.go` unchanged; fill removed in Task 1). ✓
- Swarm folds token count / borderless → Task 5. ✓
- Tests updated (view/status/transcript/swarm/model) → each task updates its test file; Task 6 sweeps stragglers. ✓
- Non-goals (no logic/provider/tool changes, no Nerd Font) → respected; all tasks are render-only. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code. ✓

**Type consistency:** `coralColor/goldColor/tealColor/mauveColor/userColor` defined in Task 1 and consumed by name in Tasks 2, 4, 5. `renderWelcomeBanner`, `renderInputArea`, `renderTranscriptFrame`, `renderCompletedToolCall`, `renderApprovalPanel`, `renderSwarmPanel` signatures unchanged. `transcriptFrameRows` retyped to 0 (const int) — consistent with its uses at model.go:200,501. `indentBlock(string, string) string` reused in Task 5 from transcript.go. ✓
