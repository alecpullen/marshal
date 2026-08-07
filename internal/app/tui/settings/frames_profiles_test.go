package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func profilesTestState() *state {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"small": {Name: "small", Provider: "ollama", Model: "qwen3:8b"},
		"large": {Name: "large", Provider: "ollama", Model: "qwen3:32b"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local": {Name: "local", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "large"},
		}},
	}
	return newState(cfg)
}

func TestProfilesFrameListsProfiles(t *testing.T) {
	s := profilesTestState()
	f := profilesFrame(s)
	rows := f.List.Rows()
	if len(rows) == 0 {
		t.Fatal("profilesFrame should have at least one row")
	}
	var localRow *field
	for _, r := range rows {
		if r.ID == "profiles.local" {
			localRow = r
			break
		}
	}
	if localRow == nil {
		t.Fatal("profilesFrame should have a row with id profiles.local")
	}
	if !strings.Contains(localRow.Title, "1 role") {
		t.Fatalf("local row title = %q, want to contain '1 role'", localRow.Title)
	}
}

func TestProfileEntryHasOneRowPerRole(t *testing.T) {
	s := profilesTestState()
	f := profilesFrame(s)
	rows := f.List.Rows()
	var localRow *field
	for _, r := range rows {
		if r.ID == "profiles.local" {
			localRow = r
			break
		}
	}
	if localRow == nil {
		t.Fatal("no profiles.local row")
	}
	detail := localRow.Build()
	detailRows := detail.List.Rows()
	// Base model + Fast model + Embeddings + a roles header + every
	// dispatchable role except implementer (surfaced as Base model).
	wantRows := 4 + (len(routing.AllRoles) - 1)
	if len(detailRows) != wantRows {
		t.Fatalf("profile detail should have %d rows, got %d", wantRows, len(detailRows))
	}
	if detailRows[0].ID != "profiles.local.implementer" {
		t.Fatalf("first row id = %q, want profiles.local.implementer (Base model)", detailRows[0].ID)
	}
	if detailRows[1].ID != "profiles.local.fast" {
		t.Fatalf("second row id = %q, want profiles.local.fast", detailRows[1].ID)
	}
	if last := detailRows[len(detailRows)-1].ID; last != "profiles.local.sdd_branch_reviewer" {
		t.Fatalf("last row id = %q, want profiles.local.sdd_branch_reviewer", last)
	}
	// Find the implementer row — it should show the assigned preset
	var implRow *field
	for _, r := range detailRows {
		if strings.HasSuffix(r.ID, "."+string(routing.RoleImplementer)) {
			implRow = r
			break
		}
	}
	if implRow == nil {
		t.Fatal("no implementer row in profile detail")
	}
	if implRow.GetStr() != "large" {
		t.Fatalf("implementer preset = %q, want 'large'", implRow.GetStr())
	}
	if implRow.Display == nil {
		t.Fatal("implementer (Base model) row must carry a Display closure")
	}
	if value, badge := implRow.Display(); value != "qwen3:32b" || badge != "ollama" {
		t.Fatalf("implementer Display = (%q, %q), want (qwen3:32b, ollama)", value, badge)
	}
}

func TestProfileRolePickAssignsAndClears(t *testing.T) {
	s := profilesTestState()
	// Get the implementer role field for the "local" profile
	field := roleModelField(s, "local", routing.RoleImplementer)
	if field.GetStr() != "large" {
		t.Fatalf("initial preset = %q, want 'large'", field.GetStr())
	}
	// Pick a different preset
	if err := field.PickOnPick("small"); err != nil {
		t.Fatalf("pickOnPick(small) = %v", err)
	}
	if field.GetStr() != "small" {
		t.Fatalf("after pick, preset = %q, want 'small'", field.GetStr())
	}
	// Clear via unset sentinel
	if err := field.PickOnPick(unsetRoleValue); err != nil {
		t.Fatalf("pickOnPick(unset) = %v", err)
	}
	if field.GetStr() != "" {
		t.Fatalf("after unset, preset = %q, want empty", field.GetStr())
	}
	// Pick an unknown preset should error
	if err := field.PickOnPick("nonexistent"); err == nil {
		t.Fatal("pickOnPick(nonexistent) should error")
	}
}

func TestProfilesAddCreatesEmptyProfile(t *testing.T) {
	s := profilesTestState()
	ps := newPaneStack(profilesFrame(s))
	ps.SetSize(80, 24)
	// Press 'a' to trigger the add prompt
	ps.Update(kp("a"))
	// Submit the name
	for _, r := range "fast" {
		ps.Update(kp(string(r)))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p, ok := s.cfg.AgentProfiles["fast"]
	if !ok {
		t.Fatal("add should create profile 'fast'")
	}
	if len(p.Roles) != 0 {
		t.Fatalf("new profile should have empty Roles, got %v", p.Roles)
	}
	// Try adding a duplicate name
	ps.Update(kp("a"))
	for _, r := range "fast" {
		ps.Update(kp(string(r)))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ps.Top().List.ErrMsg == "" {
		t.Fatal("duplicate add should set errMsg")
	}
}

func profileTestConfig() config.Config {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"base":  {Name: "base", Provider: "anthropic", Model: "claude-sonnet-4-5"},
		"cheap": {Name: "cheap", Provider: "anthropic", Model: "claude-haiku-4-5"},
		"deep":  {Name: "deep", Provider: "anthropic", Model: "claude-opus-4-5"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"coding": {Name: "coding", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "base"},
		}},
	}
	return cfg
}

