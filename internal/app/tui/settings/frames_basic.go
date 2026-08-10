package settings

import (
	"fmt"
	"strconv"
	"time"

	"marshal/internal/app/tui/picker"
)

func privacyFrame(s *state) *frame {
	return newFrame("Privacy", func() []*field {
		return []*field{
			{ID: "privacy.remote_providers", Title: "Remote providers allowed", Kind: kindToggle,
				TomlPath: "privacy.remote_providers_allowed",
				Desc:     "allow remote providers globally",
				GetBool:  func() bool { return s.cfg.Privacy.RemoteProvidersAllowed },
				SetBool:  func(v bool) { s.cfg.Privacy.RemoteProvidersAllowed = v }},
			{ID: "privacy.redact_secrets", Title: "Redact secrets", Kind: kindToggle,
				TomlPath: "privacy.redact_secrets",
				Desc:     "scrub likely secrets from context sent to models",
				GetBool:  func() bool { return s.cfg.Privacy.RedactSecrets },
				SetBool:  func(v bool) { s.cfg.Privacy.RedactSecrets = v }},
			{ID: "privacy.include_gitignored", Title: "Include gitignored files", Kind: kindToggle,
				TomlPath: "privacy.include_gitignored_files",
				Desc:     "let indexing and context include gitignored paths",
				GetBool:  func() bool { return s.cfg.Privacy.IncludeGitignoredFiles },
				SetBool:  func(v bool) { s.cfg.Privacy.IncludeGitignoredFiles = v }},
		}
	})
}

func snapshotsFrame(s *state) *frame {
	return newFrame("Snapshots", func() []*field {
		return []*field{
			{ID: "snapshots.enabled", Title: "Enabled", Kind: kindToggle,
				TomlPath: "snapshots.enabled",
				Desc:     "capture before-write snapshots of changed files",
				GetBool:  func() bool { return s.cfg.Snapshots.Enabled },
				SetBool:  func(v bool) { s.cfg.Snapshots.Enabled = v }},
			func() *field {
				f := intField("snapshots.retention_days", "Retention days",
					func() int { return s.cfg.Snapshots.RetentionDays }, 0,
					func(v int) { s.cfg.Snapshots.RetentionDays = v })
				f.TomlPath = "snapshots.retention_days"
				f.Desc = "days before snapshot files are eligible for cleanup"
				return f
			}(),
			func() *field {
				f := bytesField("snapshots.max_file_bytes", "Max file size",
					func() int64 { return int64(s.cfg.Snapshots.MaxFileBytes) },
					func(v int64) { s.cfg.Snapshots.MaxFileBytes = int(v) })
				f.TomlPath = "snapshots.max_file_bytes"
				f.Desc = "skip snapshots for files larger than this"
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
				f.TomlPath = "project.name"
				f.Desc = "display name shown in the status bar and session list"
				return f
			}(),
			func() *field {
				f := listDrillExt("project.languages", "Languages", &s.cfg.Project.Languages, sliceOpts(&s.cfg.Project.Languages))
				f.TomlPath = "project.languages"
				f.Desc = "programming languages used in this project"
				return f
			}(),
			func() *field {
				f := scalarField("commands.test", "Test command",
					func() string { return s.cfg.Commands.Test },
					func(v string) error { s.cfg.Commands.Test = v; return nil })
				f.TomlPath = "commands.test"
				f.Desc = "shell command to run project tests"
				return f
			}(),
			func() *field {
				f := scalarField("commands.format", "Format command",
					func() string { return s.cfg.Commands.Format },
					func(v string) error { s.cfg.Commands.Format = v; return nil })
				f.TomlPath = "commands.format"
				f.Desc = "shell command to format source files"
				return f
			}(),
			func() *field {
				f := scalarField("commands.vet", "Vet command",
					func() string { return s.cfg.Commands.Vet },
					func(v string) error { s.cfg.Commands.Vet = v; return nil })
				f.TomlPath = "commands.vet"
				f.Desc = "shell command to lint or vet the codebase"
				return f
			}(),
		}
	})
}

