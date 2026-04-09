// internal/tui/app.go
// Root model. Owns the two-column layout (sidebar | main panel + statusbar).
// All screen routing lives here. Sub-models receive their dimensions via
// View(w, h int) and never reference terminal size directly.

package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/store"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Messages (inter-component) ────────────────────────────────────────────────

// SubmitTaskMsg is sent when the user submits a task (prompt or composer).
type SubmitTaskMsg struct{ Task string }

// TaskStartedMsg is sent by the loop engine when a queued task begins running.
type TaskStartedMsg struct{ ID string }

// TaskCompleteMsg is sent by the loop engine when a task finishes.
type TaskCompleteMsg struct {
	ID     string
	Passed bool
	SHA    string
}

// LogLineMsg appends a line to the active task block.
type LogLineMsg struct{ Line LogLine }

// ThinkBlockMsg sets the think-block content for the current task block.
type ThinkBlockMsg struct{ Content string }

// CompactionMsg signals that compaction occurred.
type CompactionMsg struct {
	RoundsDropped int
	TokensSaved   int
	Summary       string
}

// OpenDiffMsg is sent to open the diff viewer for a specific task.
type OpenDiffMsg struct {
	TaskID string
	SHA    string
}

// JumpToTaskMsg scrolls the main panel to the given task block.
type JumpToTaskMsg struct{ ID string }

// OpenFileMsg is sent when the user selects a file in the explorer (inline editor).
type OpenFileMsg struct{ Path string }

// OpenFileExternalMsg opens a file directly in the configured external editor.
type OpenFileExternalMsg struct{ Path string }

// OpenFuzzyMsg is sent when the user requests the fuzzy file picker.
type OpenFuzzyMsg struct{}

// editorClosedMsg is sent when the external editor process exits.
type editorClosedMsg struct{ err error }

// ExecCommandMsg is sent when the user submits a vim-style ":command".
type ExecCommandMsg struct {
	Name string
	Args []string
}

// ── Focus section ─────────────────────────────────────────────────────────────

type focusSection int

const (
	focusMain      focusSection = iota // prompt input
	focusFileTree                      // sidebar file explorer
	focusTaskQueue                     // sidebar task list
)

// ── Overlay type ──────────────────────────────────────────────────────────────

type overlay int

const (
	overlayNone overlay = iota
	overlayComposer
	overlayDiff
	overlayConfig
	overlayFileEditor
	overlayHelp
	overlaySessions
	overlayFuzzy
)

// ── Root model ────────────────────────────────────────────────────────────────

type Model struct {
	width  int
	height int

	sidebar    SidebarModel
	main       MainModel
	composer   ComposerModel
	diff       DiffModel
	config     ConfigModel
	fileEditor FileEditorModel
	help       HelpModel
	sessions   SessionsModel
	fuzzy      FuzzyModel

	overlay  overlay
	focus    focusSection
	editor   string // resolved editor binary (e.g. "vim")
	repoRoot string

	loopAdapter *LoopAdapter // for command handlers

	execURL    string
	criticURL  string
	execConn   connStatus
	criticConn connStatus
}

func New() Model {
	return Model{
		sidebar:  newSidebarModel("."),
		main:     newMainModel(),
		composer: newComposerModel(),
		config:   newConfigModel(),
		help:     newHelpModel(),
	}
}

// WithRepoRoot sets the project root used by the file tree explorer.
func (m Model) WithRepoRoot(root string) Model {
	m.sidebar.fileTree = newFileTreeModel(root)
	m.repoRoot = root
	return m
}

// WithConfig populates the config overlay with live values and stores endpoint URLs.
func (m Model) WithConfig(cfg *config.Config) Model {
	m.config = m.config.WithCfg(cfg)
	m.execURL = cfg.Executor.BaseURL
	m.criticURL = cfg.Critic.BaseURL
	return m
}

// WithEditor sets the editor binary used when opening files from the explorer.
func (m Model) WithEditor(editor string) Model {
	m.editor = editor
	return m
}

// WithLoopAdapter sets the loop adapter for command handlers.
func (m Model) WithLoopAdapter(adapter *LoopAdapter) Model {
	m.loopAdapter = adapter
	return m
}

// WithStore sets the store and initializes the sessions model.
func (m Model) WithStore(s *store.Store) Model {
	m.sessions = newSessionsModel(s)
	m.fuzzy = newFuzzyModel(m.repoRoot)
	return m
}

