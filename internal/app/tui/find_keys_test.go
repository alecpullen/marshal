// internal/app/tui/find_keys_test.go — the keys and the command that reach find
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

// These tests drive the REAL key router rather than the find entry points.
//
// That distinction is the whole point of the file: every function in find.go can
// be correct while no key reaches any of them, and a reader then has a feature
// they cannot use. Driving `handleKeypress` is what makes "the key is wired" a
// claim with evidence behind it.

// findKeyModel returns a model whose composer owns the keys, with a searchable
// transcript.
func findKeyModel(t *testing.T) Model {
	t.Helper()
	m := findScrollableModel(t)
	return m
}

// pressString sends one keypress by NAME through the real message path, and
// returns the resulting model.
//
// The key is built from Bubble Tea's own constants rather than by hand, so a
// test naming "f3" exercises exactly the key the runtime delivers. A hand-built
// message with the wrong Code is a test that passes while the binding is
// unreachable — the failure this whole file exists to prevent.
func pressString(t *testing.T, m Model, key string) Model {
	t.Helper()
	k := keyPressNamed(key)
	updated, cmd := m.Update(k)
	return drainCmds(asModel(t, updated), cmd)
}

// keyPressNamed builds the keypress a terminal sends for a key name.
func keyPressNamed(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "f3":
		return tea.KeyPressMsg{Code: tea.KeyF3}
	case "shift+f3":
		return tea.KeyPressMsg{Code: tea.KeyF3, Mod: tea.ModShift}
	case "f6":
		return tea.KeyPressMsg{Code: tea.KeyF6}
	case "ctrl+f":
		return tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	if r := []rune(name); len(r) == 1 {
		// A printable character carries its Text, which is what a real
		// terminal sends and what the textarea appends.
		return tea.KeyPressMsg{Code: r[0], Text: name}
	}
	panic("keyPressNamed: unknown key " + name)
}

// `/` in conversation focus opens find. It is the key a reader reaches for, and
// the alternative — a slash command with no keystroke — makes finding a phrase
// the one operation in the app that needs two.
func TestSlashOpensFindWhenTheConversationOwnsTheKeys(t *testing.T) {
	m := findKeyModel(t)
	// The conversation owns the keys. F6 cycles focus, so drive it rather than
	// setting the field: the test then also catches a focus change that broke
	// the cycle.
	m = pressString(t, m, "f6")
	if m.effectiveFocus() != FocusConversation {
		t.Fatalf("precondition: F6 left focus on %v", m.effectiveFocus())
	}
	if m.composerReceivesTyping() {
		t.Fatal("precondition: the composer still receives typing, so / would be typed literally")
	}

	m = pressString(t, m, "/")

	if !m.find.open {
		t.Fatal("/ did not open find while the conversation owned the keys")
	}
}

// `/` with the COMPOSER focused must stay a slash: it is how every slash command
// is typed, and stealing it would make /help unreachable.
func TestSlashIsNotStolenFromTheComposer(t *testing.T) {
	m := findKeyModel(t)
	if m.effectiveFocus() != FocusComposer {
		t.Fatalf("precondition: focus is %v, want the composer", m.effectiveFocus())
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: '/', Text: "/"})
	if handled {
		t.Fatal("/ was consumed while the composer owned the keys")
	}
	if updated.(Model).find.open {
		t.Fatal("/ opened find from the composer")
	}
}

// Typing into an open find must edit the QUERY, not the composer's draft. This
// is the assertion that find owns the keys while it is open — without it, a
// reader's query is typed into a prompt they cannot see and the draft is
// silently corrupted.
func TestTypingWhileFindIsOpenEditsTheQueryAndNotTheDraft(t *testing.T) {
	m := findKeyModel(t)
	m.input.SetValue("a half-written prompt")
	m.openFind("")

	// The letters of "tokens", one at a time, through the real router.
	for _, r := range "tokens" {
		m = pressString(t, m, string(r))
	}

	if got := m.find.query; got != "tokens" {
		t.Fatalf("the query is %q, want %q", got, "tokens")
	}
	if got := m.input.Value(); got != "a half-written prompt" {
		t.Fatalf("the draft became %q; typing while find is open must not touch it", got)
	}
	if len(m.find.matches) == 0 {
		t.Fatal("typing produced no results")
	}
}

