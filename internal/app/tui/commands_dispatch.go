package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/doctorpanel"
	"marshal/internal/app/tui/plugins"
	"marshal/internal/app/tui/skills"
	"marshal/internal/interaction"
)

// tuiCommandEffects holds the interactive logic for commands registered
// with TUIOnly in the commands registry. The split is explicit: pure
// (headless-capable) commands carry a registry Handler; interactive
// commands live here, one entry each, next to the dispatch entry point.
var tuiCommandEffects map[string]func(m *Model, args []string) (tea.Model, tea.Cmd)

// retiredInteractionCommands explains the removed five-mode commands. They are
// handled before the unknown-command fallback and never mutate access.
var retiredInteractionCommands = map[string]string{
	"plan":    "/plan was removed. Planning remains available through the marshal-writing-plans skill; load it with /skills.",
	"default": "/default was removed. Use /ask for approval-gated work or /act for routine workspace work.",
	"edit":    "/edit was removed. Use /act; Auto is controlled with /auto on|off.",
	"copilot": "/copilot was removed. Use /auto on for Act + Auto, or /act for normal Act.",
}

// parseReviewArgs extracts /review options: --model <pair> (or
// --model=<pair>), --base <ref> (or --base=<ref>), and an explicit
// range token containing ".." (e.g. main...HEAD). Remaining words are
// the focus. Only one of --base / explicit range may be given; the
// caller reports the conflict.
func parseReviewArgs(args []string) (model, base, reviewRange string, remaining []string) {
	out := make([]string, 0, len(args))
	seenFocus := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--model" && i+1 < len(args):
			model = args[i+1]
			i++
		case strings.HasPrefix(a, "--model="):
			model = strings.TrimPrefix(a, "--model=")
		case a == "--base" && i+1 < len(args):
			base = args[i+1]
			i++
		case strings.HasPrefix(a, "--base="):
			base = strings.TrimPrefix(a, "--base=")
		case !seenFocus && strings.Contains(a, ".."):
			reviewRange = a
		default:
			out = append(out, a)
			seenFocus = true
		}
	}
	return model, base, reviewRange, out
}

// isSendNowCommand reports whether raw is the /send-now command, matching the
// command token exactly. The suffix is left for sendNowCommand to take verbatim
// rather than being re-split: the payload is steering text, and shlex would
// strip its quotes and collapse its newlines.
func (m *Model) isSendNowCommand(raw string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(raw), "/send-now")
	if !ok {
		return false
	}
	// The token boundary is exact: "/send-now-extra" names a different command.
	if len(rest) > 0 && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' {
		return false
	}
	_, ok = m.lookupCommand("send-now")
	return ok
}

// sendNowCommand is the /send-now effect, kept beside the other interactive
// command effects. It receives the raw line, not parsed args: the payload is
// literal steering text, so quotes, newlines and interior whitespace must
// survive exactly as typed. With text it submits that text; with no argument it
// flushes the existing queue, or opens the composer when there is nothing
// queued. Its failure path restores the raw draft the Enter handler cleared, so
// a rejected submission never costs the user their line.
func (m *Model) sendNowCommand(raw string) (tea.Model, tea.Cmd) {
	text := ""
	if rest, ok := strings.CutPrefix(strings.TrimSpace(raw), "/send-now"); ok {
		// Only the separating whitespace is dropped: whatever the user typed
		// after it is literal steering text, quotes and newlines included.
		text = strings.TrimLeft(rest, " \t\r\n")
	}
	if strings.TrimSpace(text) == "" {
		return m.sendNowFlushOrOpen(raw)
	}
	snap, ok := m.sendNowTarget()
	if !ok {
		m.restoreSendNowDraft(raw)
		m.rejectSendNow(sendNowUnavailable + " The command text was kept.")
		return m, nil
	}
	// The Enter handler cleared the draft before dispatching, so a refused
	// request has to put it back: the user's line is theirs until the runtime
	// actually accepts it.
	if !m.submitSendNow(acpwire.SendNowRequest{
		SessionID: m.sess.ID(),
		TurnID:    snap.TurnID,
		Text:      text,
	}, nil) {
		m.restoreSendNowDraft(raw)
	}
	return m, nil
}

