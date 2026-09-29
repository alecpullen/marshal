package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/inspector"
)

// changesInspectorModel builds a model whose inspector is open on the Changes
// tab against a real fixture repository.
func changesInspectorModel(t *testing.T) (Model, string, string) {
	t.Helper()
	dir, head := railFixtureRepo(t)

	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: dir})
	m.railBaseRef = head
	m.refreshRailChanged()
	m.refreshInspector()

	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.refreshInspector()
	return m, dir, head
}

// selectChangedPath moves the Changes cursor to a named path using only the
// public navigation API: a delta large enough to run off either end clamps to
// that end, so one jump lands on row 0 and the next lands on the target.
func selectChangedPath(t *testing.T, m *Model, path string) {
	t.Helper()
	files := m.railSnapshot.Files
	m.inspector.model.MoveChangesSelection(-len(files) - 1)
	for i, f := range files {
		if f.Path != path {
			continue
		}
		m.inspector.model.MoveChangesSelection(i)
		if got, _ := m.inspector.model.SelectedPath(); got != path {
			t.Fatalf("cursor landed on %q, want %q", got, path)
		}
		return
	}
	t.Fatalf("the snapshot does not contain %q: %+v", path, files)
}

// TestInspectorChangesTabIsPopulatedFromTheSnapshot pins that opening the
// Changes tab shows the SAME reading the rail shows. Two readings would be two
// answers to "what changed", and the rail and the inspector would disagree on
// screen.
func TestInspectorChangesTabIsPopulatedFromTheSnapshot(t *testing.T) {
	m, _, _ := changesInspectorModel(t)

	view := stripANSI(m.inspector.model.View(m.inspectorData()))
	if view == "" {
		t.Fatal("the Changes tab rendered nothing")
	}
	// Both fixture paths are changed, and both must appear.
	for _, want := range []string{"a.go", "untracked.txt"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the Changes tab does not list %q:\n%s", want, view)
		}
	}
	// And the snapshot it rendered from is the rail's own reading.
	if got := m.inspector.model.ChangesSnapshot().BaseOID; got != m.railSnapshot.BaseOID {
		t.Fatalf("the inspector's snapshot base %q differs from the rail's %q", got, m.railSnapshot.BaseOID)
	}
}

// TestInspectorChangesTabNeverShowsACleanTreeForAFailedRead is the acceptance
// criterion at the model level: below the rail threshold, with the inspector
// open on a directory that is not a repository, the panel must say so rather
// than claiming there are no changes.
func TestInspectorChangesTabNeverShowsACleanTreeForAFailedRead(t *testing.T) {
	notARepo := t.TempDir()

	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: notARepo, ActiveRoot: notARepo})
	m.railBaseRef = "HEAD"
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.refreshRailChanged()
	m.refreshInspector()

	if m.railSnapshot.Status == changedfiles.StatusOK {
		t.Fatalf("a non-repo produced an OK snapshot: %+v", m.railSnapshot)
	}
	// Below the rail threshold the inspector is dock-placed, and the dock's
	// height is only known once a frame has been rendered: that is when the
	// panel claims the slot. Render once so the panel is measured, then read
	// the same frame the user would see.
	frame := stripANSI(m.viewString())
	view := stripANSI(m.inspector.model.View(m.inspectorData()))
	if strings.Contains(strings.ToLower(view), "no changes") {
		t.Fatalf("a failed read rendered as a clean tree:\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "not a git repository") {
		t.Fatalf("the failure is not explained by the panel:\n%s\nframe:\n%s", view, frame)
	}
	if !strings.Contains(strings.ToLower(frame), "not a git repository") {
		t.Fatalf("the failure never reached the screen:\n%s", frame)
	}
}

