package plugins

import (
	"fmt"

	"marshal/internal/app/config"
	"marshal/internal/skills"
)

// Contents is everything a plugin contributes, discovered by convention:
// skills/<name>/SKILL.md bundles, commands/*.md slash commands,
// hooks.toml hook entries, mcp.toml server/policy config, and optional
// plugin.toml metadata.
type Contents struct {
	Manifest    Manifest
	HasManifest bool
	Skills      []skills.Skill
	Commands    []CommandSpec
	Hooks       []config.HookConfig
	MCPServers  map[string]config.MCPServerConfig
	MCPPolicies map[string]string
}

// Empty reports whether the plugin contributes anything at all.
func (c Contents) Empty() bool {
	return len(c.Skills) == 0 && len(c.Commands) == 0 && len(c.Hooks) == 0 && len(c.MCPServers) == 0 && len(c.MCPPolicies) == 0
}

// ScanPlugin discovers all loadable content in the plugin directory at
// dir. A plugin is valid when it contributes at least one item; content
// types are independent, so a commands-only or hooks-only plugin is fine.
// Malformed plugin.toml, hooks.toml, or mcp.toml files invalidate the
// whole plugin (the author intended content marshal cannot understand);
// malformed individual skills and commands are skipped.
func ScanPlugin(dir string) (Contents, error) {
	var c Contents

	manifest, ok, err := ParseManifest(dir)
	if err != nil {
		return Contents{}, err
	}
	c.Manifest, c.HasManifest = manifest, ok

	if c.Skills, err = scanSkills(dir); err != nil {
		return Contents{}, err
	}
	if c.Commands, err = parseCommandSpecs(dir); err != nil {
		return Contents{}, err
	}
	if c.Hooks, err = parseHooksFile(dir); err != nil {
		return Contents{}, err
	}
	if c.MCPServers, c.MCPPolicies, err = parseMCPFile(dir); err != nil {
		return Contents{}, err
	}

	if c.Empty() {
		return Contents{}, fmt.Errorf("%s contains no loadable plugin content (skills/, commands/*.md, hooks.toml, mcp.toml)", dir)
	}
	return c, nil
}
