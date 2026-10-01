package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
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
	// ActionToggleInspector and ActionSideRail are SEPARATE ids because they
	// were one id with one key that did something different from what its label
	// said: Ctrl+B toggles the inspector, while the row labelled "Side rail"
	// claimed that key. The catalog is the single place a key's meaning is
	// declared, so a label that names the other surface is exactly the drift
	// the catalog exists to prevent.
	ActionToggleInspector      ActionID = "toggle-inspector"
	ActionSideRail             ActionID = "side-rail"
	ActionExpandInspector      ActionID = "expand-inspector"
	ActionThinking             ActionID = "thinking"
	ActionTranscriptNotebook   ActionID = "transcript-notebook"
	ActionTranscriptLegacy     ActionID = "transcript-legacy"
	ActionTranscriptConfigured ActionID = "transcript-configured"
	ActionNotebookOrder        ActionID = "notebook-event-order"
	ActionRollback             ActionID = "rollback"
	ActionHelp                 ActionID = "help"

	// Copy actions are separate IDs rather than one "copy" with an argument:
	// "Copy answer" and "Copy output" are different promises about different
	// bytes, and the palette must be able to offer, disable, and explain each
	// one on its own terms.
	ActionCopyAnswer ActionID = "copy-answer"
	ActionSelectText ActionID = "select-text"
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
	// ActionCopyContext copies the open Context row. It is its own action for
	// the same reason the two above are: it reads a different source (the
	// inspector's Context tab rather than the conversation or the Changes
	// list), and one action with three sources would have to guess which.
	ActionCopyContext ActionID = "copy-context"
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
		id: ActionToggleInspector, label: "Inspector",
		desc: "toggle the conversation inspector for this session",
		key:  "Ctrl+B", priority: actionPriorityOptional,
	},
	{
		id: ActionSideRail, label: "Side rail",
		desc:     "show or hide the widescreen side rail for this session",
		priority: actionPriorityOptional,
	},
	{
		id: ActionExpandInspector, label: "Expand inspector",
		desc: "expand the inspector over the body (Esc returns it)",
		key:  "Ctrl+Shift+B", priority: actionPriorityOptional,
	},
	{
		id: ActionThinking, label: "Thinking blocks",
		desc: "expand or collapse reasoning blocks in the transcript",
		key:  "Ctrl+G", priority: actionPriorityOptional,
	},
	{id: ActionTranscriptNotebook, label: "Use notebook transcript", desc: "show the notebook transcript for this session", priority: actionPriorityOptional},
	{id: ActionTranscriptLegacy, label: "Use legacy transcript", desc: "show the legacy transcript for this session", priority: actionPriorityOptional},
	{id: ActionTranscriptConfigured, label: "Use configured transcript view", desc: "clear the session override and follow the configured default", priority: actionPriorityOptional},
	{id: ActionNotebookOrder, label: "Toggle notebook event order", desc: "switch the current structured narration between sections and event order", priority: actionPriorityOptional},
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
		desc: "copy the selection if there is one, otherwise the answer the reader is on",
		key:  "y", priority: actionPriorityLikely,
	},
	{
		id: ActionSelectText, label: "Select text",
		desc: "begin selecting in the conversation; arrows extend, y copies, esc clears",
		key:  "v", priority: actionPriorityLikely,
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
	{
		id: ActionCopyContext, label: "Copy context detail",
		desc:     "copy the context section or request entry open in the inspector, redacted",
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
	// InspectorOnScreen reports that the inspector is drawn this frame (side,
	// dock, or body-expanded) — distinct from suspended, which is "open but
	// yielding the slot".
	InspectorOnScreen bool
	// InspectorAvailable reports that this build wired an inspector at all, so
	// Ctrl+B has something to toggle.
	InspectorAvailable bool
	// InspectorExpanded reports that the inspector is already occupying the
	// body, so the expand action has nothing left to do.
	InspectorExpanded bool
	// SideRailAvailable reports that the read-only rail renders at the current
	// width AND is enabled in settings. It is a different question from
	// InspectorOnScreen: the rail and the inspector are separate surfaces with
	// separate visibility rules, and collapsing them into one field is what let
	// a row labelled "Side rail" claim the inspector's key.
	SideRailAvailable bool
	TodosActive       bool
	RollbackEligible  bool
	MemoryAvailable   bool
	MouseCaptured     bool
	// CopyBlock is the block a copy action would act on: the one under the
	// reading anchor, or the newest when the reader is following. It is the
	// resolved block rather than the sources it offers, so availability and
	// dispatch cannot disagree about which block was meant.
	CopyBlock conversation.Block
	// CopyBlockFound reports whether CopyBlock resolved at all.
	CopyBlockFound bool
	// HasSelection reports that the reader has selected text on the
	// conversation surface, so a copy would take that text rather than the
	// block's own targets.
	HasSelection         bool
	SelectionActive      bool
	TranscriptView       config.TranscriptView
	TranscriptOverridden bool
	// ConversationFocused reports that the conversation owns the keys, which
	// is what a selection gesture needs.
	ConversationFocused bool
	// TranscriptEmpty reports that there is nothing to select in.
	TranscriptEmpty bool
	// InspectedPathSelected reports that the inspector's Changes tab has a
	// selected path, so a copy of it has something to copy.
	InspectedPathSelected bool
	// InspectedPatchLoaded reports that a patch has been fetched for the
	// inspected file. It is separate from the selection because selecting a
	// file reads nothing: the two facts arrive at different times, and an
	// action enabled on the selection alone would promise a patch that does
	// not exist yet.
	InspectedPatchLoaded bool
	// ContextDetailOpen reports that a row's body is open on the inspector's
	// Context tab, so the context copy has something to copy. It is only ever
	// set while that tab is the one on display: a body left open behind a tab
	// the reader switched away from is not what they are looking at.
	ContextDetailOpen      bool
	NotebookOrderTarget    itemKey
	NotebookOrderAvailable bool
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

// actionSnapshotCache memoizes the resolved action snapshot between frames.
//
// The footer renders on EVERY frame — a spinner tick is 80ms — and resolving the
// snapshot is not free: the copy actions read m.copyBlock(), which builds the
// whole conversation document (grouping every transcript item and FNV-hashing
// every block's full text). Doing that per frame made the hint cluster's cost
// O(total conversation text) per tick, which a long session pays forever for a
// string that only changes when the state behind it does.
//
// key is the CHEAP state the snapshot is derived from. Anything expensive — the
// document, the roster, the inspector's selection — is reached through the key
// rather than hashed into it: the key must be cheaper than the work it guards or
// the cache is a pessimisation.
type actionSnapshotCache struct {
	ctx actionContext
	key actionSnapshotKey
	// valid distinguishes "resolved, and it was empty" from "not resolved yet".
	valid bool
}

// actionSnapshotKey is the cheap state actionSnapshot's result depends on.
//
// Every field is a value the snapshot reads directly. A field MISSING from here
// does not corrupt anything — the snapshot is a pure function of the key plus
// the model — but it does mean a stale answer is served until something else in
// the key changes, which for availability is a disabled action that should be
// enabled. So the key errs towards being too wide: the fields are all ints,
// bools and strings already in memory, and comparing a dozen of them is nothing
// next to building a document.
type actionSnapshotKey struct {
	busy             bool
	queueLen         int
	runningSubagents bool
	railEnabled      bool
	todosActive      bool
	rollbackEligible bool
	memoryAvailable  bool
	mouseCaptured    bool
	// transcriptVersion stands in for the document and the block spans: both are
	// rebuilt by the same refresh, so one counter covers every content change
	// that could alter what a copy action resolves to.
	//
	// It does NOT cover a change of READING POSITION, and that is what the two
	// fields below are for. Scrolling moves no content, so transcriptVersion is
	// untouched by it — but copyBlock() resolves the block from the reader's
	// position, so a snapshot memoized before a scroll would go on offering
	// "Copy answer" for a block the reader has scrolled away from.
	transcriptVersion uint64
	// viewportFollow is the first input of copyBlock(): while it is set the copy
	// target is the NEWEST block, and while it is clear it is the anchored one.
	viewportFollow bool
	// anchorBlock is the block the reader is on, and viewportTop the fallback
	// copyBlock uses when there is no anchor yet. Both are recorded only while
	// NOT following — a following reader holds no position (captureReadingAnchor
	// clears the anchor deliberately), and the viewport's bottom offset moves
	// with the content, so keying on it there would invalidate the cache on
	// every spinner tick and defeat the memo it guards.
	//
	// The block identity is a string, not the whole anchor: copyBlock() reads
	// the identity and the fallback position, and the offset inside the block
	// does not change which block a copy resolves to.
	anchorBlock conversation.BlockID
	viewportTop int
	// pickerCommand distinguishes the palette from any other dock panel, which
	// is what OtherPanelOpen is computed from.
	pickerCommand   string
	panelOwnsKeys   bool
	drilledID       int64
	hasSelection    bool
	selectionActive bool
	conversation    bool
	inspector       bool
	inspTab         inspector.Tab
	inspRendering   bool
	inspExpanded    bool
	inspAgentOpen   bool
	// inspAgentID is the selected agent's runtime ID. It is only meaningful
	// while inspAgentOpen, and it is the value the snapshot actually reads, so
	// including the "open" flag above is bookkeeping and this is the data.
	inspAgentID int64
}

// actionSnapshotKeyOf reads the cheap key from the model.
func (m Model) actionSnapshotKeyOf() actionSnapshotKey {
	k := actionSnapshotKey{
		busy:              m.busy,
		queueLen:          m.effectiveQueueLen(),
		runningSubagents:  m.hasRunningSubagent(),
		railEnabled:       m.railEnabled(),
		todosActive:       m.state != nil && len(m.state.Todos()) > 0,
		rollbackEligible:  m.state != nil && m.state.HasBackup(),
		memoryAvailable:   m.memoryDB != nil,
		mouseCaptured:     m.effectiveMouseCapture(),
		transcriptVersion: m.transcriptVersion,
		pickerCommand:     m.pickerCommand,
		panelOwnsKeys:     m.panelOwnsKeys(),
		hasSelection:      m.hasSelection(),
		selectionActive:   m.selectionActive(),
		conversation:      m.effectiveFocus() == FocusConversation,
	}
	// The reader's position, recorded only when they have one. See the field
	// comments: a following reader is pinned to the bottom, and the offset that
	// goes with "the bottom" moves with every new line of output.
	k.viewportFollow = m.viewportFollow
	if !m.viewportFollow {
		k.anchorBlock = m.readingAnchor.Block
		k.viewportTop = m.viewport.YOffset()
	}
	if v, ok := m.drilledInto(); ok && v.Status == session.SubagentRunning {
		k.drilledID = v.ID
	}
	if m.inspector != nil {
		k.inspector = true
		k.inspRendering = m.inspector.isRendering()
		k.inspExpanded = m.inspector.replacesBodyOnly()
		k.inspTab = m.inspector.model.SelectedTab()
		k.inspAgentOpen = m.inspector.model.AgentDetailOpen()
		if k.inspAgentOpen {
			k.inspAgentID = m.inspector.model.AgentIDSelected()
		}
	}
	return k
}

// actionSnapshot takes the single context snapshot every resolution path
// uses.
//
// The result is memoized against actionSnapshotKey, because the footer resolves
// it on every frame and one of its inputs — the conversation document, reached
// through the copy actions — costs a full rebuild of the conversation's blocks.
// The cache lives on the model rather than in a package variable so two models
// (a test and a live session, or the parent and a drilled child's render) can
// never share an answer.
func (m Model) actionSnapshot() actionContext {
	key := m.actionSnapshotKeyOf()
	if m.actionCache != nil && m.actionCache.valid && m.actionCache.key == key {
		return m.actionCache.ctx
	}
	ctx := m.resolveActionSnapshot(key)
	// The write goes through the shared pointer: the footer calls this from
	// View's value receiver, and a struct field would be written to a copy and
	// discarded — the cache would never hit.
	if m.actionCache != nil {
		m.actionCache.ctx, m.actionCache.key, m.actionCache.valid = ctx, key, true
	}
	return ctx
}

// invalidateActionSnapshot drops the memoized snapshot.
//
// It exists for the paths that change an input the key does not carry — most
// importantly an inspector whose data changed without any keyed field moving,
// so a copy action's availability would otherwise be judged on the previous
// tab's selection.
func (m *Model) invalidateActionSnapshot() {
	if m.actionCache != nil {
		m.actionCache.valid = false
	}
}

// resolveActionSnapshot builds the snapshot from the model. It takes the key so
// the fields already read for it are not read twice.
func (m Model) resolveActionSnapshot(key actionSnapshotKey) actionContext {
	ctx := actionContext{
		Busy:                  key.busy,
		QueueLen:              key.queueLen,
		HasRunningSubagent:    key.runningSubagents,
		SideRailAvailable:     key.railEnabled,
		TodosActive:           key.todosActive,
		RollbackEligible:      key.rollbackEligible,
		MemoryAvailable:       key.memoryAvailable,
		MouseCaptured:         key.mouseCaptured,
		DrilledRunningChildID: key.drilledID,
		HasSelection:          key.hasSelection,
		SelectionActive:       key.selectionActive,
		ConversationFocused:   key.conversation,
	}
	ctx.TranscriptView = m.effectiveTranscriptView()
	ctx.TranscriptOverridden = m.transcriptViewOverride != nil
	if key.inspector {
		ctx.InspectorAvailable = true
		ctx.InspectorOnScreen = key.inspRendering
		ctx.InspectorExpanded = key.inspExpanded
	}
	// The dock holds one panel; the palette is one of them, so it is excluded
	// or every action in the list would read as "resolve the open panel".
	ctx.OtherPanelOpen = key.panelOwnsKeys && m.pickerCommand != actionPaletteCommand
	// The inspector's own selection is a second, independent target: the
	// Agents tab can be showing a running child while the transcript is
	// drilled into a different one, and only the runtime ID identifies which
	// the user means.
	if key.inspector && key.inspTab == inspector.TabAgents && key.inspAgentOpen &&
		m.inspector.model.SelectedAgentRunning() {
		ctx.InspectorAgentRunningID = key.inspAgentID
	}
	// The copy actions resolve their block through the same path dispatch
	// uses, so availability and behaviour cannot describe different blocks.
	ctx.CopyBlock, ctx.CopyBlockFound = m.copyBlock()
	ctx.NotebookOrderTarget, ctx.NotebookOrderAvailable = m.notebookOrderTarget()
	ctx.TranscriptEmpty = len(m.blockRenderSpans) == 0
	// The inspected-change actions read the inspector's own state, which is
	// the only place that knows what is selected and what has been fetched.
	if key.inspector {
		_, ctx.InspectedPathSelected = m.inspector.model.CapturePath()
		_, _, _, ctx.InspectedPatchLoaded = m.inspector.model.CapturedPatch()
		// Only when the Context tab is on display: a body left open under a
		// tab the reader has switched away from is not "the context entry they
		// are looking at", and enabling the action on it would offer a copy of
		// something off screen.
		if key.inspTab == inspector.TabContext {
			ctx.ContextDetailOpen = m.inspector.model.ContextDetailOpen()
		}
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
	case ActionTranscriptNotebook:
		if ctx.TranscriptView == config.TranscriptNotebook {
			return true, "notebook transcript is already active"
		}
		if ctx.SelectionActive {
			return true, "clear the transcript selection or finish the drag before switching views"
		}
	case ActionTranscriptLegacy:
		if ctx.TranscriptView == config.TranscriptLegacy {
			return true, "legacy transcript is already active"
		}
		if ctx.SelectionActive {
			return true, "clear the transcript selection or finish the drag before switching views"
		}
	case ActionTranscriptConfigured:
		if !ctx.TranscriptOverridden {
			return true, "the transcript already follows the configured default"
		}
		if ctx.SelectionActive {
			return true, "clear the transcript selection or finish the drag before switching views"
		}
	case ActionNotebookOrder:
		if ctx.TranscriptView != config.TranscriptNotebook {
			return true, "switch to the notebook transcript first"
		}
		if !ctx.NotebookOrderAvailable {
			return true, "move the reading anchor to a structured narration"
		}
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
	case ActionToggleInspector:
		if !ctx.InspectorAvailable {
			return true, "the conversation inspector is not available in this build"
		}
	case ActionSideRail:
		if !ctx.SideRailAvailable {
			return true, "the side rail is off in settings, or the terminal is too narrow for it"
		}
	case ActionExpandInspector:
		if !ctx.InspectorOnScreen {
			return true, "open the inspector first (Ctrl+B)"
		}
		if ctx.InspectorExpanded {
			return true, "the inspector is already expanded"
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
		// A selection is copyable on its own terms: it needs no block TARGET,
		// because it is a phrase inside the readable text rather than one of
		// the block's offered payloads. Without this, `y` over a selection
		// would be greyed out with "no text to copy" while the text sat
		// highlighted on screen.
		if ctx.HasSelection {
			return false, ""
		}
		if !ctx.CopyBlockFound {
			return true, "the conversation is empty"
		}
		if len(ctx.CopyBlock.CopyTargets) == 0 {
			return true, "the block under the reading position has no text to copy"
		}
	case ActionSelectText:
		if !ctx.ConversationFocused {
			return true, "focus the conversation to select text in it"
		}
		if ctx.TranscriptEmpty {
			return true, "the conversation is empty"
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
	case ActionCopyContext:
		if !ctx.ContextDetailOpen {
			return true, "no context entry is open — press Enter on a row in the inspector's Context tab"
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
		if id == ActionTranscriptNotebook || id == ActionTranscriptLegacy || id == ActionTranscriptConfigured {
			status := "configured default"
			if ctx.TranscriptOverridden {
				status = "session override"
			}
			a.Desc += " Current: " + string(ctx.TranscriptView) + " (" + status + ")."
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
	case ActionTranscriptNotebook:
		m.setTranscriptView(config.TranscriptNotebook)
		m.transcriptViewOverride = transcriptViewPtr(config.TranscriptNotebook)
		m.invalidateActionSnapshot()
	case ActionTranscriptLegacy:
		m.setTranscriptView(config.TranscriptLegacy)
		m.transcriptViewOverride = transcriptViewPtr(config.TranscriptLegacy)
		m.invalidateActionSnapshot()
	case ActionTranscriptConfigured:
		m.transcriptViewOverride = nil
		m.setTranscriptView(m.configuredTranscriptView())
		m.invalidateActionSnapshot()
	case ActionNotebookOrder:
		m.toggleNotebookWorkOrder(ctx.NotebookOrderTarget)
		m.lastTranscriptHash = 0
		m.refreshViewport()
		m.invalidateActionSnapshot()
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
	case ActionToggleInspector:
		if m.inspector != nil {
			m.inspector.toggle(m.inspectorSideAvailable())
			m.resize(m.rawWidth, m.rawHeight)
		}
	case ActionSideRail:
		m.railHidden = !m.railHidden
		m.resize(m.rawWidth, m.rawHeight)
	case ActionExpandInspector:
		if m.inspector != nil {
			m.inspector.expandBody()
			m.refreshViewport()
		}
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
		// Mirror the availability check above: a live selection is copied
		// directly, because it is not a target on a block.
		if m.hasSelection() {
			return *m, m.copySelectionText()
		}
		return *m, m.copySelection(conversation.SourceAnswer)
	case ActionSelectText:
		m.beginSelectionAtReadingAnchor()
		m.refreshViewport()
		return *m, nil
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
	case ActionCopyContext:
		return *m, m.copyInspectedContext()
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