func indexingFrame(s *state) *frame {
	return newFrame("Indexing", func() []*field {
		return []*field{
			{ID: "indexing.treesitter", Title: "Use treesitter", Kind: kindToggle,
				TomlPath: "indexing.use_treesitter",
				Desc:     "enable tree-sitter for symbol extraction",
				GetBool:  func() bool { return s.cfg.Indexing.UseTreesitter },
				SetBool:  func(v bool) { s.cfg.Indexing.UseTreesitter = v }},
			{ID: "indexing.embeddings", Title: "Use embeddings", Kind: kindToggle,
				TomlPath: "indexing.use_embeddings",
				Desc:     "enable embedding-based semantic search",
				GetBool:  func() bool { return s.cfg.Indexing.UseEmbeddings },
				SetBool:  func(v bool) { s.cfg.Indexing.UseEmbeddings = v }},
			func() *field {
				f := &field{
					ID:       "indexing.embedding_preset",
					Title:    "Embedding preset",
					Kind:     kindPicker,
					TomlPath: "indexing.embedding_preset",
					Desc:     "preset used to embed code for semantic search · unset disables embeddings",
					Keywords: []string{"embedding", "semantic", "search", "vector"},
					GetStr:   func() string { return s.cfg.Indexing.EmbeddingPreset },
					Display: func() (string, string) {
						if s.cfg.Indexing.EmbeddingPreset == "" {
							return "", "(unset — embeddings off)"
						}
						return s.cfg.Indexing.EmbeddingPreset, ""
					},
					PickOptions: func() []picker.Item {
						names := sortedKeys(s.cfg.Models.Presets)
						if len(names) == 0 {
							return []picker.Item{{Label: "Add a preset first in Model Presets", Value: "__none__", Badge: "required"}}
						}
						current := s.cfg.Indexing.EmbeddingPreset
						items := make([]picker.Item, 0, len(names)+1)
						for _, n := range names {
							p := s.cfg.Models.Presets[n]
							badge := ""
							if n == current {
								badge = "● now"
							}
							items = append(items, picker.Item{Label: n, Detail: p.Provider + "/" + p.Model, Badge: badge, Value: n})
						}
						items = append(items, picker.Item{Label: "(unset — embeddings off)", Value: unsetRoleValue, Badge: "clear"})
						return items
					},
					PickOnPick: func(v string) error {
						switch v {
						case "__none__":
							return fmt.Errorf("add a preset first in the Model Presets section")
						case unsetRoleValue:
							s.cfg.Indexing.EmbeddingPreset = ""
							return nil
						}
						if _, ok := s.cfg.Models.Presets[v]; !ok {
							return fmt.Errorf("unknown preset %q", v)
						}
						s.cfg.Indexing.EmbeddingPreset = v
						return nil
					},
				}
				return f
			}(),
			{ID: "indexing.summarise", Title: "Summarise files", Kind: kindToggle,
				TomlPath: "indexing.summarise_files",
				Desc:     "generate file summaries during indexing",
				GetBool:  func() bool { return s.cfg.Indexing.SummariseFiles },
				SetBool:  func(v bool) { s.cfg.Indexing.SummariseFiles = v }},
			func() *field {
				f := listDrillExt("indexing.ignore", "Ignore patterns", &s.cfg.Indexing.Ignore, sliceOpts(&s.cfg.Indexing.Ignore))
				f.TomlPath = "indexing.ignore"
				f.Desc = "glob patterns to skip during indexing"
				return f
			}(),
		}
	})
}

func skillsFrame(s *state) *frame {
	return newFrame("Skills", func() []*field {
		return []*field{
			func() *field {
				f := listDrillExt("skills.autoload", "Autoload", &s.cfg.Skills.Autoload, sliceOpts(&s.cfg.Skills.Autoload))
				f.TomlPath = "skills.autoload"
				f.Desc = "skills injected into every session before the first turn"
				return f
			}(),
		}
	})
}

func webFrame(s *state) *frame {
	return newFrame("Web", func() []*field {
		return []*field{
			{ID: "web.enabled", Title: "Enabled", Kind: kindToggle,
				TomlPath: "web.enabled",
				Desc:     "allow web.fetch / web.search tools",
				GetBool:  func() bool { return s.cfg.Web.Enabled },
				SetBool:  func(v bool) { s.cfg.Web.Enabled = v }},
			func() *field {
				f := scalarField("web.fetch_timeout", "Fetch timeout",
					func() string { return s.cfg.Web.FetchTimeout.String() },
					durationSetter(func(d time.Duration) { s.cfg.Web.FetchTimeout = d }))
				f.TomlPath = "web.fetch_timeout"
				f.Desc = "max duration for a single web fetch request"
				return f
			}(),
			func() *field {
				f := scalarField("web.search_provider", "Search provider",
					func() string { return s.cfg.Web.SearchProvider },
					func(v string) error { s.cfg.Web.SearchProvider = v; return nil })
				f.TomlPath = "web.search_provider"
				f.Desc = "search backend name (e.g. google, bing, serpapi)"
				return f
			}(),
			func() *field {
				f := scalarField("web.search_url", "Search URL",
					func() string { return s.cfg.Web.SearchURL },
					func(v string) error { s.cfg.Web.SearchURL = v; return nil })
				f.TomlPath = "web.search_url"
				f.Desc = "custom search API endpoint URL"
				return f
			}(),
			func() *field {
				f := secretRow("web.search_key", "Search key",
					func() string { return s.cfg.Web.SearchKey },
					func(v string) { s.cfg.Web.SearchKey = v })
				f.TomlPath = "web.search_key"
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
				f.TomlPath = "swarm.budget.max_fix_rounds"
				f.Desc = "max LLM retry rounds per swarm task"
				return f
			}(),
			func() *field {
				f := intField("swarm.max_total_tokens", "Max total tokens",
					func() int { return s.cfg.Swarm.Budget.MaxTotalTokens }, 0,
					func(v int) { s.cfg.Swarm.Budget.MaxTotalTokens = v })
				f.TomlPath = "swarm.budget.max_total_tokens"
				f.Desc = "total token budget across all swarm rounds"
				return f
			}(),
			func() *field {
				f := mapIntDrill("swarm.tool_iters", "Tool iters", &s.cfg.Swarm.Budget.ToolIters)
				f.TomlPath = "swarm.budget.tool_iters"
				f.Desc = "per-tool iteration limits"
				return f
			}(),
		}
	})
}

