package settings

// sectionSpec maps a sidebar entry to its root frame builder.
type sectionSpec struct {
	id    string
	title string
	root  func(s *state) *frame
}

func sectionList() []sectionSpec {
	return []sectionSpec{
		{id: "agent", title: "Agent", root: agentFrame},
		{id: "providers", title: "Providers", root: providersFrame},
		{id: "presets", title: "Model Presets", root: presetsFrame},
		{id: "profiles", title: "Profiles", root: profilesFrame},
		{id: "privacy", title: "Privacy", root: privacyFrame},
		{id: "shell", title: "Shell", root: shellFrame},
		{id: "sandbox", title: "Sandbox", root: sandboxFrame},
		{id: "indexing", title: "Indexing", root: indexingFrame},
		{id: "web", title: "Web", root: webFrame},
		{id: "swarm", title: "Swarm", root: swarmFrame},
		{id: "sdd", title: "SDD", root: sddFrame},
		{id: "mcp", title: "MCP", root: mcpFrame},
		{id: "snapshots", title: "Snapshots", root: snapshotsFrame},
		{id: "hooks", title: "Hooks", root: hooksFrame},
		{id: "permissions", title: "Permissions", root: permissionsFrame},
		{id: "diagnostics", title: "Diagnostics", root: diagnosticsFrame},
		{id: "project", title: "Project", root: projectFrame},
		{id: "interface", title: "Interface", root: interfaceFrame},
	}
}
