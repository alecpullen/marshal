package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/app/tui/memory"
	"marshal/internal/tools/registry"
)

// ActionID identifies one UI operation the user can invoke by key or from
// the action palette. The catalog is the single place a key's meaning is
// declared, which is what stops the footer, the palette, and key dispatch
// from disagreeing about what Ctrl+X does.
type ActionID string

const (
	ActionStopAgent     ActionID = "stop-agent"
	ActionClearQueue    ActionID = "clear-queue"
	ActionInspectAgent  ActionID = "inspect-agent"
	ActionCancelTurn    ActionID = "cancel-turn"
	ActionToggleMouse   ActionID = "toggle-mouse"
	ActionFocusNext     ActionID = "focus-next-surface"
	ActionFocusPrevious ActionID = "focus-previous-surface"
	ActionPalette       ActionID = "action-palette"
	ActionMode          ActionID = "interaction-mode"
	ActionSettings      ActionID = "settings"
	ActionMemory        ActionID = "memory"
	ActionModels        ActionID = "models"
	ActionTasks         ActionID = "tasks"
	ActionSideRail      ActionID = "side-rail"
	ActionThinking      ActionID = "thinking"
	ActionRollback      ActionID = "rollback"
	ActionHelp          ActionID = "help"

	// Copy actions are separate IDs rather than one "copy" with an argument:
	// "Copy answer" and "Copy output" are different promises about different
	// bytes, and the palette must be able to offer, disable, and explain each
	// one on its own terms.
	ActionCopyAnswer ActionID = "copy-answer"
	ActionCopyCode   ActionID = "copy-code"
	ActionCopyOutput ActionID = "copy-output"
	ActionCopyPath   ActionID = "copy-path"

	// The inspected-change copies act on the inspector's Changes tab rather
	// than on the conversation, so they are separate actions rather than a
	// second meaning for the conversation ones: two actions with one key each
	// is an ambiguous action, and the palette is where a user finds out which
	// is which.
	ActionCopyInspectedPath ActionID = "copy-inspected-path"
	ActionCopyPatch         ActionID = "copy-patch"
)

// Action priorities order the palette: a user opening it in an emergency
// should meet "stop the agent" before "toggle the side rail". They are not
// footer priorities — the footer's own cluster is minimal by design.
const (
	actionPriorityEssential = 0
	actionPriorityLikely    = 1
	actionPriorityOptional  = 2
)

// actionDef is one catalog entry. Availability is computed per context by
// availability(), never stored: an action that is listed but not currently
// possible is offered with the reason it cannot run, which is more useful
// than hiding it and leaving the user to guess.
type actionDef struct {
	id ActionID
	// label is the palette row's primary text.
	label string
	// desc explains what the action does.
	desc string
	// key is the bound keystroke, "" when the action is palette-only.
	key string
	// hint, when non-empty, is the footer verb for key. An empty hint keeps
	// the action out of the footer even though it has a key: the footer is
	// deliberately minimal (see help.FooterHints) and F6/F2 belong in the
	// palette and the /help cheatsheet, not in the always-on cluster.
	hint string
	// priority orders the palette listing.
	priority int
	// dynamic marks an action whose footer verb depends on state — the mouse
	// hint flips between releasing and capturing.
	dynamic bool
}

