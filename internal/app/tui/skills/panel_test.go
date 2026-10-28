package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/settings"
	"marshal/internal/skills"
)

func TestLoadResultRoutesErrorToActiveListAndSetsStatusOnSuccess(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	state := session.New(config.Default(), work, time.Now(), session.Persistence{})

	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{
		Name:        "debug",
		Description: "debugging skill",
		Body:        "# Debug\n\nSteps.\n",
	})

	p := NewPanel(home, work, true, state, idx)
	debugSkill, _ := idx.Load("debug")
	p.stack = append(p.stack, p.detailFrame(skills.ScopedSkill{Skill: debugSkill, Scope: scopeGlobal}))

	// Simulate a failed load while drilled into the detail frame.
	p.Update(loadResultMsg{Err: fmt.Errorf("boom")})
	if got := p.ActiveList().ErrMsg; got != "boom" {
		t.Fatalf("active list error = %q, want %q", got, "boom")
	}

	// Simulate a successful load.
	p.Update(loadResultMsg{Name: "debug"})
	want := "loaded debug — content matched the current session"
	if p.status != want {
		t.Fatalf("status = %q, want %q", p.status, want)
	}
	if p.ActiveList().ErrMsg != "" {
		t.Fatalf("error not cleared after success: %q", p.ActiveList().ErrMsg)
	}
}

func TestPanelUsesRuntimeSkillIndexForLoad(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	state := session.New(config.Default(), work, time.Now(), session.Persistence{})

	runtimeIdx := skills.NewIndex()
	runtimeIdx.Set("runtime-skill", skills.Skill{
		Name:        "runtime-skill",
		Description: "only in runtime index",
		Body:        "# Runtime\n",
	})

	// Do not install the skill on disk; the panel should still find it via the runtime index.
	p := NewPanel(home, work, true, state, runtimeIdx)
	if _, ok := p.activeIndex().Load("runtime-skill"); !ok {
		t.Fatal("activeIndex did not return the runtime index")
	}
}

func TestPanelRootList(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	globalDir := filepath.Join(home, ".config", "marshal", "skills")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(globalDir, "debug.md")
	content := `+++
name = "debug"
description = "debugging skill"
+++

# Debug`
	if err := os.WriteFile(skillFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	state := session.New(config.Config{}, work, time.Now(), session.Persistence{})
	p := NewPanel(home, work, true, state, nil)

	rows := settings.FieldListRows(p.list)
	if len(rows) == 0 {
		t.Fatalf("expected rows, got none")
	}
	found := false
	for _, r := range rows {
		if settings.FieldID(r) == skillRowID(scopeGlobal, "debug.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skill.debug row, rows: %v", rows)
	}
}

func TestPanelProjectScopeDisabled(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	state := session.New(config.Config{}, work, time.Now(), session.Persistence{})
	p := NewPanel(home, work, false, state, nil)

	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open install frame
	// The install frame should only list "global" in the scope enum.
	// This is a smoke test; exact assertions depend on frame navigation.
}

func TestPanelViewRendersPushedInstallFrame(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	state := session.New(config.Config{}, work, time.Now(), session.Persistence{})
	p := NewPanel(home, work, true, state, nil)

	// With no skills installed, the selectable rows are the gate toggle and
	// "＋ Install skill"; move down to the install row before pressing Enter.
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(p.stack) == 0 {
		t.Fatalf("expected install frame pushed onto stack")
	}

	view := p.View(80, 20)
	if !strings.Contains(view, "Install skill") {
		t.Fatalf("expected install frame title in view, got:\n%s", view)
	}
	if !strings.Contains(view, "Source") {
		t.Fatalf("expected install frame Source field in view, got:\n%s", view)
	}
	if strings.Contains(view, "＋ Install skill") {
		t.Fatalf("expected root list hidden while install frame is active, got:\n%s", view)
	}
}

// The skills panel is framed, and its frame never renders past the dock's
// height budget.
func TestSkillsPanelIsFramed(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil, nil)
	const maxHeight = 20
	view := p.View(80, maxHeight)
	rows := strings.Split(ansi.Strip(view), "\n")
	if !strings.HasPrefix(rows[0], "╭─ Skills") {
		t.Fatalf("first row should open the Skills frame, got %q", rows[0])
	}
	if len(rows) > maxHeight {
		t.Fatalf("view is %d rows tall, past the %d-row budget", len(rows), maxHeight)
	}
}

// A narrow dock stacks every field row onto two physical lines, so the pane's
// height budget has to be counted in lines rather than rows. It used to count
// rows: the list overflowed the height the dock gave it and the selected row
// drifted off-frame, which only shows up through a consumer that owns a frame.
func TestSkillPanelStackedFieldsKeepSelectionVisible(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	p := NewPanel(home, work, true, nil, nil)
	global := p.globalSkillsDir()
	if err := os.MkdirAll(global, 0755); err != nil {
		t.Fatalf("create skill store %s: %v", global, err)
	}
	for _, name := range []string{"alpha", "beta", "gamma", "delta"} {
		writeSkillEntry(t, global, name+".md", name, "fixture")
	}
	p.list.Refresh()

	const width, maxHeight = 36, 14
	// The list's own budget: the panel reserves 4 rows for its header, footer
	// and frame.
	const listHeight = maxHeight - 4

	listRows := func(t *testing.T) []string {
		t.Helper()
		view := ansi.Strip(p.ActiveList().View())
		if view == "" {
			return nil
		}
		return strings.Split(view, "\n")
	}

	if got := len(listRows(t)); got > listHeight {
		t.Fatalf("list renders %d physical rows, past its %d-row budget:\n%s",
			got, listHeight, ansi.Strip(p.ActiveList().View()))
	}
	if plain := ansi.Strip(p.View(width, maxHeight)); !strings.Contains(plain, chrome.SelectionMarker) {
		t.Fatalf("the selected row is off-frame:\n%s", plain)
	}

	// Navigate to the install action through the real panel, so the window has
	// to follow the cursor rather than the cursor landing in view by accident.
	for i := 0; i <= len(settings.FieldListRows(p.ActiveList())) && settings.FieldID(p.ActiveList().CursorRow()) != "action.install"; i++ {
		p.Update(keyDown)
	}
	if got := settings.FieldID(p.ActiveList().CursorRow()); got != "action.install" {
		t.Fatalf("cursor is on %q, want action.install", got)
	}

	view := p.View(width, maxHeight)
	plain := ansi.Strip(view)
	if rows := strings.Split(plain, "\n"); len(rows) > maxHeight {
		t.Fatalf("frame is %d rows tall, past the %d-row budget:\n%s", len(rows), maxHeight, plain)
	}
	if got := len(listRows(t)); got > listHeight {
		t.Fatalf("list renders %d physical rows after navigation, past its %d-row budget:\n%s",
			got, listHeight, ansi.Strip(p.ActiveList().View()))
	}
	// The selected action, not just some row, is what the window keeps.
	focused := false
	for _, row := range strings.Split(plain, "\n") {
		if strings.Contains(row, chrome.SelectionMarker) && strings.Contains(row, "Install skill") {
			focused = true
		}
	}
	if !focused {
		t.Fatalf("the selected install action is off-frame:\n%s", plain)
	}

	// One action, taken after that navigation, still opens its frame.
	p.Update(keyEnter)
	if len(p.stack) != 1 {
		t.Fatalf("enter on the install action pushed %d frames, want 1", len(p.stack))
	}
	plain = ansi.Strip(p.View(width, maxHeight))
	if rows := strings.Split(plain, "\n"); len(rows) > maxHeight {
		t.Fatalf("install frame is %d rows tall, past the %d-row budget:\n%s", len(rows), maxHeight, plain)
	}
	if !strings.Contains(plain, "Source") {
		t.Fatalf("the install frame's Source field is off-frame:\n%s", plain)
	}
}

