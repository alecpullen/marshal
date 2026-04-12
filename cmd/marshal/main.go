package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/config"
	"github.com/alec/marshal/internal/git"
	"github.com/alec/marshal/internal/logging"
	"github.com/alec/marshal/internal/loop"
	"github.com/alec/marshal/internal/pipeline"
	"github.com/alec/marshal/internal/planner"
	"github.com/alec/marshal/internal/session"
	"github.com/alec/marshal/internal/skills"
	"github.com/alec/marshal/internal/ui/tui"
	"github.com/spf13/cobra"
)

// Version is set at link time via -ldflags; falls back to VCS info.
var Version = ""

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// globalFlags are persistent flags shared by all subcommands.
type globalFlags struct {
	verbose bool
	profile string
}

func rootCmd() *cobra.Command {
	gf := &globalFlags{}

	root := &cobra.Command{
		Use:   "marshal",
		Short: "AI coding assistant with multi-model orchestration",
		Long: `Marshal is an AI-powered coding assistant that combines Aider's feature surface
with a four-role multi-model orchestration system. Every user turn is a
discrete, branch-isolated, critic-reviewed task.`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return logging.Init(logging.Options{Verbose: gf.verbose})
		},
	}

	root.PersistentFlags().BoolVarP(&gf.verbose, "verbose", "v", false, "Enable debug logging")
	root.PersistentFlags().StringVar(&gf.profile, "profile", "", "Config profile to activate (e.g. dev)")

	root.AddCommand(
		versionCmd(),
		configCmd(gf),
		chatCmd(gf),
		runCmd(gf),
		pipelineCmd(gf),
		debugCmd(gf),
	)

	return root
}

// ── version ──────────────────────────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the marshal version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(resolvedVersion())
			return nil
		},
	}
}

func resolvedVersion() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return "dev"
}

// ── config ───────────────────────────────────────────────────────────────────

func configCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage marshal configuration",
	}
	cmd.AddCommand(configShowCmd(gf))
	return cmd
}

func configShowCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved configuration (secrets redacted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.Options{Profile: gf.profile})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			redacted := cfg.Redacted()
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(redacted)
		},
	}
}

// ── chat ──────────────────────────────────────────────────────────────────────

func chatCmd(gf *globalFlags) *cobra.Command {
	var noShip bool

	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Start an interactive AI coding session (TUI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.Options{Profile: gf.profile})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, err := git.New(cwd, git.RepoConfig{CoAuthoredBy: cfg.Model.Executor.Model})
			if err != nil {
				return fmt.Errorf("not a git repo: %w", err)
			}

			store, err := tui.OpenStore(repo.Root())
			if err != nil {
				return fmt.Errorf("session store: %w", err)
			}
			defer store.Close()

			var gitSess *git.Session
			if cfg.Git.Enabled {
				gitSess = git.NewSession(repo, git.SessionOptions{KeepBranch: noShip})
				if err := gitSess.Start(); err != nil {
					return fmt.Errorf("session start: %w", err)
				}
			}

			reg, err := backend.NewRegistry(cfg, nil)
			if err != nil {
				if gitSess != nil {
					_ = gitSess.Teardown()
				}
				return fmt.Errorf("backend registry: %w", err)
			}

			// Load skills: built-ins first, then user-defined (~/.config/marshal/skills/*.toml).
			// Missing user directory is silently ignored. User skills can override built-ins.
			skillsReg := skills.New()
			if err := skills.LoadBuiltins(skillsReg); err != nil {
				return fmt.Errorf("loading built-in skills: %w", err)
			}
			skillsDir := ""
			if home, err := os.UserHomeDir(); err == nil {
				skillsDir = filepath.Join(home, ".config", "marshal", "skills")
			}
			userSkills, err := skills.Load(skillsDir)
			if err != nil {
				return fmt.Errorf("loading user skills: %w", err)
			}
			// Merge user skills into registry (overriding built-ins if triggers collide).
			for _, s := range userSkills.All() {
				_ = skillsReg.Register(s) // ignore duplicate errors
			}

			if runErr := tui.Run(cmd.Context(), cfg, repo, gitSess, store, reg, skillsReg); runErr != nil {
				if gitSess != nil {
					_ = gitSess.Teardown()
				}
				return runErr
			}

			if cfg.Git.Enabled && !noShip {
				sha, shipErr := gitSess.Ship("marshal chat session")
				if shipErr != nil {
					fmt.Fprintf(os.Stderr, "ship: %v\n", shipErr)
				} else {
					fmt.Printf("shipped to %s (%s)\n", gitSess.TargetBranch, sha[:8])
				}
			}
			if gitSess != nil {
				_ = gitSess.Teardown()
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&noShip, "no-ship", false, "Keep changes on staging; do not merge to target on exit")
	return cmd
}

