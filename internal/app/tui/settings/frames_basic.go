package settings

import (
	"strconv"
	"time"
)

func privacyFrame(s *state) *frame {
	return newFrame("Privacy", func() []*field {
		return []*field{
			{id: "privacy.remote_providers", title: "Remote providers allowed", kind: kindToggle,
				tomlPath: "privacy.remote_providers_allowed",
				desc:     "allow remote providers globally",
				getBool:  func() bool { return s.cfg.Privacy.RemoteProvidersAllowed },
				setBool:  func(v bool) { s.cfg.Privacy.RemoteProvidersAllowed = v }},
			{id: "privacy.redact_secrets", title: "Redact secrets", kind: kindToggle,
				tomlPath: "privacy.redact_secrets",
				desc:     "scrub likely secrets from context sent to models",
				getBool:  func() bool { return s.cfg.Privacy.RedactSecrets },
				setBool:  func(v bool) { s.cfg.Privacy.RedactSecrets = v }},
			{id: "privacy.include_gitignored", title: "Include gitignored files", kind: kindToggle,
				tomlPath: "privacy.include_gitignored_files",
				desc:     "let indexing and context include gitignored paths",
				getBool:  func() bool { return s.cfg.Privacy.IncludeGitignoredFiles },
				setBool:  func(v bool) { s.cfg.Privacy.IncludeGitignoredFiles = v }},
		}
	})
}

func snapshotsFrame(s *state) *frame {
	return newFrame("Snapshots", func() []*field {
		return []*field{
			{id: "snapshots.enabled", title: "Enabled", kind: kindToggle,
				tomlPath: "snapshots.enabled",
				desc:     "capture before-write snapshots of changed files",
				getBool:  func() bool { return s.cfg.Snapshots.Enabled },
				setBool:  func(v bool) { s.cfg.Snapshots.Enabled = v }},
			func() *field {
				f := intField("snapshots.retention_days", "Retention days",
					func() int { return s.cfg.Snapshots.RetentionDays }, 0,
					func(v int) { s.cfg.Snapshots.RetentionDays = v })
				f.tomlPath = "snapshots.retention_days"
				f.desc = "days before snapshot files are eligible for cleanup"
				return f
			}(),
			func() *field {
				f := bytesField("snapshots.max_file_bytes", "Max file size",
					func() int64 { return int64(s.cfg.Snapshots.MaxFileBytes) },
					func(v int64) { s.cfg.Snapshots.MaxFileBytes = int(v) })
				f.tomlPath = "snapshots.max_file_bytes"
				f.desc = "skip snapshots for files larger than this"
				return f
			}(),
		}
	})
}

func projectFrame(s *state) *frame {
	return newFrame("Project", func() []*field {
		return []*field{
			func() *field {
				f := scalarField("project.name", "Project name",
					func() string { return s.cfg.Project.Name },
					func(v string) error { s.cfg.Project.Name = v; return nil })
				f.tomlPath = "project.name"
				f.desc = "display name shown in the status bar and session list"
				return f
			}(),
			func() *field {
				f := listDrillExt("project.languages", "Languages", &s.cfg.Project.Languages, sliceOpts(&s.cfg.Project.Languages))
				f.tomlPath = "project.languages"
				f.desc = "programming languages used in this project"
				return f
			}(),
			func() *field {
				f := scalarField("commands.test", "Test command",
					func() string { return s.cfg.Commands.Test },
					func(v string) error { s.cfg.Commands.Test = v; return nil })
				f.tomlPath = "commands.test"
				f.desc = "shell command to run project tests"
				return f
			}(),
			func() *field {
				f := scalarField("commands.format", "Format command",
					func() string { return s.cfg.Commands.Format },
					func(v string) error { s.cfg.Commands.Format = v; return nil })
				f.tomlPath = "commands.format"
				f.desc = "shell command to format source files"
				return f
			}(),
			func() *field {
				f := scalarField("commands.vet", "Vet command",
					func() string { return s.cfg.Commands.Vet },
					func(v string) error { s.cfg.Commands.Vet = v; return nil })
				f.tomlPath = "commands.vet"
				f.desc = "shell command to lint or vet the codebase"
				return f
			}(),
		}
	})
}

func indexingFrame(s *state) *frame {
	return newFrame("Indexing", func() []*field {
		return []*field{
			{id: "indexing.treesitter", title: "Use treesitter", kind: kindToggle,
				tomlPath: "indexing.use_treesitter",
				desc:     "enable tree-sitter for symbol extraction",
				getBool:  func() bool { return s.cfg.Indexing.UseTreesitter },
				setBool:  func(v bool) { s.cfg.Indexing.UseTreesitter = v }},
			{id: "indexing.embeddings", title: "Use embeddings", kind: kindToggle,
				tomlPath: "indexing.use_embeddings",
				desc:     "enable embedding-based semantic search",
				getBool:  func() bool { return s.cfg.Indexing.UseEmbeddings },
				setBool:  func(v bool) { s.cfg.Indexing.UseEmbeddings = v }},
			{id: "indexing.summarise", title: "Summarise files", kind: kindToggle,
				tomlPath: "indexing.summarise_files",
				desc:     "generate file summaries during indexing",
				getBool:  func() bool { return s.cfg.Indexing.SummariseFiles },
				setBool:  func(v bool) { s.cfg.Indexing.SummariseFiles = v }},
			func() *field {
				f := listDrillExt("indexing.ignore", "Ignore patterns", &s.cfg.Indexing.Ignore, sliceOpts(&s.cfg.Indexing.Ignore))
				f.tomlPath = "indexing.ignore"
				f.desc = "glob patterns to skip during indexing"
				return f
			}(),
		}
	})
}