// TestPanelOwnsAsyncResults pins the fix for the bug where pressing Enter on
// Install did nothing: the panel's result types are unexported, so the model
// cannot route them by type and the dock host dropped them. Every async result
// this panel emits must be claimed via dock.MessageOwner, or its Update case
// is dead code and the action completes on disk with no visible effect.
func TestPanelOwnsAsyncResults(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil, nil)
	for _, msg := range []tea.Msg{
		loadResultMsg{},
		installResultMsg{},
		removeResultMsg{},
	} {
		if !p.OwnsMsg(msg) {
			t.Errorf("OwnsMsg(%T) = false, want true — this message would be dropped", msg)
		}
	}
	if p.OwnsMsg(tea.KeyPressMsg{}) {
		t.Error("OwnsMsg claimed a keypress; the dock routes those already")
	}
}

// TestInstallWithEmptySourceReportsError pins that an empty source surfaces an
// error rather than silently returning a nil command.
func TestInstallWithEmptySourceReportsError(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil, nil)
	p.stack = append(p.stack, p.installFrame())
	p.installSource = "   "

	if cmd := p.runInstall(); cmd != nil {
		t.Fatal("expected no command for an empty source")
	}
	if p.installErr == "" {
		t.Error("empty source produced no error message — the action looks broken")
	}
	if got := p.ActiveList().ErrMsg; got == "" {
		t.Error("error was not surfaced on the visible list")
	}
}

// scopedDebug is the ScopedSkill the fixture panel has installed.
func scopedDebug(t *testing.T, p *Panel) skills.ScopedSkill {
	t.Helper()
	scoped, err := skills.ListScopes(p.globalSkillsDir(), p.projectSkillsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scoped {
		if s.Skill.Name == "debug" {
			return s
		}
	}
	t.Fatal("debug skill not found")
	return skills.ScopedSkill{}
}

// newAutoloadPanel builds a panel over one installed global skill.
func newAutoloadPanel(t *testing.T) (*Panel, string) {
	t.Helper()
	home, work := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".config", "marshal", "skills")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "+++\nname = \"debug\"\ndescription = \"debugging skill\"\n+++\n\n# Debug"
	if err := os.WriteFile(filepath.Join(dir, "debug.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	state := session.New(config.Default(), work, time.Now(), session.Persistence{})
	return NewPanel(home, work, true, state, nil), home
}

func fieldByID(t *testing.T, fields []*settings.Field, id string) *settings.Field {
	t.Helper()
	for _, f := range fields {
		if settings.FieldID(f) == id {
			return f
		}
	}
	t.Fatalf("no field %q", id)
	return nil
}

// Toggling autoload has to reach disk, or the setting silently vanishes
// when the session ends — which is the whole point of it being persistent.
func TestAutoloadToggleWritesUserConfig(t *testing.T) {
	p, home := newAutoloadPanel(t)
	frame := p.detailFrame(scopedDebug(t, p))
	auto := fieldByID(t, settings.FieldListRows(settings.FrameList(frame)), "detail.autoload")

	if auto.GetBool() {
		t.Fatal("should start off")
	}
	auto.SetBool(true)

	if !p.isAutoloaded("debug") {
		t.Fatal("in-memory config not updated")
	}
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Default() ships ["using-skills"]; toggling debug on appends to it.
	want := []string{"using-skills", "debug"}
	if !reflect.DeepEqual(cfg.Skills.Autoload, want) {
		t.Fatalf("on-disk autoload = %v, want %v", cfg.Skills.Autoload, want)
	}

	auto.SetBool(false)
	cfg, err = config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Toggling debug off leaves the shipped default in place.
	want = []string{"using-skills"}
	if !reflect.DeepEqual(cfg.Skills.Autoload, want) {
		t.Fatalf("autoload = %v after toggling off, want %v", cfg.Skills.Autoload, want)
	}
}

// Toggling one skill must not disturb entries for skills managed elsewhere.
func TestAutoloadTogglePreservesOtherEntries(t *testing.T) {
	p, home := newAutoloadPanel(t)
	p.state.Config.Skills.Autoload = []string{"using-superpowers"}

	frame := p.detailFrame(scopedDebug(t, p))
	fieldByID(t, settings.FieldListRows(settings.FrameList(frame)), "detail.autoload").SetBool(true)

	cfg, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"using-superpowers", "debug"}
	if strings.Join(cfg.Skills.Autoload, ",") != strings.Join(want, ",") {
		t.Fatalf("autoload = %v, want %v", cfg.Skills.Autoload, want)
	}
}

// An always-on skill is invisible from the list without a marker.
func TestAutoloadShowsInRowSummary(t *testing.T) {
	p, _ := newAutoloadPanel(t)
	p.state.Config.Skills.Autoload = []string{"debug"}
	p.list.Refresh()

	row := fieldByID(t, settings.FieldListRows(p.list), skillRowID(scopeGlobal, "debug.md"))
	if got := row.Summary(); !strings.Contains(got, "autoload") {
		t.Fatalf("row summary = %q, want it to mention autoload", got)
	}
}

// The footer counted every row in the active list, so a single installed
// skill read "3 skills" (header + skill + install action) and drilling into
// one read "6 skills" (its detail fields).
func TestFooterCountsSkillsNotRows(t *testing.T) {
	p, _ := newAutoloadPanel(t)

	if got := p.countLabel(); got != "1 skill" {
		t.Fatalf("root count = %q, want %q", got, "1 skill")
	}

	p.stack = append(p.stack, p.detailFrame(scopedDebug(t, p)))
	if got := p.countLabel(); got != "1 skill" {
		t.Fatalf("detail count = %q, want %q", got, "1 skill")
	}
}

func TestFooterCountTracksFilter(t *testing.T) {
	p, _ := newAutoloadPanel(t)
	p.filter.SetValue("nomatch")
	p.list.Refresh()

	if got := p.countLabel(); got != "0 skills" {
		t.Fatalf("filtered count = %q, want %q", got, "0 skills")
	}

	p.filter.SetValue("debug")
	p.list.Refresh()
	if got := p.countLabel(); got != "1 skill" {
		t.Fatalf("filtered count = %q, want %q", got, "1 skill")
	}
}

// The gate status row is the panel's live control for the session-scope
// skill-load gate: it must exist whenever the panel has a session, describe
// the configured threshold, and toggle the session state (never the config)
// through the same act closure Enter drives.
func TestGateStatusFieldTogglesSessionState(t *testing.T) {
	p, _ := newAutoloadPanel(t)
	p.list.Refresh()

	gate := fieldByID(t, settings.FieldListRows(p.list), "action.gate")
	if !p.state.SkillGateEnabled() {
		t.Fatal("gate should start enabled for a fresh session")
	}
	if got := settings.FieldDesc(gate); !strings.Contains(got, "131072") {
		t.Fatalf("gate desc = %q, want it to mention the default threshold", got)
	}

	gate.Act()
	if p.state.SkillGateEnabled() {
		t.Fatal("gate still enabled after act")
	}
	if p.status != "skill load gate off" {
		t.Fatalf("status = %q, want %q", p.status, "skill load gate off")
	}
	if got := settings.FieldDesc(fieldByID(t, settings.FieldListRows(p.list), "action.gate")); !strings.Contains(got, "off") {
		t.Fatalf("gate desc = %q, want the off wording after toggling", got)
	}

	// Re-enabling clears every sticky decision with it.
	p.state.SkillGateRecordDeny("debug")
	gate.Act()
	if !p.state.SkillGateEnabled() {
		t.Fatal("gate not re-enabled after second act")
	}
	if got := p.status; got != "skill load gate on" {
		t.Fatalf("status = %q, want %q", got, "skill load gate on")
	}
	if n := len(p.state.SkillGateDecisions()); n != 0 {
		t.Fatalf("decisions after re-enable = %d, want 0", n)
	}
}

// Sticky decisions render as one clearable row each: ✕ for a deny (with its
// counter), ✓ for an allow. Clearing drops only that decision so the next
// load re-prompts.
func TestGateDecisionRowsRenderAndClear(t *testing.T) {
	p, _ := newAutoloadPanel(t)

	p.state.SkillGateRecordDeny("debug")
	p.state.SkillGateRecordDeny("debug")
	p.state.SkillGateRecordAllow("other", false)
	p.list.Refresh()

	rows := settings.FieldListRows(p.list)
	if got, want := settings.FieldTitle(fieldByID(t, rows, "gate.debug")), "✕ debug (denied ×2)"; got != want {
		t.Fatalf("deny title = %q, want %q", got, want)
	}
	if got, want := settings.FieldTitle(fieldByID(t, rows, "gate.other")), "✓ other (allowed)"; got != want {
		t.Fatalf("allow title = %q, want %q", got, want)
	}

	fieldByID(t, rows, "gate.debug").Act()
	if p.status != "cleared debug — next load re-prompts" {
		t.Fatalf("status = %q, want the cleared message", p.status)
	}
	p.list.Refresh()
	for _, f := range settings.FieldListRows(p.list) {
		if settings.FieldID(f) == "gate.debug" {
			t.Fatal("gate.debug row still present after clear")
		}
	}
	// Clearing one decision must not disturb the others.
	foundOther := false
	for _, f := range settings.FieldListRows(p.list) {
		if settings.FieldID(f) == "gate.other" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatal("gate.other row vanished after clearing debug")
	}
}

// Re-enabling the gate wipes the sticky decisions (the session
// SkillGateSetEnabled contract), so the panel's decision rows must
// disappear with them.
func TestGateDecisionRowsVanishAfterReEnable(t *testing.T) {
	p, _ := newAutoloadPanel(t)
	p.state.SkillGateRecordDeny("debug")
	p.state.SkillGateSetEnabled(false)
	p.state.SkillGateSetEnabled(true)
	p.list.Refresh()

	for _, f := range settings.FieldListRows(p.list) {
		if strings.HasPrefix(settings.FieldID(f), "gate.") {
			t.Fatalf("decision row %q survived a gate re-enable", settings.FieldID(f))
		}
	}
}

// Panels without a session (nil state) must still build their field list —
// the gate rows are simply absent rather than a nil dereference.
func TestGateRowsOmittedWithoutState(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil, nil)
	for _, f := range settings.FieldListRows(p.list) {
		if id := settings.FieldID(f); id == "action.gate" || strings.HasPrefix(id, "gate.") {
			t.Fatalf("gate row %q rendered without a session state", id)
		}
	}
}

