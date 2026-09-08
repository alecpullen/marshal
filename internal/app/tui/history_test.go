package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

func newHistoryTestModel(t *testing.T, history ...string) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.history = history
	m.histIdx = -1
	return m
}

func pressKey(m Model, code rune) (Model, bool) {
	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: code})
	return mm.(Model), handled
}

func TestUpRecallsLatestThenOlderAndClamps(t *testing.T) {
	m := newHistoryTestModel(t, "newest", "older")
	m, handled := pressKey(m, tea.KeyUp)
	if !handled || m.input.Value() != "newest" {
		t.Fatalf("first up: handled=%v value=%q, want true/newest", handled, m.input.Value())
	}
	m, _ = pressKey(m, tea.KeyUp)
	if m.input.Value() != "older" {
		t.Fatalf("second up: value=%q, want older", m.input.Value())
	}
	m, _ = pressKey(m, tea.KeyUp)
	if m.input.Value() != "older" || m.histIdx != 1 {
		t.Fatalf("third up should clamp at the oldest entry: value=%q histIdx=%d", m.input.Value(), m.histIdx)
	}
}

func TestDownStepsNewerAndRestoresDraft(t *testing.T) {
	m := newHistoryTestModel(t, "newest", "older")
	m.input.SetValue("my draft")
	m, _ = pressKey(m, tea.KeyUp) // stash "my draft", recall "newest"
	m, _ = pressKey(m, tea.KeyUp) // "older"
	m, _ = pressKey(m, tea.KeyDown)
	if m.input.Value() != "newest" {
		t.Fatalf("down: value=%q, want newest", m.input.Value())
	}
	m, _ = pressKey(m, tea.KeyDown)
	if m.input.Value() != "my draft" || m.histIdx != -1 {
		t.Fatalf("down past newest should restore the draft: value=%q histIdx=%d", m.input.Value(), m.histIdx)
	}
}

func TestUpOnNonFirstLineDoesNotRecall(t *testing.T) {
	m := newHistoryTestModel(t, "newest")
	m.input.SetValue("line one\nline two")
	m.input, _ = m.input.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // cursor to line 2
	if m.input.Line() == 0 {
		t.Fatal("test requires the cursor on a later line")
	}
	m, handled := pressKey(m, tea.KeyUp)
	if handled || m.histIdx != -1 || m.input.Value() != "line one\nline two" {
		t.Fatalf("up on a later line should fall through to the textarea: handled=%v histIdx=%d value=%q",
			handled, m.histIdx, m.input.Value())
	}
}

func TestRecallPutsCursorAtStartOfEntry(t *testing.T) {
	m := newHistoryTestModel(t, "newest", "older one\nolder two\nolder three")
	m, handled := pressKey(m, tea.KeyUp)
	if !handled || m.input.Value() != "newest" || m.input.Line() != 0 {
		t.Fatalf("first up: handled=%v value=%q line=%d, want true/newest/0", handled, m.input.Value(), m.input.Line())
	}
	m, _ = pressKey(m, tea.KeyUp)
	if m.input.Value() != "older one\nolder two\nolder three" || m.input.Line() != 0 {
		t.Fatalf("second up should land on the first line of the multi-line entry: value=%q line=%d",
			m.input.Value(), m.input.Line())
	}
	// The next up-arrow is consumed as history recall (cursor on line 0),
	// not as within-entry cursor movement.
	m, handled = pressKey(m, tea.KeyUp)
	if !handled || m.input.Value() != "older one\nolder two\nolder three" {
		t.Fatalf("third up should recall/clamp in history, not move within the entry: handled=%v value=%q",
			handled, m.input.Value())
	}
	// Stepping back down through history also starts each entry (and the
	// restored draft) at line 0.
	m.input.SetValue("draft one\ndraft two")
	m.histIdx = 0 // as if we recalled "newest" with a draft stashed
	m.draft = "draft one\ndraft two"
	m, handled = pressKey(m, tea.KeyDown)
	if !handled || m.input.Value() != "draft one\ndraft two" || m.input.Line() != 0 {
		t.Fatalf("down past newest should restore the draft at line 0: handled=%v value=%q line=%d",
			handled, m.input.Value(), m.input.Line())
	}
}

func TestCompletionPopupKeepsArrowPrecedence(t *testing.T) {
	m := newViewTestModelWithRegistry(t, 80, 24)
	m.history = []string{"newest"}
	m.histIdx = -1
	m.input.SetValue("/pl")
	m.updateCompletionPopups()
	if m.activeCompletionPopup() == nil {
		t.Fatal("expected a visible completion popup for /pl")
	}
	m, handled := pressKey(m, tea.KeyUp)
	if !handled || m.histIdx != -1 || m.input.Value() != "/pl" {
		t.Fatalf("popup should own arrows: handled=%v histIdx=%d value=%q", handled, m.histIdx, m.input.Value())
	}
}

