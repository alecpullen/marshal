package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/settings"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
)

// testSession builds the session a roster writes through. Its working
// directory is workDir, so a project save lands in workDir/.marshal/config.toml.
func testSession(t *testing.T, workDir string) acpclient.Session {
	t.Helper()
	state := session.New(config.Default(), workDir, time.Unix(100, 0), session.Persistence{})
	_, sess := inproc.New(inproc.Deps{State: state})
	return sess
}

func TestRosterRendersPresetBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"reason": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RolePlanner: {Preset: "reason"},
		}},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, nil, "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "●") {
		t.Fatalf("preset binding should show ● glyph:\n%s", view)
	}
}

// TestRosterShowsQuotaWhenSet: the roster footer carries both subscription
// windows, which the status line deliberately does not.
func TestRosterShowsQuotaWhenSet(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, nil, "", nil)
	p.SetQuota(&acpwire.Quota{
		PlanType:              "plus",
		PrimaryUsedPercent:    50,
		PrimaryResetAfterMs:   14845000, // 4h7m
		SecondaryUsedPercent:  8,
		SecondaryResetAfterMs: 601645000, // 6d23h
	})
	view := ansi.Strip(p.View(100, 30))
	for _, want := range []string{"quota 50%", "4h7m", "8%", "6d23h", "plus"} {
		if !strings.Contains(view, want) {
			t.Fatalf("roster view missing %q:\n%s", want, view)
		}
	}
}

// TestRosterOmitsQuotaWhenUnset: providers that report no quota must not
// render a quota line.
func TestRosterOmitsQuotaWhenUnset(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, nil, "", nil)
	view := ansi.Strip(p.View(100, 30))
	if strings.Contains(view, "quota") {
		t.Fatalf("roster view should not mention quota:\n%s", view)
	}
}

// TestRosterQuotaClearedByNil: a nil reading hides the line again.
func TestRosterQuotaClearedByNil(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, nil, "", nil)
	p.SetQuota(&acpwire.Quota{PrimaryUsedPercent: 50})
	if view := ansi.Strip(p.View(100, 30)); !strings.Contains(view, "quota") {
		t.Fatalf("expected a quota line:\n%s", view)
	}
	p.SetQuota(nil)
	if view := ansi.Strip(p.View(100, 30)); strings.Contains(view, "quota") {
		t.Fatalf("quota line should be gone after SetQuota(nil):\n%s", view)
	}
}

func TestRosterRendersCustomAgentBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"reason": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleReviewer: {CustomAgent: "strict"},
		}},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"strict": {Name: "strict", Preset: "reason"},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, nil, "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "◆") {
		t.Fatalf("custom-agent binding should show ◆ glyph:\n%s", view)
	}
}

func TestRosterRendersFallbackGlyph(t *testing.T) {
	// When a role is unset and falls back to the implementer preset,
	// it resolves to a preset (● glyph). The ↩ glyph is for legacy
	// provider/model fallback.
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"impl": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "impl"},
		}},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, nil, "", nil)
	view := p.View(80, 24)
	// The planner role is unset and falls back to implementer → preset.
	if !containsGlyph(view, "●") {
		t.Fatalf("resolved role should show ● glyph:\n%s", view)
	}
}

func TestRosterRendersUnresolvedGlyph(t *testing.T) {
	// When no profile exists and no legacy config, roles show ⚠.
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{}
	cfg.Profile.Default = "nonexistent"
	p := NewRosterPanel(cfg, nil, "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "⚠") {
		t.Fatalf("unresolved role should show ⚠ glyph:\n%s", view)
	}
}

func TestRosterFilterPreFill(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, nil, "planner", nil)
	if p.FilterValue() != "planner" {
		t.Fatalf("filter = %q, want planner", p.FilterValue())
	}
}

func TestCustomAgentDrillEditsPreset(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"fast": {Provider: "ollama", Model: "qwen"}}
	cfg.CustomAgents = map[string]routing.CustomAgent{"my-scout": {Name: "my-scout", Preset: "fast"}}
	p := NewRosterPanel(cfg, nil, "", nil)
	// Build the custom-agent frame and verify the preset field reads "fast".
	frame := p.customAgentFrame("my-scout")
	fields := settings.FrameList(frame).Rows()
	var presetField *settings.Field
	for _, f := range fields {
		if settings.FieldID(f) == "roster.ca.my-scout.preset" {
			presetField = f
			break
		}
	}
	if presetField == nil {
		t.Fatal("preset field not found in custom agent frame")
	}
	if settings.FieldGetStr(presetField) != "fast" {
		t.Fatalf("preset = %q, want fast", settings.FieldGetStr(presetField))
	}
}