// --- removal fixtures and helpers -----------------------------------------
//
// Every removal test below drives Panel.Update and the commands it emits, so
// the assertions cover the path a user actually takes: drill in, arm, confirm.

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// newRemovalPanel builds a stateless panel over a fresh home/work pair with
// both skill stores created, returning the panel and those store roots.
func newRemovalPanel(t *testing.T) (p *Panel, global, project string) {
	t.Helper()
	home, work := t.TempDir(), t.TempDir()
	p = NewPanel(home, work, true, nil, nil)
	global, project = p.globalSkillsDir(), p.projectSkillsDir()
	for _, dir := range []string{global, project} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("create skill store %s: %v", dir, err)
		}
	}
	return p, global, project
}

// skillFixture renders a skill file whose frontmatter name is deliberately
// independent of its filename.
func skillFixture(name, description string) string {
	return fmt.Sprintf("+++\nname = %q\ndescription = %q\n+++\n\n# %s\n", name, description, name)
}

// writeSkillEntry writes a top-level .md skill into dir and returns its path.
func writeSkillEntry(t *testing.T, dir, file, name, description string) string {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(skillFixture(name, description)), 0644); err != nil {
		t.Fatalf("write skill %s: %v", path, err)
	}
	return path
}

// writeBundleEntry writes <dir>/<bundle>/SKILL.md and returns the bundle dir.
func writeBundleEntry(t *testing.T, dir, bundle, name string) string {
	t.Helper()
	bundleDir := filepath.Join(dir, bundle)
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		t.Fatalf("create bundle %s: %v", bundleDir, err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, skills.BundleFileName), []byte(skillFixture(name, "bundled")), 0644); err != nil {
		t.Fatalf("write bundle skill: %v", err)
	}
	return bundleDir
}

// discovered returns the entry a fresh ListScopes reports for a store child.
func discovered(t *testing.T, p *Panel, scope, source string) skills.ScopedSkill {
	t.Helper()
	list, err := skills.ListScopes(p.globalSkillsDir(), p.projectSkillsDir())
	if err != nil {
		t.Fatalf("ListScopes: %v", err)
	}
	for _, s := range list {
		if s.Scope == scope && s.SourceName == source {
			return s
		}
	}
	t.Fatalf("no discovered skill scope=%q source=%q in %+v", scope, source, list)
	return skills.ScopedSkill{}
}