// WithInitialTask returns a model with a pre-populated task.
// This is used when a task is provided via CLI arguments.
func (m Model) WithInitialTask(task string) Model {
	// Queue the task immediately - the loop adapter will pick it up
	m.sidebar.Enqueue(task)
	m.main.Enqueue(task)
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.main.Init(),
		doConnCheck(m.execURL, m.criticURL),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		var cmd tea.Cmd
		m.main, cmd = m.main.Update(msg)
		if m.overlay == overlayFileEditor {
			m.fileEditor, _ = m.fileEditor.Update(msg)
		}
		return m, cmd

	case tea.KeyMsg:
		// Overlay-level keys take priority (file editor handles its own esc)
		if m.overlay != overlayNone && m.overlay != overlayFileEditor && m.overlay != overlayFuzzy {
			switch msg.String() {
			case "q", "esc":
				m.overlay = overlayNone
				return m, nil
			}
		}
		// Fuzzy picker handles esc internally to cancel without closing overlay
		if m.overlay == overlayFuzzy {
			switch msg.String() {
			case "esc":
				// Let fuzzy handle it - it will set cancelled flag
			}
		}

		// Global keys (not blocked by overlay)
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			if m.overlay == overlayNone {
				m.cycleFocus()
				return m, nil
			}
		case "ctrl+o":
			if m.overlay == overlayNone {
				m.overlay = overlayConfig
				return m, nil
			}
		case "?":
			if m.overlay == overlayNone {
				m.overlay = overlayHelp
				return m, nil
			}
		}

	// Task lifecycle messages — update sidebar and main panel
	case SubmitTaskMsg:
		sidebarCmd := m.sidebar.Enqueue(msg.Task)
		mainCmd := m.main.Enqueue(msg.Task)
		cmds = append(cmds, sidebarCmd, mainCmd)
		return m, tea.Batch(cmds...)

	case TaskStartedMsg:
		m.sidebar.SetRunning(msg.ID)
		m.main.SetRunning(msg.ID)

	case TaskCompleteMsg:
		// Track rounds - simplified: always pass 1 for now (would come from actual round count)
		m.sidebar.SetComplete(msg.ID, msg.Passed, 1)
		m.main.SetComplete(msg.ID, msg.Passed, msg.SHA)

	case LogLineMsg:
		m.main.AppendLine(msg.Line)

	case ThinkBlockMsg:
		m.main.SetThinkBlock(msg.Content)

	case CompactionMsg:
		m.main.AppendLine(LogLine{
			Kind:    lineCompaction,
			Content: fmt.Sprintf("rounds %d-%d summarized, ~%d tok saved", 1, msg.RoundsDropped, msg.TokensSaved),
		})

	case OpenDiffMsg:
		m.diff = newDiffModel(msg.TaskID, msg.SHA, m.repoRoot)
		m.overlay = overlayDiff
		return m, m.diff.Init()

	case JumpToTaskMsg:
		var cmd tea.Cmd
		m.main, cmd = m.main.Update(msg)
		return m, cmd

	case OpenFileMsg:
		mainW := m.width - sidebarWidth - 1
		if mainW < 1 {
			mainW = 80
		}
		fe, err := newFileEditorModel(msg.Path, mainW)
		if err != nil {
			return m, func() tea.Msg {
				return LogLineMsg{Line: LogLine{Kind: lineError, Content: "open: " + err.Error()}}
			}
		}
		m.fileEditor = fe
		m.overlay = overlayFileEditor
		return m, m.fileEditor.Init()

	case OpenFileExternalMsg:
		return m, m.execEditor(msg.Path)

	case editorClosedMsg:
		// Rebuild the file tree in case files were added/removed while editing.
		m.sidebar.fileTree.rebuild()
		if msg.err != nil {
			return m, func() tea.Msg {
				return LogLineMsg{Line: LogLine{Kind: lineError, Content: "editor: " + msg.err.Error()}}
			}
		}
		return m, nil

	case connCheckMsg:
		m.execConn = msg.exec
		m.criticConn = msg.critic
		m.config = m.config.SetConnStatus(msg.exec, msg.critic)
		// Schedule the next re-check; HTTP runs in the tick goroutine (4s timeout, 30s interval).
		execURL, criticURL := m.execURL, m.criticURL
		return m, tea.Tick(30*time.Second, func(_ time.Time) tea.Msg {
			return doConnCheck(execURL, criticURL)()
		})

	case ExecCommandMsg:
		return m.execCommand(msg)

	case OpenFuzzyMsg:
		m.fuzzy = newFuzzyModel(m.repoRoot)
		m.overlay = overlayFuzzy
		return m, m.fuzzy.Init()

	case closeSessionsMsg:
		m.overlay = overlayNone
		return m, nil

	case resumeSessionMsg:
		m.overlay = overlayNone
		// Pre-fill prompt with session task
		m.main.input.SetValue(msg.session.Task)
		m.main.input.Focus()
		return m, nil

	case sessionDeletedMsg:
		m.sessions = m.sessions.HandleDeleteResult(msg)
		return m, nil

	case fuzzyInitMsg:
		m.fuzzy = m.fuzzy.WithFiles(msg.files)
		return m, nil
	}

	// Delegate to active overlay or main panel
	switch m.overlay {
	case overlayComposer:
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(msg)
		cmds = append(cmds, cmd)
		// Check if composer submitted or cancelled
		if m.composer.submitted {
			task := m.composer.value
			m.composer = newComposerModel()
			m.overlay = overlayNone
			cmds = append(cmds, func() tea.Msg { return SubmitTaskMsg{Task: task} })
		}
		if m.composer.cancelled {
			m.main.input.SetValue(m.composer.value)
			m.main.input.CursorEnd()
			m.composer = newComposerModel()
			m.overlay = overlayNone
		}
	case overlayDiff:
		var cmd tea.Cmd
		m.diff, cmd = m.diff.Update(msg)
		cmds = append(cmds, cmd)
	case overlayConfig:
		var cmd tea.Cmd
		m.config, cmd = m.config.Update(msg)
		cmds = append(cmds, cmd)
	case overlayFileEditor:
		// ctrl+e: auto-save and hand off to external editor
		if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "ctrl+e" {
			path := m.fileEditor.path
			// Auto-save any pending changes before launching external editor
			if m.fileEditor.modified {
				m.fileEditor.saveFile()
			}
			m.overlay = overlayNone
			return m, m.execEditor(path)
		}
		var cmd tea.Cmd
		m.fileEditor, cmd = m.fileEditor.Update(msg)
		cmds = append(cmds, cmd)
		if m.fileEditor.closed {
			m.overlay = overlayNone
			m.sidebar.fileTree.rebuild()
		}
	case overlayNone:
		var mainCmd, sidebarCmd tea.Cmd
		if m.focus != focusMain {
			// Sidebar sections have exclusive key focus
			m.sidebar, sidebarCmd = m.sidebar.Update(msg)
		} else {
			m.main, mainCmd = m.main.Update(msg)
		}
		cmds = append(cmds, mainCmd, sidebarCmd)

		// Check if main panel wants to open composer
		if m.main.openComposer {
			m.composer = newComposerModel()
			m.composer.SetValue(m.main.input.Value())
			m.main.openComposer = false
			m.overlay = overlayComposer
		}
	case overlayHelp:
		var cmd tea.Cmd
		m.help, cmd = m.help.Update(msg)
		cmds = append(cmds, cmd)
	case overlaySessions:
		var cmd tea.Cmd
		m.sessions, cmd = m.sessions.Update(msg)
		cmds = append(cmds, cmd)
	case overlayFuzzy:
		var cmd tea.Cmd
		m.fuzzy, cmd = m.fuzzy.Update(msg)
		cmds = append(cmds, cmd)
		// Check if fuzzy picker submitted or cancelled
		if sel, ok := m.fuzzy.SelectedFile(); ok {
			m.composer.pinnedFiles = append(m.composer.pinnedFiles, fileChip{
				path:     sel,
				language: languageFromExt(sel),
			})
			m.fuzzy = newFuzzyModel(m.repoRoot)
			m.overlay = overlayComposer
		}
		if m.fuzzy.Cancelled() {
			m.fuzzy = newFuzzyModel(m.repoRoot)
			m.overlay = overlayComposer
		}
	}

	return m, tea.Batch(cmds...)
}