func TestToolDenylistValidation(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{"x": {Name: "x", Preset: "p"}}
	p := NewRosterPanel(cfg, nil, "", nil)
	// ValidateToolDenylist checks names against the session's tools. With no
	// session, no tools are known, so no name can be verified.
	err := p.ValidateToolDenylist([]string{"nonexistent_tool"})
	if err == nil {
		t.Fatal("invalid tool name should fail validation")
	}
	// With a session that names the tool, it should pass.
	p2 := NewRosterPanel(cfg, testSessionOverTools(t, []string{"file.read"}, true), "", nil)
	err2 := p2.ValidateToolDenylist([]string{"file.read"})
	if err2 != nil {
		t.Fatalf("valid tool name should pass validation: %v", err2)
	}
	err3 := p2.ValidateToolDenylist([]string{"file.read", "nonexistent"})
	if err3 == nil {
		t.Fatal("mix of valid and invalid should fail")
	}
}

// TestRosterValidationFollowsTheSessionsTools verifies that denylist
// validation reads the session's Tools, so the roster knows exactly the
// tools the session reports and nothing else.
func TestRosterValidationFollowsTheSessionsTools(t *testing.T) {
	cfg := config.Default()

	// A session with no registry cannot verify a name.
	unbuilt := NewRosterPanel(cfg, testSessionOverTools(t, nil, false), "", nil)
	err := unbuilt.ValidateToolDenylist([]string{"file.read"})
	if err == nil {
		t.Fatal("a name must fail while the session has no registry")
	}
	if want := `unknown tool: "file.read" (no registry available)`; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	// An empty list validates either way.
	if err := unbuilt.ValidateToolDenylist(nil); err != nil {
		t.Fatalf("an empty denylist should validate: %v", err)
	}

	// A session that names file.read verifies that name and refuses others.
	built := NewRosterPanel(cfg, testSessionOverTools(t, []string{"file.read"}, true), "", nil)
	if err := built.ValidateToolDenylist([]string{"file.read"}); err != nil {
		t.Fatalf("a listed tool should pass: %v", err)
	}
	err = built.ValidateToolDenylist([]string{"nope.tool"})
	if err == nil {
		t.Fatal("an unlisted tool must fail")
	}
	if want := `unknown tool: "nope.tool"`; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// testSessionOverTools builds the session a roster validates through, whose
// Tools reports names and known.
func testSessionOverTools(t *testing.T, names []string, known bool) acpclient.Session {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	_, sess := inproc.New(inproc.Deps{
		State: state,
		Tools: func() ([]string, bool) { return names, known },
	})
	return sess
}

func containsGlyph(s string, g string) bool {
	return strings.Contains(s, g)
}

// TestRosterPersistViaDirectPickedMsg tests that a PickedMsg with a
// pre-configured pendingPick callback persists correctly via ChangedMsg.
func TestRosterPersistViaDirectPickedMsg(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".marshal", "config.toml")

	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "tiny"},
		"slow": {Provider: "ollama", Model: "big"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"default": {Name: "default", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
		}},
	}
	cfg.Profile.Default = "default"

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectConfig(cfgPath, cfg, config.Layers{}); err != nil {
		t.Fatal(err)
	}

	p := NewRosterPanel(cfg, testSession(t, dir), "", nil)

	// Set up pendingPick to mutate the state's config (the one persistNow reads).
	p.pendingPick = func(v string) error {
		stateCfg := settings.StateCfg(p.state)
		profile := stateCfg.AgentProfiles["default"]
		profile.Roles[routing.RoleImplementer] = routing.RoleBinding{Preset: v}
		stateCfg.AgentProfiles["default"] = profile
		return nil
	}

	cmd := p.Update(picker.PickedMsg{Value: "slow"})
	if cmd == nil {
		t.Fatal("expected a Cmd after picker commit, got nil")
	}

	got := cmd()
	changed, ok := got.(settings.ChangedMsg)
	if !ok {
		t.Fatalf("expected settings.ChangedMsg, got %T: %+v", got, got)
	}
	if changed.SaveErr != nil {
		t.Fatalf("ChangedMsg.SaveErr = %v", changed.SaveErr)
	}
	profile, ok := changed.Cfg.AgentProfiles["default"]
	if !ok {
		t.Fatal("ChangedMsg.Cfg missing default profile")
	}
	binding, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		t.Fatal("ChangedMsg.Cfg missing implementer role")
	}
	if binding.Preset != "slow" {
		t.Fatalf("implementer preset = %q, want %q", binding.Preset, "slow")
	}
}

func TestRosterPersistNoopWhenNoCommit(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, nil, "", nil)

	// A plain key press (filter typing) should NOT trigger persistence.
	cmd := p.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, ok := msg.(settings.ChangedMsg); ok {
				t.Fatal("filter typing should not emit ChangedMsg")
			}
		}
	}
}