// press delivers a key through Panel.Update and feeds the emitted command's
// message straight back, the way the dock host does.
func press(t *testing.T, p *Panel, key tea.KeyPressMsg) {
	t.Helper()
	cmd := p.Update(key)
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		p.Update(msg)
	}
}

// focusRow parks the active list's cursor on the row with the given field ID.
func focusRow(t *testing.T, p *Panel, id string) {
	t.Helper()
	for i, r := range settings.FieldListRows(p.ActiveList()) {
		if settings.FieldID(r) == id {
			p.ActiveList().SetCursor(i)
			return
		}
	}
	t.Fatalf("no row %q in the active list", id)
}

// openSkillDetail stacks the detail frame for s with its Remove row focused.
func openSkillDetail(t *testing.T, p *Panel, s skills.ScopedSkill) {
	t.Helper()
	p.stack = append(p.stack, p.detailFrame(s))
	focusRow(t, p, "action.remove")
}

// openSkillFromList drills into a skill's detail from the root list, so at
// least one removal test covers the real drill-in path.
func openSkillFromList(t *testing.T, p *Panel, rowID string) {
	t.Helper()
	focusRow(t, p, rowID)
	press(t, p, keyEnter)
	if len(p.stack) == 0 {
		t.Fatalf("enter on %q did not open a detail frame", rowID)
	}
	focusRow(t, p, "action.remove")
}

func mustExistPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to survive: %v", path, err)
	}
}

func mustBeGonePath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, Lstat error = %v", path, err)
	}
}

// TestRemoveUsesDiscoveredSource pins the audit finding: removal targets the
// direct child discovery recorded, never the frontmatter name.
func TestRemoveUsesDiscoveredSource(t *testing.T) {
	t.Run("top-level file ignores the frontmatter name", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		target := writeSkillEntry(t, global, "actual.md", "displayed", "fixture")
		sibling := writeSkillEntry(t, global, "sibling.md", "sibling", "fixture")

		openSkillFromList(t, p, skillRowID(scopeGlobal, "actual.md"))
		press(t, p, keyEnter) // arms
		mustExistPath(t, target)
		press(t, p, keyEnter) // confirms

		mustBeGonePath(t, target)
		mustExistPath(t, sibling)
		mustExistPath(t, global)
	})

	t.Run("bundle removes its directory", func(t *testing.T) {
		p, _, project := newRemovalPanel(t)
		bundle := writeBundleEntry(t, project, "bundle-dir", "bundled")
		sibling := writeSkillEntry(t, project, "sibling.md", "sibling", "fixture")

		openSkillDetail(t, p, discovered(t, p, scopeProject, "bundle-dir"))
		press(t, p, keyEnter)
		press(t, p, keyEnter)

		mustBeGonePath(t, bundle)
		mustExistPath(t, sibling)
		mustExistPath(t, project)
	})

	t.Run("malicious metadata name reaches nothing outside the store", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		// The pre-fix bug joined the frontmatter name onto the store, so
		// "../../victim" resolved to this sentinel.
		victim := filepath.Join(filepath.Dir(filepath.Dir(global)), "victim")
		if err := os.WriteFile(victim, []byte("sentinel"), 0644); err != nil {
			t.Fatalf("write sentinel: %v", err)
		}
		target := writeSkillEntry(t, global, "innocent.md", "../../victim", "hostile name")

		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "innocent.md"))
		press(t, p, keyEnter)
		press(t, p, keyEnter)

		mustBeGonePath(t, target)
		mustExistPath(t, victim)
	})
}

// TestRemoveReportsStaleOrSymlinkSource pins that a changed, vanished, or
// symlinked entry surfaces an error instead of silently removing something
// else.
func TestRemoveReportsStaleOrSymlinkSource(t *testing.T) {
	t.Run("entry replaced after the detail opened", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		target := writeSkillEntry(t, global, "target.md", "target", "original body")
		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "target.md"))

		// Replace the discovered child behind the same name, after the
		// snapshot the detail frame holds was taken.
		replacement := writeSkillEntry(t, global, "replacement.md", "target", "replacement body")
		if err := os.Rename(replacement, target); err != nil {
			t.Fatalf("rename replacement over target: %v", err)
		}

		press(t, p, keyEnter)
		press(t, p, keyEnter)

		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "replaced") {
			t.Fatalf("error = %q, want it to mention the replacement", got)
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("replacement should survive: %v", err)
		}
		if !strings.Contains(string(raw), "replacement body") {
			t.Errorf("replacement was modified: %q", raw)
		}
	})

	t.Run("entry missing after the detail opened", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		target := writeSkillEntry(t, global, "ghost.md", "ghost", "fixture")
		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "ghost.md"))

		if err := os.Remove(target); err != nil {
			t.Fatalf("remove ghost: %v", err)
		}
		press(t, p, keyEnter)
		press(t, p, keyEnter)

		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "missing") {
			t.Fatalf("error = %q, want it to mention the missing entry", got)
		}
	})

	t.Run("symlinked entry", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		outside := writeSkillEntry(t, t.TempDir(), "outside.md", "outside", "fixture")
		link := filepath.Join(global, "link.md")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("platform cannot create symlinks: %v", err)
		}

		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "link.md"))
		press(t, p, keyEnter)
		press(t, p, keyEnter)

		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "symlink") {
			t.Fatalf("error = %q, want it to refuse the symlink", got)
		}
		mustExistPath(t, outside)
		mustExistPath(t, link)
	})
}

// TestRemoveConfirmationDisarmsOnNavigationAndEscape pins that an armed
// confirmation never survives leaving the row or the detail frame.
func TestRemoveConfirmationDisarmsOnNavigationAndEscape(t *testing.T) {
	t.Run("leaving the remove row", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		target := writeSkillEntry(t, global, "debug.md", "debug", "fixture")
		s := discovered(t, p, scopeGlobal, "debug.md")
		openSkillDetail(t, p, s)

		press(t, p, keyEnter) // arms
		if !p.removeArmed[removalIdentity(s)] {
			t.Fatal("remove did not arm on the first enter")
		}
		press(t, p, keyUp)   // leaves the row
		press(t, p, keyDown) // and comes back
		press(t, p, keyEnter)

		mustExistPath(t, target)
		if !p.removeArmed[removalIdentity(s)] {
			t.Fatal("enter after navigation armed nothing — it deleted instead")
		}

		// Two consecutive confirmations on the same row still delete.
		press(t, p, keyEnter)
		mustBeGonePath(t, target)
	})

	t.Run("leaving and reopening the detail", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		target := writeSkillEntry(t, global, "debug.md", "debug", "fixture")
		s := discovered(t, p, scopeGlobal, "debug.md")
		openSkillDetail(t, p, s)

		press(t, p, keyEnter) // arms
		press(t, p, keyEsc)   // pops the frame
		if len(p.stack) != 0 {
			t.Fatal("escape did not pop the detail frame")
		}
		mustExistPath(t, target)

		openSkillDetail(t, p, s)
		press(t, p, keyEnter)
		mustExistPath(t, target)

		press(t, p, keyEnter)
		mustBeGonePath(t, target)
	})
}