// displayOf finds a role row in the profile frame and returns its rendered
// value and badge.
func displayOf(t *testing.T, s *state, role routing.AgentRole) (string, string) {
	t.Helper()
	f := roleModelField(s, "coding", role)
	if f.Display == nil {
		t.Fatalf("role %s has no Display closure", role)
	}
	return f.Display()
}

func TestRolesInheritBaseModel(t *testing.T) {
	s := newState(profileTestConfig())
	value, badge := displayOf(t, s, routing.RoleTester)
	if value != "claude-sonnet-4-5" {
		t.Errorf("value = %q, want claude-sonnet-4-5", value)
	}
	if badge != "← base" {
		t.Errorf("badge = %q, want \"← base\"", badge)
	}
}

func TestFastRolesUseFastModelWhenSet(t *testing.T) {
	cfg := profileTestConfig()
	p := cfg.AgentProfiles["coding"]
	p.Roles[routing.RoleFast] = routing.RoleBinding{Preset: "cheap"}
	cfg.AgentProfiles["coding"] = p
	s := newState(cfg)

	value, badge := displayOf(t, s, routing.RoleRouter)
	if value != "claude-haiku-4-5" || badge != "← fast" {
		t.Errorf("router got (%q, %q), want (claude-haiku-4-5, ← fast)", value, badge)
	}
	value, badge = displayOf(t, s, routing.RoleTester)
	if value != "claude-sonnet-4-5" || badge != "← base" {
		t.Errorf("tester got (%q, %q), want (claude-sonnet-4-5, ← base)", value, badge)
	}
}

func TestOverriddenRoleShowsOverrideBadge(t *testing.T) {
	cfg := profileTestConfig()
	p := cfg.AgentProfiles["coding"]
	p.Roles[routing.RoleReviewer] = routing.RoleBinding{Preset: "deep"}
	cfg.AgentProfiles["coding"] = p
	s := newState(cfg)

	value, badge := displayOf(t, s, routing.RoleReviewer)
	if value != "claude-opus-4-5" || badge != "● override" {
		t.Errorf("got (%q, %q), want (claude-opus-4-5, ● override)", value, badge)
	}
}

func TestDeleteRestoresInheritedDisplay(t *testing.T) {
	cfg := profileTestConfig()
	p := cfg.AgentProfiles["coding"]
	p.Roles[routing.RoleReviewer] = routing.RoleBinding{Preset: "deep"}
	cfg.AgentProfiles["coding"] = p
	s := newState(cfg)

	roleModelField(s, "coding", routing.RoleReviewer).Del()
	if _, ok := s.cfg.AgentProfiles["coding"].Roles[routing.RoleReviewer]; ok {
		t.Fatal("Del must remove the binding, not blank it")
	}
	value, badge := displayOf(t, s, routing.RoleReviewer)
	if value != "claude-sonnet-4-5" || badge != "← base" {
		t.Errorf("got (%q, %q), want the inherited (claude-sonnet-4-5, ← base)", value, badge)
	}
}

func TestGetStrReportsStoredNotInherited(t *testing.T) {
	s := newState(profileTestConfig())
	if got := roleModelField(s, "coding", routing.RoleTester).GetStr(); got != "" {
		t.Errorf("GetStr = %q, want empty — an inherited row must not read as modified", got)
	}
}

func TestBaseAndFastFieldIDsAreStable(t *testing.T) {
	s := newState(profileTestConfig())
	if got := baseModelField(s, "coding").ID; got != "profiles.coding.implementer" {
		t.Errorf("base field ID = %q, want profiles.coding.implementer", got)
	}
	if got := fastModelField(s, "coding").ID; got != "profiles.coding.fast" {
		t.Errorf("fast field ID = %q, want profiles.coding.fast", got)
	}
}

func TestImplementerNotDuplicatedInRoleList(t *testing.T) {
	s := newState(profileTestConfig())
	for _, f := range profileRoleFields(s, "coding") {
		if f.ID == "profiles.coding.implementer" {
			t.Fatal("implementer must appear only as Base model, not in the role list")
		}
	}
}

func discoveredConfig() config.Config {
	cfg := profileTestConfig()
	cfg.Providers = map[string]config.ProviderConfig{
		"anthropic": {BaseURL: "https://api.anthropic.com"},
	}
	cfg.Privacy.RemoteProvidersAllowed = true
	return cfg
}

func TestPresetForModelReusesExisting(t *testing.T) {
	s := newState(discoveredConfig())
	before := len(s.cfg.Models.Presets)
	name := presetForModel(s, "anthropic", "claude-sonnet-4-5", schema.ModelInfo{})
	if name != "base" {
		t.Errorf("name = %q, want the existing preset \"base\"", name)
	}
	if len(s.cfg.Models.Presets) != before {
		t.Error("matching an existing provider+model must not create a preset")
	}
}

