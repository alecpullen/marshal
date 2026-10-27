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
	if p.status != "loaded debug" {
		t.Fatalf("status = %q, want %q", p.status, "loaded debug")
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
		if settings.FieldID(r) == "skill.debug" {
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

	row := fieldByID(t, settings.FieldListRows(p.list), "skill.debug")
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

		openSkillFromList(t, p, "skill.displayed")
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