// TestRemoveConfirmationIsScopedBySource pins that the armed flag belongs to
// one discovered entry, not to a name that another entry can share.
func TestRemoveConfirmationIsScopedBySource(t *testing.T) {
	t.Run("across scopes", func(t *testing.T) {
		p, global, project := newRemovalPanel(t)
		globalTarget := writeSkillEntry(t, global, "same.md", "dup", "global")
		projectTarget := writeSkillEntry(t, project, "same.md", "dup", "project")

		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "same.md"))
		press(t, p, keyEnter) // arms the global copy

		// The project copy shares a name but not an identity: stacking its
		// detail must not inherit the armed confirmation.
		openSkillDetail(t, p, discovered(t, p, scopeProject, "same.md"))
		press(t, p, keyEnter)
		mustExistPath(t, projectTarget)
		mustExistPath(t, globalTarget)

		press(t, p, keyEnter)
		mustBeGonePath(t, projectTarget)
		mustExistPath(t, globalTarget)
	})

	t.Run("two entries sharing a name in one store", func(t *testing.T) {
		p, global, _ := newRemovalPanel(t)
		first := writeSkillEntry(t, global, "one.md", "dup", "first")
		second := writeSkillEntry(t, global, "two.md", "dup", "second")

		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "one.md"))
		press(t, p, keyEnter) // arms one.md

		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "two.md"))
		press(t, p, keyEnter) // shares the name, not the confirmation
		mustExistPath(t, second)

		press(t, p, keyEnter)
		mustBeGonePath(t, second)
		mustExistPath(t, first)
	})
}

// --- install targeting fixtures and helpers -------------------------------
//
// The install tests below drive Panel.runInstall and feed the emitted result
// back through Update, so the assertions cover the path a user actually takes.

// isolatedConfigDir points MARSHAL_CONFIG_DIR at a fresh temp directory —
// making the global store a path this test owns rather than a directory
// shared with the rest of the package — and returns its skills subdirectory.
func isolatedConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MARSHAL_CONFIG_DIR", dir)
	return filepath.Join(dir, "skills")
}

// writeInstallSource writes a local .md install source outside the isolated
// home and returns its path.
func writeInstallSource(t *testing.T, name, description string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".md")
	if err := os.WriteFile(path, []byte(skillFixture(name, description)), 0644); err != nil {
		t.Fatalf("write install source %s: %v", path, err)
	}
	return path
}

// installAction fills in the install frame's fields, runs the action and
// feeds its result message back, the way the dock host does.
func installAction(t *testing.T, p *Panel, source, scope string) {
	t.Helper()
	p.stack = append(p.stack, p.installFrame())
	p.installSource = source
	p.installScope = scope
	cmd := p.runInstall()
	if cmd == nil {
		t.Fatalf("runInstall returned no command for source %q scope %q (err %q)", source, scope, p.installErr)
	}
	if msg := cmd(); msg != nil {
		p.Update(msg)
	}
}

// TestInstallUsesEffectiveGlobalSkillsDir pins that a global install lands in
// the config directory the panel and the runtime read — config.UserDir's
// effective location — not the ~/.config path skills.ScopeDir hardcodes.
func TestInstallUsesEffectiveGlobalSkillsDir(t *testing.T) {
	cases := []struct {
		name string
		// setup points the config directory somewhere specific and returns
		// the effective Marshal config directory plus whether it was moved
		// away from <home>/.config/marshal.
		setup func(t *testing.T, home string) (effective string, overridden bool)
	}{
		{
			name: "MARSHAL_CONFIG_DIR",
			setup: func(t *testing.T, _ string) (string, bool) {
				dir := t.TempDir()
				t.Setenv("MARSHAL_CONFIG_DIR", dir)
				// Set XDG too, so the case proves MARSHAL_CONFIG_DIR wins.
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
				return dir, true
			},
		},
		{
			name: "XDG_CONFIG_HOME",
			setup: func(t *testing.T, _ string) (string, bool) {
				xdg := t.TempDir()
				t.Setenv("MARSHAL_CONFIG_DIR", "")
				t.Setenv("XDG_CONFIG_HOME", xdg)
				return filepath.Join(xdg, "marshal"), true
			},
		},
		{
			name: "neither set",
			setup: func(t *testing.T, home string) (string, bool) {
				t.Setenv("MARSHAL_CONFIG_DIR", "")
				t.Setenv("XDG_CONFIG_HOME", "")
				return filepath.Join(home, ".config", "marshal"), false
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			effective, overridden := tc.setup(t, home)
			source := writeInstallSource(t, "installed-here", "fixture")

			p := NewPanel(home, work, true, nil, nil)
			installAction(t, p, source, scopeGlobal)

			if p.installErr != "" {
				t.Fatalf("install error: %s", p.installErr)
			}
			if got := p.ActiveList().ErrMsg; got != "" {
				t.Fatalf("install surfaced a visible error: %q", got)
			}
			mustExistPath(t, filepath.Join(effective, "skills", "installed-here.md"))
			if overridden {
				mustBeGonePath(t, filepath.Join(home, ".config", "marshal", "skills", "installed-here.md"))
			}

			// Discovery is what the panel and the runtime index read from, so
			// the installed entry must be found there and nowhere else.
			if got := discovered(t, p, scopeGlobal, "installed-here.md"); got.Skill.Name != "installed-here" {
				t.Fatalf("discovered skill = %+v, want the installed one", got.Skill)
			}
			if !strings.Contains(p.status, "restart Marshal") {
				t.Fatalf("status = %q, want it to disclose the restart", p.status)
			}
		})
	}
}

