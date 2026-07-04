# Polished Current TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Polish the current Marshal TUI two-panel layout so it looks and feels as close as practical to the high-quality browser mockup in `docs/mockups/tui-redesign.html`, while keeping the current Chat + right informational panel architecture.

**Architecture:** Keep the existing Bubble Tea model and current 70/30 two-column layout. Extract a small visual system for colors, borders, panel chrome, status bar, and truncation; then replace the ad hoc `View()` composition with focused render helpers for shell, chat, sidebar, approval, provider error, and status. Do not implement the Phase 2 grid in this plan.

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbletea`), Bubbles (`github.com/charmbracelet/bubbles`), Lip Gloss (`github.com/charmbracelet/lipgloss`), `github.com/charmbracelet/x/ansi`, standard Go tests.

## Global Constraints

- Preserve the current two-column surface: main Chat panel on the left, right informational panel with Plan/Context/Log tabs.
- Match `docs/mockups/tui-redesign.html` visual direction: dark terminal shell, rounded bordered panels, cyan active accents, violet status brand segment, muted secondary text, compact professional density.
- Do not build the Phase 2 multi-panel grid; use it only as future vision context.
- `View()` remains read-only and must not mutate viewport, input, or layout fields.
- All rendered lines must fit within the configured terminal width at `80x24`, `100x30`, and `120x40`.
- Preserve existing keybindings and behavior unless a task explicitly changes copy only.
- No new third-party dependencies.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/app/tui/model.go` | Existing model, layout, key routing, and render entry point. Split large render blocks into helper functions inside this file first to avoid broad package churn. |
| `internal/app/tui/model_test.go` | Layout bounds, visual copy, status/sidebar/approval/provider-error regression tests. |
| `docs/mockups/tui-redesign.html` | Visual reference only; no runtime dependency. |
| `docs/mockups/tui-redesign.png` | Desktop render reference for human comparison. |
| `docs/mockups/tui-redesign-mobile.png` | Narrow render reference for human comparison. |

## Target Visual Inventory

The implementation should approximate these mockup elements in terminal-native form:

- Outer app shell title: `Marshal`
- Header mode strip: `Ask Plan Auto Swarm`, with current mode highlighted
- Left panel title row: `Chat` and right-aligned `live transcript`
- Transcript roles colored separately: user cyan, agent violet, tool yellow, output yellow
- Collapsed thinking line: muted italic `⚙ thought for 4s`
- Live thinking panel: rounded dim border, title `thinking`, subtitle `streaming · Ctrl+G expands history`
- Input row: cyan prompt glyph and placeholder `Ask Marshal...`
- Help row: compact shortcut list, muted, wrapped to available width
- Right panel tab row: `1 Plan`, `2 Context`, `3 Log`; active tab uses cyan border/accent
- Sidebar Plan body: check/active/pending markers and compact task labels
- Context summary: boxed subsection with token usage
- Provider error: compact bordered banner below sidebar, not a full red slab
- Status bar: violet `MARSHAL` segment, mode, role, model/provider, privacy, context usage, fixed-width `IDLE`/`WORKING`
- Approval + diff state: diff panel plus approval panel with clear command/reason/risk/actions

---

### Task 1: Lock Visual Tokens and Render Primitives

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Produces: `renderPanel(title string, meta string, body string, width int, height int) string`
- Produces: `renderKeyHelp(width int, focused bool) string`
- Produces: visual style variables used by later tasks: `shellBorderColor`, `panelBorderColor`, `panelTitleStyle`, `mutedStyle`, `userRoleStyle`, `agentRoleStyle`, `toolRoleStyle`, `outputRoleStyle`, `activePillStyle`, `inactivePillStyle`

- [ ] **Step 1: Add failing tests for the baseline visual contract**

Append these tests to `internal/app/tui/model_test.go`:

