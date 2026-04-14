package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alecpullen/marshal/internal/commands"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/loop"
	"github.com/alecpullen/marshal/internal/repomap"
	"github.com/alecpullen/marshal/internal/session"
	"github.com/alecpullen/marshal/internal/skills"
	"github.com/alecpullen/marshal/internal/watch"
)

// progRef is a shared mutable pointer so that the model (a value type) can
// reach the tea.Program after it is created.  run.go sets p before p.Run().
type progRef struct{ p *tea.Program }

// chatEntry is one logical row in the visible chat history.
type chatEntry struct {
	kind    string // "user" | "marshal" | "executor" | "pass" | "fail" | "system" | "lint"
	content string
}

// model is the top-level Bubbletea model.
type model struct {
	// Dependencies injected by run.go.
	runGate   func(ctx context.Context, prompt string) (action, text string, err error)
	runEngine func(ctx context.Context, prompt string, sink loop.Sink, executorExtra, criticExtra string, chatFiles, readOnlyFiles []string) error
	cfg       *config.Config
	gitSess   *git.Session
	repo      *git.Repo
	skillsReg *skills.Registry
	store     *session.Store
	sessionID string
	repoRoot  string
	pref      *progRef
	watchMgr  *watch.Manager

	ctx    context.Context
	cancel context.CancelFunc

	// UI components.
	viewport viewport.Model
	input    textarea.Model

	// State.
	entries         []chatEntry
	busy            bool
	width           int
	height          int
	streaming       *strings.Builder
	readOnlyFiles   []string
	chatFiles       []string
	multilineMode   bool
	thinkTokens     int
	reasoningEffort string
	cachedRepoMap   string
}

const (
	inputHeight  = 3
	borderPad    = 2
	statusHeight = 1
)

func newModel(
	ctx context.Context,
	runGate func(context.Context, string) (string, string, error),
	runEngine func(context.Context, string, loop.Sink, string, string, []string, []string) error,
	skillsReg *skills.Registry,
	store *session.Store,
	sessionID string,
	repoRoot string,
	readOnlyFiles []string,
	watchMgr *watch.Manager,
	pref *progRef,
	cfg *config.Config,
	gitSess *git.Session,
	repo *git.Repo,
) model {
	ctx, cancel := context.WithCancel(ctx)

	ta := textarea.New()
	ta.Placeholder = "Ask Marshal anything…"
	ta.Focus()
	ta.SetHeight(inputHeight)
	ta.CharLimit = 4000
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")

	vp := viewport.New(80, 20)
	vp.SetContent("")

	return model{
		runGate:       runGate,
		runEngine:     runEngine,
		cfg:           cfg,
		gitSess:       gitSess,
		repo:          repo,
		skillsReg:     skillsReg,
		store:         store,
		sessionID:     sessionID,
		repoRoot:      repoRoot,
		pref:          pref,
		watchMgr:      watchMgr,
		ctx:           ctx,
		cancel:        cancel,
		viewport:      vp,
		input:         ta,
		streaming:     &strings.Builder{},
		readOnlyFiles: readOnlyFiles,
	}
}

// --- tea.Model ---------------------------------------------------------------

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.relayout()

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.cancel()
			return m, tea.Quit

		case tea.KeyEsc:
			if m.busy {
				// Interrupt current streaming response without quitting.
				m.cancel()
				m = m.appendEntry("system", " interrupted")
				m = m.unlock()
				// Reset context for future operations.
				m.ctx, m.cancel = context.WithCancel(context.Background())
			} else {
				// When idle, clear the input field if it has content.
				if m.input.Value() != "" {
					m.input.Reset()
				}
			}

		case tea.KeyCtrlS:
			// Ctrl+S submits in multiline mode (or always as an alternative).
			if !m.busy {
				prompt := strings.TrimSpace(m.input.Value())
				if prompt != "" {
					m.input.Reset()
					if strings.HasPrefix(prompt, "/") {
						next, cmd := m.handleSlash(prompt)
						return next, cmd
					}
					m.input.Blur()
					m.busy = true
					m = m.appendEntry("user", prompt)
					cmds = append(cmds, m.callGate(prompt))
				}
			}

		case tea.KeyEnter:
			if !m.busy {
				if m.multilineMode {
					// In multiline mode, Enter inserts a newline in the textarea.
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					cmds = append(cmds, cmd)
				} else {
					prompt := strings.TrimSpace(m.input.Value())
					if prompt != "" {
						m.input.Reset()
						if strings.HasPrefix(prompt, "/") {
							next, cmd := m.handleSlash(prompt)
							return next, cmd
						}
						m.input.Blur()
						m.busy = true
						m = m.appendEntry("user", prompt)
						cmds = append(cmds, m.callGate(prompt))
					}
				}
			}

		default:
			if !m.busy {
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				cmds = append(cmds, cmd)
			}
		}

	// Marshal gate result ——————————————————————————————————————————————————

	case MarshalGateMsg:
		if msg.Err != nil {
			m = m.appendEntry("system", "gate error: "+msg.Err.Error())
			cmds = append(cmds, m.startEngine(msg.Prompt, "", ""))
			break
		}
		switch msg.Action {
		case "proceed":
			cmds = append(cmds, m.startEngine(msg.Prompt, "", ""))
		case "chat":
			m = m.appendEntry("marshal", msg.Text)
			m = m.unlock()
		default: // "clarify"
			m = m.appendEntry("marshal", msg.Text)
			m = m.unlock()
		}

	// Engine events ——————————————————————————————————————————————————————

	case RoundStartMsg:
		if msg.Round > 1 {
			m = m.flushStreaming()
			m = m.appendEntry("system",
				fmt.Sprintf("retrying (round %d/%d)…", msg.Round, msg.MaxRounds))
		}

	case LintErrorsMsg:
		m = m.flushStreaming()
		m = m.appendEntry("lint", "lint errors:\n"+msg.Summary)

	case TokenMsg:
		m.streaming.WriteString(msg.Content)
		m = m.rebuildViewport()

	case VerdictMsg:
		m = m.flushStreaming()
		if msg.Verdict == "PASS" {
			m = m.appendEntry("pass", "✓ PASS  "+msg.Summary)
		} else {
			m = m.appendEntry("fail", "✗ FAIL  "+msg.Summary)
		}

	case TaskMergedMsg:
		sha := msg.StagingSHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		m = m.appendEntry("system", fmt.Sprintf("merged to staging (%s)", sha))

	case TaskFailedMsg:
		m = m.appendEntry("system", "task failed after all rounds")

	case TaskDoneMsg:
		m = m.flushStreaming()
		if msg.Err != nil && !errors.Is(msg.Err, loop.ErrTaskFailed) {
			m = m.appendEntry("system", "error: "+msg.Err.Error())
		}
		m = m.unlock()

	// Shell/async command results ———————————————————————————————————————

	case ShellResultMsg:
		if msg.Err != nil {
			errText := msg.Err.Error()
			if msg.Output != "" {
				errText = msg.Output
			}
			m = m.appendEntry("system", "error: "+errText)
		} else if msg.Output != "" {
			m = m.appendEntry("system", msg.Output)
		} else {
			m = m.appendEntry("system", msg.Label+": done")
		}

	case MapRefreshedMsg:
		if msg.Err != nil {
			m = m.appendEntry("system", fmt.Sprintf("map refresh error: %v", msg.Err))
		} else {
			m.cachedRepoMap = msg.Map
			lines := strings.Count(msg.Map, "\n") + 1
			m = m.appendEntry("system", fmt.Sprintf("Repository map refreshed (%d lines)", lines))
		}

	case EditorResultMsg:
		if msg.Err != nil {
			m = m.appendEntry("system", fmt.Sprintf("editor error: %v", msg.Err))
		} else if msg.Text != "" {
			m.input.SetValue(msg.Text)
			m.input.Focus()
			hint := "Editor content loaded — press Enter to submit"
			if m.multilineMode {
				hint = "Editor content loaded — press Ctrl+S to submit"
			}
			m = m.appendEntry("system", hint)
		} else {
			m = m.appendEntry("system", "editor closed (no content)")
		}

	default:
		var vpCmd, taCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		if !m.busy {
			m.input, taCmd = m.input.Update(msg)
		}
		cmds = append(cmds, vpCmd, taCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading…"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.viewport.View(),
		m.renderInputBox(),
		m.renderStatus(),
	)
}