// actionCatalog lists the actions in palette order. Form-owned keys stay out
// of it on purpose: while an approval, question, skill gate, command editor,
// or completion popup is up, those forms own their keys and the palette
// cannot be reached, so listing them here would advertise a dispatcher that
// never runs.
//
// Only operations that exist today are listed. Later tasks add their own
// entries as their features land; there are no placeholders.
var actionCatalog = []actionDef{
	{
		id: ActionStopAgent, label: "Stop inspected agent",
		desc: "stop the running child agent being inspected",
		key:  "Ctrl+X", hint: "stop agent", priority: actionPriorityEssential,
	},
	{
		id: ActionClearQueue, label: "Clear queued messages",
		desc: "drop the steering messages waiting for the running turn",
		key:  "Ctrl+X", hint: "clear queue", priority: actionPriorityEssential,
	},
	{
		id: ActionInspectAgent, label: "Inspect running agent",
		desc: "open the most recently started running agent's transcript",
		key:  "Ctrl+F", hint: "inspect agent", priority: actionPriorityLikely,
	},
	{
		id: ActionCancelTurn, label: "Cancel turn",
		desc: "interrupt the running turn (Ctrl+C also interrupts)",
		key:  "Esc", hint: "cancel", priority: actionPriorityEssential,
	},
	{
		id: ActionToggleMouse, label: "Toggle mouse capture",
		desc: "release the mouse for native click-drag selection, or take it back",
		key:  "Ctrl+S", priority: actionPriorityLikely, dynamic: true,
	},
	{
		id: ActionFocusNext, label: "Focus next surface",
		desc: "move keyboard focus to the next available surface",
		key:  "F6", priority: actionPriorityOptional,
	},
	{
		id: ActionFocusPrevious, label: "Focus previous surface",
		desc: "move keyboard focus to the previous available surface",
		key:  "Shift+F6", priority: actionPriorityOptional,
	},
	{
		id: ActionPalette, label: "Action palette",
		desc: "search every action available right now",
		key:  "F2", priority: actionPriorityOptional,
	},
	{
		id: ActionMode, label: "Interaction mode",
		desc: "pick plan, default, edit, copilot, or auto",
		key:  "Tab", priority: actionPriorityOptional,
	},
	{
		id: ActionSettings, label: "Settings",
		desc: "open the settings and configuration browser",
		key:  "Ctrl+O", priority: actionPriorityOptional,
	},
	{
		id: ActionMemory, label: "Memory",
		desc: "browse this project's durable memory",
		key:  "Ctrl+K", priority: actionPriorityOptional,
	},
	{
		id: ActionModels, label: "Models and providers",
		desc: "switch the active model preset or connect a provider",
		key:  "Ctrl+P", priority: actionPriorityOptional,
	},
	{
		id: ActionTasks, label: "Task list",
		desc: "cycle the pinned task panel: expanded, collapsed, hidden",
		key:  "Ctrl+T", priority: actionPriorityOptional,
	},
	{
		id: ActionSideRail, label: "Side rail",
		desc: "toggle the widescreen side rail for this session",
		key:  "Ctrl+B", priority: actionPriorityOptional,
	},
	{
		id: ActionThinking, label: "Thinking blocks",
		desc: "expand or collapse reasoning blocks in the transcript",
		key:  "Ctrl+G", priority: actionPriorityOptional,
	},
	{
		id: ActionRollback, label: "Roll back the last patch",
		desc: "revert the most recent patch (Ctrl+R twice: the first press arms it)",
		key:  "Ctrl+R", priority: actionPriorityOptional,
	},
	{
		id: ActionHelp, label: "Help",
		desc: "print the command and keybinding cheatsheet",
		key:  "?", priority: actionPriorityOptional,
	},
	{
		id: ActionCopyAnswer, label: "Copy answer",
		desc: "copy the answer the reader is on, as its original Markdown",
		key:  "y", priority: actionPriorityLikely,
	},
	{
		id: ActionCopyCode, label: "Copy code block",
		desc:     "copy a code block from the answer the reader is on",
		priority: actionPriorityOptional,
	},
	{
		id: ActionCopyOutput, label: "Copy tool output",
		desc:     "copy the captured output of the tool call the reader is on",
		priority: actionPriorityOptional,
	},
	{
		id: ActionCopyPath, label: "Copy file path",
		desc:     "copy the path the tool call the reader is on referred to",
		priority: actionPriorityOptional,
	},
	{
		id: ActionCopyInspectedPath, label: "Copy inspected path",
		desc:     "copy the path selected on the inspector's Changes tab",
		priority: actionPriorityOptional,
	},
	{
		id: ActionCopyPatch, label: "Copy captured patch",
		desc:     "copy the patch fetched for the inspected file, as fetched",
		priority: actionPriorityOptional,
	},
}