```go
func TestPolishedViewContainsCurrentLayoutChrome(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Marshal",
		"Ask",
		"Plan",
		"Auto",
		"Swarm",
		"Chat",
		"live transcript",
		"1 Plan",
		"2 Context",
		"3 Log",
		"MARSHAL",
		"Ask Marshal...",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestPolishedViewFitsCommonTerminalSizes(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{
		{80, 24},
		{100, 30},
		{120, 40},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
			m := New(state)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			m = updated.(Model)

			view := m.View()
			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			if len(lines) > size.height {
				t.Fatalf("line count = %d, want <= %d\n%s", len(lines), size.height, view)
			}
			for i, line := range lines {
				if got := visibleRunes(line); got > size.width {
					t.Fatalf("line %d width = %d, want <= %d\n%s", i+1, got, size.width, line)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run tests and confirm they fail on the current render**

Run:

```bash
go test ./internal/app/tui -run 'TestPolishedViewContainsCurrentLayoutChrome|TestPolishedViewFitsCommonTerminalSizes' -v
```

Expected: at least `TestPolishedViewContainsCurrentLayoutChrome` fails because the current TUI does not render the mockup-style mode strip, `live transcript`, or numbered tab copy consistently.

- [ ] **Step 3: Replace the visual token block**

In `internal/app/tui/model.go`, replace the current `var (` style block beginning near `accentColor` with:

```go
var (
	shellBorderColor = lipgloss.Color("238")
	panelBorderColor = lipgloss.Color("240")
	panelSoftColor   = lipgloss.Color("236")
	accentColor      = lipgloss.Color("38")
	violetColor      = lipgloss.Color("99")
	dimColor         = lipgloss.Color("244")
	mutedColor       = lipgloss.Color("247")
	successColor     = lipgloss.Color("71")
	warningColor     = lipgloss.Color("178")
	errorColor       = lipgloss.Color("167")

	mutedStyle = lipgloss.NewStyle().Foreground(dimColor)
	panelTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")).
			Bold(true)
	thinkingLineStyle = lipgloss.NewStyle().
				Foreground(dimColor).
				Italic(true)
	userRoleStyle = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	agentRoleStyle = lipgloss.NewStyle().Foreground(violetColor).Bold(true)
	toolRoleStyle = lipgloss.NewStyle().Foreground(warningColor).Bold(true)
	outputRoleStyle = lipgloss.NewStyle().Foreground(warningColor).Bold(true)

	activePillStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentColor).
			Foreground(accentColor).
			Padding(0, 1)
	inactivePillStyle = lipgloss.NewStyle().
			Foreground(dimColor).
			Padding(0, 1)

	statusBarBrand = lipgloss.NewStyle().
			Background(violetColor).
			Foreground(lipgloss.Color("255")).
			Padding(0, 1).
			Bold(true)
	statusBarBg = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("252"))
	statusBarBusy = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(warningColor).
			Padding(0, 1).
			Bold(true)
	errorBannerStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(errorColor).
				Foreground(lipgloss.Color("252")).
				Padding(0, 1)
)
```

- [ ] **Step 4: Add `renderPanel`**

Add this helper after `renderMessage`:

```go
func renderPanel(title string, meta string, body string, width int, height int) string {
	if width < 4 {
		width = 4
	}
	if height < 2 {
		height = 2
	}
	// width and height are the interior content dimensions; lipgloss adds the
	// rounded border on top of them, so the rendered panel is width+2 by height+2.
	innerWidth := max(width, 1)
	truncatedTitle := truncateRunes(title, innerWidth)
	header := panelTitleStyle.Render(truncatedTitle)
	if meta != "" {
		metaWidth := innerWidth - visibleRunes(truncatedTitle)
		if metaWidth > 0 {
			header = lipgloss.JoinHorizontal(
				lipgloss.Top,
				header,
				strings.Repeat(" ", max(metaWidth-visibleRunes(meta), 1)),
				mutedStyle.Render(truncateRunes(meta, metaWidth)),
			)
		}
	}
	contentHeight := max(height-1, 1)
	content := lipgloss.NewStyle().
		Width(innerWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Render(body)
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Render(lipgloss.JoinVertical(lipgloss.Left, header, content))
}
```

- [ ] **Step 5: Add `renderKeyHelp`**

Add this helper after `renderPanel`:

```go
func renderKeyHelp(width int, focused bool) string {
	items := []string{
		"Esc unfocus",
		"Tab tabs",
		"Ctrl+O settings",
		"Ctrl+K memories",
		"Ctrl+G thinking",
	}
	if !focused {
		items = []string{
			"Enter focus",
			"1-3 tabs",
			"Ctrl+O settings",
			"Ctrl+K memories",
			"Ctrl+G thinking",
		}
	}
	text := strings.Join(items, "  ")
	return mutedStyle.MaxWidth(max(width, 1)).Render(truncateRunes(text, max(width, 1)))
}
```

- [ ] **Step 6: Run tests**

Run:

```bash
go test ./internal/app/tui -run 'TestPolishedViewContainsCurrentLayoutChrome|TestPolishedViewFitsCommonTerminalSizes' -v
```

Expected: the chrome test may still fail until Task 2 wires the helpers into `View()`, but the package must compile.

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "style(tui): add polished render tokens and primitives"
```

---

### Task 2: Recompose the Current Two-Panel Shell

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `renderPanel`, `renderKeyHelp`, status styles from Task 1
- Produces: `renderModeStrip(active string, width int) string`
- Produces: `renderStatusBar(width int, state *session.State, busy bool) string`

- [ ] **Step 1: Add tests for shell/status copy**

Append to `internal/app/tui/model_test.go`:

```go
func TestPolishedStatusBarShowsRouteWhenActive(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActiveRoute(session.RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Active:    true,
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"MARSHAL",
		"Auto",
		"implementer",
		"qwen2.5-coder:14b @ ollama",
		"local",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing status item %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

Run:

```bash
go test ./internal/app/tui -run TestPolishedStatusBarShowsRouteWhenActive -v
```

Expected: FAIL because current status uses `project=`, `cwd=`, and old route fallback copy rather than the polished status bar inventory.

- [ ] **Step 3: Add `renderModeStrip`**

Add this helper after `renderKeyHelp`:

```go
func renderModeStrip(active string, width int) string {
	if active == "" {
		active = "Auto"
	}
	labels := []string{"Ask", "Plan", "Auto", "Swarm"}
	rendered := make([]string, 0, len(labels))
	for _, label := range labels {
		if strings.EqualFold(label, active) {
			rendered = append(rendered, lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(label))
			continue
		}
		rendered = append(rendered, mutedStyle.Render(label))
	}
	return lipgloss.NewStyle().
		Width(max(width, 1)).
		MaxWidth(max(width, 1)).
		Render(strings.Join(rendered, " "))
}
```

- [ ] **Step 4: Add `renderStatusBar`**

Add this helper after `renderModeStrip`:

```go
func renderStatusBar(width int, state *session.State, busy bool) string {
	route := state.ActiveRoute()
	role := "inactive"
	modelProvider := "no model"
	locality := "remote-ok"
	if route.Active {
		role = string(route.Role)
		modelProvider = fmt.Sprintf("%s @ %s", route.Model, route.Provider)
		if route.LocalOnly {
			locality = "local"
		}
	} else if !state.Config.Privacy.RemoteProvidersAllowed {
		locality = "local"
	}

	busyText := "IDLE"
	if busy {
		busyText = "WORKING"
	}
	parts := []string{
		statusBarBrand.Render("MARSHAL"),
		" Auto ",
		fmt.Sprintf(" %s ", truncateRunes(role, 16)),
		fmt.Sprintf(" %s ", truncateRunes(modelProvider, 28)),
		fmt.Sprintf(" %s ", locality),
		statusBarBusy.Width(9).Render(busyText),
	}
	line := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	return statusBarBg.Width(width).MaxWidth(width).Render(truncateRunes(line, width))
}
```

- [ ] **Step 5: Recompose `View()` with a shell header and panel helpers**

Inside `View()`, keep the settings/memory overlay early returns. Replace the main layout assembly after `tc := m.state.PendingApproval()` with helper-oriented composition:

```go
chatPanel := m.renderChatPanel(tc)
inputPanel := m.renderInputArea()
leftColumn := lipgloss.JoinVertical(lipgloss.Left, chatPanel, inputPanel)

rightColumn := m.renderRightInfoPanel(tc)

mainLayout := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)
if err := m.state.ProviderError(); err != nil {
	errorBanner := m.renderProviderError(err)
	rightColumn = lipgloss.JoinVertical(lipgloss.Left, rightColumn, errorBanner)
	mainLayout = lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)
}