// --- layout ------------------------------------------------------------------

func (m model) relayout() model {
	m.viewport.Width = m.width
	m.viewport.Height = m.height - (inputHeight + borderPad) - statusHeight
	if m.viewport.Height < 1 {
		m.viewport.Height = 1
	}
	m.input.SetWidth(m.width - 4)
	return m.rebuildViewport()
}

// --- content -----------------------------------------------------------------

func (m model) unlock() model {
	m.busy = false
	m.input.Focus()
	return m
}

func (m model) appendEntry(kind, content string) model {
	m.entries = append(m.entries, chatEntry{kind: kind, content: content})
	return m.rebuildViewport()
}

func (m model) flushStreaming() model {
	if m.streaming.Len() == 0 {
		return m
	}
	m.entries = append(m.entries, chatEntry{kind: "executor", content: m.streaming.String()})
	m.streaming = &strings.Builder{}
	return m.rebuildViewport()
}

func (m model) rebuildViewport() model {
	var sb strings.Builder
	w := m.viewport.Width
	if w <= 0 {
		w = 80
	}
	for i, e := range m.entries {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(renderEntry(e, w))
	}
	if m.streaming.Len() > 0 {
		if len(m.entries) > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(styleExecutor.Width(w).Render(m.streaming.String()))
	}
	m.viewport.SetContent(sb.String())
	// Only auto-scroll if user is already at the bottom (following the stream).
	// This allows users to scroll up and read earlier content while streaming.
	if m.viewport.AtBottom() {
		m.viewport.GotoBottom()
	}
	return m
}

func renderEntry(e chatEntry, width int) string {
	switch e.kind {
	case "user":
		return stylePromptPrefix.Render("> ") + styleUser.Width(width-2).Render(e.content)
	case "marshal":
		return styleMarshal.Width(width).Render(e.content)
	case "executor":
		return styleExecutor.Width(width).Render(e.content)
	case "pass":
		return stylePass.Width(width).Render(e.content)
	case "fail":
		return styleFail.Width(width).Render(e.content)
	case "lint":
		return styleLint.Width(width).Render(e.content)
	default: // "system"
		return styleSystem.Width(width).Render(e.content)
	}
}

func (m model) renderInputBox() string {
	hint := "Enter: send"
	if m.multilineMode {
		hint = "Ctrl+S: send  Enter: newline"
	}
	m.input.Placeholder = "Ask Marshal anything… (" + hint + ")"
	return styleInputBorder.Width(m.width - 2).Render(m.input.View())
}

func (m model) renderStatus() string {
	var status string
	if m.busy {
		status = styleStatusActive.Render("● running")
	} else {
		status = styleStatusBar.Render("○ ready")
	}
	hint := styleStatusBar.Render("ctrl+c quit  shift+enter newline")
	gap := m.width - lipgloss.Width(status) - lipgloss.Width(hint)
	if gap < 1 {
		gap = 1
	}
	return status + strings.Repeat(" ", gap) + hint
}

// --- commands ----------------------------------------------------------------

// callGate returns a tea.Cmd that calls the Marshal model and delivers
// MarshalGateMsg.
func (m model) callGate(prompt string) tea.Cmd {
	runGate := m.runGate
	ctx := m.ctx
	return func() tea.Msg {
		action, text, err := runGate(ctx, prompt)
		return MarshalGateMsg{Action: action, Text: text, Prompt: prompt, Err: err}
	}
}

// startEngine returns a tea.Cmd that runs the executor-critic loop and
// delivers TaskDoneMsg when it finishes. It snapshots the current chatFiles
// and readOnlyFiles so mid-task /add commands don't affect in-flight work.
func (m model) startEngine(prompt, executorExtra, criticExtra string) tea.Cmd {
	pref := m.pref
	runEngine := m.runEngine
	ctx := m.ctx
	chatFiles := append([]string{}, m.chatFiles...)
	readOnlyFiles := append([]string{}, m.readOnlyFiles...)
	return func() tea.Msg {
		sink := NewChanSink(pref.p)
		err := runEngine(ctx, prompt, sink, executorExtra, criticExtra, chatFiles, readOnlyFiles)
		return TaskDoneMsg{Err: err}
	}
}

// handleSlash dispatches a "/" prefixed input to a built-in command or skill.
func (m model) handleSlash(input string) (tea.Model, tea.Cmd) {
	action, _ := commands.Dispatch(input, m.skillsReg)
	switch action.Kind {
	case commands.KindSkill:
		m = m.appendEntry("user", input)
		m.input.Blur()
		m.busy = true
		return m, m.startEngine(
			action.Prompt,
			action.Skill.Executor.SystemExtra,
			action.Skill.Critic.SystemExtra,
		)

	case commands.KindBuiltin:
		return m.handleBuiltin(action)

	default: // KindUnknown
		m = m.appendEntry("system",
			fmt.Sprintf("unknown command %q — type /help for available commands", action.Name))
		return m, nil
	}
}

