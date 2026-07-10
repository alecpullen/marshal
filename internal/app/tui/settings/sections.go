package settings

type section struct {
	id    string
	title string
	build func(s *state) sectionPane
}

// sectionList is the ordered sidebar registry. Later tasks replace the
// staticPane factories with real editors section by section.
func sectionList() []section {
	placeholder := func(s *state) sectionPane {
		return &staticPane{text: "Editor coming soon — edit .marshal/config.toml directly."}
	}
	return []section{
		{id: "agent", title: "Agent", build: newAgentPane},
		{id: "providers", title: "Providers", build: newProvidersPane},
		{id: "presets", title: "Model Presets", build: placeholder},
		{id: "privacy", title: "Privacy", build: newPrivacyPane},
		{id: "shell", title: "Shell", build: newShellPane},
		{id: "sandbox", title: "Sandbox", build: newSandboxPane},
		{id: "indexing", title: "Indexing", build: newIndexingPane},
		{id: "web", title: "Web", build: newWebPane},
		{id: "swarm", title: "Swarm", build: placeholder},
		{id: "mcp", title: "MCP", build: placeholder},
		{id: "snapshots", title: "Snapshots", build: newSnapshotsPane},
		{id: "hooks", title: "Hooks", build: placeholder},
		{id: "permissions", title: "Permissions", build: placeholder},
		{id: "diagnostics", title: "Diagnostics", build: placeholder},
		{id: "commands", title: "Commands", build: newCommandsPane},
	}
}