topBar := lipgloss.JoinHorizontal(
	lipgloss.Top,
	mutedStyle.Width(3).Render("● ● ●"),
	lipgloss.NewStyle().Width(max(m.width-28, 1)).Align(lipgloss.Center).Bold(true).Render("Marshal"),
	renderModeStrip("Auto", 20),
)

statusBar := renderStatusBar(m.width, m.state, m.busy)
return lipgloss.JoinVertical(lipgloss.Left, topBar, mainLayout, statusBar)
```

This step will compile after the temporary render-method stubs in the next step are added. Later tasks replace those stubs with the finished polished renderers.

- [ ] **Step 6: Add temporary compiling stubs**

Add these methods below `View()` so Task 2 compiles independently:

```go
func (m Model) renderChatPanel(tc *session.PendingToolCall) string {
	return renderPanel("Chat", "live transcript", m.viewport.View(), m.leftWidth, m.chatHeight)
}

func (m Model) renderInputArea() string {
	inputStyle := lipgloss.NewStyle().
		Width(m.leftWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Padding(0, 1)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		inputStyle.Render(m.input.View()),
		renderKeyHelp(m.leftWidth, m.inputFocused),
	)
}

func (m Model) renderRightInfoPanel(tc *session.PendingToolCall) string {
	return renderPanel("1 Plan  2 Context  3 Log", "inspector", "", m.rightWidth, m.contentHeight)
}