// handleBuiltin executes a built-in slash command.
func (m model) handleBuiltin(a commands.Action) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch a.Name {

	// ── Async commands (shell / git / network) ──────────────────────────────

	case commands.CmdRun:
		m, cmd = m.handleRun(a)
	case commands.CmdTest:
		m, cmd = m.handleTest(a)
	case commands.CmdGit:
		m, cmd = m.handleGit(a)
	case commands.CmdLint:
		m, cmd = m.handleLint(a)
	case commands.CmdDiff:
		m, cmd = m.handleDiff()
	case commands.CmdCommit:
		m, cmd = m.handleCommit(a)
	case commands.CmdShip:
		m, cmd = m.handleShip(a)
	case commands.CmdUndo:
		m, cmd = m.handleUndo()
	case commands.CmdRevert:
		m, cmd = m.handleRevert(a)
	case commands.CmdWeb:
		m, cmd = m.handleWeb(a)
	case commands.CmdPaste:
		m, cmd = m.handlePaste()
	case commands.CmdCopy:
		m, cmd = m.handleCopy(a)
	case commands.CmdCopyContext:
		m, cmd = m.handleCopyContext()
	case commands.CmdEditor:
		m, cmd = m.handleEditor(a)
	case commands.CmdEdit:
		m, cmd = m.handleEdit(a)
	case commands.CmdMapRefresh:
		m, cmd = m.handleMapRefresh()

	// ── Sync commands ───────────────────────────────────────────────────────

	case commands.CmdSkills:
		m = m.appendEntry("system", m.formatSkillsList())
	case commands.CmdHelp:
		m = m.appendEntry("system", m.formatHelp())
	case commands.CmdHistory:
		m = m.appendEntry("system", m.formatHistory())
	case commands.CmdTokens:
		m = m.appendEntry("system", m.formatTokens())
	case commands.CmdMap:
		m = m.appendEntry("system", m.formatRepoMap())
	case commands.CmdSettings:
		m = m.appendEntry("system", m.formatSettings())
	case commands.CmdReport:
		m = m.appendEntry("system", m.formatReport())
	case commands.CmdAdd:
		m = m.handleAdd(a)
	case commands.CmdDrop:
		m = m.handleDrop(a)
	case commands.CmdLs:
		m = m.handleLs()
	case commands.CmdReadOnly:
		m = m.handleReadOnly(a)
	case commands.CmdVoice:
		m = m.appendEntry("system", "/voice — requires portaudio build tag (go build -tags portaudio)")
	case commands.CmdWatch:
		m = m.handleWatch(a)
	case commands.CmdUnwatch:
		m = m.handleUnwatch()
	case commands.CmdSave:
		m = m.handleSave(a)
	case commands.CmdLoad:
		m = m.handleLoad(a)
	case commands.CmdReset:
		m = m.handleReset()
	case commands.CmdClear:
		m = m.handleClear()
	case commands.CmdDiscard:
		m = m.handleDiscard()
	case commands.CmdSession:
		m = m.appendEntry("system", m.formatSession())
	case commands.CmdTask:
		if a.Prompt == "" {
			m = m.appendEntry("system", "usage: /task <description>")
		} else {
			// Submit task directly, bypassing the marshal gate.
			m.input.Blur()
			m.busy = true
			m = m.appendEntry("user", a.Prompt)
			return m, m.startEngine(a.Prompt, "", "")
		}
	case commands.CmdQuit:
		m.cancel()
		return m, tea.Quit
	case commands.CmdModel:
		m = m.handleModel(a)
	case commands.CmdThinkTokens:
		m = m.handleThinkTokens(a)
	case commands.CmdReasoningEffort:
		m = m.handleReasoningEffort(a)
	case commands.CmdMultilineMode:
		m = m.handleMultilineMode()

	default:
		m = m.appendEntry("system",
			fmt.Sprintf("unknown command %q — type /help for available commands", a.Name))
	}
	return m, cmd
}

// ─────────────────────────────────────────────────────────────────────────────
// Async command handlers (return model + tea.Cmd)
// ─────────────────────────────────────────────────────────────────────────────

func (m model) handleRun(a commands.Action) (model, tea.Cmd) {
	if a.Prompt == "" {
		m = m.appendEntry("system", "usage: /run <command>")
		return m, nil
	}
	label := "$ " + a.Prompt
	m = m.appendEntry("system", label)
	dir, prompt := m.repoRoot, a.Prompt
	return m, func() tea.Msg {
		out, err := runShell(dir, prompt)
		return ShellResultMsg{Label: label, Output: out, Err: err}
	}
}

func (m model) handleTest(a commands.Action) (model, tea.Cmd) {
	testCmd := detectTestCommand(m.repoRoot, a.Prompt)
	label := "$ " + testCmd
	m = m.appendEntry("system", label)
	dir := m.repoRoot
	return m, func() tea.Msg {
		out, err := runShell(dir, testCmd)
		return ShellResultMsg{Label: label, Output: out, Err: err}
	}
}

func (m model) handleGit(a commands.Action) (model, tea.Cmd) {
	if a.Prompt == "" {
		m = m.appendEntry("system", "usage: /git <args>")
		return m, nil
	}
	label := "$ git " + a.Prompt
	m = m.appendEntry("system", label)
	dir, args := m.repoRoot, a.Prompt
	return m, func() tea.Msg {
		out, err := runShell(dir, "git "+args)
		return ShellResultMsg{Label: label, Output: out, Err: err}
	}
}

func (m model) handleLint(a commands.Action) (model, tea.Cmd) {
	// Determine linter command from config or fallback.
	lintCmd := "golangci-lint run ./..."
	if m.cfg != nil && m.cfg.Linters.Go != "" {
		lintCmd = m.cfg.Linters.Go + " ./..."
	}
	if a.Prompt != "" {
		base := "golangci-lint run"
		if m.cfg != nil && m.cfg.Linters.Go != "" {
			base = m.cfg.Linters.Go
		}
		lintCmd = base + " " + a.Prompt
	}
	label := "$ " + lintCmd
	m = m.appendEntry("system", label)
	dir := m.repoRoot
	return m, func() tea.Msg {
		out, err := runShell(dir, lintCmd)
		return ShellResultMsg{Label: label, Output: out, Err: err}
	}
}