func TestEscResetsHistoryNavigation(t *testing.T) {
	m := newHistoryTestModel(t, "newest")
	m.input.SetValue("draft")
	m, _ = pressKey(m, tea.KeyUp)
	if m.histIdx != 0 {
		t.Fatalf("histIdx = %d, want 0", m.histIdx)
	}
	m, _ = pressKey(m, tea.KeyEscape)
	if m.histIdx != -1 || m.draft != "" {
		t.Fatalf("esc should reset history nav: histIdx=%d draft=%q", m.histIdx, m.draft)
	}
}

func TestSubmitRecordsPromptAndResetsNavigation(t *testing.T) {
	m := newHistoryTestModel(t)
	m.input.SetValue("hello world")
	m, _ = pressKey(m, tea.KeyEnter)
	if len(m.history) == 0 || m.history[0] != "hello world" {
		t.Fatalf("history = %v, want newest-first with hello world", m.history)
	}
	if m.histIdx != -1 || m.draft != "" {
		t.Fatalf("submit should reset nav: histIdx=%d draft=%q", m.histIdx, m.draft)
	}
}

func TestRecordPromptCollapsesDuplicatesAndSkipsEmpty(t *testing.T) {
	m := newHistoryTestModel(t)
	m.recordPrompt("")
	m.recordPrompt("same")
	m.recordPrompt("same")
	if len(m.history) != 1 || m.history[0] != "same" {
		t.Fatalf("history = %v, want [same]", m.history)
	}
}

func TestHistoryDisabledToggle(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.Config.History.Enabled = false
	m := New(state)
	m.history = []string{"newest"}
	m.histIdx = -1
	if m.recallOlder() {
		t.Fatal("recallOlder should not consume up when history is disabled")
	}
	m.recordPrompt("typed")
	if len(m.history) != 1 {
		t.Fatalf("recordPrompt should be a no-op when disabled, history = %v", m.history)
	}
}

// F3: history recall leaves completion state clean — no popup, no Esc
// suppression, no stale idempotency cache — and the next edit evaluates
// triggers normally.
func TestRecallDismissesPopupsAndResetsCache(t *testing.T) {
	m := newViewTestModelWithRegistry(t, 80, 24)
	m.history = []string{"/plan fix auth"}
	m.histIdx = -1
	// Dirty popup state: bare "/" opened the popup, then Esc dismissed it
	// (leaving suppression and a stale idempotency cache behind).
	m.input.SetValue("/")
	m.updateCompletionPopups()
	if m.activeCompletionPopup() == nil {
		t.Fatal("precondition: popup visible for /")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	// Recall the slash-command entry.
	recalled, handled := pressKey(m, tea.KeyUp)
	if !handled || recalled.input.Value() != "/plan fix auth" {
		t.Fatalf("recall failed: handled=%v value=%q", handled, recalled.input.Value())
	}
	if recalled.activeCompletionPopup() != nil {
		t.Fatal("recalled input must not show a completion popup")
	}
	if recalled.completionSuppressed {
		t.Fatal("recall should clear Esc suppression so the next edit re-evaluates")
	}
	if recalled.lastInputForPopups != "" {
		t.Fatal("recall must reset the popup idempotency cache")
	}
	// The very next edit evaluates normally (cache reset did its job).
	recalled.input.SetValue("/pl")
	recalled.updateCompletionPopups()
	if recalled.activeCompletionPopup() == nil {
		t.Fatal("first edit after recall should evaluate triggers normally")
	}
}

// F3 regression guard: a recalled slash entry never opens the popup on its
// own (recall paths return before trigger evaluation by design).
func TestRecalledSlashEntryShowsNoPopup(t *testing.T) {
	m := newHistoryTestModel(t, "/help")
	recalled, handled := pressKey(m, tea.KeyUp)
	if !handled || recalled.input.Value() != "/help" {
		t.Fatalf("recall failed: handled=%v value=%q", handled, recalled.input.Value())
	}
	if recalled.activeCompletionPopup() != nil {
		t.Fatal("recall alone must not open the completions panel")
	}
}

func TestPromptRecordedToDBAndLoadedOnStartup(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}

	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{DB: database, SessionID: "sess-1"})
	m := New(state, WithMemoryStore(database, projectID))
	m.input.SetValue("remember me")
	m, _ = pressKey(m, tea.KeyEnter)

	got, err := database.RecentPrompts(projectID, 200)
	if err != nil {
		t.Fatalf("RecentPrompts: %v", err)
	}
	if len(got) != 1 || got[0] != "remember me" {
		t.Fatalf("db history = %v, want [remember me]", got)
	}

	// A fresh model for the same project loads history at startup.
	state2 := session.New(config.Default(), t.TempDir(), time.Unix(200, 0),
		session.Persistence{DB: database, SessionID: "sess-2"})
	m2 := New(state2, WithMemoryStore(database, projectID))
	if len(m2.history) != 1 || m2.history[0] != "remember me" {
		t.Fatalf("startup history = %v, want [remember me]", m2.history)
	}
}