// Backspace edits the query, so a mistyped query can be corrected without
// closing the search and losing the entry anchor.
func TestBackspaceWhileFindIsOpenEditsTheQuery(t *testing.T) {
	m := findKeyModel(t)
	m.openFind("token")
	if len(m.find.matches) == 0 {
		t.Fatal("precondition: the query matched nothing")
	}

	m = pressString(t, m, "backspace")

	if m.find.query != "toke" {
		t.Fatalf("the query is %q after backspace, want %q", m.find.query, "toke")
	}
}

// Enter and Esc reach their two different meanings through the real router.
// They are the pair that must not be confused: Enter keeps the reader on the
// match, Esc puts them back.
func TestEnterAndEscapeReachFindThroughTheRouter(t *testing.T) {
	t.Run("enter accepts", func(t *testing.T) {
		m := findKeyModel(t)
		m.openFind("tokens")
		if len(m.find.matches) < 2 {
			t.Fatalf("precondition: got %d matches, want at least 2", len(m.find.matches))
		}
		m.nextFindMatch()
		if !m.gotoFindMatch() {
			t.Fatal("the jump failed")
		}
		accepted := m.readingAnchor

		m = pressString(t, m, "enter")

		if m.find.open {
			t.Fatal("Enter did not close find")
		}
		if m.readingAnchor.Block != accepted.Block || m.readingAnchor.Offset != accepted.Offset {
			t.Fatalf("Enter moved the reader to %q+%d, want it left at the match %q+%d",
				m.readingAnchor.Block, m.readingAnchor.Offset, accepted.Block, accepted.Offset)
		}
	})

	t.Run("escape restores", func(t *testing.T) {
		m := findKeyModel(t)
		entryRow := m.blockSpans[0].startLine
		m.viewportFollow = false
		m.viewport.SetYOffset(entryRow)
		m.captureReadingAnchor()
		entry := m.readingAnchor

		m.openFind("tokens")
		m.nextFindMatch()
		if !m.gotoFindMatch() {
			t.Fatal("the jump failed")
		}
		if m.readingAnchor.Block == entry.Block && m.readingAnchor.Offset == entry.Offset {
			t.Fatal("precondition: the jump did not move the reader")
		}

		m = pressString(t, m, "esc")

		if m.find.open {
			t.Fatal("Esc did not close find")
		}
		if m.readingAnchor.Block != entry.Block || m.readingAnchor.Offset != entry.Offset {
			t.Fatalf("Esc left the reader at %q+%d, want the entry anchor %q+%d",
				m.readingAnchor.Block, m.readingAnchor.Offset, entry.Block, entry.Offset)
		}
	})
}

// F3 and Shift+F3 step through results without closing the search, which is the
// key every terminal user already has in their fingers.
func TestF3StepsThroughResults(t *testing.T) {
	m := findKeyModel(t)
	m.openFind("tokens")
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want at least 2", len(m.find.matches))
	}
	first := m.currentFindMatchOrFail(t)

	m = pressString(t, m, "f6")
	m = pressString(t, m, "f3")
	second := m.currentFindMatchOrFail(t)
	if second.Block == first.Block && second.Range == first.Range {
		t.Fatal("F3 did not move to another match")
	}
	if !m.find.open {
		t.Fatal("F3 closed the search; it must step, not exit")
	}

	m = pressString(t, m, "shift+f3")
	back := m.currentFindMatchOrFail(t)
	if back.Block != first.Block || back.Range != first.Range {
		t.Fatalf("Shift+F3 gave %+v, want the original match %+v", back, first)
	}
}

// F3 with no search open must do nothing rather than inventing one: an empty
// search would put the reader in a mode they did not choose.
func TestF3DoesNothingWithNoSearchOpen(t *testing.T) {
	m := findKeyModel(t)
	m = pressString(t, m, "f6")
	if m.find.open {
		t.Fatal("precondition: find is already open")
	}

	m = pressString(t, m, "f3")

	if m.find.open {
		t.Fatal("F3 opened a search on its own")
	}
	if m.find.query != "" {
		t.Fatalf("F3 set the query to %q", m.find.query)
	}
}