// sendNowFlushOrOpen implements the no-argument form: flush the queue when a
// supported active turn has steering waiting, otherwise open the composer. A
// turn that cannot accept the action keeps the raw draft and says why.
func (m *Model) sendNowFlushOrOpen(raw string) (tea.Model, tea.Cmd) {
	snap, ok := m.sendNowTarget()
	if !ok {
		m.restoreSendNowDraft(raw)
		m.rejectSendNow(sendNowUnavailable + " The command text was kept.")
		return m, nil
	}
	if len(m.sess.Status().Steering.Queue) == 0 {
		// Nothing to flush: the composer is the remaining way to write one.
		m.openSteeringComposer()
		return m, nil
	}
	if !m.submitSendNow(acpwire.SendNowRequest{
		SessionID: m.sess.ID(),
		TurnID:    snap.TurnID,
		Text:      "",
	}, nil) {
		m.restoreSendNowDraft(raw)
	}
	return m, nil
}

// restoreSendNowDraft puts the raw command line back into the chat editor after
// a rejected /send-now, so the user can edit and retry rather than retype.
func (m *Model) restoreSendNowDraft(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	m.input.SetValue(raw)
	m.input.MoveToEnd()
}

// newSessionEffect is the shared /new and /clear handler. It records a pending
// REPLACEMENT whose target (the new session's name) is applied only after the
// runtime accepts the work membership an end would stop: the current
// conversation's live work is settled first, and nothing about the new session
// is built until then.
//
// The existing busy refusal is preserved: a turn in flight still refuses a
// replacement outright rather than warning about it.
func newSessionEffect(m *Model, args []string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	name := strings.TrimSpace(strings.Join(args, " "))
	return m, m.beginEnd(&endIntent{kind: acpwire.EndIntentReplace, name: name})
}

// startNewSession builds the replacement session and re-points every model
// field that depends on the current session, dropping per-message UI state that
// belonged to the old one. It runs only after an accepted replacement: the
// runtime froze the old conversation at acceptance, so the swap acts on a
// settled membership and no old work is transferred or reopened.
func startNewSession(m *Model, name string) tea.Cmd {
	oldCount := 0
	if page, err := m.sess.Messages(m.ctx, acpwire.MessageQuery{Last: 1}); err == nil {
		oldCount = page.Total
	}
	// Whatever the outcome, the end is over: the old conversation settled and a
	// failed replacement starts nothing, so a retry begins from idle.
	m.endStage = endIdle
	sess, err := m.host.NewSession(m.ctx, name)
	if err != nil {
		m.note(fmt.Sprintf("Failed to start new session: %v", err))
		m.refreshViewport()
		return nil
	}

	m.sess = sess
	// A replacement session restarts its watch-resume sequence, so the model's
	// mark restarts with it.
	m.watchResumeSeq = 0

	// Drop per-message UI state that belongs to the old session.
	m.viewStack = nil
	// The command list follows the session, so the popup's cache of it does
	// too: a replacement may report a different set.
	if m.cmdPopup != nil {
		m.cmdPopup.items = nil
	}
	m.syncTranscriptWidth()
	m.resetExpansion()
	m.nodeRegions = nil
	// The sheet's changed files follow the workspace the new session reports;
	// a replaced session's cache starts empty.
	st := m.sess.Status()
	m.workspaceSeen = st.Workspace
	// The session the model now holds serves the new conversation's work:
	// its snapshot seeds the view and its cards replace the old ones, so no
	// work from the old session leaks into the new one.
	m.resetWork()

	msg := fmt.Sprintf("Started new conversation. Cleared %d messages.", oldCount)
	if name != "" {
		msg = fmt.Sprintf("Started new conversation named %q. Cleared %d messages.", name, oldCount)
	}
	m.note(msg)
	m.refreshViewport()
	// The session is always new, so its change pump always starts here.
	return tea.Batch(
		refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true}),
		pumpChanged(m.sess.Changed()),
	)
}