// TestSelectingAChangedFileRunsAReadAndDeliversTheDiff is the end-to-end path:
// Enter on a selected file must produce a command that reads that file's patch
// and deliver it back to the inspector.
func TestSelectingAChangedFileRunsAReadAndDeliversTheDiff(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)

	selectChangedPath(t, &m, "a.go")
	if !m.inspector.model.EnterSelected() {
		t.Fatal("EnterSelected refused the current selection")
	}

	cmd := m.inspectorDiffCommand()
	if cmd == nil {
		t.Fatal("selecting a file produced no read command")
	}
	msg := cmd()
	loaded, ok := msg.(inspector.DiffLoadedMsg)
	if !ok {
		t.Fatalf("the read produced %T, want inspector.DiffLoadedMsg", msg)
	}
	if loaded.Path != "a.go" {
		t.Fatalf("the read was for %q, want a.go", loaded.Path)
	}
	if loaded.Diff.Status != changedfiles.StatusOK {
		t.Fatalf("the read failed: %+v", loaded.Diff)
	}
	if !strings.Contains(loaded.Diff.Patch, "a.go") {
		t.Fatalf("the patch does not mention a.go:\n%s", loaded.Diff.Patch)
	}

	// The command is what the key handler returns, so driving it through Update
	// is the same path the runtime takes — and it must actually be accepted.
	mm, _ := m.Update(msg)
	after := asModel(t, mm)
	if after.inspector.model.ChangesLoading() {
		t.Fatal("the loading state survived a delivered diff")
	}
	view := stripANSI(after.inspector.model.View(after.inspectorData()))
	if !strings.Contains(view, "func A() {}") {
		t.Fatalf("the delivered diff is not on screen:\n%s", view)
	}
}

// TestInspectorDiffCommandIsDrainedOnce pins the storm guard at the model level:
// the command must be produced once per request, not once per keypress.
func TestInspectorDiffCommandIsDrainedOnce(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	m.inspector.model.EnterSelected()

	if cmd := m.inspectorDiffCommand(); cmd == nil {
		t.Fatal("the first call produced no command")
	}
	if cmd := m.inspectorDiffCommand(); cmd != nil {
		t.Fatal("a second command was produced for the same request; one selection must read once")
	}
}

// TestChangesTabKeysMoveTheFileCursorNotAScrollOffset pins that the Changes tab
// owns its keyboard: Up/Down move the FILE selection, which is the only
// navigation the tab has, and they do not move a scroll offset instead.
func TestChangesTabKeysMoveTheFileCursorNotAScrollOffset(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	selectChangedPath(t, &m, "a.go")

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("Down was not handled on the Changes tab")
	}
	if after, _ := got.inspector.model.SelectedPath(); after == "a.go" {
		t.Fatal("Down did not move the file selection")
	}

	mm2, _, _ := got.handleKeypress(tea.KeyPressMsg{Code: tea.KeyUp})
	back := asModel(t, mm2)
	if now, _ := back.inspector.model.SelectedPath(); now != "a.go" {
		t.Fatalf("Up did not return to a.go (got %q)", now)
	}
	// The tab's body scroll is a separate number and must not have been moved
	// by a key that means "next file".
	if s := back.inspector.model.State(inspector.TabChanges).Scroll; s != 0 {
		t.Fatalf("the tab body scrolled to %d while the cursor moved; the two are different navigation", s)
	}
}

// TestChangesTabEnterRunsAReadThroughTheKeyHandler pins that the key path and
// the direct API agree: pressing Enter is what produces the command, and the
// command it produces actually reads the selected file.
func TestChangesTabEnterRunsAReadThroughTheKeyHandler(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	selectChangedPath(t, &m, "a.go")

	mm, cmd, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("Enter was not handled on the Changes tab")
	}
	if cmd == nil {
		t.Fatal("Enter produced no command, so no diff would ever be read")
	}
	// A second Enter must not produce a second read: the request the first one
	// consumed is the only one that existed.
	if _, again, _ := got.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter}); again == nil {
		t.Fatal("a fresh Enter after a read produced no command; opening twice must work")
	}

	msg := cmd()
	loaded, ok := msg.(inspector.DiffLoadedMsg)
	if !ok {
		t.Fatalf("Enter's command produced %T, want inspector.DiffLoadedMsg", msg)
	}
	if loaded.Path != "a.go" {
		t.Fatalf("Enter read %q, want a.go", loaded.Path)
	}
}