// TestRosterProfilePickerMutatesState verifies that picking a new active
// profile from the roster root actually changes the working state's profile
// default, not just a captured struct copy.
func TestRosterProfilePickerMutatesState(t *testing.T) {
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"fast": {Name: "fast", Roles: map[routing.AgentRole]routing.RoleBinding{}},
		"slow": {Name: "slow", Roles: map[routing.AgentRole]routing.RoleBinding{}},
	}
	cfg.Profile.Default = "fast"
	p := NewRosterPanel(cfg, nil, "", nil)

	// Simulate the profile picker's OnPick callback directly; in the UI this is
	// invoked via the picker overlay after pressing Enter on the Profile row.
	profileField := settings.FieldListCursorRow(p.list)
	if settings.FieldTitle(profileField) != "Profile" {
		t.Fatalf("cursor not on Profile row: %q", settings.FieldTitle(profileField))
	}
	if err := settings.FieldPickOnPick(profileField)("slow"); err != nil {
		t.Fatalf("OnPick failed: %v", err)
	}

	if got := settings.StateCfg(p.state).Profile.Default; got != "slow" {
		t.Fatalf("profile default = %q, want slow (picker wrote to a copy)", got)
	}
}

// TestRosterRolePickerOpensOverlay tests that pressing Enter on a role field
// opens a picker overlay locally, and that PickedMsg triggers persistence.
func TestRosterRolePickerOpensOverlay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".marshal", "config.toml")

	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "tiny"},
		"slow": {Provider: "ollama", Model: "big"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"default": {Name: "default", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
		}},
	}
	cfg.Profile.Default = "default"

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectConfig(cfgPath, cfg, config.Layers{}); err != nil {
		t.Fatal(err)
	}

	p := NewRosterPanel(cfg, testSession(t, dir), "", nil)

	// Navigate down to the Planner role field.
	// Cursor starts at 0 (Profile). Press down once to reach Planner:
	//   0 = Profile (kindPicker)
	//   1 = header.swarm_roles (kindHeader, skipped by cursor)
	//   2 = Planner (kindPicker)
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown, Text: "down"})

	// Press Enter on the Planner role field to trigger the picker.
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Text: "enter"})

	// Verify a picker overlay was opened.
	if !p.pickerActive {
		t.Fatal("expected pickerActive to be true after Enter on a role field")
	}
	if p.pickerModel == nil {
		t.Fatal("expected pickerModel to be non-nil after Enter on a role field")
	}
	if p.pendingPick == nil {
		t.Fatal("expected pendingPick to be set after Enter on a role field")
	}

	// The field list's openRow for kindPicker returns nil (it just stores the
	// picker request). The panel opens the picker model itself, so cmd may be
	// nil here — that's fine. The important thing is pickerActive/pickerModel.

	// Simulate a picker selection: PickedMsg with value "slow".
	cmd = p.Update(picker.PickedMsg{Value: "slow"})
	if cmd == nil {
		t.Fatal("expected a Cmd after picker commit, got nil")
	}

	// Verify picker state is cleared.
	if p.pickerActive {
		t.Fatal("expected pickerActive to be false after PickedMsg")
	}
	if p.pickerModel != nil {
		t.Fatal("expected pickerModel to be nil after PickedMsg")
	}
	if p.pendingPick != nil {
		t.Fatal("expected pendingPick to be nil after PickedMsg")
	}

	// Verify persistence via ChangedMsg.
	got := cmd()
	changed, ok := got.(settings.ChangedMsg)
	if !ok {
		t.Fatalf("expected settings.ChangedMsg, got %T: %+v", got, got)
	}
	if changed.SaveErr != nil {
		t.Fatalf("ChangedMsg.SaveErr = %v", changed.SaveErr)
	}
	profile, ok := changed.Cfg.AgentProfiles["default"]
	if !ok {
		t.Fatal("ChangedMsg.Cfg missing default profile")
	}
	binding, ok := profile.Roles[routing.RolePlanner]
	if !ok {
		t.Fatal("ChangedMsg.Cfg missing planner role")
	}
	if binding.Preset != "slow" {
		t.Fatalf("planner preset = %q, want %q", binding.Preset, "slow")
	}
}

func TestRosterDrillOpensCustomAgentFrame(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"reviewer-x": {Name: "reviewer-x", Preset: ""},
	}
	p := NewRosterPanel(cfg, nil, "", nil)

	// Move the cursor onto the custom agent drill row and press enter.
	drillToRow(t, p, "reviewer-x")
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	out := p.View(120, 30)
	if !strings.Contains(out, "System prompt") {
		t.Fatalf("drill should open the agent's edit frame:\n%s", out)
	}

	// Esc pops back to the roster instead of closing the panel.
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("esc while drilled should pop the frame, not close the panel")
	}
	out = p.View(120, 30)
	if !strings.Contains(out, "Custom Agents") {
		t.Fatalf("expected the roster root after popping:\n%s", out)
	}
}

