# TUI Redesign Implementation Plan

> **For Antigravity:** REQUIRED WORKFLOW: Use `.agent/workflows/execute-plan.md` to execute this plan in single-flow mode.

**Goal:** Transform the basic scrolling console TUI into a full-screen, responsive, multi-panel dashboard utilizing Bubble Tea AltScreen, Lip Gloss layout formatting, and bubble/viewport for smooth scrolling.

**Architecture:** Use a dual-column layout with flexible panel sizing, dynamic layout updates on terminal resize (`tea.WindowSizeMsg`), tab deck state for the right column, and modular rendering functions.

**Tech Stack:** Go, Charmbracelet Bubble Tea (`github.com/charmbracelet/bubbletea`), Bubbles (`github.com/charmbracelet/bubbles/viewport`, `github.com/charmbracelet/bubbles/textinput`), Lip Gloss (`github.com/charmbracelet/lipgloss`).

---

### Task 1: Update Model struct with layout and tab state

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Step 1: Write the failing test**
In `internal/app/tui/model_test.go`, add `TestModelLayoutStateInit` to verify the new fields (`width`, `height`, `activeTab`, `inputFocused`, `viewport` from bubbles/viewport) exist and have proper defaults.

```go
func TestModelLayoutStateInit(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	if !model.inputFocused {
		t.Error("expected inputFocused to be true by default")
	}
	if model.activeTab != 0 {
		t.Errorf("expected activeTab to be 0 (Plan), got %d", model.activeTab)
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/app/tui -run TestModelLayoutStateInit`
Expected: Compile error (fields activeTab, inputFocused, etc. undefined on Model struct)

**Step 3: Write minimal implementation**
In `internal/app/tui/model.go`, import `"github.com/charmbracelet/bubbles/viewport"` and add state fields to the `Model` struct:
```go
type Model struct {
	state          *session.State
	input          textinput.Model
	editingCommand bool
	runner         AgentRunner
	ctx            context.Context
	busy           bool

	// New Layout State
	width        int
	height       int
	activeTab    int // 0 = Plan, 1 = Context, 2 = Log
	inputFocused bool
	viewport     viewport.Model
}
```
Update `New()` to initialize `inputFocused: true` and a default `viewport` object:
```go
func New(state *session.State, opts ...Option) Model {
	input := textinput.New()
	input.Placeholder = "Ask Marshal..."
	input.Focus()
	input.CharLimit = 4000
	input.Width = 80

	m := Model{
		state:          state,
		input:          input,
		editingCommand: false,
		ctx:            context.Background(),
		inputFocused:   true,
		activeTab:      0,
		viewport:       viewport.New(0, 0),
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}
```