// Ctrl+F keeps its existing meaning. It is the keyboard route into a running
// child agent, and turning it into find would take that away — the two are
// deliberately different keys.
func TestCtrlFKeepsItsAgentInspectionMeaning(t *testing.T) {
	m := findKeyModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("explore", child)
	child.AddMessage(session.RoleUser, "child text", session.ContentTypePlain)
	m.refreshViewport()

	m = pressString(t, m, "ctrl+f")

	if m.find.open {
		t.Fatal("Ctrl+F opened find; it must keep inspecting a running agent")
	}
	if _, ok := m.drilledInto(); !ok {
		t.Fatal("Ctrl+F did not drill into the running subagent")
	}
}

// The /find command opens the search with its argument as the query. The command
// is the discoverable route; `/` is the fast one.
func TestFindCommandOpensTheSearchWithItsArgument(t *testing.T) {
	m := findKeyModel(t)

	updated, _ := m.dispatchCommand("/find tokens")
	got := asModel(t, updated)

	if !got.find.open {
		t.Fatal("/find did not open the search")
	}
	if got.find.query != "tokens" {
		t.Fatalf("the query is %q, want %q", got.find.query, "tokens")
	}
	if len(got.find.matches) == 0 {
		t.Fatal("/find opened a search with no results for a query that matches")
	}
}

// A bare /find opens the search with an empty query, so the reader can type.
func TestFindCommandWithoutArgumentsOpensAnEmptySearch(t *testing.T) {
	m := findKeyModel(t)

	updated, _ := m.dispatchCommand("/find")
	got := asModel(t, updated)

	if !got.find.open {
		t.Fatal("/find did not open the search")
	}
	if got.find.query != "" {
		t.Fatalf("a bare /find set the query to %q", got.find.query)
	}
	if len(got.find.matches) != 0 {
		t.Fatalf("an empty query produced %d matches", len(got.find.matches))
	}
}

// A multi-word argument is one query, not several: /find is about a phrase, and
// searching for the first word of "the parser drops" would be a different
// search from the one the reader asked for.
//
// Two cases, and they take different paths through the dispatcher. A QUOTED
// argument arrives as one shlex element, so it would pass even if the effect
// used args[0]; an UNQUOTED one arrives as several, and is the case the join
// exists for. Testing only the quoted form is how a test passes while the
// unquoted form — the one people actually type — searches for one word.
func TestFindCommandKeepsAMultiWordArgumentWhole(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"quoted", `/find "distinctive phrase"`, "distinctive phrase"},
		{"unquoted", `/find distinctive phrase`, "distinctive phrase"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := findKeyModel(t)

			updated, _ := m.dispatchCommand(tc.raw)
			got := asModel(t, updated)

			if !got.find.open {
				t.Fatal("/find did not open the search")
			}
			if got.find.query != tc.want {
				t.Fatalf("the query is %q, want %q", got.find.query, tc.want)
			}
		})
	}
}

// Opening a search must not touch the agent. The command path is the one a
// script or a muscle-memory keystroke reaches, so it is the one most likely to
// be routed through the prompt handler by mistake.
func TestFindCommandSubmitsNothingToTheAgent(t *testing.T) {
	m := findKeyModel(t)
	before := len(m.state.Messages())
	if m.busy {
		t.Fatal("precondition: the model is busy")
	}

	updated, _ := m.dispatchCommand("/find tokens")
	got := asModel(t, updated)

	if n := len(got.state.Messages()); n != before {
		t.Fatalf("/find added %d messages to the session", n-before)
	}
	if got.busy {
		t.Fatal("/find started a turn")
	}
	if q := got.state.SteeringQueue(); len(q) != 0 {
		t.Fatalf("/find queued %d steering messages", len(q))
	}
}

// The status line must name find while it owns the keys, so a reader who typed
// into a field that does not look like the composer can see it arrived.
func TestFindIsVisibleInTheStatusLineThroughTheRouter(t *testing.T) {
	m := findKeyModel(t)
	m = pressString(t, m, "f6")
	m = pressString(t, m, "/")
	m = pressString(t, m, "t")

	if !m.find.open {
		t.Fatal("precondition: find is not open")
	}
	var joined string
	for _, s := range m.statusLeftSegments() {
		joined += " " + stripANSI(s.text)
	}
	if !strings.Contains(joined, "find") {
		t.Fatalf("the status line does not mention find: %q", joined)
	}
}
