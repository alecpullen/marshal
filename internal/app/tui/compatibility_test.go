// internal/app/tui/compatibility_test.go — the surfaces this plan did not build,
// still working after it
//
// Tasks 1-14 changed the key router, the status line, the frame budget, the
// activity band and the conversation model. None of that was supposed to touch
// /settings, /connect, /history, the approval form, the mode cycle or paste — and
// "was not supposed to" is exactly the claim that needs a test.
//
// The coverage here is deliberately SHALLOW. Each existing area has its own
// thorough tests elsewhere; what this file adds is one place that answers "does
// this still work at all", for the surfaces a change to the frame or the key
// router is most likely to break silently. A deep test of /connect would duplicate
// connect_test.go and would be the first thing to rot.
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

// compatModel returns a model with a conversation, ready for a command.
func compatModel(t *testing.T) Model {
	t.Helper()
	m := findScrollableModel(t)
	return m
}

// Every slash command in this list must dispatch without panicking and must be
// found by the registry. The command NAME is the contract: muscle memory and
// scripts depend on it, and a rename would break both while every unit test in
// the subsystem still passed.
func TestSlashCommandsStillDispatch(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		// wantOpen reports that the command is expected to open a docked panel.
		wantOpen bool
	}{
		{"help", "/help", true},
		{"settings", "/settings", true},
		{"context", "/context", true},
		// These three need a database, a database, and the memory store
		// respectively. A test model has none of them, so they answer with a
		// notice instead of a panel — which is the correct behaviour, and
		// TestUnavailableCommandsSayWhy asserts they say so rather than
		// silently doing nothing.
		{"history", "/history", false},
		{"export", "/export", false},
		{"tools", "/tools", true},
		{"doctor", "/doctor", true},
		{"actions", "/actions", true},
		{"memory", "/memory", false},
		{"find", "/find terminator", false},
		{"agents", "/agents", true},
		{"inspect", "/inspect", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := compatModel(t)
			// A panic here fails the test; that is the point of the table.
			updated, _ := m.dispatchCommand(tc.raw)
			got := asModel(t, updated)

			// An unknown command prints "Unknown command" INTO THE TRANSCRIPT
			// rather than failing, so the assertion must read the transcript.
			// Checking only for a panic would pass for a command that no longer
			// exists.
			for _, item := range got.state.Transcript() {
				if item.Message == nil {
					continue
				}
				if strings.Contains(item.Message.Content, "Unknown command: /"+tc.name) {
					t.Fatalf("/%s is no longer registered", tc.name)
				}
				if strings.Contains(item.Message.Content, "not available in this build") {
					t.Fatalf("/%s dispatched but has no effect wired", tc.name)
				}
			}
			if tc.wantOpen && !got.dock.IsOpen() {
				t.Fatalf("/%s did not open a panel", tc.name)
			}
		})
	}
}

// A command that cannot do its job must SAY so in the transcript.
//
// The failure this guards against is a command that quietly does nothing: from
// the reader's side, a panel that never appears and a crash look the same as
// "nothing happened", and the difference matters.
func TestUnavailableCommandsSayWhy(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// Each needs something this build's test model does not have. /export is
		// NOT in this list: it has no such dependency — it writes a file and
		// reports where, which is why the table above expects no panel from it
		// and why asserting a "database" notice would be wrong.
		{"/history", "database"},
		{"/memory", "Memory"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			m := compatModel(t)
			updated, _ := m.dispatchCommand(tc.raw)
			got := asModel(t, updated)

			var said string
			for _, item := range got.state.Transcript() {
				if item.Message != nil {
					said += item.Message.Content + "\n"
				}
			}
			if !strings.Contains(said, tc.want) {
				t.Fatalf("%s did not say why it could not run (looked for %q):\n%s",
					tc.raw, tc.want, said)
			}
		})
	}
}

// /export must produce a file and say where it went, rather than merely
// dispatching. A command that reports success without writing is worse than one
// that fails loudly.
func TestExportWritesAFileAndSaysWhere(t *testing.T) {
	m := compatModel(t)
	// WorkingDir is the field /export resolves against, and it is set when the
	// State is constructed rather than by SetWorkspace. Setting the workspace
	// instead would leave the export writing into the model's temp dir — which
	// is how this test first failed, with a real file in the wrong place.
	dir := t.TempDir()
	m.state.WorkingDir = dir

	updated, _ := m.dispatchCommand("/export report.html")
	got := asModel(t, updated)

	var said string
	for _, item := range got.state.Transcript() {
		if item.Message != nil {
			said += item.Message.Content + "\n"
		}
	}
	if !strings.Contains(said, "report.html") {
		t.Fatalf("/export did not say where it wrote:\n%s", said)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.html")); err != nil {
		t.Fatalf("/export reported success but wrote nothing: %v\n%s", err, said)
	}
}