// TestInstallProjectScopeAndTrust pins that a project install is rooted in the
// project only when the folder is trusted, and that every refusal is visible
// and starts no command.
func TestInstallProjectScopeAndTrust(t *testing.T) {
	t.Run("trusted project installs below its own .marshal/skills", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		globalDir := isolatedConfigDir(t)
		source := writeInstallSource(t, "project-only", "fixture")

		p := NewPanel(home, work, true, nil, nil)
		installAction(t, p, source, scopeProject)

		if p.installErr != "" {
			t.Fatalf("install error: %s", p.installErr)
		}
		want := filepath.Join(work, ".marshal", "skills", "project-only.md")
		mustExistPath(t, want)
		mustBeGonePath(t, filepath.Join(globalDir, "project-only.md"))
		if got := discovered(t, p, scopeProject, "project-only.md"); got.Skill.Name != "project-only" {
			t.Fatalf("discovered skill = %+v, want the installed one", got.Skill)
		}
	})

	t.Run("untrusted project scope fails visibly", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		globalDir := isolatedConfigDir(t)
		source := writeInstallSource(t, "project-only", "fixture")

		p := NewPanel(home, work, false, nil, nil)
		p.stack = append(p.stack, p.installFrame())
		p.installSource = source
		p.installScope = scopeProject

		if cmd := p.runInstall(); cmd != nil {
			t.Fatal("untrusted project scope started an install")
		}
		if p.installErr == "" {
			t.Fatal("untrusted project scope produced no error message")
		}
		if got := p.ActiveList().ErrMsg; got == "" {
			t.Fatal("untrusted project scope surfaced no visible error")
		}
		mustBeGonePath(t, p.projectSkillsDir())
		mustBeGonePath(t, globalDir)
	})

	t.Run("unknown scope fails visibly", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		globalDir := isolatedConfigDir(t)
		source := writeInstallSource(t, "anywhere", "fixture")

		p := NewPanel(home, work, true, nil, nil)
		p.stack = append(p.stack, p.installFrame())
		p.installSource = source
		p.installScope = "elsewhere"

		if cmd := p.runInstall(); cmd != nil {
			t.Fatal("unknown scope started an install")
		}
		if !strings.Contains(p.installErr, "unknown skill scope") {
			t.Fatalf("error = %q, want it to name the unknown scope", p.installErr)
		}
		if got := p.ActiveList().ErrMsg; got == "" {
			t.Fatal("unknown scope surfaced no visible error")
		}
		mustBeGonePath(t, p.projectSkillsDir())
		mustBeGonePath(t, globalDir)
	})
}

// TestSkillDiskChangeReportsRestart pins that a disk-level install or removal
// is disclosed as a change that needs a restart, and that it leaves the
// runtime index — a startup artifact — untouched.
func TestSkillDiskChangeReportsRestart(t *testing.T) {
	// newIndexWithPluginSkill is the runtime index a session would already
	// hold: one skill loaded from a source this panel never writes.
	newIndexWithPluginSkill := func() *skills.Index {
		idx := skills.NewIndex()
		idx.Set("plugin-only", skills.Skill{
			Name:        "plugin-only",
			Description: "loaded by a plugin",
			Body:        "# Plugin\n",
		})
		return idx
	}
	assertIndexUnchanged := func(t *testing.T, idx *skills.Index, absent string) {
		t.Helper()
		if _, ok := idx.Load(absent); ok {
			t.Fatalf("runtime index gained %q from a disk change", absent)
		}
		plugin, ok := idx.Load("plugin-only")
		if !ok {
			t.Fatal("runtime index lost its pre-existing skill")
		}
		if plugin.Description != "loaded by a plugin" {
			t.Fatalf("pre-existing skill changed: %+v", plugin)
		}
	}

	t.Run("install", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		globalDir := isolatedConfigDir(t)
		idx := newIndexWithPluginSkill()
		source := writeInstallSource(t, "fresh-skill", "fixture")

		p := NewPanel(home, work, true, nil, idx)
		installAction(t, p, source, scopeGlobal)

		if p.installErr != "" {
			t.Fatalf("install error: %s", p.installErr)
		}
		mustExistPath(t, filepath.Join(globalDir, "fresh-skill.md"))
		if !strings.Contains(p.status, "restart Marshal to apply it to the runtime") {
			t.Fatalf("status = %q, want the restart disclosure", p.status)
		}
		assertIndexUnchanged(t, idx, "fresh-skill")
	})

	t.Run("P0-protected remove", func(t *testing.T) {
		home, work := t.TempDir(), t.TempDir()
		globalDir := isolatedConfigDir(t)
		idx := newIndexWithPluginSkill()

		p := NewPanel(home, work, true, nil, idx)
		if err := os.MkdirAll(globalDir, 0755); err != nil {
			t.Fatalf("create skill store %s: %v", globalDir, err)
		}
		target := writeSkillEntry(t, globalDir, "doomed.md", "doomed", "fixture")

		openSkillDetail(t, p, discovered(t, p, scopeGlobal, "doomed.md"))
		press(t, p, keyEnter) // arms
		mustExistPath(t, target)
		press(t, p, keyEnter) // confirms

		mustBeGonePath(t, target)
		if got := p.ActiveList().ErrMsg; got != "" {
			t.Fatalf("remove surfaced an error: %q", got)
		}
		if !strings.Contains(p.status, "restart Marshal to drop it from the runtime") {
			t.Fatalf("status = %q, want the restart disclosure", p.status)
		}
		assertIndexUnchanged(t, idx, "doomed")
	})
}

// --- Load now fixtures ----------------------------------------------------
//
// The load tests below drill in from the root list and drive Panel.Update and
// the commands it emits, so they cover the path a user actually takes.

// writeSkillBody writes dir/file as a skill named name whose body carries
// marker, returning the parsed value the file yields.
func writeSkillBody(t *testing.T, dir, file, name, marker string) skills.Skill {
	t.Helper()
	content := fmt.Sprintf("+++\nname = %q\ndescription = %q\n+++\n\n# %s %s\n", name, marker+" skill", name, marker)
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0644); err != nil {
		t.Fatalf("write skill %s: %v", file, err)
	}
	skill, err := skills.Parse(content)
	if err != nil {
		t.Fatalf("parse skill %s: %v", file, err)
	}
	return skill
}

// newLoadPanel builds a panel with a live session over fresh, created stores.
func newLoadPanel(t *testing.T, idx *skills.Index) (*Panel, string, string) {
	t.Helper()
	home, work := t.TempDir(), t.TempDir()
	state := session.New(config.Default(), work, time.Now(), session.Persistence{})
	p := NewPanel(home, work, true, state, idx)
	for _, dir := range []string{p.globalSkillsDir(), p.projectSkillsDir()} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("create skill store %s: %v", dir, err)
		}
	}
	return p, p.globalSkillsDir(), p.projectSkillsDir()
}

// runtimeIndex builds what the production startup path hands a session:
// built-ins plus both stores, the project layer overwriting the global one.
func runtimeIndex(t *testing.T, p *Panel) *skills.Index {
	t.Helper()
	idx, err := skills.LoadSkills(p.globalSkillsDir(), p.projectSkillsDir())
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	return idx
}

// skillBodies returns the skill bodies the session has actually loaded.
func skillBodies(p *Panel) []string {
	var bodies []string
	for _, item := range p.state.Transcript() {
		if item.Message != nil && item.Message.ContentType == session.ContentTypeSkillBody {
			bodies = append(bodies, item.Message.Content)
		}
	}
	return bodies
}

// openLoadRow drills into the row for scope/source and parks on Load now.
func openLoadRow(t *testing.T, p *Panel, scope, source string) {
	t.Helper()
	openSkillFromList(t, p, skillRowID(scope, source))
	focusRow(t, p, "action.load")
}