// drillToRow walks the active list with down-keys until the cursor row's
// title matches (roster order is deterministic: profile, role groups,
// custom agents, budgets). Uses activeList() so it works inside drilled frames.
func drillToRow(t *testing.T, p *Panel, title string) {
	t.Helper()
	for i := 0; i < 40; i++ {
		if row := settings.FieldListCursorRow(p.activeList()); row != nil && settings.FieldTitle(row) == title {
			return
		}
		p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatalf("row %q not found", title)
}

func TestRosterCreatesCustomAgent(t *testing.T) {
	p := NewRosterPanel(config.Default(), nil, "", nil)
	drillToRow(t, p, "＋ New custom agent")
	enterCmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Execute the returned Cmd to deliver createAgentPromptMsg to the panel.
	if enterCmd != nil {
		if msg := enterCmd(); msg != nil {
			p.Update(msg)
		}
	}

	// The action opens the picker overlay in free-text mode for the name.
	if !p.pickerActive {
		t.Fatal("create action should open the name picker overlay")
	}
	cmd := p.Update(picker.PickedMsg{Value: "reviewer-x"})
	if cmd == nil {
		t.Fatal("creation should emit a persist command")
	}

	cfg := settings.StateCfg(p.state)
	ca, ok := cfg.CustomAgents["reviewer-x"]
	if !ok {
		t.Fatalf("agent not created: %#v", cfg.CustomAgents)
	}
	if ca.Name != "reviewer-x" {
		t.Fatalf("Name = %q, want reviewer-x (stored TOML field must be written)", ca.Name)
	}

	// The new agent's edit frame opens immediately — the preset picker is
	// the first field, so "pick a preset it layers on" is the next gesture.
	out := p.View(120, 30)
	if !strings.Contains(out, "Preset") || !strings.Contains(out, "System prompt") {
		t.Fatalf("expected the new agent's edit frame after creation:\n%s", out)
	}

	// Duplicate names are rejected.
	p2 := NewRosterPanel(cfg, nil, "", nil)
	drillToRow(t, p2, "＋ New custom agent")
	enterCmd = p2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if enterCmd != nil {
		if msg := enterCmd(); msg != nil {
			p2.Update(msg)
		}
	}
	p2.Update(picker.PickedMsg{Value: "reviewer-x"})
	if got := len(settings.StateCfg(p2.state).CustomAgents); got != 1 {
		t.Fatalf("duplicate name created a second entry, count = %d", got)
	}
}

func TestRosterDeletesCustomAgentWithConfirm(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"doomed": {Name: "doomed", Preset: ""},
	}
	p := NewRosterPanel(cfg, nil, "", nil)
	drillToRow(t, p, "doomed")
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open the agent frame

	drillToRow(t, p, "Delete this agent")
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // arm
	if _, ok := settings.StateCfg(p.state).CustomAgents["doomed"]; !ok {
		t.Fatal("first press should arm, not delete")
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm
	if _, ok := settings.StateCfg(p.state).CustomAgents["doomed"]; ok {
		t.Fatal("second press should delete the agent")
	}
	// After deletion the panel is back at the roster root.
	out := p.View(120, 30)
	if !strings.Contains(out, "Custom Agents") || strings.Contains(out, "System prompt") {
		t.Fatalf("expected the roster root after deletion:\n%s", out)
	}
}

func TestRosterTwoColumnShowsDetailPane(t *testing.T) {
	p := NewRosterPanel(config.Default(), nil, "", nil)
	out := p.View(140, 20)
	// Above the breakpoint the panel fills the dock width.
	// Check a content line (not the title line) to verify width.
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 lines, got %d:\n%s", len(lines), out)
	}
	// Line index 3 is the first field row (Profile).
	if got := ansi.StringWidth(lines[3]); got < 100 {
		t.Fatalf("roster should fill the dock width, content line is %d cols: %q", got, out)
	}
}

