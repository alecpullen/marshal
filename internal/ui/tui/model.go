package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alec/marshal/internal/commands"
	"github.com/alec/marshal/internal/loop"
	"github.com/alec/marshal/internal/session"
	"github.com/alec/marshal/internal/skills"
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
	pref      *progRef

	ctx    context.Context
	cancel context.CancelFunc

	// UI components.
	viewport viewport.Model
	input    textarea.Model

	// State.
	entries   []chatEntry
	busy      bool
	width     int
	height    int
	streaming *strings.Builder // in-progress executor tokens
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
		runGate:   runGate,
		runEngine: runEngine,
		skillsReg: skillsReg,
		store:     store,
		sessionID: sessionID,
		pref:      pref,
		ctx:       ctx,
		cancel:    cancel,
		viewport:  vp,
		input:     ta,
		streaming: &strings.Builder{},
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
	case "skills":
		m = m.appendEntry("system", m.formatSkillsList())
	case "help":
		m = m.appendEntry("system", helpText)
	case "history":
		m = m.appendEntry("system", m.formatHistory())
	case "ship":
		m = m.appendEntry("system", "/ship — not yet implemented (coming in M9)")
	case "undo":
		m = m.appendEntry("system", "/undo — not yet implemented (coming in M9)")
	case "revert":
		m = m.appendEntry("system", "/revert — not yet implemented (coming in M9)")
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

const helpText = `Commands:
  /ship           ship staged work to the target branch (M9)
  /undo           undo the last task (M9)
  /revert <id>    revert a specific task by ID (M9)
  /history        show task ledger for current session
  /skills         list available skill extensions
  /help           show this help

Skills from ~/.config/marshal/skills/*.toml are also available.
Ctrl+C to quit.`
