package plugins

import (
	"path/filepath"
	"testing"
)

func TestScanPluginFullBundle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.toml"), "name = \"widgets\"\ndescription = \"Widget tools\"\n")
	writePluginSkill(t, dir, "alpha", testSkillMD)
	writePluginCommand(t, dir, "review.md", testCommandMD)
	writeFile(t, filepath.Join(dir, "hooks.toml"), testHooksTOML)
	writeFile(t, filepath.Join(dir, "mcp.toml"), testMCPTOML)

	c, err := ScanPlugin(dir)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	if !c.HasManifest || c.Manifest.Name != "widgets" {
		t.Fatalf("manifest = %+v", c.Manifest)
	}
	if len(c.Skills) != 1 || c.Skills[0].Name != "alpha" {
		t.Fatalf("Skills = %v", c.Skills)
	}
	if len(c.Commands) != 1 || c.Commands[0].Name != "review" {
		t.Fatalf("Commands = %v", c.Commands)
	}
	if len(c.Hooks) != 2 {
		t.Fatalf("Hooks = %v", c.Hooks)
	}
	if len(c.MCPServers) != 1 || len(c.MCPPolicies) != 2 {
		t.Fatalf("MCP = %v / %v", c.MCPServers, c.MCPPolicies)
	}
	if c.Empty() {
		t.Fatal("full bundle must not be Empty")
	}
}

func TestScanPluginSkillsOnlyStillValid(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)

	c, err := ScanPlugin(dir)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	if len(c.Skills) != 1 || c.HasManifest {
		t.Fatalf("Contents = %+v", c)
	}
}

func TestScanPluginCommandsOnlyValid(t *testing.T) {
	dir := t.TempDir()
	writePluginCommand(t, dir, "review.md", testCommandMD)

	c, err := ScanPlugin(dir)
	if err != nil {
		t.Fatalf("a commands-only plugin should be valid: %v", err)
	}
	if len(c.Commands) != 1 {
		t.Fatalf("Commands = %v", c.Commands)
	}
}

func TestScanPluginEmptyPluginRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# nothing marshal understands\n")

	if _, err := ScanPlugin(dir); err == nil {
		t.Fatal("a plugin with no loadable content should error")
	}
}

func TestScanPluginMalformedHooksInvalidatesPlugin(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)
	writeFile(t, filepath.Join(dir, "hooks.toml"), "{{{ not toml")

	if _, err := ScanPlugin(dir); err == nil {
		t.Fatal("malformed hooks.toml should invalidate the whole plugin")
	}
}

func TestScanPluginPoliciesOnlyRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mcp.toml"), "[mcp.policies]\n\"mcp.docs.search\" = \"allow\"\n")

	if _, err := ScanPlugin(dir); err == nil {
		t.Fatal("a plugin with only MCP policies should error: policies are invisible executable content without a server")
	}
}
