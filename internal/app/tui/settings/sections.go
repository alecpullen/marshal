package settings

type section struct {
	id    string
	title string
	build func(s *state) sectionPane
}

// sectionList is the ordered sidebar registry. Each entry maps the
// sidebar id to a factory that builds the section's pane against the
// shared working-copy state.
func sectionList() []section {
	return []section{
		{id: "agent", title: "Agent", build: newAgentPane},
		{id: "providers", title: "Providers", build: newProvidersPane},
		{id: "presets", title: "Model Presets", build: newPresetsPane},
		{id: "privacy", title: "Privacy", build: newPrivacyPane},
		{id: "shell", title: "Shell", build: newShellPane},
		{id: "sandbox", title: "Sandbox", build: newSandboxPane},
		{id: "indexing", title: "Indexing", build: newIndexingPane},
		{id: "web", title: "Web", build: newWebPane},
		{id: "swarm", title: "Swarm", build: newSwarmPane},
		{id: "mcp", title: "MCP", build: newMCPPane},
		{id: "snapshots", title: "Snapshots", build: newSnapshotsPane},
		{id: "hooks", title: "Hooks", build: newHooksPane},
		{id: "permissions", title: "Permissions", build: newPermissionsPane},
		{id: "diagnostics", title: "Diagnostics", build: newDiagnosticsPane},
		{id: "commands", title: "Commands", build: newCommandsPane},
	}
}