// TestRosterCreateErrorKeepsPickerOpen verifies that a rejected name
// (empty or duplicate) keeps the picker open and surfaces the error in the
// picker's view, so the user can fix the input and retry. Covers both the
// "empty name" path and the "duplicate name" path.
func TestRosterCreateErrorKeepsPickerOpen(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"existing": {Name: "existing"},
	}
	p := NewRosterPanel(cfg, nil, "", nil)

	openCreatePicker := func(t *testing.T) {
		t.Helper()
		drillToRow(t, p, "＋ New custom agent")
		cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil {
			if msg := cmd(); msg != nil {
				p.Update(msg)
			}
		}
		if !p.pickerActive {
			t.Fatal("create action should open the name picker overlay")
		}
	}

	// Case 1: empty name.
	openCreatePicker(t)
	persistCmd := p.Update(picker.PickedMsg{Value: ""})
	if persistCmd != nil {
		t.Fatal("empty name should not emit a persist command")
	}
	if !p.pickerActive {
		t.Fatal("picker should stay open after empty-name rejection")
	}
	if p.pickerModel == nil || p.pickerModel.ErrMsg() == "" {
		t.Fatal("picker should show an error message")
	}
	out := p.View(120, 30)
	if !strings.Contains(ansi.Strip(out), "empty") {
		t.Fatalf("view should surface the empty-name error:\n%s", out)
	}

	// Typing into the filter clears the error so the user gets a clean slate.
	p.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if got := p.pickerModel.ErrMsg(); got != "" {
		t.Fatalf("typing should clear the picker error, got %q", got)
	}

	// Case 2: duplicate name. Re-open the same create picker flow with the
	// typed-but-rejected value still in flight. Drive Enter on "existing".
	p.Update(picker.PickedMsg{Value: "existing"})
	if !p.pickerActive {
		t.Fatal("picker should stay open after duplicate-name rejection")
	}
	if p.pickerModel.ErrMsg() == "" {
		t.Fatal("picker should show a duplicate-name error")
	}
	if !strings.Contains(p.pickerModel.ErrMsg(), "existing") {
		t.Fatalf("error should name the duplicate, got %q", p.pickerModel.ErrMsg())
	}
	out = p.View(120, 30)
	if !strings.Contains(ansi.Strip(out), "existing") {
		t.Fatalf("view should surface the duplicate-name error:\n%s", out)
	}
	if got := len(settings.StateCfg(p.state).CustomAgents); got != 1 {
		t.Fatalf("duplicate must not be inserted, count = %d", got)
	}

	// Case 3: fix the input and submit — creation succeeds and picker closes.
	p.Update(picker.PickedMsg{Value: "fresh"})
	if p.pickerActive {
		t.Fatal("successful retry should close the picker")
	}
	if _, ok := settings.StateCfg(p.state).CustomAgents["fresh"]; !ok {
		t.Fatal("successful retry should create the agent")
	}
	if got := len(settings.StateCfg(p.state).CustomAgents); got != 2 {
		t.Fatalf("expected 2 agents, got %d", got)
	}
}

// A drilled-in frame had no header at all: customAgentFrame builds only
// picker and scalar fields, so its rows sat directly under the breadcrumb.
func TestDrilledFrameRendersHeader(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"reviewer-x": {Name: "reviewer-x", Preset: ""},
	}
	p := NewRosterPanel(cfg, nil, "", nil)

	drillToRow(t, p, "reviewer-x")
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	out := ansi.Strip(p.View(120, 30))
	if !strings.Contains(out, "System prompt") {
		t.Fatalf("precondition: drill did not open the edit frame:\n%s", out)
	}
	if !strings.Contains(out, "reviewer-x ─") {
		t.Errorf("drilled frame has no rule-backed header naming it:\n%s", out)
	}
}

// The root roster's five KindHeader rows keep flHeaderStyle in the two-column
// layout, which this renders: in two columns a divider would erase the detail
// pane's text on its row, so the panel leaves group dividers off.
func TestRootRosterHeadersUnchanged(t *testing.T) {
	p := NewRosterPanel(config.Default(), nil, "", nil)
	out := ansi.Strip(p.View(120, 30))
	if !strings.Contains(out, "Swarm Roles") {
		t.Fatalf("precondition: root roster missing its section headers:\n%s", out)
	}
	if strings.Contains(out, "Swarm Roles ─") {
		t.Errorf("root section header gained a rule; it must keep flHeaderStyle:\n%s", out)
	}
}

// The roster count is the closed frame's footer, not a content row.
func TestAgentsCountIsInTheBottomBorder(t *testing.T) {
	p := NewRosterPanel(config.Default(), nil, "", nil)
	rows := strings.Split(ansi.Strip(p.View(80, 30)), "\n")
	last := rows[len(rows)-1]
	if !strings.HasPrefix(last, "╰─ ") || !strings.Contains(last, "entries") {
		t.Fatalf("last row should carry the entries count in the bottom border, got %q", last)
	}
}