// execEditor launches the configured external editor on path via tea.ExecProcess.
func (m Model) execEditor(path string) tea.Cmd {
	editor := m.editor
	if editor == "" {
		editor = "vim"
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorClosedMsg{err: err}
	})
}

// cycleFocus advances focus: main → files → tasks → main.
func (m *Model) cycleFocus() {
	switch m.focus {
	case focusMain:
		m.focus = focusFileTree
		m.sidebar.SetSubFocus(subFocusFiles)
		m.main.input.Blur()
	case focusFileTree:
		m.focus = focusTaskQueue
		m.sidebar.SetSubFocus(subFocusTasks)
	case focusTaskQueue:
		m.focus = focusMain
		m.sidebar.SetSubFocus(subFocusNone)
		m.main.input.Focus()
	}
}

func (m Model) View() string {
	totalH := m.height
	headerH := 2 // content row + thick border below
	statusH := 2 // thick border above + content row
	contentH := totalH - headerH - statusH
	if contentH < 0 {
		contentH = 0
	}

	mainW := m.width - sidebarWidth - 1 // 1 for sidebar border

	header := m.renderHeader()
	sidebar := m.sidebar.View(contentH)

	// File editor replaces the main panel (sidebar stays visible).
	var mainView string
	if m.overlay == overlayFileEditor {
		mainView = m.fileEditor.View(mainW, contentH)
	} else {
		mainView = m.main.View(mainW, contentH)
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, mainView)
	status := m.renderStatusbar()
	base := lipgloss.JoinVertical(lipgloss.Left, header, body, status)

	// Full-screen overlays drawn on top
	switch m.overlay {
	case overlayComposer:
		return m.renderOverlay(base, m.composer.View(mainW, contentH))
	case overlayDiff:
		return m.renderOverlay(base, m.diff.View(m.width, totalH))
	case overlayConfig:
		return m.renderOverlay(base, m.config.View(m.width, totalH))
	case overlayHelp:
		return m.renderOverlay(base, m.help.View(m.width, totalH))
	case overlaySessions:
		return m.renderOverlay(base, m.sessions.View(m.width, totalH))
	case overlayFuzzy:
		return m.renderOverlay(base, m.fuzzy.View(m.width, totalH))
	}

	return base
}