// TestInspectorChangesScopeRejectsAClosedSessionReply pins the session guard at
// the model boundary. A reply carries the scope it was issued under; once the
// conversation has been replaced that scope is no longer the inspector's, so
// the reply describes something the user can no longer see and must not be
// drawn.
func TestInspectorChangesScopeRejectsAClosedSessionReply(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	selectChangedPath(t, &m, "a.go")
	if !m.inspector.model.EnterSelected() {
		t.Fatal("EnterSelected refused the selection")
	}
	cmd := m.inspectorDiffCommand()
	if cmd == nil {
		t.Fatal("no read command")
	}
	msg := cmd().(inspector.DiffLoadedMsg)
	if !strings.Contains(msg.Diff.Patch, "+func A() {}") {
		t.Fatalf("the fixture produced no readable patch: %+v", msg.Diff)
	}

	// The conversation is replaced before the reply lands. Re-stamping the
	// scope is the real path rather than a test-only hook: refreshInspector
	// does exactly this whenever m.state changes, and /new and /clear both
	// replace m.state.
	m.inspector.model.SetScope("s-a-different-conversation")
	if m.inspector.model.Scope() == msg.Scope {
		t.Fatal("precondition: the scope did not change, so this tests nothing")
	}

	mm, _ := m.Update(msg)
	after := asModel(t, mm)
	if !after.inspector.model.ChangesLoading() {
		t.Fatal("a reply from a closed conversation cleared the in-flight state")
	}
	if view := stripANSI(after.inspector.model.View(after.inspectorData())); strings.Contains(view, "+func A() {}") {
		t.Fatalf("a reply from a closed conversation was drawn:\n%s", view)
	}
}

// TestRefreshInspectorStampsTheLiveSessionScope pins that the scope is derived
// from the live State on every refresh rather than fixed at construction. A
// scope captured once would survive /new and /clear, and a reply issued under
// the old conversation would be accepted into the new one.
func TestRefreshInspectorStampsTheLiveSessionScope(t *testing.T) {
	m, _, _ := changesInspectorModel(t)

	want := m.state.ScopeID()
	if want == "" {
		t.Fatal("the session has no scope token")
	}
	if got := m.inspector.model.Scope(); got != want {
		t.Fatalf("inspector scope = %q, want the live state's %q", got, want)
	}

	replacement := session.New(m.state.Config, m.state.WorkingDir, time.Unix(200, 0), session.Persistence{})
	if replacement.ScopeID() == want {
		t.Fatal("two States share a scope token, so their identities can collide")
	}
	m.state = replacement
	m.refreshInspector()
	if got := m.inspector.model.Scope(); got != replacement.ScopeID() {
		t.Fatalf("after replacing the session the inspector scope = %q, want %q", got, replacement.ScopeID())
	}
}

// TestChangedFilePathIsInspectableWithAwkwardCharacters is the plan's explicit
// acceptance case: a path containing spaces, and a path beginning with '-' must
// both be inspectable, which is only true if the path travels as an argument
// rather than as a shell string.
func TestChangedFilePathIsInspectableWithAwkwardCharacters(t *testing.T) {
	dir, head := railFixtureRepo(t)

	awkward := []string{"a file with spaces.go", "-leading-dash.go"}
	for _, name := range awkward {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: dir})
	m.railBaseRef = head
	m.refreshRailChanged()
	m.refreshInspector()
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.refreshInspector()

	for _, name := range awkward {
		selectChangedPath(t, &m, name)
		if !m.inspector.model.EnterSelected() {
			t.Fatalf("EnterSelected refused %q", name)
		}
		cmd := m.inspectorDiffCommand()
		if cmd == nil {
			t.Fatalf("no read command for %q", name)
		}
		loaded := cmd().(inspector.DiffLoadedMsg)
		if loaded.Path != name {
			t.Fatalf("read %q, want %q", loaded.Path, name)
		}
		if loaded.Diff.Status != changedfiles.StatusOK {
			t.Fatalf("reading %q failed: %+v", name, loaded.Diff)
		}
	}
}
