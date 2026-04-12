package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/config"
	"github.com/alec/marshal/internal/git"
	"github.com/alec/marshal/internal/logging"
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

// ── chat (stub) ───────────────────────────────────────────────────────────────

func chatCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "chat",
		Short: "Start an interactive AI coding session (TUI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return stubNotImplemented("chat (M4)")
		},
	}
}

// ── run (stub) ────────────────────────────────────────────────────────────────

func runCmd(gf *globalFlags) *cobra.Command {
	var noTUI bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "run <task>",
		Short: "Run a single task against the current repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return stubNotImplemented("run (M3)")
		},
	}

	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Disable TUI (headless mode)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit NDJSON events (implies --no-tui)")
	return cmd
}

// ── pipeline (stub) ───────────────────────────────────────────────────────────

func pipelineCmd(gf *globalFlags) *cobra.Command {
	var pipelineOnly bool

	cmd := &cobra.Command{
		Use:   "pipeline <feature>",
		Short: "Decompose and run a multi-task pipeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return stubNotImplemented("pipeline (M9)")
		},
	}

	cmd.Flags().BoolVar(&pipelineOnly, "pipeline-only", false, "Emit the task graph and exit without executing")
	return cmd
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