// TestRosterInteractionFieldsUseCanonicalDefaults pins read/edit/save of the
// optional Mode/Auto roster controls: nil inherits, explicit false survives,
// and Ask clears an explicit true Auto.
func TestRosterInteractionFieldsUseCanonicalDefaults(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{"worker": {Name: "worker"}}
	p := NewRosterPanel(cfg, nil, "", nil)

	var mode, auto *settings.Field
	for _, f := range p.customAgentFrame("worker").List.Rows() {
		switch settings.FieldTitle(f) {
		case "Mode":
			mode = f
		case "Auto":
			auto = f
		case "Approval mode":
			t.Fatal("roster still exposes the retired Approval mode row")
		}
	}
	if mode == nil || auto == nil {
		t.Fatalf("roster missing Mode/Auto: mode=%v auto=%v", mode, auto)
	}
	if got := mode.Options(); len(got) != 3 || got[0] != "inherit" || got[1] != "ask" || got[2] != "act" {
		t.Fatalf("Mode options = %v, want [inherit ask act]", got)
	}
	if got := auto.Options(); len(got) != 3 || got[0] != "inherit" || got[1] != "on" || got[2] != "off" {
		t.Fatalf("Auto options = %v, want [inherit on off]", got)
	}
	if got := mode.GetStr(); got != "inherit" {
		t.Fatalf("initial mode = %q, want inherit", got)
	}
	if got := auto.GetStr(); got != "inherit" {
		t.Fatalf("initial auto = %q, want inherit", got)
	}

	if err := auto.SetStr("off"); err != nil {
		t.Fatalf("auto off: %v", err)
	}
	ca := settings.StateCfg(p.state).CustomAgents["worker"]
	if ca.Auto == nil || *ca.Auto {
		t.Fatalf("explicit auto=false was not preserved: %+v", ca.Auto)
	}
	if got := auto.GetStr(); got != "off" {
		t.Fatalf("auto after off = %q, want off", got)
	}

	if err := mode.SetStr("act"); err != nil {
		t.Fatalf("mode act: %v", err)
	}
	ca = settings.StateCfg(p.state).CustomAgents["worker"]
	if ca.Mode == nil || *ca.Mode != interaction.ModeAct {
		t.Fatalf("mode = %+v, want act", ca.Mode)
	}
	if err := auto.SetStr("on"); err != nil {
		t.Fatalf("auto on: %v", err)
	}
	ca = settings.StateCfg(p.state).CustomAgents["worker"]
	if ca.Auto == nil || !*ca.Auto {
		t.Fatalf("auto on = %+v, want true", ca.Auto)
	}

	if err := mode.SetStr("ask"); err != nil {
		t.Fatalf("mode ask: %v", err)
	}
	ca = settings.StateCfg(p.state).CustomAgents["worker"]
	if ca.Mode == nil || *ca.Mode != interaction.ModeAsk {
		t.Fatalf("mode = %+v, want ask", ca.Mode)
	}
	if ca.Auto == nil || *ca.Auto {
		t.Fatalf("Ask must clear an explicit true Auto, got %+v", ca.Auto)
	}

	if err := auto.SetStr("inherit"); err != nil {
		t.Fatalf("auto inherit: %v", err)
	}
	if got := settings.StateCfg(p.state).CustomAgents["worker"].Auto; got != nil {
		t.Fatalf("inherit must clear the Auto pointer, got %+v", got)
	}
	if err := mode.SetStr("inherit"); err != nil {
		t.Fatalf("mode inherit: %v", err)
	}
	if got := settings.StateCfg(p.state).CustomAgents["worker"].Mode; got != nil {
		t.Fatalf("inherit must clear the Mode pointer, got %+v", got)
	}

	// The editor still routes through its save path.
	if cmd := p.persistNow(); cmd == nil {
		t.Fatal("roster edits produced no persist command")
	} else {
		_ = cmd()
	}
}

// --- focused input routing -------------------------------------------------

// rosterFixture builds a roster over an isolated config file with two presets,
// one profile and one custom agent, so every edit and pick has somewhere real
// to land.
func rosterFixture(t *testing.T) (*Panel, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".marshal", "config.toml")
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "tiny"},
		"slow": {Provider: "ollama", Model: "big"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"default": {Name: "default", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
		}},
	}
	cfg.Profile.Default = "default"
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"scout": {Name: "scout", Preset: "fast"},
	}
	return NewRosterPanel(cfg, testSession(t, dir), "", nil), cfgPath
}

// openAgentFrame drills into the custom agent "scout" and returns after its
// edit frame is on the stack.
func openAgentFrame(t *testing.T, p *Panel) {
	t.Helper()
	drillToRow(t, p, "scout")
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(p.stack) != 1 {
		t.Fatalf("enter on the custom agent row pushed %d frames, want 1", len(p.stack))
	}
}

// runCommand runs cmd and feeds the message it produced back through the panel,
// the way the dock host does.
func runCommand(t *testing.T, p *Panel, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		p.Update(msg)
	}
}

// changedReceipt runs a commit command and returns its persistence receipt,
// failing when the command is missing or produced something else.
func changedReceipt(t *testing.T, cmd tea.Cmd) settings.ChangedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("the commit emitted no command")
	}
	msg := cmd()
	changed, ok := msg.(settings.ChangedMsg)
	if !ok {
		t.Fatalf("commit produced %T, want settings.ChangedMsg", msg)
	}
	if changed.SaveErr != nil {
		t.Fatalf("saving the commit failed: %v", changed.SaveErr)
	}
	return changed
}