// TestLoadNowUsesMatchingSessionContent pins that the panel loads only the
// content the current session actually holds, and says so as content
// equality — never as a claim about which scope the runtime loaded from.
func TestLoadNowUsesMatchingSessionContent(t *testing.T) {
	t.Run("matching inactive entry loads exactly its body", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		onDisk := writeSkillBody(t, global, "debug.md", "debug", "alpha")
		p.skillIndex = runtimeIndex(t, p)

		openLoadRow(t, p, scopeGlobal, "debug.md")
		press(t, p, keyEnter)

		if got := p.ActiveList().ErrMsg; got != "" {
			t.Fatalf("load surfaced an error: %q", got)
		}
		if !p.state.HasActiveSkill("debug") {
			t.Fatal("matching inactive entry was not activated")
		}
		if !strings.Contains(p.status, "loaded debug") || !strings.Contains(p.status, "content matched the current session") {
			t.Fatalf("status = %q, want the content-match wording", p.status)
		}
		bodies := skillBodies(p)
		if len(bodies) != 1 {
			t.Fatalf("loaded bodies = %d, want 1: %v", len(bodies), bodies)
		}
		if !strings.Contains(bodies[0], onDisk.Body) {
			t.Fatalf("loaded body = %q, want the selected body %q", bodies[0], onDisk.Body)
		}
	})

	t.Run("absent runtime entry stays unloaded", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, skills.NewIndex())
		writeSkillBody(t, global, "debug.md", "debug", "alpha")

		openLoadRow(t, p, scopeGlobal, "debug.md")
		press(t, p, keyEnter)

		if p.state.HasActiveSkill("debug") {
			t.Fatal("loaded a skill the runtime index does not hold")
		}
		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "restart Marshal") {
			t.Fatalf("error = %q, want it to point at a restart", got)
		}
		if got := len(skillBodies(p)); got != 0 {
			t.Fatalf("loaded bodies = %d, want 0", got)
		}
	})

	t.Run("different scope contents: only the session value loads", func(t *testing.T) {
		p, global, project := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "alpha")
		writeSkillBody(t, project, "debug.md", "debug", "beta")
		p.skillIndex = runtimeIndex(t, p) // project overwrites global

		openLoadRow(t, p, scopeGlobal, "debug.md")
		press(t, p, keyEnter)

		if p.state.HasActiveSkill("debug") {
			t.Fatal("loaded the global entry although the session holds the project body")
		}
		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "different") {
			t.Fatalf("error = %q, want it to say the contents differ", got)
		}

		press(t, p, keyEsc)
		openLoadRow(t, p, scopeProject, "debug.md")
		press(t, p, keyEnter)

		if !p.state.HasActiveSkill("debug") {
			t.Fatal("the entry matching the session was not loaded")
		}
		if bodies := skillBodies(p); len(bodies) != 1 || !strings.Contains(bodies[0], "beta") {
			t.Fatalf("loaded bodies = %v, want the project body", bodies)
		}
	})

	t.Run("plugin-overridden value stays unloaded", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "on-disk")
		idx := runtimeIndex(t, p)
		plugin := skills.Skill{Name: "debug", Description: "from a plugin", Body: "# Debug plugin\n"}
		idx.Set("debug", plugin)
		p.skillIndex = idx

		openLoadRow(t, p, scopeGlobal, "debug.md")
		press(t, p, keyEnter)

		if p.state.HasActiveSkill("debug") {
			t.Fatal("loaded the shadowed on-disk value")
		}
		got := p.ActiveList().ErrMsg
		if !strings.Contains(got, "different") || !strings.Contains(got, "precedence") {
			t.Fatalf("error = %q, want it to name precedence", got)
		}
		if current, _ := idx.Load("debug"); current != plugin {
			t.Fatalf("shared index mutated: %+v", current)
		}
	})

	t.Run("already-active name keeps its body", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "alpha")
		p.skillIndex = runtimeIndex(t, p)
		if err := skills.LoadSkillIntoSession(p.skillIndex, p.state, "debug"); err != nil {
			t.Fatalf("activate: %v", err)
		}
		before := len(skillBodies(p))

		openLoadRow(t, p, scopeGlobal, "debug.md")
		press(t, p, keyEnter)

		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "already active") {
			t.Fatalf("error = %q, want it to say the skill is already active", got)
		}
		if got := len(skillBodies(p)); got != before {
			t.Fatalf("loaded bodies = %d, want %d — an active body was re-sent", got, before)
		}
	})

	t.Run("identical content in two scopes reports equality only", func(t *testing.T) {
		p, global, project := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "same")
		writeSkillBody(t, project, "debug.md", "debug", "same")
		p.skillIndex = runtimeIndex(t, p)

		// Both rows are loadable while nothing is active, and neither claims
		// which scope the runtime loaded from.
		for _, scope := range []string{scopeGlobal, scopeProject} {
			p.stack = append(p.stack, p.detailFrame(discovered(t, p, scope, "debug.md")))
			row := fieldByID(t, settings.FieldListRows(settings.FrameList(p.stack[len(p.stack)-1])), "action.load")
			desc := settings.FieldDesc(row)
			if !strings.Contains(desc, "content matches the current session") {
				t.Fatalf("%s desc = %q, want the content-equality wording", scope, desc)
			}
			for _, claim := range []string{"global", "project", "scope"} {
				if strings.Contains(desc, claim) {
					t.Fatalf("%s desc = %q, claims provenance (%q)", scope, desc, claim)
				}
			}
			p.stack = p.stack[:len(p.stack)-1]
		}

		openLoadRow(t, p, scopeProject, "debug.md")
		press(t, p, keyEnter)
		if !p.state.HasActiveSkill("debug") {
			t.Fatal("identical content in the project scope did not load")
		}
	})
}