func (m model) handleDiff() (model, tea.Cmd) {
	label := "$ git diff HEAD"
	m = m.appendEntry("system", label)
	dir := m.repoRoot
	return m, func() tea.Msg {
		out, err := runShell(dir, "git diff HEAD")
		return ShellResultMsg{Label: label, Output: out, Err: err}
	}
}

func (m model) handleCommit(a commands.Action) (model, tea.Cmd) {
	if m.repo == nil {
		m = m.appendEntry("system", "git not available")
		return m, nil
	}
	msg := a.Prompt
	if msg == "" {
		msg = "marshal: manual commit"
	}
	m = m.appendEntry("system", "committing: "+msg)
	repo := m.repo
	return m, func() tea.Msg {
		if err := repo.CommitAll(msg); err != nil {
			return ShellResultMsg{Label: "/commit", Output: err.Error(), Err: err}
		}
		return ShellResultMsg{Label: "/commit", Output: "committed: " + msg}
	}
}

func (m model) handleShip(a commands.Action) (model, tea.Cmd) {
	if m.gitSess == nil {
		m = m.appendEntry("system", "git integration not enabled (set git.enabled=true in marshal.toml)")
		return m, nil
	}
	msg := a.Prompt
	if msg == "" {
		msg = "marshal: ship session"
	}
	target := m.gitSess.TargetBranch
	m = m.appendEntry("system", fmt.Sprintf("shipping to %s…", target))
	gitSess := m.gitSess
	store := m.store
	sessID := m.sessionID
	return m, func() tea.Msg {
		sha, err := gitSess.Ship(msg)
		if err != nil {
			return ShellResultMsg{Label: "/ship", Output: err.Error(), Err: err}
		}
		short := sha
		if len(short) > 8 {
			short = short[:8]
		}
		if store != nil && sessID != "" {
			_ = store.ShipSession(sessID, gitSess.StagingBranch, sha)
		}
		return ShellResultMsg{Label: "/ship",
			Output: fmt.Sprintf("shipped to %s (%s)\nnew staging: %s", target, short, gitSess.StagingBranch)}
	}
}

func (m model) handleUndo() (model, tea.Cmd) {
	if m.repo == nil {
		m = m.appendEntry("system", "git not available")
		return m, nil
	}
	m = m.appendEntry("system", "reverting last commit on staging branch…")
	dir := m.repoRoot
	return m, func() tea.Msg {
		out, err := runShell(dir, "git revert HEAD --no-edit")
		return ShellResultMsg{Label: "/undo", Output: out, Err: err}
	}
}

func (m model) handleRevert(a commands.Action) (model, tea.Cmd) {
	if a.Arg == "" {
		m = m.appendEntry("system", "usage: /revert <task-id>")
		return m, nil
	}
	if m.store == nil {
		m = m.appendEntry("system", "session store not available")
		return m, nil
	}
	m = m.appendEntry("system", fmt.Sprintf("reverting task %s…", a.Arg))
	store := m.store
	sessID := m.sessionID
	dir := m.repoRoot
	taskID := a.Arg
	return m, func() tea.Msg {
		task, err := store.GetTask(taskID)
		if err != nil {
			return ShellResultMsg{Label: "/revert", Output: "", Err: fmt.Errorf("task %q not found: %w", taskID, err)}
		}
		if task.SessionID != sessID {
			return ShellResultMsg{Label: "/revert", Output: "", Err: fmt.Errorf("task %s belongs to a different session", taskID)}
		}
		if task.StagingSHA == nil || *task.StagingSHA == "" {
			return ShellResultMsg{Label: "/revert", Output: "", Err: fmt.Errorf("task %s has no staging SHA (may not have passed)", taskID)}
		}
		out, err := runShell(dir, "git revert "+*task.StagingSHA+" --no-edit")
		return ShellResultMsg{Label: "/revert", Output: out, Err: err}
	}
}

func (m model) handleWeb(a commands.Action) (model, tea.Cmd) {
	if a.Arg == "" {
		m = m.appendEntry("system", "usage: /web <url>")
		return m, nil
	}
	url := a.Arg
	m = m.appendEntry("system", "fetching "+url+"…")
	return m, func() tea.Msg {
		text, err := fetchWebContent(url)
		if err != nil {
			return ShellResultMsg{Label: "/web", Output: "", Err: err}
		}
		return ShellResultMsg{Label: "/web", Output: text}
	}
}

func (m model) handlePaste() (model, tea.Cmd) {
	m = m.appendEntry("system", "reading clipboard…")
	return m, func() tea.Msg {
		text, err := clipboard.ReadAll()
		if err != nil {
			return ShellResultMsg{Label: "/paste", Output: "", Err: fmt.Errorf("clipboard: %w", err)}
		}
		if strings.TrimSpace(text) == "" {
			return ShellResultMsg{Label: "/paste", Output: "(clipboard is empty)"}
		}
		return ShellResultMsg{Label: "/paste", Output: text}
	}
}

func (m model) handleCopy(a commands.Action) (model, tea.Cmd) {
	text := a.Arg
	if text == "" {
		// Copy last executor response.
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].kind == "executor" || m.entries[i].kind == "pass" {
				text = m.entries[i].content
				break
			}
		}
	}
	if text == "" {
		m = m.appendEntry("system", "nothing to copy — provide text or run a task first")
		return m, nil
	}
	m = m.appendEntry("system", "copying to clipboard…")
	return m, func() tea.Msg {
		if err := clipboard.WriteAll(text); err != nil {
			return ShellResultMsg{Label: "/copy", Output: "", Err: fmt.Errorf("clipboard: %w", err)}
		}
		return ShellResultMsg{Label: "/copy", Output: "copied to clipboard"}
	}
}

func (m model) handleCopyContext() (model, tea.Cmd) {
	var sb strings.Builder
	for _, e := range m.entries {
		switch e.kind {
		case "user":
			sb.WriteString("> ")
			sb.WriteString(e.content)
		case "executor":
			sb.WriteString(e.content)
		case "marshal":
			sb.WriteString("[marshal] ")
			sb.WriteString(e.content)
		default:
			sb.WriteString("[")
			sb.WriteString(e.kind)
			sb.WriteString("] ")
			sb.WriteString(e.content)
		}
		sb.WriteString("\n\n")
	}
	text := sb.String()
	m = m.appendEntry("system", "copying context to clipboard…")
	return m, func() tea.Msg {
		if err := clipboard.WriteAll(text); err != nil {
			return ShellResultMsg{Label: "/copy-context", Output: "", Err: fmt.Errorf("clipboard: %w", err)}
		}
		return ShellResultMsg{Label: "/copy-context", Output: "context copied to clipboard"}
	}
}