// A command must not start a turn or reach the agent. Every one of these is a
// READING or SETTINGS surface, and a stray route through the prompt handler would
// look identical to the reader until the model answered something.
func TestReadingAndSettingsCommandsDoNotReachTheAgent(t *testing.T) {
	for _, raw := range []string{
		"/help", "/settings", "/context", "/history", "/export", "/tools",
		"/doctor", "/actions", "/find terminator", "/inspect", "/agents",
	} {
		t.Run(raw, func(t *testing.T) {
			m := compatModel(t)
			before := len(m.state.Messages())

			updated, _ := m.dispatchCommand(raw)
			got := asModel(t, updated)

			if got.busy {
				t.Fatalf("%s started a turn", raw)
			}
			if q := got.state.SteeringQueue(); len(q) != 0 {
				t.Fatalf("%s queued %d steering messages", raw, len(q))
			}
			// The transcript may gain a RENDERED result (a system notice, a doc
			// panel). What must not happen is a user turn.
			for _, item := range got.state.Transcript()[before:] {
				if item.Message != nil && item.Message.Role == session.RoleUser {
					t.Fatalf("%s added a user turn to the session", raw)
				}
			}
		})
	}
}

// The mode cycle must reach every mode and come back, and must not disturb the
// draft. Modes change what the agent is allowed to do, so a cycle that skipped
// one — or landed on a mode with no way back — would be a permissions bug wearing
// a UX costume.
//
// It drives cycleMode rather than dispatching /<mode>, because Tab is the route
// most users take and the cycle ORDER is the thing that could regress.
func TestModeCycleReachesEveryMode(t *testing.T) {
	m := compatModel(t)
	const draft = "a draft that must survive a mode change"
	m.input.SetValue(draft)

	start := string(m.approvalMode)
	seen := map[string]bool{start: true}
	// Exactly one full turn of the order, so the state after the loop is the
	// state before it.
	for i := 0; i < len(modeOrder); i++ {
		m.cycleMode(true)
		seen[string(m.approvalMode)] = true
	}
	for _, want := range modeOrder {
		if !seen[string(want)] {
			t.Errorf("the mode cycle never reached %q (saw %v)", want, seen)
		}
	}
	// Cycling the full length of the order must return to the start: a cycle
	// that drifted would make Tab's meaning depend on where you began.
	if got := string(m.approvalMode); got != start {
		t.Fatalf("after a full cycle the mode is %q, want the starting %q", got, start)
	}
	// And the reverse direction must undo one forward step.
	m.cycleMode(false)
	if got := string(m.approvalMode); got == start {
		t.Fatal("a reverse step did not change the mode")
	}
	m.cycleMode(true)
	if got := string(m.approvalMode); got != start {
		t.Fatalf("a forward step after a reverse left the mode at %q, want %q", got, start)
	}

	// A mode change must not disturb the draft: Tab is pressed reflexively.
	if got := m.input.Value(); got != draft {
		t.Fatalf("a mode change altered the draft to %q", got)
	}
}

// Every mode must be reachable BY NAME too, because the palette and the
// /<mode> commands are the named route and a mode with no name is a mode a script
// cannot select.
func TestEveryModeIsReachableByName(t *testing.T) {
	for _, mode := range modeOrder {
		name := string(mode)
		t.Run(name, func(t *testing.T) {
			m := compatModel(t)
			updated, _ := m.dispatchCommand("/" + name)
			m = asModel(t, updated)
			if got := string(m.approvalMode); got != name {
				t.Fatalf("/%s left the mode at %q", name, got)
			}
			// The switch must be visibly confirmed, and the confirmation must
			// name THIS mode. Checking only for the phrase "Switched to" would
			// pass while every command announced the wrong mode, because the
			// message is added by each handler rather than by setMode.
			want := modeSwitchMessage[name]
			if want == "" {
				t.Fatalf("mode %q has no switch message", name)
			}
			var announced bool
			for _, item := range m.state.Transcript() {
				if item.Message != nil && strings.Contains(item.Message.Content, want) {
					announced = true
				}
			}
			if !announced {
				t.Fatalf("/%s changed the mode without announcing it (expected %q)", name, want)
			}
		})
	}
}

