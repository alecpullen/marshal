package settings

// sectionSpec maps a sidebar entry to its root frame builder.
type sectionSpec struct {
	ID       string
	Title    string
	Root     func(s *state) *frame
	advanced bool // sections hidden from unfiltered view, reachable via search
}

func sectionList() []sectionSpec {
	return []sectionSpec{
		{ID: "agent", Title: "Agent", Root: agentFrame},
		{ID: "providers", Title: "Providers", Root: providersFrame},
		{ID: "presets", Title: "Model Presets", Root: presetsFrame},
		{ID: "profiles", Title: "Profiles", Root: profilesFrame, advanced: true},
		{ID: "custom_agents", Title: "Custom Agents", Root: customAgentsFrame},
		{ID: "privacy", Title: "Privacy", Root: privacyFrame},
		{ID: "shell", Title: "Shell", Root: shellFrame},
		{ID: "sandbox", Title: "Sandbox", Root: sandboxFrame},
		{ID: "indexing", Title: "Indexing", Root: indexingFrame},
		{ID: "web", Title: "Web", Root: webFrame},
		{ID: "swarm", Title: "Swarm", Root: swarmFrame},
		{ID: "sdd", Title: "SDD", Root: sddFrame},
		{ID: "skills", Title: "Skills", Root: skillsFrame},
		{ID: "mcp", Title: "MCP", Root: mcpFrame},
		{ID: "snapshots", Title: "Snapshots", Root: snapshotsFrame},
		{ID: "hooks", Title: "Hooks", Root: hooksFrame},
		{ID: "permissions", Title: "Permissions", Root: permissionsFrame},
		{ID: "diagnostics", Title: "Diagnostics", Root: diagnosticsFrame},
		{ID: "project", Title: "Project", Root: projectFrame},
		{ID: "interface", Title: "Interface", Root: interfaceFrame},
	}
}
