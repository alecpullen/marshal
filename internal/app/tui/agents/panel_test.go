package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/settings"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

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
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "●") {
		t.Fatalf("preset binding should show ● glyph:\n%s", view)
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
	p := NewRosterPanel(cfg, "", "", nil)
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
	p := NewRosterPanel(cfg, "", "", nil)
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
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "⚠") {
		t.Fatalf("unresolved role should show ⚠ glyph:\n%s", view)
	}
}

func TestRosterFilterPreFill(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, "", "planner", nil)
	if p.FilterValue() != "planner" {
		t.Fatalf("filter = %q, want planner", p.FilterValue())
	}
}

func TestCustomAgentDrillEditsPreset(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"fast": {Provider: "ollama", Model: "qwen"}}
	cfg.CustomAgents = map[string]routing.CustomAgent{"my-scout": {Name: "my-scout", Preset: "fast"}}
	p := NewRosterPanel(cfg, "", "", nil)
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
	p := NewRosterPanel(cfg, "", "", nil)
	// ValidateToolDenylist checks names against a live registry.
	// With no registry passed, it should still validate (empty registry).
	err := p.ValidateToolDenylist([]string{"nonexistent_tool"})
	if err == nil {
		t.Fatal("invalid tool name should fail validation")
	}
	// With a registry that has the tool, it should pass.
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}})
	p2 := NewRosterPanelWithRegistry(cfg, "", "", nil, reg)
	err2 := p2.ValidateToolDenylist([]string{"file.read"})
	if err2 != nil {
		t.Fatalf("valid tool name should pass validation: %v", err2)
	}
	err3 := p2.ValidateToolDenylist([]string{"file.read", "nonexistent"})
	if err3 == nil {
		t.Fatal("mix of valid and invalid should fail")
	}
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
	if err := config.SaveProjectConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	p := NewRosterPanel(cfg, cfgPath, "", nil)

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
	p := NewRosterPanel(cfg, "", "", nil)

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
	if err := config.SaveProjectConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	p := NewRosterPanel(cfg, cfgPath, "", nil)

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
	p := NewRosterPanel(cfg, filepath.Join(t.TempDir(), "config.toml"), "", nil)

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
	p := NewRosterPanel(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "", nil)
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
	p2 := NewRosterPanel(cfg, filepath.Join(t.TempDir(), "config.toml"), "", nil)
	drillToRow(t, p2, "＋ New custom agent")
	p2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p2.Update(picker.PickedMsg{Value: "reviewer-x"})
	if got := len(settings.StateCfg(p2.state).CustomAgents); got != 1 {
		t.Fatalf("duplicate name created a second entry, count = %d", got)
	}
}

func TestRosterTwoColumnShowsDetailPane(t *testing.T) {
	p := NewRosterPanel(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "", nil)
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
