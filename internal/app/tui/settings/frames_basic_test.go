package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

// kp is a single-rune key helper.
func kp(s string) tea.KeyPressMsg {
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func TestPrivacyFrameTogglesRemoteProviders(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(privacyFrame(s))
	ps.SetSize(60, 20)
	if ps.Top().List.CursorRow().Title != "Remote providers allowed" {
		t.Fatalf("first row should be Remote providers allowed, got %q", ps.Top().List.CursorRow().Title)
	}
	before := s.cfg.Privacy.RemoteProvidersAllowed
	ps.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.cfg.Privacy.RemoteProvidersAllowed == before {
		t.Fatal("space should toggle the working copy")
	}
}

func TestWebFrameDurationValidation(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(webFrame(s))
	ps.SetSize(60, 20)
	// move to "Fetch timeout" row
	for ps.Top().List.CursorRow().Title != "Fetch timeout" {
		ps.Update(kp("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ps.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}) // clear pre-populated value
	for _, r := range "45s" {
		ps.Update(kp(string(r)))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Web.FetchTimeout.String() != "45s" {
		t.Fatalf("expected 45s, got %v", s.cfg.Web.FetchTimeout)
	}
}

func TestSliceOpts(t *testing.T) {
	items := []string{"a", "b", "c"}
	opts := sliceOpts(&items)

	opts.MoveUp("1")
	if items[0] != "b" || items[1] != "a" {
		t.Fatalf("moveUp: %v", items)
	}
	opts.MoveDown("0")
	if items[0] != "a" || items[1] != "b" {
		t.Fatalf("moveDown: %v", items)
	}
	if got := opts.Yank("2"); got != "c" {
		t.Fatalf("yank: %v", got)
	}
	if got := opts.Yank("9"); got != nil {
		t.Fatalf("yank out of range: %v", got)
	}
	if err := opts.Paste("1", "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "x", "c"}
	if len(items) != 4 || items[2] != "x" {
		t.Fatalf("paste: %v, want %v", items, want)
	}
	if err := opts.Paste("0", 42); err == nil {
		t.Fatal("paste of wrong type should fail")
	}
}

func TestSDDSectionRegistered(t *testing.T) {
	registry := BuildRegistry(config.Default())
	for _, key := range []string{"sdd.auto_worktree", "sdd.max_fix_rounds", "sdd.plans_dir"} {
		if _, ok := registry.Lookup(key); !ok {
			t.Errorf("registry missing %s", key)
		}
	}
}

func TestUnsafePassthroughExposedAndRiskyFieldsFlagged(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(sandboxFrame(s))
	ps.SetSize(60, 20)

	// Navigate to the "Allow passthrough backend" row (last row)
	for ps.Top().List.CursorRow().Title != "Allow passthrough backend" {
		ps.Update(kp("j"))
	}
	row := ps.Top().List.CursorRow()
	if row == nil {
		t.Fatal("expected a row for unsafe_passthrough")
	}
	if row.ID != "sandbox.unsafe_passthrough" {
		t.Fatalf("expected id sandbox.unsafe_passthrough, got %q", row.ID)
	}
	if !row.Warn {
		t.Fatal("expected warn=true for unsafe_passthrough")
	}
	if row.Kind != kindToggle {
		t.Fatalf("expected kindToggle, got %v", row.Kind)
	}

	// Verify it toggles the working copy
	before := s.cfg.Tools.Shell.Sandbox.UnsafePassthrough
	ps.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.cfg.Tools.Shell.Sandbox.UnsafePassthrough == before {
		t.Fatal("space should toggle UnsafePassthrough")
	}

	// Verify the warning indicator appears in the view
	view := ps.Top().List.View()
	if !strings.Contains(view, "⚠") {
		t.Fatalf("expected warning indicator in view for risky field, got:\n%s", view)
	}
}

func TestProjectSectionOwnsNameLanguagesAndCommands(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(projectFrame(s))
	ps.SetSize(60, 20)

	// First row should be Project name
	if ps.Top().List.CursorRow().Title != "Project name" {
		t.Fatalf("first row should be Project name, got %q", ps.Top().List.CursorRow().Title)
	}

	// Navigate to Languages
	for ps.Top().List.CursorRow().Title != "Languages" {
		ps.Update(kp("j"))
	}
	if ps.Top().List.CursorRow().ID != "project.languages" {
		t.Fatalf("expected id project.languages, got %q", ps.Top().List.CursorRow().ID)
	}

	// Navigate to Test command
	for ps.Top().List.CursorRow().Title != "Test command" {
		ps.Update(kp("j"))
	}
	if ps.Top().List.CursorRow().ID != "commands.test" {
		t.Fatalf("expected id commands.test, got %q", ps.Top().List.CursorRow().ID)
	}

	// Navigate to Format command
	for ps.Top().List.CursorRow().Title != "Format command" {
		ps.Update(kp("j"))
	}
	if ps.Top().List.CursorRow().ID != "commands.format" {
		t.Fatalf("expected id commands.format, got %q", ps.Top().List.CursorRow().ID)
	}

	// Navigate to Vet command
	for ps.Top().List.CursorRow().Title != "Vet command" {
		ps.Update(kp("j"))
	}
	if ps.Top().List.CursorRow().ID != "commands.vet" {
		t.Fatalf("expected id commands.vet, got %q", ps.Top().List.CursorRow().ID)
	}

	// Verify setting ids never changed — /set commands.test etc. keeps working
	registry := BuildRegistry(config.Default())
	for _, key := range []string{"commands.test", "commands.format", "commands.vet", "project.name", "project.languages"} {
		if _, ok := registry.Lookup(key); !ok {
			t.Errorf("registry missing %s", key)
		}
	}
}

func TestEveryLeafFieldHasADescription(t *testing.T) {
	s := newState(config.Default())
	// Populate collections so drill sub-frames are exercised.
	if s.cfg.Providers == nil {
		s.cfg.Providers = map[string]config.ProviderConfig{}
	}
	s.cfg.Providers["test-provider"] = config.ProviderConfig{Type: "openai_compatible"}
	s.cfg.Models.Presets["test-preset"] = routing.ModelPreset{Name: "test-preset"}
	if s.cfg.AgentProfiles == nil {
		s.cfg.AgentProfiles = map[string]routing.AgentProfile{}
	}
	s.cfg.AgentProfiles["test-profile"] = routing.AgentProfile{Name: "test-profile", Roles: map[routing.AgentRole]routing.RoleBinding{}}
	s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries, config.HookConfig{Event: "pre_tool"})
	s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules, config.PermissionRule{Permission: "shell", Pattern: "*", Action: "confirm"})
	s.cfg.MCP.Servers["test-server"] = config.MCPServerConfig{Command: "test", Args: []string{"arg1"}, Env: map[string]string{"KEY": "val"}}

	seen := map[string]bool{}

	var checkFields func(fields []*field, path string)
	checkFields = func(fields []*field, path string) {
		for _, f := range fields {
			if f.Kind == kindDrill {
				if f.Build != nil && !seen[f.ID] {
					seen[f.ID] = true
					sub := f.Build()
					if sub != nil && sub.List != nil {
						checkFields(sub.List.Rows(), path+" > "+sub.Title)
					}
				}
				continue
			}
			if f.Desc == "" {
				t.Errorf("field %q (id=%s) at %s has empty desc", f.Title, f.ID, path)
			}
		}
	}

	for _, sec := range sectionList() {
		f := sec.Root(s)
		if f == nil || f.List == nil {
			continue
		}
		checkFields(f.List.Rows(), sec.Title)
	}
}

func fieldIDs(f *frame) []string {
	var ids []string
	for _, row := range f.List.Rows() {
		ids = append(ids, row.ID)
	}
	return ids
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func newTestSettingsState(t *testing.T) *state {
	t.Helper()
	return newState(config.Default())
}

func TestSDDFrameIncludesNewFields(t *testing.T) {
	s := newTestSettingsState(t)
	frame := sddFrame(s)
	ids := fieldIDs(frame)
	for _, want := range []string{"sdd.verify_timeout_ms", "sdd.verify.build", "sdd.verify.test", "sdd.cleanup_at_start", "sdd.max_total_tokens"} {
		if !contains(ids, want) {
			t.Errorf("sddFrame missing field %q; got %v", want, ids)
		}
	}
}

func TestDiagnosticsFrameIsMapAtRoot(t *testing.T) {
	s := newState(config.Default())
	s.cfg.Diagnostics.Commands = map[string]string{"lint": "go vet ./..."}
	ps := newPaneStack(diagnosticsFrame(s))
	ps.SetSize(60, 20)
	view := ps.Top().List.View()
	if !strings.Contains(view, "lint") {
		t.Fatalf("diagnostics root should list command keys directly, got:\n%s", view)
	}
}