// TestAgentPasteFollowsFocusedControl pins paste ownership: the picker overlay,
// then the drilled frame or field being edited, then the roster filter — never
// two of them, and never the filter underneath a visible control.
func TestAgentPasteFollowsFocusedControl(t *testing.T) {
	t.Run("root filter", func(t *testing.T) {
		p, _ := rosterFixture(t)
		p.Update(tea.PasteMsg{Content: "planner"})
		if got := p.FilterValue(); got != "planner" {
			t.Fatalf("filter = %q, want %q", got, "planner")
		}
		if got := settings.StateCfg(p.state).CustomAgents["scout"].SystemPrompt; got != "" {
			t.Fatalf("a filter paste changed the committed prompt: %q", got)
		}
	})

	t.Run("idle scalar opens and receives", func(t *testing.T) {
		p, cfgPath := rosterFixture(t)
		openAgentFrame(t, p)
		drillToRow(t, p, "System prompt")
		p.Update(tea.PasteMsg{Content: "be terse"})
		if !p.activeList().Editing() {
			t.Fatal("paste on an idle editable scalar did not open its editor")
		}
		if got := p.activeList().InputValue(); got != "be terse" {
			t.Fatalf("editor value = %q, want %q", got, "be terse")
		}
		if got := p.FilterValue(); got != "" {
			t.Fatalf("filter = %q, want empty — the paste went to the editor", got)
		}
		// The value is not committed until the editor's own Enter path.
		if got := settings.StateCfg(p.state).CustomAgents["scout"].SystemPrompt; got != "" {
			t.Fatalf("prompt = %q before Enter, want it uncommitted", got)
		}
		changed := changedReceipt(t, p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}))
		if got := changed.Cfg.CustomAgents["scout"].SystemPrompt; got != "be terse" {
			t.Fatalf("committed prompt = %q, want %q", got, "be terse")
		}
		if _, err := os.Stat(cfgPath); err != nil {
			t.Fatalf("the commit did not write the config file: %v", err)
		}
	})

	t.Run("active edit keeps the paste", func(t *testing.T) {
		p, _ := rosterFixture(t)
		openAgentFrame(t, p)
		drillToRow(t, p, "System prompt")
		p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !p.activeList().Editing() {
			t.Fatal("enter on System prompt did not open the editor")
		}
		p.Update(tea.PasteMsg{Content: "pasted"})
		if got := p.activeList().InputValue(); got != "pasted" {
			t.Fatalf("editor value = %q, want %q", got, "pasted")
		}
		if got := p.FilterValue(); got != "" {
			t.Fatalf("filter = %q, want empty", got)
		}
		if len(p.stack) != 1 {
			t.Fatalf("stack = %d frames, want the edit frame still open", len(p.stack))
		}
	})

	t.Run("custom-name picker", func(t *testing.T) {
		p, _ := rosterFixture(t)
		drillToRow(t, p, "＋ New custom agent")
		runCommand(t, p, p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}))
		if !p.pickerActive || p.pickerModel == nil {
			t.Fatal("create action did not open the name picker")
		}
		p.Update(tea.PasteMsg{Content: "new-agent"})
		if got := p.pickerModel.FilterValue(); got != "new-agent" {
			t.Fatalf("picker filter = %q, want %q", got, "new-agent")
		}
		if got := p.FilterValue(); got != "" {
			t.Fatalf("roster filter = %q, want empty — the paste belongs to the picker", got)
		}
		if _, ok := settings.StateCfg(p.state).CustomAgents["new-agent"]; ok {
			t.Fatal("the pasted name was committed before the picker's Enter")
		}
		pick := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if pick == nil {
			t.Fatal("the picker's Enter emitted no command")
		}
		changed := changedReceipt(t, p.Update(pick()))
		if _, ok := changed.Cfg.CustomAgents["new-agent"]; !ok {
			t.Fatalf("the picker commit did not create the agent: %#v", changed.Cfg.CustomAgents)
		}
	})

	t.Run("choice picker", func(t *testing.T) {
		p, _ := rosterFixture(t)
		drillToRow(t, p, "Planner")
		runCommand(t, p, p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}))
		if !p.pickerActive || p.pickerModel == nil {
			t.Fatal("enter on a role row did not open its picker")
		}
		p.Update(tea.PasteMsg{Content: "slow"})
		if got := p.pickerModel.FilterValue(); got != "slow" {
			t.Fatalf("picker filter = %q, want %q", got, "slow")
		}
		if got := p.FilterValue(); got != "" {
			t.Fatalf("roster filter = %q, want empty", got)
		}
		if got := settings.StateCfg(p.state).AgentProfiles["default"].Roles[routing.RolePlanner]; got.Preset != "" {
			t.Fatalf("planner preset = %q before the picker's Enter, want unset", got.Preset)
		}
		pick := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if pick == nil {
			t.Fatal("the picker's Enter emitted no command")
		}
		changed := changedReceipt(t, p.Update(pick()))
		profile := changed.Cfg.AgentProfiles["default"]
		if got := profile.Roles[routing.RolePlanner].Preset; got != "slow" {
			t.Fatalf("planner preset = %q after picking, want %q", got, "slow")
		}
	})
}

