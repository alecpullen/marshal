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
		"ollama/qwen3:8b":  {Name: "ollama/qwen3:8b", Provider: "ollama", Model: "qwen3:8b"},
		"ollama/qwen3:32b": {Name: "ollama/qwen3:32b", Provider: "ollama", Model: "qwen3:32b"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local": {Name: "local", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/qwen3:32b"},
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
	// Base model + Fast model + a roles header + every dispatchable role
	// except implementer (surfaced as Base model). The embedding preset
	// moved to the Indexing frame ([indexing] embedding_preset).
	wantRows := 3 + (len(routing.AllRoles) - 1)
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
	if implRow.GetStr() != "ollama/qwen3:32b" {
		t.Fatalf("implementer preset = %q, want 'ollama/qwen3:32b'", implRow.GetStr())
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
	if field.GetStr() != "ollama/qwen3:32b" {
		t.Fatalf("initial preset = %q, want 'ollama/qwen3:32b'", field.GetStr())
	}
	// Pick a different preset
	if err := field.PickOnPick("ollama/qwen3:8b"); err != nil {
		t.Fatalf("pickOnPick(ollama/qwen3:8b) = %v", err)
	}
	if field.GetStr() != "ollama/qwen3:8b" {
		t.Fatalf("after pick, preset = %q, want 'ollama/qwen3:8b'", field.GetStr())
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
		"anthropic/claude-sonnet-4-5": {Name: "anthropic/claude-sonnet-4-5", Provider: "anthropic", Model: "claude-sonnet-4-5"},
		"anthropic/claude-haiku-4-5":  {Name: "anthropic/claude-haiku-4-5", Provider: "anthropic", Model: "claude-haiku-4-5"},
		"anthropic/claude-opus-4-5":   {Name: "anthropic/claude-opus-4-5", Provider: "anthropic", Model: "claude-opus-4-5"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"coding": {Name: "coding", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "anthropic/claude-sonnet-4-5"},
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
	p.Roles[routing.RoleFast] = routing.RoleBinding{Preset: "anthropic/claude-haiku-4-5"}
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
	p.Roles[routing.RoleReviewer] = routing.RoleBinding{Preset: "anthropic/claude-opus-4-5"}
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
	p.Roles[routing.RoleReviewer] = routing.RoleBinding{Preset: "anthropic/claude-opus-4-5"}
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

func TestFindPresetForReusesExisting(t *testing.T) {
	s := newState(discoveredConfig())
	preset, ok := findPresetFor(s, "anthropic", "claude-sonnet-4-5")
	if !ok {
		t.Fatal("expected the existing \"anthropic/claude-sonnet-4-5\" preset to be found")
	}
	if preset.Name != "anthropic/claude-sonnet-4-5" {
		t.Errorf("name = %q, want the existing preset \"anthropic/claude-sonnet-4-5\"", preset.Name)
	}
	if len(s.cfg.Models.Presets) != 3 {
		t.Error("matching an existing provider+model must not create a preset")
	}
}

func TestFindPresetForMissing(t *testing.T) {
	s := newState(discoveredConfig())
	if _, ok := findPresetFor(s, "anthropic", "claude-3-7-sonnet"); ok {
		t.Fatal("findPresetFor must report false for a never-materialized model")
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

func TestApplyRolePickOnDiscoveredModelRequestsMaterializationInsteadOfCreatingAPreset(t *testing.T) {
	s := profilesTestState()
	s.discovered = map[string][]schema.ModelInfo{"ollama": {{ID: "qwen3-coder"}}}

	if err := applyRolePick(s, "local", routing.RoleImplementer, modelValuePrefix+"ollama/qwen3-coder"); err != nil {
		t.Fatalf("applyRolePick: %v", err)
	}
	// profilesTestState seeds two presets already — applying the pick must
	// not add a third.
	if len(s.cfg.Models.Presets) != 2 {
		t.Fatalf("expected no new preset to be created yet, got %d presets", len(s.cfg.Models.Presets))
	}
	pending := s.takePendingMaterialization()
	if pending == nil {
		t.Fatal("expected a pending materialization request")
	}
	if pending.Profile != "local" || pending.Role != routing.RoleImplementer ||
		pending.ProviderName != "ollama" || pending.ModelID != "qwen3-coder" {
		t.Errorf("pending = %+v, want Profile=local Role=implementer Provider=ollama Model=qwen3-coder", pending)
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

func TestRolePickerModelIDWithSlashRequestsMaterialization(t *testing.T) {
	cfg := discoveredConfig()
	cfg.Providers["groq"] = config.ProviderConfig{BaseURL: "https://api.groq.com"}
	s := newState(cfg)
	s.discovered["groq"] = []schema.ModelInfo{{ID: "moonshotai/kimi-k2-instruct"}}
	if err := applyRolePick(s, "coding", routing.RoleReviewer, "model:groq/moonshotai/kimi-k2-instruct"); err != nil {
		t.Fatalf("applyRolePick: %v", err)
	}
	// The pick must not bind a preset yet — it only records what to
	// materialize once the confirm screen completes. The id's own slash
	// must survive into the pending request.
	pending := s.takePendingMaterialization()
	if pending == nil {
		t.Fatal("expected a pending materialization request")
	}
	if pending.ProviderName != "groq" || pending.ModelID != "moonshotai/kimi-k2-instruct" {
		t.Errorf("pending = %+v, want Provider=groq Model=moonshotai/kimi-k2-instruct", pending)
	}
}
