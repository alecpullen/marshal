package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alec/marshal/internal/commands"
	"github.com/alec/marshal/internal/loop"
	"github.com/alec/marshal/internal/session"
	"github.com/alec/marshal/internal/skills"
	"github.com/alec/marshal/internal/watch"
)

// progRef is a shared mutable pointer so that the model (a value type) can
// reach the tea.Program after it is created.  run.go sets p before p.Run().
type progRef struct{ p *tea.Program }

// chatEntry is one logical row in the visible chat history.
type chatEntry struct {
	kind    string // "user" | "marshal" | "executor" | "pass" | "fail" | "system"
	content string
}

// model is the top-level Bubbletea model.
type model struct {
	// Dependencies injected by run.go.
	runGate   func(ctx context.Context, prompt string) (action, text string, err error)
	runEngine func(ctx context.Context, prompt string, sink loop.Sink, executorExtra, criticExtra string) error
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
	entries       []chatEntry
	busy          bool
	width         int
	height        int
	streaming     *strings.Builder // in-progress executor tokens
	readOnlyFiles []string         // read-only files for this session
}

const (
	inputHeight  = 3
	borderPad    = 2
	statusHeight = 1
)

func newModel(
	ctx context.Context,
	runGate func(context.Context, string) (string, string, error),
	runEngine func(context.Context, string, loop.Sink, string, string) error,
	skillsReg *skills.Registry,
	store *session.Store,
	sessionID string,
	repoRoot string,
	readOnlyFiles []string,
	watchMgr *watch.Manager,
	pref *progRef,
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
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancel()
			return m, tea.Quit
		case tea.KeyEnter:
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
		default:
			if !m.busy {
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				cmds = append(cmds, cmd)
			}
		}

	// Marshal gate result —————————————————————————————————————————————————

	case MarshalGateMsg:
		if msg.Err != nil {
			// Gate failed: treat as PROCEED so the user doesn't lose their work.
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
	m.viewport.GotoBottom()
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
// MarshalGateMsg.  The gate call is non-streaming (the response is short).
func (m model) callGate(prompt string) tea.Cmd {
	runGate := m.runGate
	ctx := m.ctx
	return func() tea.Msg {
		action, text, err := runGate(ctx, prompt)
		return MarshalGateMsg{Action: action, Text: text, Prompt: prompt, Err: err}
	}
}

// startEngine returns a tea.Cmd that runs the executor-critic loop in a
// goroutine and delivers TaskDoneMsg when it finishes.
// executorExtra and criticExtra are skill-provided system-prompt additions;
// pass empty strings for a plain (no-skill) run.
func (m model) startEngine(prompt, executorExtra, criticExtra string) tea.Cmd {
	pref := m.pref
	runEngine := m.runEngine
	ctx := m.ctx
	return func() tea.Msg {
		sink := NewChanSink(pref.p)
		err := runEngine(ctx, prompt, sink, executorExtra, criticExtra)
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
	switch a.Name {
	// Info/display commands
	case commands.CmdSkills:
		m = m.appendEntry("system", m.formatSkillsList())
	case commands.CmdHelp:
		m = m.appendEntry("system", m.formatHelp())
	case commands.CmdHistory:
		m = m.appendEntry("system", m.formatHistory())
	case commands.CmdTokens:
		m = m.appendEntry("system", "/tokens — not yet implemented (M10)")
	case commands.CmdMap:
		m = m.appendEntry("system", m.formatRepoMap())
	case commands.CmdMapRefresh:
		m = m.appendEntry("system", "/map-refresh — not yet implemented (M10)")
	case commands.CmdSettings:
		m = m.appendEntry("system", m.formatSettings())
	case commands.CmdReport:
		m = m.appendEntry("system", "/report — not yet implemented (M10)")

	// Git workflow commands
	case commands.CmdShip:
		m = m.handleShip(a)
	case commands.CmdUndo:
		m = m.handleUndo()
	case commands.CmdRevert:
		m = m.handleRevert(a)
	case commands.CmdCommit:
		m = m.handleCommit(a)

	// File management commands
	case commands.CmdAdd:
		m = m.handleAdd(a)
	case commands.CmdDrop:
		m = m.handleDrop(a)
	case commands.CmdLs:
		m = m.handleLs()
	case commands.CmdDiff:
		m = m.handleDiff()
	case commands.CmdReadOnly:
		m = m.handleReadOnly(a)

	// Shell/execution commands
	case commands.CmdRun:
		m = m.handleRun(a)
	case commands.CmdTest:
		m = m.handleTest(a)
	case commands.CmdGit:
		m = m.handleGit(a)
	case commands.CmdLint:
		m = m.handleLint(a)

	// External integration commands
	case commands.CmdWeb:
		m = m.appendEntry("system", "/web — not yet implemented (M10b)")
	case commands.CmdPaste:
		m = m.appendEntry("system", "/paste — not yet implemented (M10)")
	case commands.CmdVoice:
		m = m.appendEntry("system", "/voice — not yet implemented (M10b)")
	case commands.CmdWatch:
		m = m.handleWatch(a)
	case commands.CmdUnwatch:
		m = m.handleUnwatch()

	// Session/state commands
	case commands.CmdSave:
		m = m.appendEntry("system", "/save — not yet implemented (M10)")
	case commands.CmdLoad:
		m = m.appendEntry("system", "/load — not yet implemented (M10)")
	case commands.CmdReset:
		m = m.appendEntry("system", "/reset — not yet implemented (M10)")
	case commands.CmdClear:
		m = m.handleClear()
	case commands.CmdDiscard:
		m = m.appendEntry("system", "/discard — not yet implemented (M10)")
	case commands.CmdSession:
		m = m.appendEntry("system", m.formatSession())
	case commands.CmdTask:
		m = m.appendEntry("system", "/task — not yet implemented (M10)")
	case commands.CmdQuit:
		m.cancel()
		return m, tea.Quit

	// Editor/context commands
	case commands.CmdCopy:
		m = m.appendEntry("system", "/copy — not yet implemented (M10)")
	case commands.CmdCopyContext:
		m = m.appendEntry("system", "/copy-context — not yet implemented (M10)")
	case commands.CmdEditor:
		m = m.appendEntry("system", "/editor — not yet implemented (M10)")
	case commands.CmdEdit:
		m = m.appendEntry("system", "/edit — not yet implemented (M10)")

	// Model/configuration commands
	case commands.CmdModel:
		m = m.appendEntry("system", "/model — not yet implemented (M10)")
	case commands.CmdThinkTokens:
		m = m.appendEntry("system", "/think-tokens — not yet implemented (M10)")
	case commands.CmdReasoningEffort:
		m = m.appendEntry("system", "/reasoning-effort — not yet implemented (M10)")
	case commands.CmdMultilineMode:
		m = m.handleMultilineMode()

	default:
		m = m.appendEntry("system",
			fmt.Sprintf("unknown command %q — type /help for available commands", a.Name))
	}
	return m, nil
}

// formatSkillsList returns a human-readable list of loaded skills.
func (m model) formatSkillsList() string {
	all := m.skillsReg.All()
	if len(all) == 0 {
		return "No skills loaded. Place .toml files in ~/.config/marshal/skills/"
	}
	var sb strings.Builder
	sb.WriteString("Available skills:\n")
	for _, s := range all {
		sb.WriteString(fmt.Sprintf("  %-12s %s\n", s.Trigger, s.Description))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatHistory returns a human-readable task ledger for the current session.
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
		status := t.Status
		if status == "" {
			status = "running"
		}
		summary := ""
		if t.Summary != nil && *t.Summary != "" {
			summary = *t.Summary
		} else {
			summary = t.Prompt
			if len(summary) > 50 {
				summary = summary[:47] + "..."
			}
		}
		sb.WriteString(fmt.Sprintf("  [%s] %-12s %s\n", status, t.ID, summary))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatHelp returns the full help text for all commands.
func (m model) formatHelp() string {
	return `Commands:

Git Workflow:
  /ship [msg]     squash-merge staging branch to target branch
  /undo           revert the most recent successful task
  /revert <id>    revert a specific task by ID
  /commit [msg]   commit current changes with message

Info/Display:
  /history        show task ledger for current session
  /skills         list available skill extensions
  /help           show this help
  /tokens         show token usage statistics
  /map            show repository map
  /map-refresh    rebuild the repository map
  /settings       show current settings
  /report         generate a session report

File Management:
  /add <file>     add file(s) to the chat context
  /drop <file>    remove file(s) from the chat context
  /ls             list files in the current context
  /diff           show diff of current changes
  /read-only <f>  add file(s) as read-only context

Shell/Execution:
  /run <cmd>      run a shell command
  /test [args]    run tests (auto-detects test runner)
  /git <args>     run a git command
  /lint [files]   run linter on files

External Integration:
  /web <url>      fetch and include web content
  /paste          paste from clipboard
  /voice          record and transcribe voice input
  /watch [dir]    watch files for AI comment markers
  /unwatch        stop watching files

Session/State:
  /save [name]    save current session state
  /load [name]    load a saved session state
  /reset          reset session state
  /clear          clear the chat display
  /discard        discard current task changes
  /session        show session information
  /task <desc>    create a named task
  /quit           exit the application

Editor/Context:
  /copy [text]    copy text to clipboard
  /copy-context   copy current chat context
  /editor [file]  open external editor
  /edit <file>    edit a file directly

Model/Configuration:
  /model [name]   show or switch active model
  /think-tokens <n>  set thinking token budget
  /reasoning-effort <n>  set reasoning effort level
  /multiline-mode toggle multiline input mode

Skills from ~/.config/marshal/skills/*.toml are also available.
Type /quit or press Ctrl+C to exit.`
}

// --- Command handlers --------------------------------------------------------

func (m model) handleShip(a commands.Action) model {
	// TODO: Implement ship command - squash merge staging to target
	m = m.appendEntry("system", "/ship — squash-merging staging to target (M10 implementation needed)")
	return m
}

func (m model) handleUndo() model {
	// TODO: Implement undo command - revert most recent task
	m = m.appendEntry("system", "/undo — reverting most recent task (M10 implementation needed)")
	return m
}

func (m model) handleRevert(a commands.Action) model {
	if a.Arg == "" {
		m = m.appendEntry("system", "usage: /revert <task-id>")
		return m
	}
	// TODO: Implement revert command
	m = m.appendEntry("system", fmt.Sprintf("/revert %s — reverting specific task (M10 implementation needed)", a.Arg))
	return m
}

func (m model) handleCommit(a commands.Action) model {
	msg := a.Prompt
	if msg == "" {
		msg = "marshal: manual commit"
	}
	// TODO: Implement commit command
	m = m.appendEntry("system", fmt.Sprintf("/commit — committing with message: %s (M10 implementation needed)", msg))
	return m
}

func (m model) handleAdd(a commands.Action) model {
	if len(a.Args) == 0 {
		m = m.appendEntry("system", "usage: /add <file1> [file2] ...")
		return m
	}
	// TODO: Implement add command - add files to context
	m = m.appendEntry("system", fmt.Sprintf("/add %v — adding files to context (M10 implementation needed)", a.Args))
	return m
}

func (m model) handleDrop(a commands.Action) model {
	if len(a.Args) == 0 {
		m = m.appendEntry("system", "usage: /drop <file1> [file2] ...")
		return m
	}
	// TODO: Implement drop command - remove files from context
	m = m.appendEntry("system", fmt.Sprintf("/drop %v — removing files from context (M10 implementation needed)", a.Args))
	return m
}

func (m model) handleLs() model {
	// TODO: Implement ls command - list files in context
	m = m.appendEntry("system", "/ls — files in context:\n  (M10 implementation needed)")
	return m
}

func (m model) handleDiff() model {
	// TODO: Implement diff command - show current diff
	m = m.appendEntry("system", "/diff — showing current changes (M10 implementation needed)")
	return m
}

func (m model) handleReadOnly(a commands.Action) model {
	if len(a.Args) == 0 {
		// List current read-only files
		if len(m.readOnlyFiles) == 0 {
			m = m.appendEntry("system", "Read-only files: none")
		} else {
			var sb strings.Builder
			sb.WriteString("Read-only files:\n")
			for _, f := range m.readOnlyFiles {
				sb.WriteString(fmt.Sprintf("  %s\n", f))
			}
			m = m.appendEntry("system", strings.TrimRight(sb.String(), "\n"))
		}
		return m
	}

	// Add files to read-only list
	added := 0
	for _, path := range a.Args {
		// Clean and validate path
		path = filepath.Clean(path)
		if strings.HasPrefix(path, "..") {
			m = m.appendEntry("system", fmt.Sprintf("  skipped %s (outside repo)", path))
			continue
		}
		// Check if file exists
		absPath := filepath.Join(m.repoRoot, path)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			m = m.appendEntry("system", fmt.Sprintf("  skipped %s (not found)", path))
			continue
		}

		// Add to session store
		if m.store != nil && m.sessionID != "" {
			if err := m.store.AddReadOnlyFile(m.sessionID, path); err != nil {
				m = m.appendEntry("system", fmt.Sprintf("  error adding %s: %v", path, err))
				continue
			}
		}

		// Add to in-memory list if not already present
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

func (m model) handleRun(a commands.Action) model {
	if a.Prompt == "" {
		m = m.appendEntry("system", "usage: /run <command>")
		return m
	}
	// TODO: Implement run command
	m = m.appendEntry("system", fmt.Sprintf("/run %s — executing command (M10 implementation needed)", a.Prompt))
	return m
}

func (m model) handleTest(a commands.Action) model {
	// TODO: Implement test command - auto-detect and run tests
	args := a.Prompt
	if args == "" {
		args = "(auto-detect)"
	}
	m = m.appendEntry("system", fmt.Sprintf("/test %s — running tests (M10 implementation needed)", args))
	return m
}

func (m model) handleGit(a commands.Action) model {
	if a.Prompt == "" {
		m = m.appendEntry("system", "usage: /git <args>")
		return m
	}
	// TODO: Implement git command passthrough
	m = m.appendEntry("system", fmt.Sprintf("/git %s — running git command (M10 implementation needed)", a.Prompt))
	return m
}

func (m model) handleLint(a commands.Action) model {
	// TODO: Implement lint command
	files := a.Prompt
	if files == "" {
		files = "(changed files)"
	}
	m = m.appendEntry("system", fmt.Sprintf("/lint %s — running linter (M10 implementation needed)", files))
	return m
}

func (m model) handleClear() model {
	// Clear the chat display
	m.entries = nil
	m.streaming.Reset()
	m = m.rebuildViewport()
	m = m.appendEntry("system", "Chat display cleared.")
	return m
}

func (m model) handleMultilineMode() model {
	// TODO: Implement multiline mode toggle
	m = m.appendEntry("system", "/multiline-mode — toggling multiline input (M10 implementation needed)")
	return m
}

func (m model) formatRepoMap() string {
	// TODO: Implement repo map display
	return "Repository map:\n  (M10 implementation needed - will show PageRank-ranked file list)"
}

func (m model) formatSettings() string {
	// TODO: Implement settings display
	return "Current settings:\n  (M10 implementation needed - will show active config)"
}

func (m model) formatSession() string {
	if m.sessionID == "" {
		return "No active session."
	}
	return fmt.Sprintf("Session: %s\n  (M10 will show more session details)", m.sessionID)
}

func (m model) handleWatch(a commands.Action) model {
	if m.watchMgr == nil {
		m = m.appendEntry("system", "Watch mode not available (no repository)")
		return m
	}

	if m.watchMgr.IsActive() {
		m = m.appendEntry("system", "Watch mode is already active. Use /unwatch to stop.")
		return m
	}

	if err := m.watchMgr.Start(); err != nil {
		m = m.appendEntry("system", fmt.Sprintf("Failed to start watch mode: %v", err))
		return m
	}

	msg := "Watch mode started. Monitoring files for // ai: markers..."
	if a.Arg != "" {
		msg = fmt.Sprintf("Watch mode started on %s. Monitoring for // ai: markers...", a.Arg)
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
