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
	// AllRoles + the embedding role, which is not in AllRoles but must
	// still be settable from /settings.
	wantRows := len(routing.AllRoles) + 1
	if len(detailRows) != wantRows {
		t.Fatalf("profile detail should have %d rows (one per role), got %d", wantRows, len(detailRows))
	}
	if last := detailRows[len(detailRows)-1].ID; last != "profiles.local.embedding" {
		t.Fatalf("last row id = %q, want profiles.local.embedding", last)
	}
	firstRole := routing.AllRoles[0]
	expectedID := "profiles.local." + string(firstRole)
	if detailRows[0].ID != expectedID {
		t.Fatalf("first row id = %q, want %q", detailRows[0].ID, expectedID)
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
	if !strings.Contains(implRow.Desc, "ollama/qwen3:32b") {
		t.Fatalf("implementer desc = %q, want to contain 'ollama/qwen3:32b'", implRow.Desc)
	}
}

func TestProfileRolePickAssignsAndClears(t *testing.T) {
	s := profilesTestState()
	// Get the implementer role field for the "local" profile
	field := rolePresetField(s, "local", routing.RoleImplementer)
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
