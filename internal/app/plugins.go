package app

import (
	"log/slog"

	"marshal/internal/app/config"
	"marshal/internal/commands"
	"marshal/internal/plugins"
	"marshal/internal/skills"
)

// loadPluginContents loads and verifies the global and (when the project
// is trusted) project plugin stores, applies their contents to the
// startup config and skill index, and returns the plugin slash commands
// for Run() to register. Plugin problems never abort startup — LoadStore
// already warns and skips; an unreadable store only logs here.
func loadPluginContents(cfg *config.Config, idx *skills.Index, homeDir, workingDir string, projectTrusted bool, logger *slog.Logger) []commands.Command {
	var loaded []plugins.LoadedPlugin
	global, err := plugins.LoadStore(plugins.GlobalStoreDir(homeDir), plugins.GlobalLockPath(homeDir), logger)
	if err != nil {
		logger.Warn("failed to load global plugins", "error", err)
	}
	loaded = append(loaded, global...)
	if projectTrusted {
		project, err := plugins.LoadStore(plugins.ProjectStoreDir(workingDir), plugins.ProjectLockPath(workingDir), logger)
		if err != nil {
			logger.Warn("failed to load project plugins", "error", err)
		}
		loaded = append(loaded, project...)
	}
	return applyPluginContents(cfg, idx, loaded, logger)
}

// applyPluginContents merges verified plugin contents into the startup
// config and skill index, and returns the slash commands to register.
// Precedence is config > project plugin > global plugin. loaded is
// ordered global-first, so skills and hooks iterate forward (project wins
// skill name collisions by overwrite; hooks append config, then global,
// then project) while commands and MCP entries iterate in reverse with
// first-wins — project plugins win among plugins, and keys already in
// config (servers, policies) are never overridden.
func applyPluginContents(cfg *config.Config, idx *skills.Index, loaded []plugins.LoadedPlugin, logger *slog.Logger) []commands.Command {
	for _, p := range loaded {
		for _, skill := range p.Contents.Skills {
			idx.Set(skill.Name, skill)
		}
		cfg.Hooks.Entries = append(cfg.Hooks.Entries, p.Contents.Hooks...)
	}

	var cmds []commands.Command
	seenCommands := map[string]bool{}
	for i := len(loaded) - 1; i >= 0; i-- {
		p := loaded[i]
		for _, spec := range p.Contents.Commands {
			if seenCommands[spec.Name] {
				logger.Warn("skipping duplicate plugin command", "command", spec.Name, "plugin", p.Name)
				continue
			}
			seenCommands[spec.Name] = true
			cmds = append(cmds, commands.Command{
				Name:        spec.Name,
				Description: spec.Description,
				Group:       "plugins",
				PromptBody:  spec.Body,
			})
		}
		for name, srv := range p.Contents.MCPServers {
			if cfg.MCP.Servers == nil {
				cfg.MCP.Servers = map[string]config.MCPServerConfig{}
			}
			if _, exists := cfg.MCP.Servers[name]; exists {
				logger.Warn("skipping plugin MCP server: name already configured", "server", name, "plugin", p.Name)
				continue
			}
			cfg.MCP.Servers[name] = srv
		}
		for tool, policy := range p.Contents.MCPPolicies {
			if cfg.MCP.Policies == nil {
				cfg.MCP.Policies = map[string]string{}
			}
			if _, exists := cfg.MCP.Policies[tool]; exists {
				logger.Warn("skipping plugin MCP policy: already configured", "tool", tool, "plugin", p.Name)
				continue
			}
			cfg.MCP.Policies[tool] = policy
		}
	}
	return cmds
}