// ── run (stub) ────────────────────────────────────────────────────────────────

func runCmd(gf *globalFlags) *cobra.Command {
	var noShip bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "run <task>",
		Short: "Run a single task against the current repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := args[0]

			cfg, err := config.Load(config.Options{Profile: gf.profile})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, err := git.New(cwd, git.RepoConfig{
				CoAuthoredBy: cfg.Model.Executor.Model,
			})
			if err != nil {
				return fmt.Errorf("not a git repo: %w", err)
			}

			// Open (or create) the session database.
			dbDir := filepath.Join(repo.Root(), ".marshal")
			if err := os.MkdirAll(dbDir, 0o755); err != nil {
				return err
			}
			ensureMarshalExcluded(repo.Root())
			store, err := session.Open(filepath.Join(dbDir, "sessions.db"))
			if err != nil {
				return fmt.Errorf("session store: %w", err)
			}
			defer store.Close()

			// Start git session only when git integration is enabled.
			var gitSess *git.Session
			sessID := newCmdID()
			now := time.Now()
			sessRecord := &session.Session{
				ID:        sessID,
				StartedAt: now,
			}
			if cfg.Git.Enabled {
				gitSess = git.NewSession(repo, git.SessionOptions{KeepBranch: noShip})
				if err := gitSess.Start(); err != nil {
					return fmt.Errorf("session start: %w", err)
				}
				sessRecord.TargetBranch = gitSess.TargetBranch
				sessRecord.TargetStartSHA = gitSess.TargetStartSHA
				sessRecord.StagingBranch = gitSess.StagingBranch
			}
			if err := store.InsertSession(sessRecord); err != nil {
				if gitSess != nil {
					_ = gitSess.Teardown()
				}
				return fmt.Errorf("insert session: %w", err)
			}

			// Build backend registry.
			reg, err := backend.NewRegistry(cfg, nil)
			if err != nil {
				if gitSess != nil {
					_ = gitSess.Teardown()
				}
				return fmt.Errorf("backend registry: %w", err)
			}

			// Choose sink.
			var sink loop.Sink = loop.StdoutSink{}
			_ = jsonOutput // M11 will add NDJSON sink

			// Run the task loop.
			eng := loop.New(
				loop.Config{
					MaxRounds:    cfg.Loop.MaxRounds,
					CompactAfter: cfg.Loop.CompactAfter,
					SessionID:    sessID,
					GitEnabled:   cfg.Git.Enabled,
					LinterCfg:    cfg.Linters,
					EditFormat:   cfg.Loop.EditFormat,
				},
				repo, gitSess, store, reg, sink,
			)
			runErr := eng.Run(cmd.Context(), prompt)

			if runErr == nil {
				if cfg.Git.Enabled && !noShip {
					sha, shipErr := gitSess.Ship(prompt)
					if shipErr != nil {
						return fmt.Errorf("ship: %w", shipErr)
					}
					if err := store.ShipSession(sessID, gitSess.StagingBranch, sha); err != nil {
						return fmt.Errorf("update session: %w", err)
					}
					fmt.Printf("shipped to %s (%s)\n", gitSess.TargetBranch, sha[:8])
				} else if cfg.Git.Enabled {
					fmt.Printf("task passed — staged on %s (use /ship to land on %s)\n",
						gitSess.StagingBranch, gitSess.TargetBranch)
				} else {
					fmt.Println("task passed")
				}
				if gitSess != nil {
					_ = gitSess.Teardown()
				}
				return nil
			}

			if gitSess != nil {
				_ = gitSess.Teardown()
			}
			if errors.Is(runErr, loop.ErrTaskFailed) {
				return fmt.Errorf("task failed")
			}
			return runErr
		},
	}

	cmd.Flags().BoolVar(&noShip, "no-ship", false, "Leave changes on the staging branch instead of shipping to target")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit NDJSON events (M11)")
	return cmd
}

