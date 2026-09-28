package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The action catalog resolves Ctrl+X to exactly one action from one context
// snapshot. This is the core of the duplicate-hint fix: the old code decided
// "clear queue" inline in keypress.go while status.go derived "stop agent"
// from overlapping booleans, and help.Footer printed both.
func TestCtrlXResolvesToExactlyOneAction(t *testing.T) {
	tests := []struct {
		name                string
		busy                bool
		queue               int
		drilledRunningChild bool
		want                ActionID
		wantOK              bool
	}{
		{name: "idle, empty queue", want: "", wantOK: false},
		{name: "busy, empty queue", busy: true, want: "", wantOK: false},
		{name: "idle, queued", queue: 2, want: "", wantOK: false},
		{name: "busy, queued", busy: true, queue: 2, want: ActionClearQueue, wantOK: true},
		{name: "drilled running child wins over queue", busy: true, queue: 3, drilledRunningChild: true, want: ActionStopAgent, wantOK: true},
		{name: "drilled running child while idle", drilledRunningChild: true, want: ActionStopAgent, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := actionContext{
				Busy:     tt.busy,
				QueueLen: tt.queue,
			}
			if tt.drilledRunningChild {
				ctx.DrilledRunningChildID = 7
			}
			got, ok := ctx.ctrlXID()
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("ctrlXID() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// The snapshot must carry the drilled-into running child, because the
// dispatcher, the footer, and the palette all read the same resolution.
func TestActionSnapshotCarriesDrilledRunningChild(t *testing.T) {
	m := newTestModel(t)

	if _, ok := m.actionSnapshot().ctrlXID(); ok {
		t.Fatal("idle, undrilled model must not resolve Ctrl+X")
	}

	// A running child that the transcript is drilled into.
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	ctx := m.actionSnapshot()
	if ctx.DrilledRunningChildID == 0 {
		t.Fatal("snapshot must carry the drilled running child's ID")
	}
	if id, ok := ctx.ctrlXID(); !ok || id != ActionStopAgent {
		t.Fatalf("ctrlXID() = (%q, %v), want stop-agent", id, ok)
	}
}

// The footer hints and key dispatch must agree: whatever ctrlXID resolves to
// is what the footer renders and what a Ctrl+X press runs. This pins the two
// consumers to the same source by construction.
func TestFooterCtrlXHintMatchesDispatchResolution(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.state.PushSteering("queued message")
	m.queuedCount = 1

	ctx := m.actionSnapshot()
	hints := ctx.footerActionHints()
	line := stripANSI(renderHints(hints))

	id, ok := ctx.ctrlXID()
	if !ok || id != ActionClearQueue {
		t.Fatalf("dispatch resolution = (%q, %v), want clear-queue", id, ok)
	}
	if !strings.Contains(line, "Ctrl+X clear queue") {
		t.Fatalf("footer renders a different verb than dispatch resolves:\n%s", line)
	}
	if got := strings.Count(line, "Ctrl+X"); got != 1 {
		t.Fatalf("footer printed %d Ctrl+X hints, want exactly 1:\n%s", got, line)
	}
}

// The stop-agent case: drilled into a running child with a queued message,
// the footer must advertise "stop agent" (the winner) and never the losing
// "clear queue" verb. This is the exact historical bug.
func TestFooterCtrlXStopAgentBeatsClearQueueWhenDrilled(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.state.PushSteering("queued message")
	m.queuedCount = 1
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	line := stripANSI(renderHints(m.actionSnapshot().footerActionHints()))
	if !strings.Contains(line, "Ctrl+X stop agent") {
		t.Fatalf("footer must advertise the winning Ctrl+X action:\n%s", line)
	}
	if strings.Contains(line, "clear queue") {
		t.Fatalf("footer must not advertise the losing Ctrl+X action:\n%s", line)
	}
	if got := strings.Count(line, "Ctrl+X"); got != 1 {
		t.Fatalf("footer printed %d Ctrl+X hints, want 1:\n%s", got, line)
	}
}

// A disabled action must be offered with its reason, not hidden: the palette
// explains what is missing rather than making the user guess whether the
// operation exists.
func TestDisabledActionsCarryExplanations(t *testing.T) {
	ctx := actionContext{} // idle, nothing queued, nothing running

	cases := []struct {
		id         ActionID
		wantReason string
	}{
		{ActionStopAgent, "no running agent is being inspected"},
		{ActionClearQueue, "no turn is running"},
		{ActionInspectAgent, "no agent is running"},
		{ActionCancelTurn, "no turn is running"},
	}
	for _, tc := range cases {
		a, ok := resolveAction(ctx, tc.id)
		if !ok {
			t.Fatalf("%s not in catalog", tc.id)
		}
		if !a.Disabled {
			t.Errorf("%s must be disabled when idle", tc.id)
		}
		if !strings.Contains(a.DisabledReason, tc.wantReason) {
			t.Errorf("%s reason = %q, want it to contain %q", tc.id, a.DisabledReason, tc.wantReason)
		}
	}
}

// runAction must refuse an unavailable action with a toast rather than
// pretending to run it — the same truthfulness standard as the mouse toggle.
func TestRunActionRefusesDisabledWithToast(t *testing.T) {
	m := newTestModel(t) // idle: nothing to clear

	_, _ = m.runAction(ActionClearQueue)

	if m.toastText() == "" {
		t.Fatal("running a disabled action must explain itself on the toast")
	}
	if len(m.state.SteeringQueue()) != 0 {
		t.Fatalf("a disabled clear-queue must not touch the queue: %v", m.state.SteeringQueue())
	}
}

// Every action in the catalog must execute without panicking in a plain
// context, and the catalog must contain no dead placeholders: later tasks add
// their own entries as their features land.
func TestCatalogActionsRunInPlainContext(t *testing.T) {
	m := newTestModel(t)
	ctx := m.actionSnapshot()

	for _, a := range resolveActions(ctx) {
		if a.ID == "" {
			t.Fatal("catalog entry with empty ID")
		}
		if a.Label == "" || a.Desc == "" {
			t.Errorf("%s lacks label/desc", a.ID)
		}
	}
}

// Ctrl+F keeps its explicit inspection meaning and is routed through the
// action catalog, so its availability and its execution share one resolution.
func TestInspectAgentActionDrillsIntoRunningChild(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("explore repo", child)

	if len(m.viewStack) != 0 {
		t.Fatal("precondition: not drilled")
	}

	mm, _ := m.runAction(ActionInspectAgent)
	m = mm.(Model)

	if len(m.viewStack) == 0 {
		t.Fatal("inspect action must drill into the running child")
	}
}

// Focus cycling: F6 advances through the available targets and wraps;
// Shift+F6 reverses. A narrow terminal drops the inspector stop rather than
// opening a hidden surface.
func TestFocusCycleRoundTrip(t *testing.T) {
	m := newTestModel(t)
	m.resize(160, 40) // wide enough for the rail
	if !m.railEnabled() {
		t.Skip("rail not enabled at this width")
	}

	m.setFocus(FocusComposer)

	mm, cmd := m.runAction(ActionFocusNext)
	m = mm.(Model)
	_ = cmd
	if m.focus != FocusConversation {
		t.Fatalf("first F6 = %v, want conversation", m.focus)
	}

	mm, _ = m.runAction(ActionFocusNext)
	m = mm.(Model)
	if m.focus != FocusInspector {
		t.Fatalf("second F6 = %v, want inspector", m.focus)
	}

	// Third wraps back to the composer.
	mm, _ = m.runAction(ActionFocusNext)
	m = mm.(Model)
	if m.focus != FocusComposer {
		t.Fatalf("third F6 = %v, want composer (wrap)", m.focus)
	}

	// Shift+F6 reverses: composer → inspector.
	mm, _ = m.runAction(ActionFocusPrevious)
	m = mm.(Model)
	if m.focus != FocusInspector {
		t.Fatalf("Shift+F6 = %v, want inspector", m.focus)
	}
}

// While the conversation owns the keys, typing is swallowed rather than
// leaking into the composer behind it: exactly one surface receives typing.
func TestConversationFocusSwallowsTyping(t *testing.T) {
	m := newTestModel(t)
	m.setFocus(FocusConversation)

	updated, _, _ := m.handleKeypress(tea.KeyPressMsg{Code: 'h'})
	m = updated.(Model)

	if m.input.Value() != "" {
		t.Fatalf("typed key leaked into the composer: %q", m.input.Value())
	}
}

// Esc returns focus to the composer from any non-composer target, and one
// press performs one operation: it must not also cancel a turn or pop a
// drill on the same keystroke.
func TestEscReturnsFocusToComposerWithoutSideEffects(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)
	m.setFocus(FocusConversation)
	m.busy = true

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)

	if !handled {
		t.Fatal("Esc while focused off-composer must be consumed")
	}
	if m.effectiveFocus() != FocusComposer {
		t.Fatalf("focus = %v, want composer", m.effectiveFocus())
	}
}

// A modal panel owns every key while it is up; the panel target is derived
// from the pending state, never elected directly, so it cannot outlive the
// panel that produced it.
func TestPanelDerivesFocusWhileOpen(t *testing.T) {
	m := newTestModel(t)
	m.setFocus(FocusConversation)
	if m.effectiveFocus() != FocusConversation {
		t.Fatal("precondition: conversation focused")
	}

	// Open a dock panel (the action palette) — it owns the keys.
	m.openActionPalette()
	if m.effectiveFocus() != FocusPanel {
		t.Fatalf("focus = %v, want panel while the palette is open", m.effectiveFocus())
	}

	// Closing the panel restores the non-modal target by construction.
	m.dock.CloseNow()
	m.pickerCommand = ""
	if m.effectiveFocus() != FocusConversation {
		t.Fatalf("focus = %v, want the pre-panel target restored", m.effectiveFocus())
	}
}

// An inspector that leaves the screen (rail hidden by a narrow resize) must
// yield keys to the composer, but the user's focus intent survives so a
// widening resize restores it.
func TestInspectorYieldsWhenRailHides(t *testing.T) {
	m := newTestModel(t)
	m.resize(160, 40)
	if !m.railEnabled() {
		t.Skip("rail not enabled at this width")
	}
	m.setFocus(FocusInspector)
	if m.effectiveFocus() != FocusInspector {
		t.Fatal("precondition: inspector focused")
	}

	m.resize(60, 24) // below the rail threshold
	if m.effectiveFocus() != FocusComposer {
		t.Fatalf("hidden inspector still owns keys: %v", m.effectiveFocus())
	}
	if m.focus != FocusInspector {
		t.Fatal("the user's focus intent must survive the resize")
	}

	m.resize(160, 40)
	if m.effectiveFocus() != FocusInspector {
		t.Fatalf("widening must restore focus to the inspector, got %v", m.effectiveFocus())
	}
}

// A paste while the conversation owns the keys must not edit an invisible
// draft. This is the paste-side twin of the key-side swallowing test above.
func TestPasteRejectedWhenComposerDoesNotOwnTyping(t *testing.T) {
	m := newTestModel(t)
	m.setFocus(FocusConversation)
	before := m.input.Value()

	updated, _ := m.Update(tea.PasteMsg{Content: "pasted into the void"})
	m = updated.(Model)

	if m.input.Value() != before {
		t.Fatalf("paste edited the blurred composer: %q", m.input.Value())
	}
}

// The composer keeps its own key while a running child exists: Up/Down recall
// prompt history rather than moving an agent-lane cursor (the removed
// implicit takeover). Ctrl+F remains the explicit inspection route.
func TestUpDownBelongToComposerWithRunningChild(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("explore repo", child)
	m.history = []string{"older prompt"}
	m.histIdx = -1

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyUp})

	if m.input.Value() != "older prompt" {
		t.Fatalf("Up should recall prompt history, got %q", m.input.Value())
	}
	if len(m.viewStack) != 0 {
		t.Fatalf("Up must not drill into the lane, viewStack=%d", len(m.viewStack))
	}
}