func (m model) handleEditor(a commands.Action) (model, tea.Cmd) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	// Create a temp file for notes; the content is loaded into the input on close.
	f, err := os.CreateTemp("", "marshal-note-*.md")
	if err != nil {
		m = m.appendEntry("system", fmt.Sprintf("failed to create temp file: %v", err))
		return m, nil
	}
	tmpPath := f.Name()
	f.Close()

	editorCmd := exec.Command(editor, tmpPath)
	return m, tea.ExecProcess(editorCmd, func(err error) tea.Msg {
		if err != nil {
			return EditorResultMsg{Err: err}
		}
		data, readErr := os.ReadFile(tmpPath)
		_ = os.Remove(tmpPath)
		if readErr != nil {
			return EditorResultMsg{Err: readErr}
		}
		return EditorResultMsg{Text: strings.TrimSpace(string(data))}
	})
}

func (m model) handleEdit(a commands.Action) (model, tea.Cmd) {
	if a.Prompt == "" {
		m = m.appendEntry("system", "usage: /edit <file>")
		return m, nil
	}

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	rel := filepath.Clean(a.Prompt)
	if strings.HasPrefix(rel, "..") {
		m = m.appendEntry("system", fmt.Sprintf("path outside repo: %s", a.Prompt))
		return m, nil
	}
	filePath := filepath.Join(m.repoRoot, rel)

	editorCmd := exec.Command(editor, filePath)
	return m, tea.ExecProcess(editorCmd, func(err error) tea.Msg {
		if err != nil {
			return EditorResultMsg{Err: err}
		}
		return EditorResultMsg{Text: ""}
	})
}

func (m model) handleMapRefresh() (model, tea.Cmd) {
	if m.repo == nil {
		m = m.appendEntry("system", "no repository available")
		return m, nil
	}
	m = m.appendEntry("system", "rebuilding repository map…")
	root := m.repo.Root()
	return m, func() tea.Msg {
		ig, _ := git.LoadMarshalIgnore(root)
		rm, err := repomap.Build(root, ig, repomap.Options{})
		if err != nil {
			return MapRefreshedMsg{Err: err}
		}
		return MapRefreshedMsg{Map: rm.String()}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Sync command handlers (return model only)
// ─────────────────────────────────────────────────────────────────────────────

func (m model) handleAdd(a commands.Action) model {
	if len(a.Args) == 0 {
		m = m.appendEntry("system", "usage: /add <file1> [file2] …")
		return m
	}
	added := 0
	for _, path := range a.Args {
		path = filepath.Clean(path)
		if strings.HasPrefix(path, "..") {
			m = m.appendEntry("system", "  skipped "+path+" (outside repo)")
			continue
		}
		abs := filepath.Join(m.repoRoot, path)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			m = m.appendEntry("system", "  skipped "+path+" (not found)")
			continue
		}
		found := false
		for _, f := range m.chatFiles {
			if f == path {
				found = true
				break
			}
		}
		if !found {
			m.chatFiles = append(m.chatFiles, path)
			added++
		}
	}
	if added > 0 {
		m = m.appendEntry("system", fmt.Sprintf("Added %d file(s) to chat context (included in next task)", added))
	} else {
		m = m.appendEntry("system", "No new files added (already in context or not found)")
	}
	return m
}

func (m model) handleDrop(a commands.Action) model {
	if len(a.Args) == 0 {
		m = m.appendEntry("system", "usage: /drop <file1> [file2] …")
		return m
	}
	dropped := 0
	for _, path := range a.Args {
		path = filepath.Clean(path)
		for i, f := range m.chatFiles {
			if f == path {
				m.chatFiles = append(m.chatFiles[:i], m.chatFiles[i+1:]...)
				dropped++
				break
			}
		}
	}
	if dropped > 0 {
		m = m.appendEntry("system", fmt.Sprintf("Removed %d file(s) from chat context", dropped))
	} else {
		m = m.appendEntry("system", "No files removed (not in chat context)")
	}
	return m
}

func (m model) handleLs() model {
	if len(m.chatFiles) == 0 && len(m.readOnlyFiles) == 0 {
		m = m.appendEntry("system", "No files in context.\nUse /add <file> to add files or /read-only <file> for read-only context.")
		return m
	}
	var sb strings.Builder
	if len(m.chatFiles) > 0 {
		sb.WriteString("Chat context files:\n")
		for _, f := range m.chatFiles {
			sb.WriteString("  " + f + "\n")
		}
	}
	if len(m.readOnlyFiles) > 0 {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("Read-only context files:\n")
		for _, f := range m.readOnlyFiles {
			sb.WriteString("  " + f + " (read-only)\n")
		}
	}
	m = m.appendEntry("system", strings.TrimRight(sb.String(), "\n"))
	return m
}

func (m model) handleReadOnly(a commands.Action) model {
	if len(a.Args) == 0 {
		if len(m.readOnlyFiles) == 0 {
			m = m.appendEntry("system", "Read-only files: none")
		} else {
			var sb strings.Builder
			sb.WriteString("Read-only files:\n")
			for _, f := range m.readOnlyFiles {
				sb.WriteString("  " + f + "\n")
			}
			m = m.appendEntry("system", strings.TrimRight(sb.String(), "\n"))
		}
		return m
	}
	added := 0
	for _, path := range a.Args {
		path = filepath.Clean(path)
		if strings.HasPrefix(path, "..") {
			m = m.appendEntry("system", "  skipped "+path+" (outside repo)")
			continue
		}
		abs := filepath.Join(m.repoRoot, path)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			m = m.appendEntry("system", "  skipped "+path+" (not found)")
			continue
		}
		if m.store != nil && m.sessionID != "" {
			if err := m.store.AddReadOnlyFile(m.sessionID, path); err != nil {
				m = m.appendEntry("system", fmt.Sprintf("  error adding %s: %v", path, err))
				continue
			}
		}
		found := false
		for _, f := range m.readOnlyFiles {
			if f == path {
				found = true
				break
			}
		}
		if !found {
			m.readOnlyFiles = append(m.readOnlyFiles, path)
			added++
		}
	}
	if added > 0 {
		m = m.appendEntry("system", fmt.Sprintf("Added %d file(s) to read-only context", added))
	} else {
		m = m.appendEntry("system", "No new files added (may already be in context or not found)")
	}
	return m
}

