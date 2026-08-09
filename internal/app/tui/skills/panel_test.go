package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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

	// With no skills installed, the only selectable row is "＋ Install skill".
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
	if len(cfg.Skills.Autoload) != 1 || cfg.Skills.Autoload[0] != "debug" {
		t.Fatalf("on-disk autoload = %v, want [debug]", cfg.Skills.Autoload)
	}

	auto.SetBool(false)
	cfg, err = config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Skills.Autoload) != 0 {
		t.Fatalf("autoload should be empty after toggling off, got %v", cfg.Skills.Autoload)
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