func (m Model) renderHeader() string {
	title := styleHeaderTitle.Render("  marshal  ")
	hints := styleHeaderHint.Render(" tab focus · ctrl+o config · ctrl+c quit  ")
	fillW := m.width - lipgloss.Width(title) - lipgloss.Width(hints)
	if fillW < 0 {
		fillW = 0
	}
	fill := styleHeaderRule.Render(strings.Repeat("─", fillW))
	content := styleHeader.Width(m.width).Render(title + fill + hints)
	border := lipgloss.NewStyle().Foreground(colBl).Background(colBg).
		Render(strings.Repeat("━", m.width))
	return lipgloss.JoinVertical(lipgloss.Left, content, border)
}

// renderOverlay draws the overlay on top of the base content with a blur effect.
// The background is dimmed using ░ characters to create the appearance of a modal
// overlay per the TUI design spec (Section 5.1 - composer modal).
func (m Model) renderOverlay(base, top string) string {
	// Place the overlay content centered on top of the dimmed background.
	// The whitespace around the modal content is filled with ░ characters
	// in dim color to create the blur effect per the TUI design spec.
	_ = base // base is preserved in the background, lipgloss.Place overlays on top
	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		top,
		lipgloss.WithWhitespaceChars("░"),
		lipgloss.WithWhitespaceForeground(colTx3),
	)
}

func (m Model) renderStatusbar() string {
	sep := styleStatusSep.Render()
	left := strings.Join(m.activeStatusSegments(), sep)

	// Connection indicators are always right-aligned in the footer.
	right := connDot(m.execConn) + lipgloss.NewStyle().Foreground(colTx3).Render(" exec") +
		"   " +
		connDot(m.criticConn) + lipgloss.NewStyle().Foreground(colTx3).Render(" critic  ")

	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	fillW := m.width - leftW - rightW - 2 // 2 for left padding in styleStatusbar
	if fillW < 0 {
		fillW = 0
	}
	fill := lipgloss.NewStyle().Background(colBg2).Render(strings.Repeat(" ", fillW))
	content := styleStatusbar.Width(m.width).Render(" " + left + fill + right)
	border := lipgloss.NewStyle().Foreground(colBl).Background(colBg).
		Render(strings.Repeat("━", m.width))
	return lipgloss.JoinVertical(lipgloss.Left, border, content)
}

