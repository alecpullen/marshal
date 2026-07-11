package settings

import "time"

func privacyFrame(s *state) *frame {
	return newFrame("Privacy", func() []*field {
		return []*field{
			{id: "privacy.remote_providers", title: "Remote providers allowed", kind: kindToggle,
				desc:    "allow remote providers globally",
				getBool: func() bool { return s.cfg.Privacy.RemoteProvidersAllowed },
				setBool: func(v bool) { s.cfg.Privacy.RemoteProvidersAllowed = v }},
			{id: "privacy.redact_secrets", title: "Redact secrets", kind: kindToggle,
				desc:    "scrub likely secrets from context sent to models",
				getBool: func() bool { return s.cfg.Privacy.RedactSecrets },
				setBool: func(v bool) { s.cfg.Privacy.RedactSecrets = v }},
			{id: "privacy.include_gitignored", title: "Include gitignored files", kind: kindToggle,
				desc:    "let indexing and context include gitignored paths",
				getBool: func() bool { return s.cfg.Privacy.IncludeGitignoredFiles },
				setBool: func(v bool) { s.cfg.Privacy.IncludeGitignoredFiles = v }},
		}
	})
}

func snapshotsFrame(s *state) *frame {
	return newFrame("Snapshots", func() []*field {
		return []*field{
			{id: "snapshots.enabled", title: "Enabled", kind: kindToggle,
				desc:    "capture before-write snapshots of changed files",
				getBool: func() bool { return s.cfg.Snapshots.Enabled },
				setBool: func(v bool) { s.cfg.Snapshots.Enabled = v }},
			intField2("snapshots.retention_days", "Retention days",
				func() int { return s.cfg.Snapshots.RetentionDays }, 0,
				func(v int) { s.cfg.Snapshots.RetentionDays = v }),
			intField2("snapshots.max_file_bytes", "Max file bytes",
				func() int { return s.cfg.Snapshots.MaxFileBytes }, 0,
				func(v int) { s.cfg.Snapshots.MaxFileBytes = v }),
		}
	})
}

func commandsFrame(s *state) *frame {
	return newFrame("Commands", func() []*field {
		return []*field{
			scalarField("commands.test", "Test command",
				func() string { return s.cfg.Commands.Test },
				func(v string) error { s.cfg.Commands.Test = v; return nil }),
			scalarField("commands.format", "Format command",
				func() string { return s.cfg.Commands.Format },
				func(v string) error { s.cfg.Commands.Format = v; return nil }),
			scalarField("commands.vet", "Vet command",
				func() string { return s.cfg.Commands.Vet },
				func(v string) error { s.cfg.Commands.Vet = v; return nil }),
			scalarField("project.name", "Project name",
				func() string { return s.cfg.Project.Name },
				func(v string) error { s.cfg.Project.Name = v; return nil }),
			listDrill("project.languages", "Languages", &s.cfg.Project.Languages),
		}
	})
}

func indexingFrame(s *state) *frame {
	return newFrame("Indexing", func() []*field {
		return []*field{
			{id: "indexing.treesitter", title: "Use treesitter", kind: kindToggle,
				getBool: func() bool { return s.cfg.Indexing.UseTreesitter },
				setBool: func(v bool) { s.cfg.Indexing.UseTreesitter = v }},
			{id: "indexing.embeddings", title: "Use embeddings", kind: kindToggle,
				getBool: func() bool { return s.cfg.Indexing.UseEmbeddings },
				setBool: func(v bool) { s.cfg.Indexing.UseEmbeddings = v }},
			{id: "indexing.summarise", title: "Summarise files", kind: kindToggle,
				getBool: func() bool { return s.cfg.Indexing.SummariseFiles },
				setBool: func(v bool) { s.cfg.Indexing.SummariseFiles = v }},
			listDrill("indexing.ignore", "Ignore patterns", &s.cfg.Indexing.Ignore),
		}
	})
}

func webFrame(s *state) *frame {
	return newFrame("Web", func() []*field {
		return []*field{
			{id: "web.enabled", title: "Enabled", kind: kindToggle,
				desc:    "allow web.fetch / web.search tools",
				getBool: func() bool { return s.cfg.Web.Enabled },
				setBool: func(v bool) { s.cfg.Web.Enabled = v }},
			scalarField("web.fetch_timeout", "Fetch timeout",
				func() string { return s.cfg.Web.FetchTimeout.String() },
				durationSetter(func(d time.Duration) { s.cfg.Web.FetchTimeout = d })),
			scalarField("web.search_provider", "Search provider",
				func() string { return s.cfg.Web.SearchProvider },
				func(v string) error { s.cfg.Web.SearchProvider = v; return nil }),
			scalarField("web.search_url", "Search URL",
				func() string { return s.cfg.Web.SearchURL },
				func(v string) error { s.cfg.Web.SearchURL = v; return nil }),
			secretRow("web.search_key", "Search key",
				func() string { return s.cfg.Web.SearchKey },
				func(v string) { s.cfg.Web.SearchKey = v }),
		}
	})
}

func swarmFrame(s *state) *frame {
	return newFrame("Swarm", func() []*field {
		return []*field{
			intField2("swarm.max_fix_rounds", "Max fix rounds",
				func() int { return s.cfg.Swarm.Budget.MaxFixRounds }, 0,
				func(v int) { s.cfg.Swarm.Budget.MaxFixRounds = v }),
			intField2("swarm.max_total_tokens", "Max total tokens",
				func() int { return s.cfg.Swarm.Budget.MaxTotalTokens }, 0,
				func(v int) { s.cfg.Swarm.Budget.MaxTotalTokens = v }),
			mapIntDrill("swarm.tool_iters", "Tool iters", &s.cfg.Swarm.Budget.ToolIters),
		}
	})
}

// diagnosticsFrame is nothing but the commands map, so the root frame IS the
// map frame (no pointless single-row drill).
func diagnosticsFrame(s *state) *frame {
	drill := mapStringDrill("diagnostics.commands", "Commands", &s.cfg.Diagnostics.Commands)
	f := drill.build()
	f.title = "Diagnostics"
	return f
}