func newCmdID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ── pipeline (M9) ─────────────────────────────────────────────────────────────

func pipelineCmd(gf *globalFlags) *cobra.Command {
	var pipelineOnly bool
	var noTUI bool

	cmd := &cobra.Command{
		Use:   "pipeline <feature>",
		Short: "Decompose and run a multi-task pipeline",
		Long: `Decomposes a high-level feature into a DAG of tasks, runs them in parallel
tiers on isolation branches, invokes the integration critic, and merges or holds.

Example:
  marshal pipeline "add staff portal with timesheets and rentman integration"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.Options{Profile: gf.profile})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Find repo root and setup git.
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			repo, err := git.New(cwd, git.RepoConfig{CoAuthoredBy: cfg.Model.Executor.Model})
			if err != nil {
				return fmt.Errorf("git repo: %w", err)
			}

			var gitSess *git.Session
			if cfg.Git.Enabled {
				gitSess = git.NewSession(repo, git.SessionOptions{})
				if err := gitSess.Start(); err != nil {
					return fmt.Errorf("git session: %w", err)
				}
				defer gitSess.Teardown()
			}

			store, err := tui.OpenStore(repo.Root())
			if err != nil {
				return fmt.Errorf("session store: %w", err)
			}
			defer store.Close()

			reg, err := backend.NewRegistry(cfg, nil)
			if err != nil {
				return fmt.Errorf("backend registry: %w", err)
			}

			marshalB, err := reg.For(config.RoleMarshal)
			if err != nil {
				return fmt.Errorf("marshal backend: %w", err)
			}

			// Generate task graph from the planner.
			feature := args[0]
			taskGraph, err := planner.Generate(cmd.Context(), marshalB, feature)
			if err != nil {
				return fmt.Errorf("planning: %w", err)
			}

			if pipelineOnly {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(taskGraph)
			}

			// Run in headless mode or TUI.
			if noTUI {
				return runPipelineHeadless(cmd.Context(), cfg, repo, gitSess, store, reg, taskGraph)
			}
			return runPipelineTUI(cmd.Context(), cfg, repo, gitSess, store, reg, taskGraph)
		},
	}

	cmd.Flags().BoolVar(&pipelineOnly, "pipeline-only", false, "Emit the task graph and exit without executing")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Run pipeline in headless mode (NDJSON output)")
	return cmd
}

func runPipelineHeadless(ctx context.Context, cfg *config.Config, repo *git.Repo, gitSess *git.Session, store *session.Store, reg *backend.Registry, graph *planner.TaskGraph) error {
	// Convert planner tasks to pipeline tasks.
	var tasks []*pipeline.PipelineTask
	for _, t := range graph.Tasks {
		tasks = append(tasks, &pipeline.PipelineTask{
			ID:          t.ID,
			Description: t.Description,
			Files:       t.FilesLikelyAffected,
			DependsOn:   t.DependsOn,
			MaxRounds:   cfg.Loop.MaxRounds,
		})
	}

	scheduler, err := pipeline.NewScheduler(tasks)
	if err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}

	fmt.Printf("{\"event\":\"pipeline_start\",\"tasks\":%d}\n", len(tasks))

	err = scheduler.Run(ctx, func(ctx context.Context, t *pipeline.PipelineTask) error {
		fmt.Printf("{\"event\":\"task_start\",\"task_id\":%q}\n", t.ID)

		// Create task branch.
		tx, err := gitSess.BeginTask(t.ID)
		if err != nil {
			fmt.Printf("{\"event\":\"task_failed\",\"task_id\":%q,\"error\":%q}\n", t.ID, err.Error())
			return err
		}
		defer tx.Abandon()

		// Run single-task loop.
		sessID := "pipeline-" + t.ID
		eng := loop.New(
			loop.Config{
				MaxRounds:    cfg.Loop.MaxRounds,
				CompactAfter: cfg.Loop.CompactAfter,
				SessionID:    sessID,
				GitEnabled:   cfg.Git.Enabled,
				LinterCfg:    cfg.Linters,
				EditFormat:   cfg.Loop.EditFormat,
			},
			repo, gitSess, store, reg, loop.StdoutSink{},
		)

		if err := eng.Run(ctx, t.Description); err != nil {
			fmt.Printf("{\"event\":\"task_failed\",\"task_id\":%q,\"error\":%q}\n", t.ID, err.Error())
			return err
		}

		// Merge to task branch.
		if err := tx.Commit(fmt.Sprintf("Pipeline task %s", t.ID)); err != nil {
			return err
		}
		if err := tx.Merge(fmt.Sprintf("Merge %s", t.ID)); err != nil {
			return err
		}

		fmt.Printf("{\"event\":\"task_passed\",\"task_id\":%q}\n", t.ID)
		return nil
	})

	if err != nil {
		fmt.Printf("{\"event\":\"pipeline_failed\",\"error\":%q}\n", err.Error())
		return err
	}

	// Integration critic.
	var taskBranches []string
	for _, t := range tasks {
		taskBranches = append(taskBranches, "marshal/task-"+t.ID)
	}

	combinedDiff, err := pipeline.ComputeCombinedDiff(repo, gitSess.StagingBranch, taskBranches)
	if err != nil {
		return fmt.Errorf("combined diff: %w", err)
	}

	criticB, _ := reg.For(config.RoleCritic)
	ic := pipeline.NewIntegrationCritic(criticB)
	verdict, err := ic.Review(ctx, combinedDiff, taskBranches)
	if err != nil {
		return fmt.Errorf("integration critic: %w", err)
	}

	if verdict.Verdict != "PASS" {
		fmt.Printf("{\"event\":\"integration_fail\",\"verdict\":%q,\"implicated\":%v}\n", verdict.Verdict, verdict.Implicated)
		return fmt.Errorf("integration critic rejected: %s", verdict.Summary)
	}

	// Topological merge.
	taskIDs := make([]string, len(tasks))
	for i, t := range tasks {
		taskIDs[i] = t.ID
	}
	newSHA, err := pipeline.TopologicalMerge(repo, gitSess.StagingBranch, tasks)
	if err != nil {
		return fmt.Errorf("topological merge: %w", err)
	}

	fmt.Printf("{\"event\":\"pipeline_complete\",\"staging_sha\":%q}\n", newSHA)
	fmt.Println("\nPipeline complete. Run '/ship' in TUI to merge to target branch.")
	return nil
}

func runPipelineTUI(ctx context.Context, cfg *config.Config, repo *git.Repo, gitSess *git.Session, store *session.Store, reg *backend.Registry, graph *planner.TaskGraph) error {
	// For now, delegate to headless mode. Full TUI pipeline view is future work.
	return runPipelineHeadless(ctx, cfg, repo, gitSess, store, reg, graph)
}

// ── debug ─────────────────────────────────────────────────────────────────────

func debugCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Low-level debugging utilities",
	}
	cmd.AddCommand(debugChatCmd(gf), debugGitSessionCmd(gf))
	return cmd
}

func debugChatCmd(gf *globalFlags) *cobra.Command {
	var role string

	cmd := &cobra.Command{
		Use:   "chat <message>",
		Short: "Stream a single reply from one model role",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.Options{Profile: gf.profile})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			registry, err := backend.NewRegistry(cfg, nil)
			if err != nil {
				return fmt.Errorf("building registry: %w", err)
			}
			b, err := registry.For(role)
			if err != nil {
				return err
			}

			req := backend.Request{
				Messages: []backend.Message{
					{Role: backend.MessageRoleUser, Content: args[0]},
				},
			}

			ch, err := b.Stream(context.Background(), req)
			if err != nil {
				return fmt.Errorf("stream: %w", err)
			}
			for chunk := range ch {
				if chunk.Err != nil {
					return chunk.Err
				}
				fmt.Print(chunk.Content)
			}
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", config.RoleExecutor,
		"Model role to query (marshal|executor|critic|compactor)")
	return cmd
}

func debugGitSessionCmd(gf *globalFlags) *cobra.Command {
	var taskID string
	var keepBranch bool

	cmd := &cobra.Command{
		Use:   "git-session",
		Short: "Exercise the full session lifecycle in the current repo",
		Long: `Creates a marshal session on the current repo, runs a task branch with a
dummy commit, merges it to staging, then Ships to the target branch.
Useful for verifying the git layer works correctly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repo, err := git.New(cwd, git.RepoConfig{CoAuthoredBy: "debug"})
			if err != nil {
				return fmt.Errorf("not in a git repo: %w", err)
			}

			s := git.NewSession(repo, git.SessionOptions{KeepBranch: keepBranch})

			fmt.Printf("starting session (target: %s)\n", func() string {
				b, _ := repo.CurrentBranch()
				return b
			}())

			if err := s.Start(); err != nil {
				return fmt.Errorf("session start: %w", err)
			}
			fmt.Printf("  staging branch: %s\n", s.StagingBranch)
			fmt.Printf("  target start SHA: %s\n", s.TargetStartSHA[:8])

			// Begin task.
			if taskID == "" {
				taskID = "debug"
			}
			tx, err := s.BeginTask(taskID)
			if err != nil {
				return fmt.Errorf("begin task: %w", err)
			}
			fmt.Printf("  task branch: %s\n", tx.Branch)

			// Write a dummy file and commit.
			dummyPath := fmt.Sprintf("marshal-debug-%s.txt", taskID)
			if err := os.WriteFile(dummyPath, []byte("marshal debug commit\n"), 0o644); err != nil {
				return err
			}
			if err := tx.Commit(fmt.Sprintf("debug: add %s", dummyPath)); err != nil {
				return fmt.Errorf("commit: %w", err)
			}
			diff, _ := tx.Diff()
			fmt.Printf("  diff against staging HEAD:\n%s\n", diff)

			// Merge to staging.
			if err := tx.Merge(fmt.Sprintf("task %s: dummy commit", taskID)); err != nil {
				return fmt.Errorf("merge: %w", err)
			}
			stagingSHA, _ := repo.HeadSHA()
			fmt.Printf("  merged to staging; staging HEAD: %s\n", stagingSHA[:8])

			// Ship to target.
			newSHA, err := s.Ship(fmt.Sprintf("marshal debug: task %s", taskID))
			if err != nil {
				return fmt.Errorf("ship: %w", err)
			}
			fmt.Printf("  shipped; %s HEAD: %s\n", s.TargetBranch, newSHA[:8])
			fmt.Printf("  new staging branch: %s\n", s.StagingBranch)

			if !keepBranch {
				if err := s.Teardown(); err != nil {
					return fmt.Errorf("teardown: %w", err)
				}
				fmt.Printf("  torn down; back on %s\n", s.TargetBranch)
			}

			// Clean up the dummy file from the working tree (it was shipped).
			_ = os.Remove(dummyPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&taskID, "task", "debug", "Task ID to use for the test branch")
	cmd.Flags().BoolVar(&keepBranch, "keep-session-branch", false, "Keep the new staging branch after teardown")
	return cmd
}

// ─────────────────────────────────────────────────────────────────────────────

func stubNotImplemented(milestone string) error {
	return fmt.Errorf("not yet implemented: %s", milestone)
}

// ensureMarshalExcluded adds ".marshal/" to .git/info/exclude (if not already
// present) so that git never tracks SQLite WAL/SHM files created inside the
// .marshal/ directory. This prevents `git add -A` from staging those files on
// a task branch, which would cause `git checkout` back to the staging branch
// to fail with "local changes would be overwritten".
func ensureMarshalExcluded(repoRoot string) {
	excludePath := filepath.Join(repoRoot, ".git", "info", "exclude")
	data, _ := os.ReadFile(excludePath)
	if strings.Contains(string(data), ".marshal/") {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	// Ensure we start on a fresh line.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, ".marshal/")
}
