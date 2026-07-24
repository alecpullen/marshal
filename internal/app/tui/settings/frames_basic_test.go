package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestPrivacyFrameTogglesRemoteProviders(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(privacyFrame(s))
	ps.SetSize(60, 20)
	if ps.top().list.CursorRow().title != "Remote providers allowed" {
		t.Fatalf("first row should be Remote providers allowed, got %q", ps.top().list.CursorRow().title)
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
	for ps.top().list.CursorRow().title != "Fetch timeout" {
		ps.Update(kp("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ps.top().list.input.SetValue("45s")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Web.FetchTimeout.String() != "45s" {
		t.Fatalf("expected 45s, got %v", s.cfg.Web.FetchTimeout)
	}
}

func TestSliceOpts(t *testing.T) {
	items := []string{"a", "b", "c"}
	opts := sliceOpts(&items)

	opts.moveUp("1")
	if items[0] != "b" || items[1] != "a" {
		t.Fatalf("moveUp: %v", items)
	}
	opts.moveDown("0")
	if items[0] != "a" || items[1] != "b" {
		t.Fatalf("moveDown: %v", items)
	}
	if got := opts.yank("2"); got != "c" {
		t.Fatalf("yank: %v", got)
	}
	if got := opts.yank("9"); got != nil {
		t.Fatalf("yank out of range: %v", got)
	}
	if err := opts.paste("1", "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "x", "c"}
	if len(items) != 4 || items[2] != "x" {
		t.Fatalf("paste: %v, want %v", items, want)
	}
	if err := opts.paste("0", 42); err == nil {
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
	for ps.top().list.CursorRow().title != "Allow passthrough backend" {
		ps.Update(kp("j"))
	}
	row := ps.top().list.CursorRow()
	if row == nil {
		t.Fatal("expected a row for unsafe_passthrough")
	}
	if row.id != "sandbox.unsafe_passthrough" {
		t.Fatalf("expected id sandbox.unsafe_passthrough, got %q", row.id)
	}
	if !row.warn {
		t.Fatal("expected warn=true for unsafe_passthrough")
	}
	if row.kind != kindToggle {
		t.Fatalf("expected kindToggle, got %v", row.kind)
	}

	// Verify it toggles the working copy
	before := s.cfg.Tools.Shell.Sandbox.UnsafePassthrough
	ps.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.cfg.Tools.Shell.Sandbox.UnsafePassthrough == before {
		t.Fatal("space should toggle UnsafePassthrough")
	}

	// Verify the warning indicator appears in the view
	view := ps.top().list.View()
	if !strings.Contains(view, "⚠") {
		t.Fatalf("expected warning indicator in view for risky field, got:\n%s", view)
	}
}

func TestProjectSectionOwnsNameLanguagesAndCommands(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(projectFrame(s))
	ps.SetSize(60, 20)

	// First row should be Project name
	if ps.top().list.CursorRow().title != "Project name" {
		t.Fatalf("first row should be Project name, got %q", ps.top().list.CursorRow().title)
	}

	// Navigate to Languages
	for ps.top().list.CursorRow().title != "Languages" {
		ps.Update(kp("j"))
	}
	if ps.top().list.CursorRow().id != "project.languages" {
		t.Fatalf("expected id project.languages, got %q", ps.top().list.CursorRow().id)
	}

	// Navigate to Test command
	for ps.top().list.CursorRow().title != "Test command" {
		ps.Update(kp("j"))
	}
	if ps.top().list.CursorRow().id != "commands.test" {
		t.Fatalf("expected id commands.test, got %q", ps.top().list.CursorRow().id)
	}

	// Navigate to Format command
	for ps.top().list.CursorRow().title != "Format command" {
		ps.Update(kp("j"))
	}
	if ps.top().list.CursorRow().id != "commands.format" {
		t.Fatalf("expected id commands.format, got %q", ps.top().list.CursorRow().id)
	}

	// Navigate to Vet command
	for ps.top().list.CursorRow().title != "Vet command" {
		ps.Update(kp("j"))
	}
	if ps.top().list.CursorRow().id != "commands.vet" {
		t.Fatalf("expected id commands.vet, got %q", ps.top().list.CursorRow().id)
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
			if f.kind == kindDrill {
				if f.build != nil && !seen[f.id] {
					seen[f.id] = true
					sub := f.build()
					if sub != nil && sub.list != nil {
						checkFields(sub.list.Rows(), path+" > "+sub.title)
					}
				}
				continue
			}
			if f.desc == "" {
				t.Errorf("field %q (id=%s) at %s has empty desc", f.title, f.id, path)
			}
		}
	}

	for _, sec := range sectionList() {
		f := sec.root(s)
		if f == nil || f.list == nil {
			continue
		}
		checkFields(f.list.Rows(), sec.title)
	}
}

func TestDiagnosticsFrameIsMapAtRoot(t *testing.T) {
	s := newState(config.Default())
	s.cfg.Diagnostics.Commands = map[string]string{"lint": "go vet ./..."}
	ps := newPaneStack(diagnosticsFrame(s))
	ps.SetSize(60, 20)
	view := ps.top().list.View()
	if !strings.Contains(view, "lint") {
		t.Fatalf("diagnostics root should list command keys directly, got:\n%s", view)
	}
}