func (m Model) activeStatusSegments() []string {
	switch m.overlay {
	case overlayComposer:
		return m.composer.StatusSegments()
	case overlayConfig:
		return []string{styleStatusAlert.Render("config")}
	case overlayFileEditor:
		name := truncate(m.fileEditor.path, 40)
		if m.fileEditor.modified {
			return []string{styleStatusAlert.Render("editing"), name, styleStatusAlert.Render("unsaved")}
		}
		return []string{styleStatusAlert.Render("editing"), name}
	case overlayHelp:
		return []string{styleStatusAlert.Render("help")}
	case overlaySessions:
		return []string{styleStatusAlert.Render("sessions")}
	case overlayFuzzy:
		return []string{styleStatusAlert.Render("files")}
	}
	segs := m.main.StatusSegments()
	switch m.focus {
	case focusFileTree:
		return append([]string{styleStatusAlert.Render("files")}, segs...)
	case focusTaskQueue:
		return append([]string{styleStatusAlert.Render("tasks")}, segs...)
	}
	if m.main.IsCommandMode() {
		return append([]string{styleStatusAlert.Render("cmd")}, segs...)
	}
	return segs
}

// execCommand dispatches an ExecCommandMsg to the appropriate action.
func (m Model) execCommand(msg ExecCommandMsg) (Model, tea.Cmd) {
	switch msg.Name {
	case "quit":
		return m, tea.Quit

	case "new":
		m.composer = newComposerModel()
		m.overlay = overlayComposer
		return m, nil

	case "diff":
		// Find last completed task to diff
		taskID, sha := "", ""
		for i := len(m.main.blocks) - 1; i >= 0; i-- {
			b := m.main.blocks[i]
			if b.state == blockPass || b.state == blockFail {
				taskID = b.id
				sha = b.sha
				break
			}
		}
		m.diff = newDiffModel(taskID, sha, m.repoRoot)
		m.overlay = overlayDiff
		return m, m.diff.Init()

	case "config":
		m.overlay = overlayConfig
		return m, nil

	case "clear":
		return m, func() tea.Msg { return ClearLogMsg{} }

	case "cancel":
		if m.loopAdapter == nil || !m.loopAdapter.IsRunning() {
			return m, func() tea.Msg {
				return LogLineMsg{Line: LogLine{Kind: lineWarning, Content: "no task is currently running"}}
			}
		}
		// Find the running task and mark it as cancelling
		for i := range m.main.blocks {
			if m.main.blocks[i].state == blockRunning {
				m.sidebar.SetCancelling(m.main.blocks[i].id)
				m.main.AppendLine(LogLine{Kind: lineWarning, Content: "cancelling task..."})
				break
			}
		}
		// Trigger cancellation
		if m.loopAdapter.CancelTask() {
			return m, func() tea.Msg {
				return LogLineMsg{Line: LogLine{Kind: lineSuccess, Content: "task cancelled"}}
			}
		}
		return m, nil

	case "retry":
		// Find last completed task (pass or fail)
		var lastBlock *taskBlock
		for i := len(m.main.blocks) - 1; i >= 0; i-- {
			if m.main.blocks[i].state == blockPass || m.main.blocks[i].state == blockFail {
				lastBlock = &m.main.blocks[i]
				break
			}
		}
		if lastBlock == nil {
			return m, func() tea.Msg {
				return LogLineMsg{Line: LogLine{Kind: lineWarning, Content: "no completed task to retry"}}
			}
		}
		// Open composer with the task description pre-filled
		m.composer = newComposerModel()
		m.composer.SetValue(lastBlock.description)
		m.overlay = overlayComposer
		return m, func() tea.Msg {
			return LogLineMsg{Line: LogLine{Kind: lineSystem, Content: "retrying: " + lastBlock.description}}
		}

	case "sessions":
		m.overlay = overlaySessions
		return m, m.sessions.Init()

	case "help":
		var cmds []tea.Cmd
		for _, line := range helpLines() {
			l := line // capture
			cmds = append(cmds, func() tea.Msg {
				return LogLineMsg{Line: LogLine{Kind: lineSystem, Content: l}}
			})
		}
		return m, tea.Sequence(cmds...)
	}

	return m, nil
}

// languageFromExt extracts language identifier from file extension.
func languageFromExt(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return ""
	}
	ext = strings.ToLower(ext[1:]) // remove leading dot
	switch ext {
	case "go":
		return "go"
	case "ts":
		return "ts"
	case "tsx":
		return "tsx"
	case "js":
		return "js"
	case "jsx":
		return "jsx"
	case "py":
		return "py"
	case "rs":
		return "rs"
	case "md":
		return "md"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "toml":
		return "toml"
	default:
		return ext
	}
}