**Step 4: Run test to verify it passes**
Run: `go test ./internal/app/tui -run TestModelLayoutStateInit`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat: add layout and tab state to tui model struct"
```

---

### Task 2: Implement focus toggle, tab switching, and window resizing in Update

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Step 1: Write the failing tests**
Write tests for window resizing, focus switching with Esc/Enter, and tab switches via Ctrl keys and number keys.

```go
func TestFocusAndTabNavigation(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	// Test Esc unfocuses input
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.inputFocused {
		t.Error("Esc did not unfocus input")
	}

	// Test Enter focuses input when unfocused
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !model.inputFocused {
		t.Error("Enter did not focus input when unfocused")
	}

	// Test Ctrl+K switches tab to Context (1)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model = updated.(Model)
	if model.activeTab != 1 {
		t.Errorf("Ctrl+K did not switch to Context tab, got activeTab=%d", model.activeTab)
	}

	// Test number key when unfocused
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc}) // unfocus
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")}) // Press '3'
	model = updated.(Model)
	if model.activeTab != 2 {
		t.Errorf("Pressing 3 did not switch to Log tab, got activeTab=%d", model.activeTab)
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/app/tui -run TestFocusAndTabNavigation`
Expected: FAIL

**Step 3: Write minimal implementation**
In `internal/app/tui/model.go`:
* Add handler for `tea.WindowSizeMsg` in `Update()` to store `width` and `height`, and configure the viewport's dimensions:
  ```go
  case tea.WindowSizeMsg:
      m.width = msg.Width
      m.height = msg.Height
      // Calculate chat viewport dimensions (70% width, height minus headers/input/status line)
      chatWidth := int(float64(m.width) * 0.70)
      chatHeight := m.height - 8
      if chatHeight < 1 {
          chatHeight = 1
      }
      m.viewport.Width = chatWidth
      m.viewport.Height = chatHeight
      return m, nil
  ```
* Integrate `m.inputFocused` into keyboard logic:
  * When `m.inputFocused` is true:
    - Pressing `Esc` sets `m.inputFocused = false` and calls `m.input.Blur()`.
    - Key combinations like `Ctrl+P` (activeTab = 0), `Ctrl+K` (activeTab = 1), `Ctrl+T` (activeTab = 2) should switch the active tab even when input is focused.
    - Otherwise, forward key to `m.input.Update(msg)`.
  * When `m.inputFocused` is false:
    - Pressing `Enter` sets `m.inputFocused = true` and calls `m.input.Focus()`.
    - Pressing runes `'1'`, `'2'`, `'3'` sets `activeTab` to 0, 1, 2 respectively.
    - Forward up/down/pageup/pagedown keys to `m.viewport.Update(msg)`.

Let's modify `Update` keyboard section:
```go
		// Always allow Ctrl+C to quit
		if msg.Type == tea.KeyCtrlC {
			m.state.Shutdown()
			return m, tea.Quit
		}

		// Global Tab navigation keys
		if msg.Type == tea.KeyCtrlP {
			m.activeTab = 0
			return m, nil
		}
		if msg.Type == tea.KeyCtrlK {
			m.activeTab = 1
			return m, nil
		}
		if msg.Type == tea.KeyCtrlT {
			m.activeTab = 2
			return m, nil
		}

		if tc != nil {
            // Approval flow... Keep current logic but ensure it interacts correctly with editingCommand
        } else {
			if m.inputFocused {
				switch msg.Type {
				case tea.KeyEsc:
					m.inputFocused = false
					m.input.Blur()
					return m, nil
				case tea.KeyEnter:
					value := strings.TrimSpace(m.input.Value())
					if value == "" || m.busy {
						return m, nil
					}
					m.input.Reset()
					if m.runner == nil {
						m.state.AddMessage(session.RoleUser, value)
						return m, nil
					}
					m.busy = true
					return m, tea.Batch(runAgentCmd(m.ctx, m.runner, value), tickCmd())
				}
			} else {
				// Unfocused controls
				switch msg.Type {
				case tea.KeyEnter:
					m.inputFocused = true
					return m, m.input.Focus()
				case tea.KeyRunes:
					switch msg.String() {
					case "1":
						m.activeTab = 0
						return m, nil
					case "2":
						m.activeTab = 1
						return m, nil
					case "3":
						m.activeTab = 2
						return m, nil
					}
				}
				// Pass scroll keys to viewport
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(msg)
				return m, vpCmd
			}
		}
```

**Step 4: Run test to verify it passes**
Run: `go test ./internal/app/tui -run TestFocusAndTabNavigation`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat: implement resizing, focus toggling, and tab controls in Update"
```

---

### Task 3: Build stylish Lip Gloss styles and View layout assembly

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Step 1: Write the failing test**
Update `TestViewContainsExpectedPanels` or create `TestAltScreenView` to verify the presence of custom styled headers, tabs, status bar, and column splits.

```go
func TestAltScreenViewLayout(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	// Simulate size
	model.width = 100
	model.height = 40

	view := model.View()
	// Check for sidebar tabs, border characters, status bar
	if !strings.Contains(view, "[1] Plan") {
		t.Error("view missing Plan tab title")
	}
	if !strings.Contains(view, "/repo") {
		t.Error("view missing working directory in status bar")
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/app/tui -run TestAltScreenViewLayout`
Expected: FAIL

**Step 3: Write minimal implementation**
In `internal/app/tui/model.go`:
* Import `github.com/charmbracelet/lipgloss` if not present.
* Create Lip Gloss styles in a separate config block or function:
  ```go
  var (
      accentColor = lipgloss.Color("86") // Cyan/Teal
      dimColor    = lipgloss.Color("244") // Gray
      activeTabStyle = lipgloss.NewStyle().
          Border(lipgloss.NormalBorder(), false, false, true, false).
          BorderForeground(accentColor).
          Foreground(accentColor).
          Padding(0, 1)
      inactiveTabStyle = lipgloss.NewStyle().
          Border(lipgloss.NormalBorder(), false, false, true, false).
          BorderForeground(dimColor).
          Foreground(dimColor).
          Padding(0, 1)
      panelStyle = lipgloss.NewStyle().
          Border(lipgloss.RoundedBorder()).
          BorderForeground(dimColor).
          Padding(0, 1)
      statusBarAccent = lipgloss.NewStyle().
          Background(accentColor).
          Foreground(lipgloss.Color("0")).
          Padding(0, 1).
          Bold(true)
      statusBarBg = lipgloss.NewStyle().
          Background(lipgloss.Color("236")).
          Foreground(lipgloss.Color("252"))
  )
  ```
* Implement modular view rendering:
  - Check if `m.settingsOpen` is true. If so, return the settings view centered on the terminal screen using `lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModel.View())`.
  - Check if `m.memoryOpen` is true. If so, return the memory view centered on the terminal screen using `lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.memoryModel.View())`.
  - `renderChatView()`: format messages into a single text block, update `viewport.SetContent()`, and return `m.viewport.View()`.
  - `renderSidebar()`: format right column. Header contains active/inactive tab buttons. Content lists Plan checklist items, Context files, or Tool logs based on `m.activeTab`.
  - `renderStatusBar()`: construct the bottom bar spanning full `m.width`.
  - Check if `m.state.PendingApproval()` is active. If so, split the main chat column to show the diff on one side and instructions/approval inputs on the right side.
  - Join left and right columns horizontally with `lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)`. Join layout and status bar vertically.

**Step 4: Run test to verify it passes**
Run: `go test ./internal/app/tui -run TestAltScreenView`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat: complete TUI rendering using Lip Gloss layout and panels"
```

---

### Task 4: Hook up AltScreen mode in application entrypoint

**Files:**
- Modify: `internal/app/app.go`

**Step 1: Write/Update manual verification details**
There isn't a simple unit test for the main loop alt screen initialization since it requires a real terminal. However, we can assert that `tea.WithAltScreen()` is passed to `tea.NewProgram`.

**Step 2: Implement changes**
In `internal/app/app.go`, ensure that `tea.NewProgram` uses `tea.WithAltScreen()` when running in full screen mode.
```go
p := tea.NewProgram(tuiModel, tea.WithAltScreen())
```

**Step 3: Run full suite and verify**
Run: `go test ./...`
Expected: PASS

**Step 4: Commit**
```bash
git add internal/app/app.go
git commit -m "feat: enable AltScreen mode for Bubble Tea program"
```