func (m model) handleClear() model {
	m.entries = nil
	m.streaming.Reset()
	m = m.rebuildViewport()
	m = m.appendEntry("system", "Chat display cleared.")
	return m
}

func (m model) handleWatch(a commands.Action) model {
	if m.watchMgr == nil {
		m = m.appendEntry("system", "Watch mode not available (no repository)")
		return m
	}
	if m.watchMgr.IsActive() {
		m = m.appendEntry("system", "Watch mode already active. Use /unwatch to stop.")
		return m
	}
	if err := m.watchMgr.Start(); err != nil {
		m = m.appendEntry("system", fmt.Sprintf("Failed to start watch mode: %v", err))
		return m
	}
	msg := "Watch mode started. Monitoring files for // ai: markers…"
	if a.Arg != "" {
		msg = fmt.Sprintf("Watch mode started on %s. Monitoring for // ai: markers…", a.Arg)
	}
	m = m.appendEntry("system", msg)
	return m
}

func (m model) handleUnwatch() model {
	if m.watchMgr == nil || !m.watchMgr.IsActive() {
		m = m.appendEntry("system", "Watch mode is not active.")
		return m
	}
	if err := m.watchMgr.Stop(); err != nil {
		m = m.appendEntry("system", fmt.Sprintf("Error stopping watch mode: %v", err))
		return m
	}
	m = m.appendEntry("system", "Watch mode stopped.")
	return m
}

func (m model) handleSave(a commands.Action) model {
	name := a.Arg
	if name == "" {
		name = "default"
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		m = m.appendEntry("system", "error: cannot determine home directory")
		return m
	}
	saveDir := filepath.Join(homeDir, ".config", "marshal", "contexts")
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		m = m.appendEntry("system", fmt.Sprintf("error creating save directory: %v", err))
		return m
	}
	type savedCtx struct {
		ChatFiles    []string `json:"chat_files"`
		ReadOnlyFiles []string `json:"read_only_files"`
	}
	data, _ := json.Marshal(savedCtx{ChatFiles: m.chatFiles, ReadOnlyFiles: m.readOnlyFiles})
	path := filepath.Join(saveDir, name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		m = m.appendEntry("system", fmt.Sprintf("error saving: %v", err))
		return m
	}
	m = m.appendEntry("system", fmt.Sprintf("Context saved as %q (%s)", name, path))
	return m
}

func (m model) handleLoad(a commands.Action) model {
	name := a.Arg
	if name == "" {
		name = "default"
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		m = m.appendEntry("system", "error: cannot determine home directory")
		return m
	}
	path := filepath.Join(homeDir, ".config", "marshal", "contexts", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		m = m.appendEntry("system", fmt.Sprintf("no saved context %q — use /save to create one", name))
		return m
	}
	type savedCtx struct {
		ChatFiles    []string `json:"chat_files"`
		ReadOnlyFiles []string `json:"read_only_files"`
	}
	var ctx savedCtx
	if err := json.Unmarshal(data, &ctx); err != nil {
		m = m.appendEntry("system", fmt.Sprintf("error loading context: %v", err))
		return m
	}
	m.chatFiles = ctx.ChatFiles
	m.readOnlyFiles = ctx.ReadOnlyFiles
	m = m.appendEntry("system", fmt.Sprintf("Context loaded from %q (%d files, %d read-only)",
		name, len(ctx.ChatFiles), len(ctx.ReadOnlyFiles)))
	return m
}

func (m model) handleReset() model {
	m.chatFiles = nil
	m.readOnlyFiles = nil
	if m.store != nil && m.sessionID != "" {
		_ = m.store.ClearReadOnlyFiles(m.sessionID)
	}
	m = m.appendEntry("system", "Context reset: all chat files and read-only files cleared.")
	return m
}

func (m model) handleDiscard() model {
	m.streaming.Reset()
	m = m.rebuildViewport()
	m = m.appendEntry("system", "Discarded pending output. (Git staging branch is unchanged — use /undo to revert committed changes.)")
	return m
}

func (m model) handleModel(a commands.Action) model {
	if m.cfg == nil {
		m = m.appendEntry("system", "Model info not available")
		return m
	}
	if a.Arg != "" {
		m = m.appendEntry("system", fmt.Sprintf(
			"Runtime model switching is not supported.\nEdit marshal.toml to change models.\nCurrent executor: %s",
			m.cfg.Model.Executor.Model))
		return m
	}
	cfg := m.cfg
	var sb strings.Builder
	sb.WriteString("Active models:\n")
	sb.WriteString(fmt.Sprintf("  executor:  %s (%s)\n", cfg.Model.Executor.Model, cfg.Model.Executor.Provider))
	sb.WriteString(fmt.Sprintf("  critic:    %s (%s)\n", cfg.Model.Critic.Model, cfg.Model.Critic.Provider))
	sb.WriteString(fmt.Sprintf("  marshal:   %s (%s)\n", cfg.Model.Marshal.Model, cfg.Model.Marshal.Provider))
	if cfg.Model.Compactor.Model != "" {
		sb.WriteString(fmt.Sprintf("  compactor: %s (%s)\n", cfg.Model.Compactor.Model, cfg.Model.Compactor.Provider))
	}
	sb.WriteString("\nEdit marshal.toml to switch models.")
	m = m.appendEntry("system", strings.TrimRight(sb.String(), "\n"))
	return m
}

func (m model) handleThinkTokens(a commands.Action) model {
	if a.Arg == "" {
		if m.thinkTokens > 0 {
			m = m.appendEntry("system", fmt.Sprintf("Think tokens budget: %d (applies to next task)", m.thinkTokens))
		} else {
			m = m.appendEntry("system", "Think tokens: not set. Usage: /think-tokens <n>")
		}
		return m
	}
	n, err := strconv.Atoi(a.Arg)
	if err != nil || n < 0 {
		m = m.appendEntry("system", "usage: /think-tokens <n>  (non-negative integer, 0 to clear)")
		return m
	}
	m.thinkTokens = n
	if n == 0 {
		m = m.appendEntry("system", "Think tokens budget cleared.")
	} else {
		m = m.appendEntry("system", fmt.Sprintf("Think tokens budget set to %d (applies to next task).", n))
	}
	return m
}