func TestPresetForModelCreatesWithLimits(t *testing.T) {
	s := newState(discoveredConfig())
	name := presetForModel(s, "anthropic", "claude-3-7-sonnet", schema.ModelInfo{
		ID: "claude-3-7-sonnet", ContextWindow: 200000, MaxOutputTokens: 64000,
	})
	if name != "anthropic-claude-3-7-sonnet" {
		t.Fatalf("name = %q, want anthropic-claude-3-7-sonnet", name)
	}
	p, ok := s.cfg.Models.Presets[name]
	if !ok {
		t.Fatal("preset was not created")
	}
	if p.Provider != "anthropic" || p.Model != "claude-3-7-sonnet" {
		t.Errorf("preset = %+v, want anthropic/claude-3-7-sonnet", p)
	}
	if p.ContextWindow != 200000 || p.MaxOutputTokens != 64000 {
		t.Errorf("limits = (%d, %d), want (200000, 64000)", p.ContextWindow, p.MaxOutputTokens)
	}
}

func TestPresetForModelSlugifiesAndDedupes(t *testing.T) {
	s := newState(discoveredConfig())
	s.cfg.Models.Presets["ollama-qwen2-5-coder-7b"] = routing.ModelPreset{
		Name: "ollama-qwen2-5-coder-7b", Provider: "other", Model: "other",
	}
	name := presetForModel(s, "ollama", "qwen2.5-coder:7b", schema.ModelInfo{})
	if name != "ollama-qwen2-5-coder-7b-2" {
		t.Errorf("name = %q, want ollama-qwen2-5-coder-7b-2", name)
	}
}

func TestPresetForModelMarksLocalOnly(t *testing.T) {
	cfg := discoveredConfig()
	cfg.Providers["ollama"] = config.ProviderConfig{BaseURL: "http://localhost:11434"}
	s := newState(cfg)
	name := presetForModel(s, "ollama", "llama3.1:8b", schema.ModelInfo{})
	if !s.cfg.Models.Presets[name].LocalOnly {
		t.Error("a localhost provider must produce a local_only preset")
	}
}

func TestRolePickerListsDiscoveredModels(t *testing.T) {
	s := newState(discoveredConfig())
	s.discovered["anthropic"] = []schema.ModelInfo{
		{ID: "claude-opus-4-5", ContextWindow: 200000, MaxOutputTokens: 64000},
	}
	items := rolePickerItems(s, "coding", routing.RoleReviewer)

	var found bool
	for _, it := range items {
		if it.Value == "model:anthropic/claude-opus-4-5" {
			found = true
			if it.Label != "claude-opus-4-5" {
				t.Errorf("label = %q, want claude-opus-4-5", it.Label)
			}
		}
	}
	if !found {
		t.Fatalf("discovered model missing from picker items: %+v", items)
	}
}

func TestRolePickerPickingModelBindsNewPreset(t *testing.T) {
	s := newState(discoveredConfig())
	s.discovered["anthropic"] = []schema.ModelInfo{
		{ID: "claude-opus-4-5", ContextWindow: 200000, MaxOutputTokens: 64000},
	}
	if err := applyRolePick(s, "coding", routing.RoleReviewer, "model:anthropic/claude-opus-4-5"); err != nil {
		t.Fatalf("applyRolePick: %v", err)
	}
	bound := s.cfg.AgentProfiles["coding"].Roles[routing.RoleReviewer].Preset
	if bound != "deep" {
		t.Fatalf("bound preset = %q, want the existing \"deep\" (anthropic/claude-opus-4-5)", bound)
	}
}

func TestRolePickerRefreshQueuesProbe(t *testing.T) {
	s := newState(discoveredConfig())
	if err := applyRolePick(s, "coding", routing.RoleReviewer, refreshModelsValue); err != nil {
		t.Fatalf("applyRolePick: %v", err)
	}
	if s.takePendingCmd() == nil {
		t.Error("refresh must queue a probe command")
	}
}

func TestRolePickerModelIDWithSlash(t *testing.T) {
	cfg := discoveredConfig()
	cfg.Providers["groq"] = config.ProviderConfig{BaseURL: "https://api.groq.com"}
	s := newState(cfg)
	s.discovered["groq"] = []schema.ModelInfo{{ID: "moonshotai/kimi-k2-instruct"}}
	if err := applyRolePick(s, "coding", routing.RoleReviewer, "model:groq/moonshotai/kimi-k2-instruct"); err != nil {
		t.Fatalf("applyRolePick: %v", err)
	}
	name := s.cfg.AgentProfiles["coding"].Roles[routing.RoleReviewer].Preset
	if got := s.cfg.Models.Presets[name].Model; got != "moonshotai/kimi-k2-instruct" {
		t.Errorf("model = %q, want moonshotai/kimi-k2-instruct — the id's own slash must survive", got)
	}
}