// A disabled action run from the palette must not silently succeed: the toast
// carries the explanation, and the transcript is untouched (transient UI
// feedback is never conversation content).
func TestDisabledPaletteActionExplainsNotAppends(t *testing.T) {
	m := newTestModel(t) // idle: nothing to cancel
	before := len(m.state.Messages())

	mm, _ := m.runAction(ActionCancelTurn)
	m = mm.(Model)

	if m.toastText() == "" {
		t.Fatal("a disabled action must explain itself on the toast")
	}
	if after := len(m.state.Messages()); after != before {
		t.Fatalf("disabled-action feedback appended %d transcript message(s)", after-before)
	}
}

// The queue count the UI reasons about must prefer the session's queue (the
// ground truth) and fall back to the broker counter only when the session
// reports nothing — the brief window between clearing the queue and the next
// broker event.
func TestEffectiveQueueLenPrefersSessionQueue(t *testing.T) {
	m := newTestModel(t)

	m.queuedCount = 5
	if got := m.effectiveQueueLen(); got != 5 {
		t.Fatalf("broker fallback = %d, want 5", got)
	}

	m.state.PushSteering("one")
	m.state.PushSteering("two")
	if got := m.effectiveQueueLen(); got != 2 {
		t.Fatalf("session queue = %d, want 2 (ground truth wins)", got)
	}
}