// TestLoadNowRevalidatesSelectedSource pins that the action re-checks the
// source and the shared index at invocation: a selection that went stale
// while the detail frame was open is never loaded.
func TestLoadNowRevalidatesSelectedSource(t *testing.T) {
	t.Run("source deleted after the detail opened", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "alpha")
		p.skillIndex = runtimeIndex(t, p)

		openLoadRow(t, p, scopeGlobal, "debug.md")
		if err := os.Remove(filepath.Join(global, "debug.md")); err != nil {
			t.Fatalf("remove source: %v", err)
		}
		press(t, p, keyEnter)

		if p.state.HasActiveSkill("debug") {
			t.Fatal("loaded from a deleted source")
		}
		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "no longer") {
			t.Fatalf("error = %q, want it to say the source is gone", got)
		}
	})

	t.Run("source replaced at the same path", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "alpha")
		p.skillIndex = runtimeIndex(t, p)

		openLoadRow(t, p, scopeGlobal, "debug.md")
		// Same path, same content, different inode: only the Lstat snapshot
		// can tell that the discovered child is no longer the one selected.
		writeSkillBody(t, global, "replacement.md", "debug", "alpha")
		if err := os.Rename(filepath.Join(global, "replacement.md"), filepath.Join(global, "debug.md")); err != nil {
			t.Fatalf("rename replacement over target: %v", err)
		}
		press(t, p, keyEnter)

		if p.state.HasActiveSkill("debug") {
			t.Fatal("loaded a replaced source")
		}
		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "replaced") {
			t.Fatalf("error = %q, want it to mention the replacement", got)
		}
	})

	t.Run("same path with changed content", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "alpha")
		p.skillIndex = runtimeIndex(t, p)

		openLoadRow(t, p, scopeGlobal, "debug.md")
		writeSkillBody(t, global, "debug.md", "debug", "beta") // same inode, new bytes
		press(t, p, keyEnter)

		if p.state.HasActiveSkill("debug") {
			t.Fatal("loaded content that changed after the detail opened")
		}
		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "changed on disk") {
			t.Fatalf("error = %q, want it to say the content changed", got)
		}
	})

	t.Run("shared index changed after the row was drawn", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		writeSkillBody(t, global, "debug.md", "debug", "alpha")
		idx := runtimeIndex(t, p)
		p.skillIndex = idx

		// The row is drawn loadable; then the session's index moves on.
		openLoadRow(t, p, scopeGlobal, "debug.md")
		idx.Set("debug", skills.Skill{Name: "debug", Description: "swapped", Body: "# swapped\n"})
		press(t, p, keyEnter)

		if p.state.HasActiveSkill("debug") {
			t.Fatal("loaded a value the index no longer holds")
		}
		if got := p.ActiveList().ErrMsg; !strings.Contains(got, "different") {
			t.Fatalf("error = %q, want it to say the session value differs", got)
		}
		if got := len(skillBodies(p)); got != 0 {
			t.Fatalf("loaded bodies = %d, want 0", got)
		}
	})
}

// TestDuplicateSkillRowsHaveDistinctIdentity pins that two entries sharing a
// name are two rows, each bound to its own discovered source.
func TestDuplicateSkillRowsHaveDistinctIdentity(t *testing.T) {
	t.Run("same name in both scopes", func(t *testing.T) {
		p, global, project := newLoadPanel(t, nil)
		writeSkillBody(t, global, "dup.md", "dup", "global")
		writeSkillBody(t, project, "dup.md", "dup", "project")

		globalID := skillRowID(scopeGlobal, "dup.md")
		projectID := skillRowID(scopeProject, "dup.md")
		if globalID == projectID {
			t.Fatalf("row IDs collide: %q", globalID)
		}

		for id, wantScope := range map[string]string{globalID: scopeGlobal, projectID: scopeProject} {
			openSkillFromList(t, p, id)
			fields := settings.FieldListRows(settings.FrameList(p.stack[len(p.stack)-1]))
			if got := fieldByID(t, fields, "detail.scope").GetStr(); got != wantScope {
				t.Fatalf("row %q opened the %q detail, want %q", id, got, wantScope)
			}
			press(t, p, keyEsc)
			if len(p.stack) != 0 {
				t.Fatal("escape did not pop the detail frame")
			}
		}
	})

	t.Run("same name twice in one store", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		writeSkillBody(t, global, "one.md", "dup", "first")
		writeSkillBody(t, global, "two.md", "dup", "second")

		oneID := skillRowID(scopeGlobal, "one.md")
		twoID := skillRowID(scopeGlobal, "two.md")
		if oneID == twoID {
			t.Fatalf("row IDs collide: %q", oneID)
		}

		// Each row navigates to its own source: the removal confirmed
		// through the second row must not touch the first file.
		openSkillFromList(t, p, twoID)
		press(t, p, keyEnter) // arms
		press(t, p, keyEnter) // confirms

		mustBeGonePath(t, filepath.Join(global, "two.md"))
		mustExistPath(t, filepath.Join(global, "one.md"))
	})
}

// TestRestartOnlySkillChangesDoNotMutateRuntimeIndex pins the restart
// contract: disk installs, edits and removals never mutate the session's
// shared index, and a new session built through the production path does
// pick the effective value up.
func TestRestartOnlySkillChangesDoNotMutateRuntimeIndex(t *testing.T) {
	t.Run("disk changes leave the runtime index alone", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		plugin := skills.Skill{Name: "plugin-only", Description: "loaded by a plugin", Body: "# Plugin\n"}
		active := skills.Skill{Name: "already-active", Description: "active skill", Body: "# Active\n"}

		idx := skills.NewIndex()
		idx.Set(plugin.Name, plugin)
		idx.Set(active.Name, active)
		p.skillIndex = idx
		p.state.ActivateSkill(active.Name)
		before := idx.List()

		source := writeInstallSource(t, "fresh-skill", "fixture")
		installAction(t, p, source, scopeGlobal)
		if p.installErr != "" {
			t.Fatalf("install error: %s", p.installErr)
		}
		if !strings.Contains(p.status, "restart Marshal") {
			t.Fatalf("status = %q, want the restart disclosure", p.status)
		}

		// Edit an installed skill and remove it; neither is a runtime change.
		writeSkillBody(t, global, "edited.md", "edited", "one")
		writeSkillBody(t, global, "edited.md", "edited", "two")
		if err := os.Remove(filepath.Join(global, "edited.md")); err != nil {
			t.Fatalf("remove fixture: %v", err)
		}

		if got := idx.List(); !reflect.DeepEqual(got, before) {
			t.Fatalf("runtime index changed: %v -> %v", before, got)
		}
		if p.state.HasActiveSkill("fresh-skill") {
			t.Fatal("install activated a skill in the running session")
		}
		if !p.state.HasActiveSkill(active.Name) {
			t.Fatal("session-active skill was disturbed")
		}

		// Reopening the panel must not make the newly installed skill
		// loadable: the session still has no content for it.
		p.list.Refresh()
		p.stack = append(p.stack, p.detailFrame(discovered(t, p, scopeGlobal, "fresh-skill.md")))
		row := fieldByID(t, settings.FieldListRows(settings.FrameList(p.stack[len(p.stack)-1])), "action.load")
		if desc := settings.FieldDesc(row); !strings.Contains(desc, "unavailable") {
			t.Fatalf("desc = %q, want the load to stay unavailable before a restart", desc)
		}
		p.stack = p.stack[:len(p.stack)-1]
	})

	t.Run("a new session loads the effective value", func(t *testing.T) {
		p, global, _ := newLoadPanel(t, nil)
		onDisk := writeSkillBody(t, global, "debug.md", "debug", "alpha")

		// A restart: the production startup path builds a fresh index, and a
		// new panel over a new session can load the effective content.
		state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
		q := NewPanel(p.homeDir, p.workDir, true, state, runtimeIndex(t, p))

		openLoadRow(t, q, scopeGlobal, "debug.md")
		press(t, q, keyEnter)

		if !state.HasActiveSkill("debug") {
			t.Fatal("a new session could not load the effective skill")
		}
		bodies := skillBodies(q)
		if len(bodies) != 1 || !strings.Contains(bodies[0], onDisk.Body) {
			t.Fatalf("loaded bodies = %v, want the effective body", bodies)
		}
	})
}