// actionContext is one snapshot of the root state every action resolution
// reads. Passing it around instead of consulting the model at each call site
// is what makes the footer, the palette, and key dispatch agree: they all
// resolve against the same facts, taken once.
type actionContext struct {
	// OtherPanelOpen reports that a modal surface other than the palette owns
	// the keys. The palette itself is excluded so the actions it lists are
	// judged on their own availability rather than on the palette's presence.
	OtherPanelOpen bool
	Busy           bool
	QueueLen       int
	// DrilledRunningChildID is the runtime ID of the running child agent the
	// transcript is drilled into, or 0.
	DrilledRunningChildID int64
	HasRunningSubagent    bool
	InspectorOnScreen     bool
	TodosActive           bool
	RollbackEligible      bool
	MemoryAvailable       bool
	MouseCaptured         bool
	// CopyBlock is the block a copy action would act on: the one under the
	// reading anchor, or the newest when the reader is following. It is the
	// resolved block rather than the sources it offers, so availability and
	// dispatch cannot disagree about which block was meant.
	CopyBlock conversation.Block
	// CopyBlockFound reports whether CopyBlock resolved at all.
	CopyBlockFound bool
	// InspectedPathSelected reports that the inspector's Changes tab has a
	// selected path, so a copy of it has something to copy.
	InspectedPathSelected bool
	// InspectedPatchLoaded reports that a patch has been fetched for the
	// inspected file. It is separate from the selection because selecting a
	// file reads nothing: the two facts arrive at different times, and an
	// action enabled on the selection alone would promise a patch that does
	// not exist yet.
	InspectedPatchLoaded bool
	// InspectorAgentRunningID is the runtime ID of the running agent selected
	// on the inspector's Agents tab, or 0.
	//
	// It is a SEPARATE field from DrilledRunningChildID because they are
	// different targets: drilling in puts a child's transcript in the
	// conversation viewport, while the inspector shows a child beside it. A
	// single field would make Ctrl+X stop whichever child the other surface
	// happened to be on.
	InspectorAgentRunningID int64
}

// Action is a resolved catalog entry: the descriptor plus the availability
// the current context gives it. It is a value so a caller cannot mutate the
// catalog.
type Action struct {
	ID             ActionID
	Label          string
	Desc           string
	KeyHint        string
	HintVerb       string
	Priority       int
	Disabled       bool
	DisabledReason string
}

// Detail is the palette's secondary text: what the key does, or why it
// cannot run.
func (a Action) Detail() string {
	if a.Disabled {
		return a.DisabledReason
	}
	if a.KeyHint != "" {
		return a.KeyHint + " · " + a.Desc
	}
	return a.Desc
}

// actionSnapshot takes the single context snapshot every resolution path
// uses.
func (m Model) actionSnapshot() actionContext {
	ctx := actionContext{
		Busy:               m.busy,
		QueueLen:           m.effectiveQueueLen(),
		HasRunningSubagent: m.hasRunningSubagent(),
		InspectorOnScreen:  m.railEnabled(),
		TodosActive:        m.state != nil && len(m.state.Todos()) > 0,
		RollbackEligible:   m.state != nil && m.state.HasBackup(),
		MemoryAvailable:    m.memoryDB != nil,
		MouseCaptured:      m.effectiveMouseCapture(),
	}
	// The dock holds one panel; the palette is one of them, so it is excluded
	// or every action in the list would read as "resolve the open panel".
	ctx.OtherPanelOpen = m.panelOwnsKeys() && m.pickerCommand != actionPaletteCommand
	if v, ok := m.drilledInto(); ok && v.Status == session.SubagentRunning {
		ctx.DrilledRunningChildID = v.ID
	}
	// The inspector's own selection is a second, independent target: the
	// Agents tab can be showing a running child while the transcript is
	// drilled into a different one, and only the runtime ID identifies which
	// the user means.
	if m.inspector != nil && m.inspector.model.SelectedTab() == inspector.TabAgents &&
		m.inspector.model.AgentDetailOpen() && m.inspector.model.SelectedAgentRunning() {
		ctx.InspectorAgentRunningID = m.inspector.model.AgentIDSelected()
	}
	// The copy actions resolve their block through the same path dispatch
	// uses, so availability and behaviour cannot describe different blocks.
	ctx.CopyBlock, ctx.CopyBlockFound = m.copyBlock()
	// The inspected-change actions read the inspector's own state, which is
	// the only place that knows what is selected and what has been fetched.
	if m.inspector != nil {
		_, ctx.InspectedPathSelected = m.inspector.model.CapturePath()
		_, _, _, ctx.InspectedPatchLoaded = m.inspector.model.CapturedPatch()
	}
	return ctx
}

// effectiveQueueLen is the queued-message count the UI reasons about. The
// session's queue is the ground truth; the broker-backed counter is the
// fallback for models built without one, and for the brief window between
// clearing the queue and the next broker event.
func (m Model) effectiveQueueLen() int {
	if m.state == nil {
		return m.queuedCount
	}
	if n := len(m.state.SteeringQueue()); n > 0 {
		return n
	}
	return m.queuedCount
}