func sddFrame(s *state) *frame {
	return newFrame("SDD", func() []*field {
		return []*field{
			{ID: "sdd.auto_worktree", Title: "Auto worktree", Kind: kindToggle,
				TomlPath: "sdd.auto_worktree",
				Desc:     "create an isolated git worktree for each SDD task",
				GetBool:  func() bool { return s.cfg.SDD.AutoWorktree },
				SetBool:  func(v bool) { s.cfg.SDD.AutoWorktree = v }},
			{ID: "sdd.max_fix_rounds", Title: "Max fix rounds", Kind: kindScalar,
				TomlPath: "sdd.max_fix_rounds",
				Desc:     "max LLM retry rounds per SDD task before escalation",
				GetStr:   func() string { return strconv.Itoa(s.cfg.SDD.MaxFixRounds) },
				SetStr:   intSetter(0, func(v int) { s.cfg.SDD.MaxFixRounds = v })},
			{ID: "sdd.plans_dir", Title: "Plans dir", Kind: kindScalar,
				TomlPath: "sdd.plans_dir",
				Desc:     "directory for SDD plan files (relative to project root)",
				GetStr:   func() string { return s.cfg.SDD.PlansDir },
				SetStr:   func(v string) error { s.cfg.SDD.PlansDir = v; return nil }},
			{ID: "sdd.verify_timeout_ms", Title: "Verify timeout (ms)", Kind: kindScalar,
				TomlPath: "sdd.verify_timeout_ms",
				Desc:     "per-task build/lint/test timeout in milliseconds",
				GetStr:   func() string { return strconv.Itoa(s.cfg.SDD.VerifyTimeoutMS) },
				SetStr:   intSetter(0, func(v int) { s.cfg.SDD.VerifyTimeoutMS = v })},
			{ID: "sdd.cleanup_at_start", Title: "Cleanup at start", Kind: kindToggle,
				TomlPath: "sdd.cleanup_at_start",
				Desc:     "run task-cleanup --stale at SDD startup",
				GetBool:  func() bool { return s.cfg.SDD.CleanupAtStart },
				SetBool:  func(v bool) { s.cfg.SDD.CleanupAtStart = v }},
			{ID: "sdd.max_total_tokens", Title: "Max total tokens", Kind: kindScalar,
				TomlPath: "sdd.max_total_tokens",
				Desc:     "token budget cap for the SDD run (0 = no cap)",
				GetStr:   func() string { return strconv.Itoa(s.cfg.SDD.MaxTotalTokens) },
				SetStr:   intSetter(0, func(v int) { s.cfg.SDD.MaxTotalTokens = v })},
			{ID: "sdd.verify.build", Title: "Verify build command", Kind: kindScalar,
				TomlPath: "sdd.verify.build",
				Desc:     "build command for per-task verification (empty = Go default)",
				GetStr:   func() string { return s.cfg.SDD.Verify.Build },
				SetStr:   func(v string) error { s.cfg.SDD.Verify.Build = v; return nil }},
			{ID: "sdd.verify.test", Title: "Verify test command", Kind: kindScalar,
				TomlPath: "sdd.verify.test",
				Desc:     "test command for per-task verification (empty = Go default)",
				GetStr:   func() string { return s.cfg.SDD.Verify.Test },
				SetStr:   func(v string) error { s.cfg.SDD.Verify.Test = v; return nil }},
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
	drill.TomlPath = "diagnostics.commands"
	return rootDrillFrame("Diagnostics", drill)
}