func (m model) handleReasoningEffort(a commands.Action) model {
	if a.Arg == "" {
		if m.reasoningEffort != "" {
			m = m.appendEntry("system", fmt.Sprintf("Reasoning effort: %s (applies to next task)", m.reasoningEffort))
		} else {
			m = m.appendEntry("system", "Reasoning effort: not set. Usage: /reasoning-effort <low|medium|high>")
		}
		return m
	}
	switch strings.ToLower(a.Arg) {
	case "low", "medium", "high":
		m.reasoningEffort = strings.ToLower(a.Arg)
		m = m.appendEntry("system", fmt.Sprintf("Reasoning effort set to %s (applies to next task).", m.reasoningEffort))
	case "none", "off", "":
		m.reasoningEffort = ""
		m = m.appendEntry("system", "Reasoning effort cleared.")
	default:
		m = m.appendEntry("system", "usage: /reasoning-effort <low|medium|high>")
	}
	return m
}

func (m model) handleMultilineMode() model {
	m.multilineMode = !m.multilineMode
	if m.multilineMode {
		m.input.KeyMap.InsertNewline.SetKeys("enter")
		m = m.appendEntry("system", "Multiline mode ON: Enter adds newline, Ctrl+S sends message.")
	} else {
		m.input.KeyMap.InsertNewline.SetKeys("shift+enter")
		m = m.appendEntry("system", "Multiline mode OFF: Enter sends message, Shift+Enter adds newline.")
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// Format helpers
// ─────────────────────────────────────────────────────────────────────────────

func (m model) formatSkillsList() string {
	all := m.skillsReg.All()
	if len(all) == 0 {
		return "No skills loaded. Place .toml files in ~/.config/marshal/skills/"
	}
	var sb strings.Builder
	sb.WriteString("Available skills:\n")
	for _, s := range all {
		sb.WriteString(fmt.Sprintf("  %-14s %s\n", s.Trigger, s.Description))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (m model) formatHistory() string {
	if m.store == nil || m.sessionID == "" {
		return "No history available (store not initialized)."
	}
	tasks, err := m.store.TasksForSession(m.sessionID)
	if err != nil {
		return fmt.Sprintf("Error loading history: %v", err)
	}
	if len(tasks) == 0 {
		return "No tasks in current session."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session %s — %d task(s):\n", m.sessionID, len(tasks)))
	for _, t := range tasks {
		status := string(t.Status)
		if status == "" {
			status = "running"
		}
		summary := t.Prompt
		if t.Summary != nil && *t.Summary != "" {
			summary = *t.Summary
		}
		if len(summary) > 60 {
			summary = summary[:57] + "…"
		}
		sb.WriteString(fmt.Sprintf("  [%-8s] %-10s %s\n", status, t.ID, summary))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (m model) formatTokens() string {
	if m.store == nil || m.sessionID == "" {
		return "Token usage: not available (store not initialized)."
	}
	rounds, err := m.store.RoundsForSession(m.sessionID)
	if err != nil {
		return fmt.Sprintf("Token usage: error loading: %v", err)
	}
	if len(rounds) == 0 {
		return "Token usage: no rounds recorded this session."
	}
	var totalP, totalC int
	taskSet := make(map[string]struct{})
	for _, r := range rounds {
		totalP += r.PromptTokens
		totalC += r.CompletionTokens
		taskSet[r.TaskID] = struct{}{}
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Token usage — session %s:\n", m.sessionID))
	sb.WriteString(fmt.Sprintf("  Tasks:             %d\n", len(taskSet)))
	sb.WriteString(fmt.Sprintf("  Rounds:            %d\n", len(rounds)))
	sb.WriteString(fmt.Sprintf("  Prompt tokens:     %d\n", totalP))
	sb.WriteString(fmt.Sprintf("  Completion tokens: %d\n", totalC))
	sb.WriteString(fmt.Sprintf("  Total tokens:      %d\n", totalP+totalC))
	return strings.TrimRight(sb.String(), "\n")
}

func (m model) formatRepoMap() string {
	if m.cachedRepoMap != "" {
		return "Repository map:\n```\n" + m.cachedRepoMap + "\n```"
	}
	if m.repo == nil {
		return "No repository available. Start marshal from a git repo."
	}
	ig, _ := git.LoadMarshalIgnore(m.repoRoot)
	rm, err := repomap.Build(m.repoRoot, ig, repomap.Options{})
	if err != nil {
		return fmt.Sprintf("Error building repo map: %v", err)
	}
	m.cachedRepoMap = rm.String()
	return "Repository map:\n```\n" + m.cachedRepoMap + "\n```"
}

func (m model) formatSettings() string {
	if m.cfg == nil {
		return "Settings not available (config not loaded)."
	}
	cfg := m.cfg
	var sb strings.Builder
	sb.WriteString("Active settings:\n")
	sb.WriteString(fmt.Sprintf("  executor:       %s (%s)\n", cfg.Model.Executor.Model, cfg.Model.Executor.Provider))
	sb.WriteString(fmt.Sprintf("  critic:         %s (%s)\n", cfg.Model.Critic.Model, cfg.Model.Critic.Provider))
	sb.WriteString(fmt.Sprintf("  marshal:        %s (%s)\n", cfg.Model.Marshal.Model, cfg.Model.Marshal.Provider))
	sb.WriteString(fmt.Sprintf("  edit_format:    %s\n", cfg.Loop.EditFormat))
	sb.WriteString(fmt.Sprintf("  max_rounds:     %d\n", cfg.Loop.MaxRounds))
	sb.WriteString(fmt.Sprintf("  compact_after:  %d\n", cfg.Loop.CompactAfter))
	sb.WriteString(fmt.Sprintf("  git_enabled:    %v\n", cfg.Git.Enabled))
	if cfg.Linters.Go != "" {
		sb.WriteString(fmt.Sprintf("  linter.go:      %s\n", cfg.Linters.Go))
	}
	if m.thinkTokens > 0 {
		sb.WriteString(fmt.Sprintf("  think_tokens:   %d\n", m.thinkTokens))
	}
	if m.reasoningEffort != "" {
		sb.WriteString(fmt.Sprintf("  reasoning:      %s\n", m.reasoningEffort))
	}
	sb.WriteString(fmt.Sprintf("  multiline_mode: %v\n", m.multilineMode))
	return strings.TrimRight(sb.String(), "\n")
}

func (m model) formatSession() string {
	if m.sessionID == "" {
		return "No active session."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session ID: %s\n", m.sessionID))
	if m.gitSess != nil {
		sb.WriteString(fmt.Sprintf("Target branch:  %s\n", m.gitSess.TargetBranch))
		sb.WriteString(fmt.Sprintf("Staging branch: %s\n", m.gitSess.StagingBranch))
		if sha := m.gitSess.TargetStartSHA; len(sha) >= 8 {
			sb.WriteString(fmt.Sprintf("Started from:   %s\n", sha[:8]))
		}
	} else {
		sb.WriteString("Git: disabled\n")
	}
	if m.store != nil {
		tasks, err := m.store.TasksForSession(m.sessionID)
		if err == nil {
			passed, failed := 0, 0
			for _, t := range tasks {
				switch t.Status {
				case session.StatusPassed:
					passed++
				case session.StatusFailed:
					failed++
				}
			}
			sb.WriteString(fmt.Sprintf("Tasks: %d total, %d passed, %d failed\n",
				len(tasks), passed, failed))
		}
	}
	if len(m.chatFiles) > 0 {
		sb.WriteString(fmt.Sprintf("Chat files: %d\n", len(m.chatFiles)))
	}
	if len(m.readOnlyFiles) > 0 {
		sb.WriteString(fmt.Sprintf("Read-only files: %d\n", len(m.readOnlyFiles)))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (m model) formatReport() string {
	if m.store == nil || m.sessionID == "" {
		return "No session data available."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session Report: %s\n", m.sessionID))
	sb.WriteString(strings.Repeat("─", 40) + "\n")
	tasks, err := m.store.TasksForSession(m.sessionID)
	if err == nil {
		passed, failed, reverted := 0, 0, 0
		for _, t := range tasks {
			switch t.Status {
			case session.StatusPassed:
				passed++
			case session.StatusFailed:
				failed++
			case session.StatusRevertedByUser:
				reverted++
			}
		}
		sb.WriteString(fmt.Sprintf("Tasks: %d total, %d passed, %d failed, %d reverted\n",
			len(tasks), passed, failed, reverted))
	}
	rounds, err := m.store.RoundsForSession(m.sessionID)
	if err == nil && len(rounds) > 0 {
		var totalP, totalC int
		for _, r := range rounds {
			totalP += r.PromptTokens
			totalC += r.CompletionTokens
		}
		sb.WriteString(fmt.Sprintf("Tokens: %d prompt, %d completion, %d total\n",
			totalP, totalC, totalP+totalC))
	}
	if m.gitSess != nil {
		sb.WriteString(fmt.Sprintf("Target branch: %s\n", m.gitSess.TargetBranch))
		sb.WriteString(fmt.Sprintf("Staging branch: %s\n", m.gitSess.StagingBranch))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (m model) formatHelp() string {
	submit := "Enter: send"
	if m.multilineMode {
		submit = "Ctrl+S: send"
	}
	return `Commands:

Git Workflow:
  /ship [msg]          squash-merge staging branch to target branch
  /undo                revert the most recent commit on the staging branch
  /revert <id>         revert a specific task by ID
  /commit [msg]        commit current changes

Info/Display:
  /history             show task ledger for current session
  /skills              list available skill extensions
  /help                show this help
  /tokens              show token usage statistics
  /map                 show repository map
  /map-refresh         rebuild the repository map
  /settings            show current settings
  /report              generate a session report
  /session             show session information
  /model [name]        show active models (runtime switching: edit marshal.toml)

File Management:
  /add <file>…         add file(s) to the chat context
  /drop <file>…        remove file(s) from the chat context
  /ls                  list files in the current context
  /diff                show git diff HEAD
  /read-only <file>…   add file(s) as read-only context

Shell/Execution:
  /run <cmd>           run a shell command
  /test [args]         run tests (auto-detects runner: go/npm/pytest/cargo)
  /git <args>          run a git command
  /lint [files]        run linter

External Integration:
  /web <url>           fetch and display web page content
  /paste               paste from clipboard into chat
  /copy [text]         copy text (or last response) to clipboard
  /copy-context        copy full chat context to clipboard
  /voice               record voice (requires -tags portaudio build)
  /watch [dir]         watch files for // ai: markers
  /unwatch             stop watching files

Session/State:
  /save [name]         save context file list (~/.config/marshal/contexts/<name>.json)
  /load [name]         load a saved context file list
  /reset               clear all context files
  /clear               clear the chat display
  /discard             discard pending output buffer
  /task <desc>         submit a task directly (skips marshal gate)
  /quit                exit the application

Editor/Context:
  /editor              open $EDITOR for a note (content loaded into input on close)
  /edit <file>         open a file in $EDITOR

Model/Configuration:
  /think-tokens <n>    set thinking token budget (0 to clear)
  /reasoning-effort <low|medium|high>   set reasoning effort level
  /multiline-mode      toggle multiline input (` + submit + `)

Skills from ~/.config/marshal/skills/*.toml are also available.
Press Ctrl+C or type /quit to exit.`
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// runShell executes command via /bin/sh in dir and returns combined output.
func runShell(dir, command string) (string, error) {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// detectTestCommand returns the appropriate test command for the repo.
func detectTestCommand(repoRoot, args string) string {
	check := func(file string) bool {
		_, err := os.Stat(filepath.Join(repoRoot, file))
		return err == nil
	}
	switch {
	case check("go.mod"):
		if args != "" {
			return "go test " + args
		}
		return "go test ./..."
	case check("package.json"):
		if args != "" {
			return "npm test -- " + args
		}
		return "npm test"
	case check("Cargo.toml"):
		if args != "" {
			return "cargo test " + args
		}
		return "cargo test"
	case check("setup.py") || check("pyproject.toml") || check("setup.cfg"):
		if args != "" {
			return "pytest " + args
		}
		return "pytest"
	default:
		if args != "" {
			return args
		}
		return "echo 'No test runner detected. Use /run to run tests manually.'"
	}
}

// fetchWebContent fetches a URL and returns plain-text content (HTML stripped).
func fetchWebContent(url string) (string, error) {
	resp, err := http.Get(url) //nolint:gosec // URL comes from the user intentionally
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	text := stripHTML(string(body))
	if len(text) > 8000 {
		text = text[:8000] + "\n[truncated]"
	}
	return text, nil
}

var (
	reHTMLTag   = regexp.MustCompile(`<[^>]+>`)
	reWhitespace = regexp.MustCompile(`[ \t]+`)
)

// stripHTML removes HTML tags and normalises whitespace.
func stripHTML(s string) string {
	s = reHTMLTag.ReplaceAllString(s, "")
	// Decode common HTML entities.
	replacer := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&nbsp;", " ",
		"&ndash;", "–", "&mdash;", "—",
	)
	s = replacer.Replace(s)
	// Collapse inline whitespace, preserve newlines.
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(reWhitespace.ReplaceAllString(l, " "))
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