// ctrlXID resolves Ctrl+X to exactly one action. The inspected running child
// comes first: while the user is looking at a child, stopping that child is
// the operation they mean. Otherwise Ctrl+X clears a nonempty queue while the
// turn is busy. Anything else leaves the key unbound.
//
// This is the single resolution the dispatcher, the footer, and the palette
// all read; it is the fix for the footer advertising "clear queue" twice
// while key dispatch could have stopped an agent instead.
func (ctx actionContext) ctrlXID() (ActionID, bool) {
	// The inspector's selection comes first. While the reader is looking at a
	// child in the Agents tab, that child is the one Ctrl+X means — the
	// transcript behind the panel may be drilled into someone else entirely,
	// or into nobody.
	if ctx.InspectorAgentRunningID != 0 {
		return ActionStopAgent, true
	}
	if ctx.DrilledRunningChildID != 0 {
		return ActionStopAgent, true
	}
	if ctx.Busy && ctx.QueueLen > 0 {
		return ActionClearQueue, true
	}
	return "", false
}

// availability reports whether an action can run in this context, and why not
// when it cannot.
func availability(ctx actionContext, id ActionID) (disabled bool, reason string) {
	switch id {
	case ActionStopAgent:
		if ctx.DrilledRunningChildID == 0 && ctx.InspectorAgentRunningID == 0 {
			return true, "no running agent is being inspected — Ctrl+F inspects one, or open the Agents tab"
		}
	case ActionClearQueue:
		if !ctx.Busy {
			return true, "no turn is running"
		}
		if ctx.QueueLen == 0 {
			return true, "no queued messages"
		}
	case ActionInspectAgent:
		if !ctx.HasRunningSubagent {
			return true, "no agent is running"
		}
	case ActionCancelTurn:
		if !ctx.Busy {
			return true, "no turn is running"
		}
	case ActionRollback:
		if !ctx.RollbackEligible {
			return true, "no patch has been applied in this session"
		}
	case ActionTasks:
		if !ctx.TodosActive {
			return true, "the task list is empty"
		}
	case ActionSideRail:
		if !ctx.InspectorOnScreen {
			return true, "the terminal is too narrow for the side rail"
		}
	case ActionMemory:
		if !ctx.MemoryAvailable {
			return true, "no project database is configured"
		}
	case ActionPalette, ActionSettings, ActionModels, ActionMode:
		if ctx.OtherPanelOpen {
			return true, "resolve the open panel or decision first"
		}
	case ActionCopyAnswer:
		if !ctx.CopyBlockFound {
			return true, "the conversation is empty"
		}
		if len(ctx.CopyBlock.CopyTargets) == 0 {
			return true, "the block under the reading position has no text to copy"
		}
	case ActionCopyCode:
		if !ctx.CopyBlockFound {
			return true, "the conversation is empty"
		}
		if len(codeTargets(ctx.CopyBlock)) == 0 {
			return true, "the block under the reading position contains no code block"
		}
	case ActionCopyOutput:
		if !ctx.CopyBlockFound {
			return true, "the conversation is empty"
		}
		if !hasCopyTarget(ctx.CopyBlock, conversation.SourceOutput) {
			return true, "the block under the reading position is not a tool call with captured output"
		}
	case ActionCopyPath:
		if !ctx.CopyBlockFound {
			return true, "the conversation is empty"
		}
		if !hasCopyTarget(ctx.CopyBlock, conversation.SourcePath) {
			return true, "the block under the reading position refers to no file path"
		}
	case ActionCopyInspectedPath:
		if !ctx.InspectedPathSelected {
			return true, "no changed file is selected in the inspector"
		}
	case ActionCopyPatch:
		if !ctx.InspectedPatchLoaded {
			return true, "no patch is loaded — press Enter on a changed file in the inspector"
		}
	}
	return false, ""
}

// resolveAction builds the resolved form of one catalog entry.
func resolveAction(ctx actionContext, id ActionID) (Action, bool) {
	for _, def := range actionCatalog {
		if def.id != id {
			continue
		}
		a := Action{
			ID:       def.id,
			Label:    def.label,
			Desc:     def.desc,
			KeyHint:  def.key,
			HintVerb: def.hint,
			Priority: def.priority,
		}
		if def.dynamic {
			// The mouse hint must describe what the key will do, so the verb
			// comes from the resolved capture state, not from the catalog.
			if ctx.MouseCaptured {
				a.HintVerb = "select"
			} else {
				a.HintVerb = "scroll"
			}
		}
		a.Disabled, a.DisabledReason = availability(ctx, id)
		return a, true
	}
	return Action{}, false
}

// resolveActions resolves the whole catalog in palette order.
func resolveActions(ctx actionContext) []Action {
	out := make([]Action, 0, len(actionCatalog))
	for _, def := range actionCatalog {
		a, ok := resolveAction(ctx, def.id)
		if !ok {
			continue
		}
		out = append(out, a)
	}
	return out
}