// TestAgentQuestionMarkAndEscapeRespectEditor pins that "?" belongs to the
// focused control rather than the roster legend, and that Escape cancels an
// active edit or prompt before it navigates back.
func TestAgentQuestionMarkAndEscapeRespectEditor(t *testing.T) {
	t.Run("scalar", func(t *testing.T) {
		p, _ := rosterFixture(t)
		openAgentFrame(t, p)
		drillToRow(t, p, "System prompt")
		p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !p.activeList().Editing() {
			t.Fatal("enter on System prompt did not open the editor")
		}

		p.Update(tea.KeyPressMsg{Text: "?"})
		if p.showLegend {
			t.Fatal("? toggled the roster legend from inside an active editor")
		}
		if !p.activeList().Editing() {
			t.Fatal("? closed the editor instead of being typed")
		}
		changed := changedReceipt(t, p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}))
		if got := changed.Cfg.CustomAgents["scout"].SystemPrompt; got != "?" {
			t.Fatalf("committed prompt = %q, want ? typed as input", got)
		}

		// A cancelled edit changes nothing and keeps the frame open.
		p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !p.activeList().Editing() {
			t.Fatal("re-opening the editor failed")
		}
		p.Update(tea.PasteMsg{Content: "discard me"})
		if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
			t.Fatalf("escape on an active editor emitted %T, want a plain cancel", cmd())
		}
		if p.activeList().Editing() {
			t.Fatal("escape did not cancel the editor")
		}
		if len(p.stack) != 1 {
			t.Fatalf("stack = %d frames after cancelling, want the frame still open", len(p.stack))
		}
		if got := settings.StateCfg(p.state).CustomAgents["scout"].SystemPrompt; got != "?" {
			t.Fatalf("cancelled edit changed the saved prompt to %q", got)
		}

		// With no editor active, escape navigates back.
		p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if len(p.stack) != 0 {
			t.Fatalf("stack = %d frames after going back, want 0", len(p.stack))
		}
		if p.showLegend {
			t.Fatal("escape toggled the legend")
		}
	})

	t.Run("collection prompt", func(t *testing.T) {
		p, _ := rosterFixture(t)
		drillToRow(t, p, "Swarm")
		p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if len(p.stack) != 1 {
			t.Fatalf("enter on Swarm pushed %d frames, want 1", len(p.stack))
		}
		drillToRow(t, p, "Tool iters")
		p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if len(p.stack) != 2 {
			t.Fatalf("enter on Tool iters pushed %d frames, want 2", len(p.stack))
		}
		p.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		if !p.activeList().Editing() {
			t.Fatal("a did not open the new-key prompt")
		}
		p.Update(tea.KeyPressMsg{Text: "?"})
		if p.showLegend {
			t.Fatal("? toggled the legend from the collection-add prompt")
		}
		if !p.activeList().Editing() {
			t.Fatal("? closed the collection-add prompt")
		}
		if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
			t.Fatal("committing the new key emitted no command")
		}
		if _, ok := settings.StateCfg(p.state).Swarm.Budget.ToolIters["?"]; !ok {
			t.Fatalf("? was not used as the new key: %#v", settings.StateCfg(p.state).Swarm.Budget.ToolIters)
		}
	})

	t.Run("picker", func(t *testing.T) {
		p, _ := rosterFixture(t)
		drillToRow(t, p, "Planner")
		runCommand(t, p, p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}))
		if !p.pickerActive || p.pickerModel == nil {
			t.Fatal("enter on a role row did not open its picker")
		}
		p.Update(tea.KeyPressMsg{Text: "?"})
		if p.showLegend {
			t.Fatal("? toggled the legend from the picker overlay")
		}
		if got := p.pickerModel.FilterValue(); got != "?" {
			t.Fatalf("picker filter = %q, want ?", got)
		}
		cancel := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if cancel == nil {
			t.Fatal("escape emitted no command from the picker")
		}
		p.Update(cancel())
		if p.pickerActive || p.pickerModel != nil {
			t.Fatal("escape did not close the picker")
		}
		if got := settings.StateCfg(p.state).AgentProfiles["default"].Roles[routing.RolePlanner].Preset; got != "" {
			t.Fatalf("the cancelled picker still wrote planner preset %q", got)
		}
	})

	t.Run("legend without an editor", func(t *testing.T) {
		p, _ := rosterFixture(t)
		p.Update(tea.KeyPressMsg{Text: "?"})
		if !p.showLegend {
			t.Fatal("? should still toggle the legend when no control owns the key")
		}
		p.Update(tea.KeyPressMsg{Text: "?"})
		if p.showLegend {
			t.Fatal("? should toggle the legend back off")
		}
	})

	t.Run("delete confirmation", func(t *testing.T) {
		p, _ := rosterFixture(t)
		openAgentFrame(t, p)
		drillToRow(t, p, "Delete this agent")
		p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !p.deleteArmed {
			t.Fatal("the first press did not arm the delete")
		}
		p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if p.deleteArmed {
			t.Fatal("escape left the delete armed")
		}
		if len(p.stack) != 0 {
			t.Fatalf("stack = %d frames, want the roster root", len(p.stack))
		}
		if _, ok := settings.StateCfg(p.state).CustomAgents["scout"]; !ok {
			t.Fatal("escape deleted the agent")
		}
	})
}
