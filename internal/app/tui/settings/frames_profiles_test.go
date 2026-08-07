package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
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