// runAction executes a resolved action. It refuses an unavailable action with
// an explanation on the toast rather than pretending to have done something —
// the same standard the mouse-capture feedback is held to.
func (m *Model) runAction(id ActionID) (tea.Model, tea.Cmd) {
	ctx := m.actionSnapshot()
	action, ok := resolveAction(ctx, id)
	if !ok {
		return *m, nil
	}
	if action.Disabled {
		return *m, m.showToast(action.Label + " — " + action.DisabledReason)
	}
	switch id {
	case ActionStopAgent:
		// The inspector's selection wins when it has one, matching ctrlXID's
		// resolution: the action the footer advertised and the action that runs
		// must be the same action, on the same child.
		target := ctx.InspectorAgentRunningID
		if target == 0 {
			target = ctx.DrilledRunningChildID
		}
		m.state.CancelSubagent(target)
		m.refreshViewport()
	case ActionClearQueue:
		m.state.ClearSteering()
		m.queuedCount = 0
		m.refreshViewport()
	case ActionInspectAgent:
		if m.drillIntoLatestRunningSubagent() {
			m.lastTranscriptHash = 0
			m.refreshViewport()
		}
	case ActionCancelTurn:
		m.cancelTurn()
	case ActionToggleMouse:
		mm, cmd := m.toggleMouseCapture()
		return mm, cmd
	case ActionFocusNext:
		return *m, m.cycleFocus(true)
	case ActionFocusPrevious:
		return *m, m.cycleFocus(false)
	case ActionPalette:
		m.openActionPalette()
	case ActionMode:
		m.openPicker("mode", "Interaction mode", "", m.modePickerItems(), "")
		m.refreshViewport()
	case ActionSettings:
		m.openSettingsBrowser("")
	case ActionMemory:
		if m.memoryDB != nil {
			m.dock.Open(memory.NewPanel(m.memoryDB, m.memoryProject))
			m.refreshViewport()
		}
	case ActionModels:
		cmd := m.openModels()
		m.refreshViewport()
		return *m, cmd
	case ActionTasks:
		m.cycleTodoPanelMode()
		m.refreshViewport()
	case ActionSideRail:
		m.railHidden = !m.railHidden
		m.resize(m.rawWidth, m.rawHeight)
	case ActionThinking:
		m.detailExpanded = !m.detailExpanded
		m.itemExpanded = map[itemKey]bool{}
		m.clearActiveToolExpansions()
		m.lastTranscriptHash = 0
		m.refreshViewport()
	case ActionRollback:
		return m.runRollback()
	case ActionHelp:
		return m.dispatchCommand("/help")
	case ActionCopyAnswer:
		return *m, m.copySelection(conversation.SourceAnswer)
	case ActionCopyCode:
		return *m, m.copySelection(conversation.SourceCode)
	case ActionCopyOutput:
		return *m, m.copySelection(conversation.SourceOutput)
	case ActionCopyPath:
		return *m, m.copySelection(conversation.SourcePath)
	case ActionCopyInspectedPath:
		return *m, m.copyInspectedPath()
	case ActionCopyPatch:
		return *m, m.copyInspectedPatch()
	}
	return *m, nil
}

// runRollback is Ctrl+R's arm-then-confirm flow. Ctrl+R is reverse-i-search
// in every readline shell, so it gets pressed reflexively; without the arming
// step a single keystroke silently rewrote the working tree. Every other
// destructive surface here (tool approval, skill/plugin removal) confirms
// first.
func (m *Model) runRollback() (tea.Model, tea.Cmd) {
	if !m.state.HasBackup() {
		return *m, nil
	}
	if !m.rollbackArmed {
		m.rollbackArmed = true
		m.state.AddMessage(session.RoleSystem,
			"Press Ctrl+R again to revert the last patch, or any other key to cancel.",
			session.ContentTypePlain)
		m.refreshViewport()
		return *m, nil
	}
	m.rollbackArmed = false
	// The error is load-bearing: a partial rollback leaves a mixed working
	// tree, and reporting success would hide that from both the user and the
	// audit trail.
	ev := registry.AuditEvent{
		Timestamp:     time.Now(),
		ToolName:      "rollback",
		ResultSummary: "Rollback applied successfully",
	}
	if err := m.state.RollbackBackup(); err != nil {
		ev.Error = err.Error()
		ev.ResultSummary = "Rollback failed"
	}
	m.state.LogToolCall(ev)
	m.refreshViewport()
	return *m, nil
}
