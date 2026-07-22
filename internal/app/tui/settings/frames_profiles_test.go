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
		"local": {Name: "local", Roles: map[routing.AgentRole]string{
			routing.RoleImplementer: "large",
		}},
	}
	return newState(cfg)
}

func TestProfilesFrameListsProfiles(t *testing.T) {
	s := profilesTestState()
	f := profilesFrame(s)
	rows := f.list.Rows()
	if len(rows) == 0 {
		t.Fatal("profilesFrame should have at least one row")
	}
	var localRow *field
	for _, r := range rows {
		if r.id == "profiles.local" {
			localRow = r
			break
		}
	}
	if localRow == nil {
		t.Fatal("profilesFrame should have a row with id profiles.local")
	}
	if !strings.Contains(localRow.title, "1 role") {
		t.Fatalf("local row title = %q, want to contain '1 role'", localRow.title)
	}
}

func TestProfileEntryHasOneRowPerRole(t *testing.T) {
	s := profilesTestState()
	f := profilesFrame(s)
	rows := f.list.Rows()
	var localRow *field
	for _, r := range rows {
		if r.id == "profiles.local" {
			localRow = r
			break
		}
	}
	if localRow == nil {
		t.Fatal("no profiles.local row")
	}
	detail := localRow.build()
	detailRows := detail.list.Rows()
	if len(detailRows) != len(routing.AllRoles) {
		t.Fatalf("profile detail should have %d rows (one per role), got %d", len(routing.AllRoles), len(detailRows))
	}
	firstRole := routing.AllRoles[0]
	expectedID := "profiles.local." + string(firstRole)
	if detailRows[0].id != expectedID {
		t.Fatalf("first row id = %q, want %q", detailRows[0].id, expectedID)
	}
	// Find the implementer row — it should show the assigned preset
	var implRow *field
	for _, r := range detailRows {
		if strings.HasSuffix(r.id, "."+string(routing.RoleImplementer)) {
			implRow = r
			break
		}
	}
	if implRow == nil {
		t.Fatal("no implementer row in profile detail")
	}
	if implRow.getStr() != "large" {
		t.Fatalf("implementer preset = %q, want 'large'", implRow.getStr())
	}
	if !strings.Contains(implRow.desc, "ollama/qwen3:32b") {
		t.Fatalf("implementer desc = %q, want to contain 'ollama/qwen3:32b'", implRow.desc)
	}
}

func TestProfileRolePickAssignsAndClears(t *testing.T) {
	s := profilesTestState()
	// Get the implementer role field for the "local" profile
	field := rolePresetField(s, "local", routing.RoleImplementer)
	if field.getStr() != "large" {
		t.Fatalf("initial preset = %q, want 'large'", field.getStr())
	}
	// Pick a different preset
	if err := field.pickOnPick("small"); err != nil {
		t.Fatalf("pickOnPick(small) = %v", err)
	}
	if field.getStr() != "small" {
		t.Fatalf("after pick, preset = %q, want 'small'", field.getStr())
	}
	// Clear via unset sentinel
	if err := field.pickOnPick(unsetRoleValue); err != nil {
		t.Fatalf("pickOnPick(unset) = %v", err)
	}
	if field.getStr() != "" {
		t.Fatalf("after unset, preset = %q, want empty", field.getStr())
	}
	// Pick an unknown preset should error
	if err := field.pickOnPick("nonexistent"); err == nil {
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
	ps.top().list.keyInput.SetValue("fast")
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
	ps.top().list.keyInput.SetValue("fast")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ps.top().list.errMsg == "" {
		t.Fatal("duplicate add should set errMsg")
	}
}