func webFrame(s *state) *frame {
	return newFrame("Web", func() []*field {
		return []*field{
			{id: "web.enabled", title: "Enabled", kind: kindToggle,
				tomlPath: "web.enabled",
				desc:     "allow web.fetch / web.search tools",
				getBool:  func() bool { return s.cfg.Web.Enabled },
				setBool:  func(v bool) { s.cfg.Web.Enabled = v }},
			func() *field {
				f := scalarField("web.fetch_timeout", "Fetch timeout",
					func() string { return s.cfg.Web.FetchTimeout.String() },
					durationSetter(func(d time.Duration) { s.cfg.Web.FetchTimeout = d }))
				f.tomlPath = "web.fetch_timeout"
				f.desc = "max duration for a single web fetch request"
				return f
			}(),
			func() *field {
				f := scalarField("web.search_provider", "Search provider",
					func() string { return s.cfg.Web.SearchProvider },
					func(v string) error { s.cfg.Web.SearchProvider = v; return nil })
				f.tomlPath = "web.search_provider"
				f.desc = "search backend name (e.g. google, bing, serpapi)"
				return f
			}(),
			func() *field {
				f := scalarField("web.search_url", "Search URL",
					func() string { return s.cfg.Web.SearchURL },
					func(v string) error { s.cfg.Web.SearchURL = v; return nil })
				f.tomlPath = "web.search_url"
				f.desc = "custom search API endpoint URL"
				return f
			}(),
			func() *field {
				f := secretRow("web.search_key", "Search key",
					func() string { return s.cfg.Web.SearchKey },
					func(v string) { s.cfg.Web.SearchKey = v })
				f.tomlPath = "web.search_key"
				return f
			}(),
		}
	})
}

func swarmFrame(s *state) *frame {
	return newFrame("Swarm", func() []*field {
		return []*field{
			func() *field {
				f := intField("swarm.max_fix_rounds", "Max fix rounds",
					func() int { return s.cfg.Swarm.Budget.MaxFixRounds }, 0,
					func(v int) { s.cfg.Swarm.Budget.MaxFixRounds = v })
				f.tomlPath = "swarm.budget.max_fix_rounds"
				f.desc = "max LLM retry rounds per swarm task"
				return f
			}(),
			func() *field {
				f := intField("swarm.max_total_tokens", "Max total tokens",
					func() int { return s.cfg.Swarm.Budget.MaxTotalTokens }, 0,
					func(v int) { s.cfg.Swarm.Budget.MaxTotalTokens = v })
				f.tomlPath = "swarm.budget.max_total_tokens"
				f.desc = "total token budget across all swarm rounds"
				return f
			}(),
			func() *field {
				f := mapIntDrill("swarm.tool_iters", "Tool iters", &s.cfg.Swarm.Budget.ToolIters)
				f.tomlPath = "swarm.budget.tool_iters"
				f.desc = "per-tool iteration limits"
				return f
			}(),
		}
	})
}

func sddFrame(s *state) *frame {
	return newFrame("SDD", func() []*field {
		return []*field{
			{id: "sdd.auto_worktree", title: "Auto worktree", kind: kindToggle,
				tomlPath: "sdd.auto_worktree",
				desc:     "create an isolated git worktree for each SDD task",
				getBool:  func() bool { return s.cfg.SDD.AutoWorktree },
				setBool:  func(v bool) { s.cfg.SDD.AutoWorktree = v }},
			{id: "sdd.max_fix_rounds", title: "Max fix rounds", kind: kindScalar,
				tomlPath: "sdd.max_fix_rounds",
				desc:     "max LLM retry rounds per SDD task before escalation",
				getStr:   func() string { return strconv.Itoa(s.cfg.SDD.MaxFixRounds) },
				setStr:   intSetter(0, func(v int) { s.cfg.SDD.MaxFixRounds = v })},
			{id: "sdd.plans_dir", title: "Plans dir", kind: kindScalar,
				tomlPath: "sdd.plans_dir",
				desc:     "directory for SDD plan files (relative to project root)",
				getStr:   func() string { return s.cfg.SDD.PlansDir },
				setStr:   func(v string) error { s.cfg.SDD.PlansDir = v; return nil }},
		}
	})
}

// SwarmFrame returns the swarm budget frame for the given state.
func SwarmFrame(s *State) *Frame { return swarmFrame(s) }

// SDDFrame returns the SDD budget frame for the given state.
func SDDFrame(s *State) *Frame { return sddFrame(s) }

// diagnosticsFrame is nothing but the commands map, so the root frame IS the
// map frame (no pointless single-row drill).
func diagnosticsFrame(s *state) *frame {
	drill := mapStringDrill("diagnostics.commands", "Commands", &s.cfg.Diagnostics.Commands)
	drill.tomlPath = "diagnostics.commands"
	return rootDrillFrame("Diagnostics", drill)
}