func (m Model) renderProviderError(err error) string {
	body := "Error: " + truncateRunes(err.Error(), max(m.rightWidth-10, 1))
	return renderPanel("Provider Error Banner", "fits AltScreen", body, m.rightWidth, 5)
}
```

- [ ] **Step 7: Run tests**

Run:

```bash
go test ./internal/app/tui -run 'TestPolishedViewContainsCurrentLayoutChrome|TestPolishedStatusBarShowsRouteWhenActive|TestPolishedViewFitsCommonTerminalSizes' -v
```

Expected: PASS for chrome/status copy if stubs are wired; bounds may fail if vertical budgeting needs Task 3 tightening.

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "style(tui): recompose current layout shell"
```

---

### Task 3: Polish Chat Transcript, Input, and Thinking Blocks

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `renderPanel`, role styles, `renderThinkingBox`, `renderThinkingSummary`
- Replaces: `renderMessage(role, content string, width int) string`
- Updates: `renderChatPanel(tc *session.PendingToolCall) string`

- [ ] **Step 1: Add transcript style tests**

Append:

```go
func TestPolishedTranscriptShowsRolesThinkingAndInput(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "fix the layout")
	state.BeginStreaming()
	state.AppendThinking("I need to inspect the render bounds and keep the newest reasoning visible.")
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.busy = true
	m.refreshViewport()

	view := m.View()
	for _, want := range []string{
		"user",
		"fix the layout",
		"thinking",
		"Ask Marshal...",
		"Ctrl+G thinking",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 2: Run the test and confirm failure if role/thinking copy is missing**

Run:

```bash
go test ./internal/app/tui -run TestPolishedTranscriptShowsRolesThinkingAndInput -v
```

Expected: FAIL until transcript rendering uses lower-case role labels, the polished thinking panel title, and the new help text.

- [ ] **Step 3: Replace `renderMessage`**

Replace the current `renderMessage` with:

```go
func renderMessage(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	label := strings.ToLower(role)
	roleStyle := mutedStyle
	switch label {
	case "user":
		roleStyle = userRoleStyle
	case "agent", "assistant":
		roleStyle = agentRoleStyle
	case "tool":
		roleStyle = toolRoleStyle
	case "output":
		roleStyle = outputRoleStyle
	}

	prefixWidth := 10
	contentWidth := max(width-prefixWidth-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", prefixWidth+2))
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}
```

- [ ] **Step 4: Update `renderThinkingBox` copy**

Replace the return body in `renderThinkingBox` with:

```go
header := lipgloss.JoinHorizontal(
	lipgloss.Top,
	"thinking",
	strings.Repeat(" ", max(boxWidth-34, 1)),
	"streaming · Ctrl+G expands history",
)
return style.Render(header+"\n\n"+tail) + "\n\n"
```

- [ ] **Step 5: Replace `renderChatPanel` stub**

Replace the Task 2 stub with:

```go
func (m Model) renderChatPanel(tc *session.PendingToolCall) string {
	if tc != nil {
		return m.renderApprovalArea(tc)
	}
	return renderPanel("Chat", "live transcript", m.viewport.View(), m.leftWidth, m.chatHeight)
}
```

- [ ] **Step 6: Tune input placeholder and border**

In `renderInputArea`, before rendering `m.input.View()`, keep the current input model unchanged but prefix with a cyan prompt:

```go
inputLine := lipgloss.JoinHorizontal(
	lipgloss.Top,
	lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("❯ "),
	m.input.View(),
)
return lipgloss.JoinVertical(
	lipgloss.Left,
	inputStyle.Render(inputLine),
	renderKeyHelp(m.leftWidth, m.inputFocused),
)
```

- [ ] **Step 7: Run tests**

Run:

```bash
go test ./internal/app/tui -run 'TestPolishedTranscriptShowsRolesThinkingAndInput|TestPolishedViewFitsCommonTerminalSizes' -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "style(tui): polish chat transcript and thinking blocks"
```

---

### Task 4: Polish Right Informational Panel

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `activePillStyle`, `inactivePillStyle`, `renderPanel`
- Produces: `renderSidebarTabs(width int, active int) string`
- Produces: `renderPlanTab(width int, height int, tc *session.PendingToolCall, busy bool) string`
- Produces: `renderContextTab(width int, height int) string`
- Produces: `renderLogTab(width int, height int) string`

- [ ] **Step 1: Add sidebar tests**

Append:

```go
func TestPolishedSidebarTabsAndContextSummary(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 18000, MaxTokens: 32000},
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Source: "repo.card", EstimatedTokens: 120},
			{Kind: contextpack.SectionFileSummary, Title: "internal/app/tui/model.go", Source: "internal/app/tui/model.go", EstimatedTokens: 8400},
		},
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)
	m.activeTab = 1

	view := m.View()
	for _, want := range []string{
		"1 Plan",
		"2 Context",
		"3 Log",
		"Context Pack",
		"18k / 32k",
		"internal/app/tui/model.go",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 2: Run the test and confirm failure**

Run:

```bash
go test ./internal/app/tui -run TestPolishedSidebarTabsAndContextSummary -v
```

Expected: FAIL until the right panel has polished tabs and compact context summary copy.

- [ ] **Step 3: Add `renderSidebarTabs`**

Add:

```go
func renderSidebarTabs(width int, active int) string {
	names := []string{"Plan", "Context", "Log"}
	parts := make([]string, 0, len(names))
	for i, name := range names {
		label := fmt.Sprintf("%d %s", i+1, name)
		if active == i {
			parts = append(parts, activePillStyle.Render(label))
			continue
		}
		parts = append(parts, inactivePillStyle.Render(label))
	}
	return lipgloss.NewStyle().
		Width(max(width, 1)).
		MaxWidth(max(width, 1)).
		Render(lipgloss.JoinHorizontal(lipgloss.Top, parts...))
}
```

- [ ] **Step 4: Add tab body helpers**

Add:

```go
func (m Model) renderPlanTab(width int, height int, tc *session.PendingToolCall, busy bool) string {
	rows := []string{
		lipgloss.NewStyle().Foreground(successColor).Render("✓") + "  Inspect current TUI layout",
		lipgloss.NewStyle().Foreground(successColor).Render("✓") + "  Apply polished visual tokens",
		lipgloss.NewStyle().Foreground(accentColor).Render("●") + "  Match mockup panel chrome",
		mutedStyle.Render("○") + "  Verify bounds at 80x24",
	}
	if tc != nil {
		rows = append(rows, "→  Pending approval: "+truncateRunes(tc.Command, max(width-22, 1)))
	} else if busy {
		rows = append(rows, "→  Agent is working")
	} else {
		rows = append(rows, "→  Ready for input")
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(strings.Join(rows, "\n"))
}

func (m Model) renderContextTab(width int, height int) string {
	pack := m.state.ContextPack()
	if pack.IsEmpty() {
		return mutedStyle.Width(width).Height(height).Render("No context pack built yet.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Context Pack\n")
	fmt.Fprintf(&b, "ctx %s / %s\n\n", compactTokenCount(pack.TokenUsage.EstimatedTokens), compactTokenCount(pack.TokenUsage.MaxTokens))
	for i, section := range pack.Sections {
		if i >= 6 {
			break
		}
		title := section.Title
		if title == "" {
			title = section.Source
		}
		fmt.Fprintf(&b, "%d  %s  %s\n", i+1, truncateRunes(title, max(width-12, 1)), compactTokenCount(section.EstimatedTokens))
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(b.String())
}

func (m Model) renderLogTab(width int, height int) string {
	auditLog := m.state.AuditLog()
	if len(auditLog) == 0 {
		return mutedStyle.Width(width).Height(height).Render("No tool calls yet.")
	}
	var b strings.Builder
	for i, event := range auditLog {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&b, "%s  %s  %s\n",
			event.Timestamp.Format("15:04"),
			truncateRunes(event.ToolName, 10),
			truncateRunes(event.ResultSummary, max(width-20, 1)),
		)
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(b.String())
}
```

Add the token helper:

```go
func compactTokenCount(tokens int) string {
	if tokens >= 1000 {
		return fmt.Sprintf("%dk", tokens/1000)
	}
	return fmt.Sprintf("%d", tokens)
}
```

- [ ] **Step 5: Replace `renderRightInfoPanel`**

Replace the Task 2 stub with:

```go
func (m Model) renderRightInfoPanel(tc *session.PendingToolCall) string {
	innerWidth := max(m.rightWidth-2, 1)
	bodyHeight := max(m.contentHeight-5, 1)
	tabs := renderSidebarTabs(innerWidth, m.activeTab)

	var body string
	switch m.activeTab {
	case 0:
		body = m.renderPlanTab(innerWidth, bodyHeight, tc, m.busy)
	case 1:
		body = m.renderContextTab(innerWidth, bodyHeight)
	default:
		body = m.renderLogTab(innerWidth, bodyHeight)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, tabs, body)
	return renderPanel("", "inspector", content, m.rightWidth, m.contentHeight)
}
```

- [ ] **Step 6: Run tests**

Run:

```bash
go test ./internal/app/tui -run 'TestPolishedSidebarTabsAndContextSummary|TestPolishedViewFitsCommonTerminalSizes' -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "style(tui): polish right informational panel"
```

---

### Task 5: Polish Approval, Diff, and Provider Error States

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `renderPanel`
- Produces: `renderApprovalArea(tc *session.PendingToolCall) string`
- Updates: `renderProviderError(err error) string`

- [ ] **Step 1: Add approval/provider-error tests**

Append:

```go
func TestPolishedApprovalStateShowsCommandReasonRiskAndActions(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetPendingApproval(&session.PendingToolCall{
		Command:      "go test ./internal/app/tui/...",
		Reason:       "Validate layout bounds and modal capture.",
		Risk:         "Low - test command, no destructive flags detected.",
		Diff:         "- old\n+ new\n",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Diff",
		"Agent wants to run",
		"go test ./internal/app/tui/...",
		"Reason",
		"Risk",
		"Enter approve",
		"d deny",
		"e edit",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing approval copy %q:\n%s", want, view)
		}
	}
}

func TestPolishedProviderErrorUsesCompactBanner(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetProviderError(errors.New("provider timeout: retrying local_heavy"))
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Provider Error Banner",
		"fits AltScreen",
		"provider timeout",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing provider error copy %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```bash
go test ./internal/app/tui -run 'TestPolishedApprovalStateShowsCommandReasonRiskAndActions|TestPolishedProviderErrorUsesCompactBanner' -v
```

Expected: FAIL until approval and provider error rendering match the polished copy.

- [ ] **Step 3: Add `renderApprovalArea`**

Add:

```go
func (m Model) renderApprovalArea(tc *session.PendingToolCall) string {
	if tc.Diff == "" {
		body := strings.Join([]string{
			panelTitleStyle.Foreground(accentColor).Render("Agent wants to run"),
			truncateRunes(tc.Command, max(m.leftWidth-4, 1)),
			"",
			mutedStyle.Render("Reason"),
			truncateRunes(tc.Reason, max(m.leftWidth-4, 1)),
			"",
			mutedStyle.Render("Risk"),
			truncateRunes(riskText(tc), max(m.leftWidth-4, 1)),
			"",
			"Enter approve  d deny  e edit  a always allow",
		}, "\n")
		return renderPanel("Approval", "pending", body, m.leftWidth, m.chatHeight)
	}

	splitWidth := max((m.leftWidth-2)/2, 10)
	diffLines := strings.Split(tc.Diff, "\n")
	maxDiffLines := max(m.chatHeight-1, 1)
	if len(diffLines) > maxDiffLines {
		diffLines = diffLines[:maxDiffLines]
	}
	for i := range diffLines {
		diffLines[i] = truncateRunes(diffLines[i], splitWidth)
	}
	diffBody := strings.Join(diffLines, "\n")
	diffPanel := renderPanel("Diff", "proposed patch", diffBody, splitWidth, m.chatHeight)

	approvalBody := strings.Join([]string{
		panelTitleStyle.Foreground(accentColor).Render("Agent wants to run"),
		truncateRunes(tc.Command, max(splitWidth, 1)),
		"",
		mutedStyle.Render("Reason"),
		truncateRunes(tc.Reason, max(splitWidth, 1)),
		"",
		mutedStyle.Render("Risk"),
		truncateRunes(riskText(tc), max(splitWidth, 1)),
		"",
		"Enter approve",
		"e edit",
		"d deny",
	}, "\n")
	approvalPanel := renderPanel("Approval", "security", approvalBody, splitWidth, m.chatHeight)
	return lipgloss.JoinHorizontal(lipgloss.Top, diffPanel, approvalPanel)
}
```

- [ ] **Step 4: Replace `renderProviderError`**

Replace the Task 2 stub with:

```go
func (m Model) renderProviderError(err error) string {
	body := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelSoftColor).
		Padding(0, 1).
		Width(max(m.rightWidth-4, 1)).
		Render(lipgloss.NewStyle().Foreground(errorColor).Render("! ") + truncateRunes(err.Error(), max(m.rightWidth-8, 1)))
	return renderPanel("Provider Error Banner", "fits AltScreen", body, m.rightWidth, min(6, max(m.contentHeight/3, 4)))
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./internal/app/tui -run 'TestPolishedApprovalStateShowsCommandReasonRiskAndActions|TestPolishedProviderErrorUsesCompactBanner|TestPolishedViewFitsCommonTerminalSizes' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "style(tui): polish approval and error states"
```

---

### Task 6: Add Final Verification Guardrails

**Files:**
- Modify: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: final render helpers from Tasks 1-5
- Produces: high-signal regression tests that prevent obvious drift from the mockup-inspired current layout

- [ ] **Step 1: Add a seeded full-surface snapshot-style test**

Append:

```go
func TestPolishedCurrentLayoutFullSurface(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActiveRoute(session.RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Active:    true,
	})
	state.AddMessage(session.RoleUser, "fix the failing TUI layout tests")
	state.AddMessage(session.RoleAgent, "I found the render drift and am tightening the layout.")
	state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(100, 0),
		ToolName:      "go test",
		ResultSummary: "FAIL: line exceeds width",
	})
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 18000, MaxTokens: 32000},
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionFileSummary, Title: "internal/app/tui/model.go", Source: "internal/app/tui/model.go", EstimatedTokens: 8400},
			{Kind: contextpack.SectionFileSummary, Title: "internal/app/session/session.go", Source: "internal/app/session/session.go", EstimatedTokens: 4100},
		},
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Marshal",
		"Chat",
		"live transcript",
		"user",
		"agent",
		"1 Plan",
		"2 Context",
		"3 Log",
		"MARSHAL",
		"implementer",
		"qwen2.5-coder:14b @ ollama",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("full surface missing %q:\n%s", want, view)
		}
	}
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	for i, line := range lines {
		if got := visibleRunes(line); got > 120 {
			t.Fatalf("line %d width = %d, want <= 120\n%s", i+1, got, line)
		}
	}
}
```

- [ ] **Step 2: Run all TUI tests**

Run:

```bash
go test ./internal/app/tui/... -v
```

Expected: PASS.

- [ ] **Step 3: Run broader app tests**

Run:

```bash
go test ./internal/app/... -v
```

Expected: PASS.

- [ ] **Step 4: Manually compare against the mockup render**

Open these files side by side:

```text
docs/mockups/tui-redesign.png
docs/mockups/tui-redesign.html
```

Run the TUI in a terminal at approximately `120x36` and compare these concrete points:

- Dark shell with compact title row and mode strip is present.
- Left panel reads as the primary Chat surface.
- Right informational panel uses numbered Plan/Context/Log tabs.
- Thinking and approval states use bordered panels instead of raw text blocks.
- Status bar has a violet `MARSHAL` segment and compact route/model state.
- Provider errors appear as compact banners, not full-width emergency blocks.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model_test.go
git commit -m "test(tui): guard polished current layout"
```

---

## Self-Review

**Spec coverage:** This plan covers the current two-panel layout, high-quality mockup fidelity, chat panel polish, right informational panel polish, thinking block styling, approval/diff state, provider error state, status bar, and terminal-width guardrails. It explicitly excludes the Phase 2 multi-panel grid.

**Placeholder scan:** No task uses `TBD`, `TODO`, `implement later`, or unspecified edge-case language. Each code-changing step includes concrete code.

**Type consistency:** New helper names are consistent across tasks: `renderPanel`, `renderKeyHelp`, `renderModeStrip`, `renderStatusBar`, `renderChatPanel`, `renderInputArea`, `renderRightInfoPanel`, `renderProviderError`, `renderSidebarTabs`, `renderPlanTab`, `renderContextTab`, `renderLogTab`, `renderApprovalArea`, and `compactTokenCount`.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-04-polished-current-tui.md`. Two execution options:

**1. Subagent-Driven (recommended)** - Dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