func init() {
	tuiCommandEffects = map[string]func(m *Model, args []string) (tea.Model, tea.Cmd){
		"exit": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			return m, m.beginEnd(&endIntent{kind: acpwire.EndIntentExit})
		},
		"quit": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			return m, m.beginEnd(&endIntent{kind: acpwire.EndIntentExit})
		},
		"effort": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if len(args) == 0 {
				m.openEffortPicker()
				m.refreshViewport()
				return m, nil
			}
			m.applyEffort(strings.TrimSpace(args[0]))
			m.refreshViewport()
			return m, nil
		},
		"set": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.handleSetCommand(args)
			m.refreshViewport()
			return m, nil
		},
		"settings": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.openSettingsBrowser(strings.Join(args, " "))
			m.refreshViewport()
			return m, nil
		},
		"memory": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openMemory(false)
			m.refreshViewport()
			return m, nil
		},
		"skills": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.dock.Open(skills.NewPanel(m.sess, m.sess.Status().Trusted))
			m.refreshViewport()
			return m, nil
		},
		"plugins": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			// Capture the session and context the panel was opened with, so a
			// later swap leaves the panel writing where it was opened, as the
			// state it used to hold did.
			sess, ctx := m.sess, m.ctx
			log := m.log()
			m.dock.Open(plugins.NewPanel(sess, m.sess.Status().Trusted, func(text string) {
				if err := sess.AddNote(ctx, acpwire.Note{Text: text, ContentType: acpwire.ContentTypePlain}); err != nil {
					log.Warn("plugin note dropped", "error", err)
				}
			}))
			m.refreshViewport()
			return m, nil
		},
		"mcp": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			return m.handleMCPCommand(args)
		},
		"trust": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			if m.sess.Status().Trusted {
				m.refreshViewport()
				return m, nil
			}
			if !m.openTrustPanel() {
				// No pending decision: the informative panel from the handler
				// already rendered; steer to the shell path instead of opening
				// a panel that would answer nothing.
				m.note("No trust decision pending. Grant permanent trust with `marshal --trust`, or restart in this directory to re-prompt once `.marshal/config.toml` exists here.")
			}
			m.refreshViewport()
			return m, nil
		},
		"doctor": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			cfg := m.sess.Config()
			diags := config.Diagnose(cfg, m.sess.Layers())
			checks, err := m.sess.Intelligence(m.ctx)
			if err != nil {
				// A session that cannot report its subsystem health still gets
				// the config diagnostics; the section simply stays out.
				checks = nil
			}
			m.dock.Open(doctorpanel.New(diags, checks))
			m.refreshViewport()
			return m, nil
		},
		"stop": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			if !m.cancelTurn() {
				m.refreshViewport()
			}
			return m, nil
		},
		"ask": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setInteractionMode(interaction.ModeAsk)
			return m, nil
		},
		"act": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setInteractionMode(interaction.ModeAct)
			return m, nil
		},
		"auto": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.setAuto(args)
			return m, nil
		},
		"mode": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if len(args) > 0 {
				switch strings.ToLower(args[0]) {
				case "ask":
					m.setInteractionMode(interaction.ModeAsk)
					return m, nil
				case "act":
					m.setInteractionMode(interaction.ModeAct)
					return m, nil
				default:
					m.note("Usage: /mode [ask|act]")
					m.refreshViewport()
					return m, nil
				}
			}
			m.openPicker("mode", "Interaction mode", "", m.modePickerItems(), "")
			m.refreshViewport()
			return m, nil
		},
		"system": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if len(args) > 0 && strings.ToLower(args[0]) == "unlock" {
				on := !m.sess.Status().SudoUnlock
				if err := m.sess.SetSudoUnlock(m.ctx, on); err != nil {
					m.note(fmt.Sprintf("Could not change the privileged unlock: %v", err))
					m.refreshViewport()
					return m, nil
				}
				if on {
					m.note("Privileged unlock enabled: sudo's own timestamp grace applies (Defaults timestamp_timeout). Marshal still re-enters sudo for every command.")
				} else {
					m.note("Privileged unlock disabled: every privileged command authenticates fresh.")
				}
				m.refreshViewport()
				return m, nil
			}
			m.setSystem(args)
			return m, nil
		},
		"swarm": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			goal := strings.TrimSpace(strings.Join(args, " "))
			if goal == "" {
				m.note("Usage: /swarm <goal>")
				m.refreshViewport()
				return m, nil
			}
			if !m.sess.Status().AgentReady {
				m.note("Swarm is not available (agent failed to initialise).")
				m.refreshViewport()
				return m, nil
			}
			if m.busy {
				return m, nil
			}
			m.openRunPreflight("swarm", goal, nil)
			m.refreshViewport()
			return m, nil
		},
		"sdd": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			parsed, err := parseSDDArgs(args)
			if err != nil {
				m.note(err.Error())
				m.refreshViewport()
				return m, nil
			}
			// /sdd new <goal> starts the authoring child.
			if parsed.New {
				return m.startSDDAuthoring(parsed)
			}
			if !m.sess.Status().AgentReady {
				m.note("Plan execution is not available (agent failed to initialise).")
				m.refreshViewport()
				return m, nil
			}
			planPath := parsed.PlanPath
			if planPath == "" {
				m.openSDDPlanPicker()
				m.refreshViewport()
				return m, nil
			}
			if m.busy {
				return m, nil
			}
			// The session inspects the plan and resolves the verify gate the
			// run would use, without writing anything. A plan it rejects is
			// reported here, before the preflight opens (decisions 3 and 9).
			pf, err := m.sess.PlanPreflight(m.ctx, acpwire.PlanPreflightParams{PlanPath: planPath, Strategy: parsed.Strategy})
			if err != nil {
				m.note("Cannot start plan run: " + err.Error())
				m.refreshViewport()
				return m, nil
			}
			m.openRunPreflight("sdd", planPath, &pf)
			m.refreshViewport()
			return m, nil
		},
		"run": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			st := m.sess.Status()
			doc := runOutcomeDoc(st.SDD, m.now())
			acts := runOutcomeActions(m.ctx, m.sess, st.SDD, st.WorkingDir, m.width)
			m.dock.Open(docpanel.New(doc, localDocActions(acts)))
			m.refreshViewport()
			return m, nil
		},
		// /work opens the Work list. The command's registry entry lands in Task
		// 19; the effect is wired here so the surface exists the moment it is
		// registered.
		"work": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openWork()
			return m, nil
		},
		"review": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if m.busy {
				return m, nil
			}
			if !m.sess.Status().AgentReady {
				m.note("Agent runner is not available.")
				m.refreshViewport()
				return m, nil
			}
			model, base, reviewRange, remaining := parseReviewArgs(args)
			if base != "" && reviewRange != "" {
				// Refused before anything starts: the run path owns the work
				// membership, the stop group and the base's encoding.
				m.note("review: --base and an explicit range are mutually exclusive")
				m.refreshViewport()
				return *m, nil
			}
			focus := strings.TrimSpace(strings.Join(remaining, " "))
			sess := m.sess
			params := acpwire.ReviewParams{Focus: focus, Model: model, Range: reviewRange, Base: base}
			return m.startSessionRun("Cannot start review", func(ctx context.Context) error {
				return sess.Review(ctx, params)
			})
		},
		"connect": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openConnect("/")
			m.refreshViewport()
			return m, nil
		},
		"models": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if arg := strings.TrimSpace(strings.Join(args, " ")); arg != "" {
				if presetName, ok := m.resolveModelArg(arg); ok {
					m.switchModelPreset(presetName)
					m.refreshViewport()
					return m, nil
				}
			}
			cmd := m.openModels()
			m.refreshViewport()
			return m, cmd
		},
		"options": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			cmd := m.openModelOptions()
			m.refreshViewport()
			return m, cmd
		},
		"profiles": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openSettingsBrowser("profiles")
			m.refreshViewport()
			return m, nil
		},
		"agents": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.openAgentsRoster(strings.TrimSpace(strings.Join(args, " ")))
			m.refreshViewport()
			return m, nil
		},
		"save": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.note("Session saved. Marshal auto-saves messages, todos, and workspace state as you work.")
			m.refreshViewport()
			return m, nil
		},
		"sessions": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openSessionPicker("")
			m.refreshViewport()
			return m, nil
		},
		"resume": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			id := strings.TrimSpace(strings.Join(args, " "))
			if id == "" {
				m.openSessionPicker("")
				m.refreshViewport()
				return m, nil
			}
			return m.beginResume(id)
		},
		"new":   newSessionEffect,
		"clear": newSessionEffect,
	}
}
