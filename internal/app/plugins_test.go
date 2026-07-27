package app

import (
	"log/slog"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/commands"
	"marshal/internal/plugins"
	"marshal/internal/skills"
)

func loadedPlugin(name string, c plugins.Contents) plugins.LoadedPlugin {
	return plugins.LoadedPlugin{Name: name, Contents: c}
}

func TestApplyPluginContentsSkillsAndHooks(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.Entries = []config.HookConfig{{Event: "turn_end", Command: "./config-hook.sh"}}
	idx := skills.NewIndex()
	// loaded is ordered global-first, project-last, as the runtime loads.
	loaded := []plugins.LoadedPlugin{
		loadedPlugin("global-plug", plugins.Contents{
			Skills: []skills.Skill{{Name: "shared", Description: "global"}},
			Hooks:  []config.HookConfig{{Event: "pre_tool_use", Command: "./global.sh"}},
		}),
		loadedPlugin("project-plug", plugins.Contents{
			Skills: []skills.Skill{{Name: "shared", Description: "project"}},
			Hooks:  []config.HookConfig{{Event: "pre_tool_use", Command: "./project.sh"}},
		}),
	}

	cmds := applyPluginContents(&cfg, idx, loaded, slog.Default())
	if len(cmds) != 0 {
		t.Fatalf("cmds = %v, want none", cmds)
	}
	skill, ok := idx.Load("shared")
	if !ok || skill.Description != "project" {
		t.Fatalf("project plugin should win skill collision, got %+v", skill)
	}
	if len(cfg.Hooks.Entries) != 3 {
		t.Fatalf("hooks = %+v", cfg.Hooks.Entries)
	}
	if cfg.Hooks.Entries[0].Command != "./config-hook.sh" ||
		cfg.Hooks.Entries[1].Command != "./global.sh" ||
		cfg.Hooks.Entries[2].Command != "./project.sh" {
		t.Fatalf("hook order (config, global, project) wrong: %+v", cfg.Hooks.Entries)
	}
}

func TestApplyPluginContentsMCPPrecedence(t *testing.T) {
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"shared": {Command: "node", Args: []string{"config.js"}},
	}
	idx := skills.NewIndex()
	loaded := []plugins.LoadedPlugin{
		loadedPlugin("global-plug", plugins.Contents{
			MCPServers: map[string]config.MCPServerConfig{
				"shared":      {Command: "npx", Args: []string{"global.js"}},
				"global-only": {Command: "npx"},
			},
			MCPPolicies: map[string]string{"mcp.shared.read": "deny"},
		}),
		loadedPlugin("project-plug", plugins.Contents{
			MCPServers: map[string]config.MCPServerConfig{
				"shared":       {Command: "npx", Args: []string{"project.js"}},
				"project-only": {Command: "node"},
			},
			MCPPolicies: map[string]string{"mcp.shared.read": "allow"},
		}),
	}

	applyPluginContents(&cfg, idx, loaded, slog.Default())

	if got := cfg.MCP.Servers["shared"].Args; len(got) != 1 || got[0] != "config.js" {
		t.Fatalf("hand-written config server must win: %+v", cfg.MCP.Servers["shared"])
	}
	if _, ok := cfg.MCP.Servers["project-only"]; !ok {
		t.Fatal("project-only server missing")
	}
	if _, ok := cfg.MCP.Servers["global-only"]; !ok {
		t.Fatal("global-only server missing")
	}
	if cfg.MCP.Policies["mcp.shared.read"] != "allow" {
		t.Fatalf("project plugin policy should win among plugins: %v", cfg.MCP.Policies)
	}
}

func TestApplyPluginContentsCommands(t *testing.T) {
	cfg := config.Default()
	idx := skills.NewIndex()
	loaded := []plugins.LoadedPlugin{
		loadedPlugin("global-plug", plugins.Contents{
			Commands: []plugins.CommandSpec{
				{Name: "review", Description: "global review", Body: "Global body."},
				{Name: "lint", Description: "global lint", Body: "Lint body."},
			},
		}),
		loadedPlugin("project-plug", plugins.Contents{
			Commands: []plugins.CommandSpec{
				{Name: "review", Description: "project review", Body: "Project body."},
			},
		}),
	}

	cmds := applyPluginContents(&cfg, idx, loaded, slog.Default())
	if len(cmds) != 2 {
		t.Fatalf("cmds = %+v, want 2 (duplicate review skipped)", cmds)
	}
	byName := map[string]commands.Command{}
	for _, c := range cmds {
		byName[c.Name] = c
	}
	review := byName["review"]
	if review.Description != "project review" || review.PromptBody != "Project body." {
		t.Fatalf("project plugin should win command collision: %+v", review)
	}
	if review.Group != "plugins" {
		t.Fatalf("Group = %q, want plugins", review.Group)
	}
	if review.Handler != nil {
		t.Fatal("plugin commands carry no Go handler")
	}
	if _, ok := byName["lint"]; !ok {
		t.Fatal("lint command missing")
	}
}