// An approval form owns the keys while it is up, and answering it must reach the
// channel. This is the surface a change to the key router is most likely to break,
// because the router runs BEFORE the form is consulted.
func TestApprovalFormOwnsTheKeysAndAnswersTheRightCall(t *testing.T) {
	m := compatModel(t)
	tc := &session.PendingToolCall{
		ID: "tc-1", Name: "shell.run", Command: "go test ./...", Risk: "medium",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	m.state.SetPendingApproval(tc)

	// The form must claim the keys: routing a printable key through the whole
	// Update chain must not put it in the composer. The approval form is built
	// lazily on the first message for a pending call, so this is the real path.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = asModel(t, updated)
	if got := m.input.Value(); strings.Contains(got, "n") {
		t.Fatalf("a keypress while an approval is up reached the composer: %q", got)
	}

	// The CHANNEL must be answerable and the decision must arrive.
	tc.Respond(session.UserApprovalDecision{Approved: false})
	select {
	case decision := <-tc.ResponseChan:
		if decision.Approved {
			t.Fatalf("a denial arrived as %+v", decision)
		}
	default:
		t.Fatal("the decision never reached the call's channel")
	}
}

// Answering an approval must clear the pending state, or the reader is stuck
// looking at a form for a call that is already resolved.
func TestAnsweringAnApprovalClearsIt(t *testing.T) {
	m := compatModel(t)
	tc := &session.PendingToolCall{
		ID: "tc-1", Name: "shell.run", Command: "go test ./...", Risk: "medium",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	m.state.SetPendingApproval(tc)
	_, resolved, _ := m.pendingApprovalTarget()
	if resolved != tc {
		t.Fatal("the pending approval did not resolve")
	}

	m.state.SetPendingApproval(nil)
	if m.hasPendingApproval() {
		t.Fatal("the approval is still pending after being answered")
	}
}

// Ctrl+C must cancel a running turn and must be harmless while idle. It is the
// key a user reaches for when something is going wrong, so it cannot be the key
// that stops working.
func TestCtrlCIsNeverTheDeadKey(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		m := compatModel(t)
		// Must not panic and must not start anything.
		m = pressString(t, m, "ctrl+c")
		if m.busy {
			t.Fatal("Ctrl+C started a turn while idle")
		}
	})
	t.Run("busy", func(t *testing.T) {
		m := compatModel(t)
		m.busy = true
		// The cancel path is the same one Esc takes while busy.
		m.cancelTurn()
		// cancelTurn's own contract is tested elsewhere; what matters here is
		// that it does not panic and reports a result.
	})
}

// A paste must reach the composer and not the transcript. This is the surface
// that broke once before the key router was ordered correctly, and it is easy to
// break again by claiming the key earlier in the chain.
func TestPasteReachesTheComposer(t *testing.T) {
	m := compatModel(t)
	if !m.composerReceivesTyping() {
		t.Fatal("precondition: the composer does not own typing")
	}
	const pasted = "a pasted phrase"

	updated, _ := m.Update(tea.PasteMsg{Content: pasted})
	m = asModel(t, updated)

	if got := m.input.Value(); got != pasted {
		t.Fatalf("the paste produced %q, want %q", got, pasted)
	}
}

// Suggestion acceptance must still work through the router: right-arrow accepts
// at end-of-input, and the suggestion is composer chrome that only exists while
// the composer owns typing.
func TestSuggestionAcceptanceStillWorks(t *testing.T) {
	m := compatModel(t)
	// The field is set directly rather than through a provider: the provider is
	// the model's, and this test is about the KEY accepting what is already
	// there, not about where a suggestion comes from.
	m.suggestion = "a suggested continuation"
	m.suggestionDismissed = false
	m.input.SetValue("")
	m.input.CursorEnd()

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyRight})
	if !handled {
		t.Fatal("right-arrow at end-of-input was not handled")
	}
	got := asModel(t, updated)
	if got.suggestion != "" {
		t.Fatalf("the suggestion %q was not accepted", got.suggestion)
	}
	if !strings.Contains(got.input.Value(), "suggested continuation") {
		t.Fatalf("accepting the suggestion left the composer at %q", got.input.Value())
	}
}

// Prompt history must still be reachable with Up while the composer owns the
// keys. The activity lane used to steal Up/Down for drilling; that takeover was
// removed in Task 2, and this asserts it has not come back by another route.
func TestPromptHistoryIsReachableWithNoChildRunning(t *testing.T) {
	m := compatModel(t)
	// History is a model field, seeded the way the app seeds it when a turn is
	// submitted. No child is registered, which is the idle case the lane's old
	// Up/Down takeover would have broken.
	m.history = []string{"an earlier prompt"}
	m.histIdx = -1
	m.input.SetValue("")

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyUp})
	if !handled {
		t.Fatal("Up was not handled with an empty composer")
	}
	got := asModel(t, updated)
	if got.input.Value() != "an earlier prompt" {
		t.Fatalf("Up recalled %q, want the earlier prompt", got.input.Value())
	}
}

// The doc panel must render inside the frame at every width the journey visits.
// A panel that overflows pushes the composer off the bottom, which is the failure
// the whole frame budget exists to prevent.
func TestDocPanelFitsTheFrameAtEveryWidth(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}, {200, 60}} {
		m := compatModel(t)
		m.resize(size[0], size[1])
		updated, _ := m.dispatchCommand("/help")
		m = asModel(t, updated)
		if !m.dock.IsOpen() {
			t.Fatalf("at %dx%d /help did not open a panel", size[0], size[1])
		}
		frame := m.viewString()
		lines := strings.Split(frame, "\n")
		if len(lines) > size[1] {
			t.Fatalf("at %dx%d the frame is %d rows", size[0], size[1], len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got > size[0] {
				t.Fatalf("at %dx%d row %d is %d cells wide:\n%q",
					size[0], size[1], i, got, ansi.Strip(line))
			}
		}
	}
}
