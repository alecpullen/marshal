package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alecpullen/marshal/internal/agents/planner"
	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/loop"
	"github.com/alecpullen/marshal/internal/marshal"
	"github.com/alecpullen/marshal/internal/pipeline"
	"github.com/alecpullen/marshal/internal/store"
	"github.com/alecpullen/marshal/internal/tui"
	"github.com/spf13/cobra"
)

var (
	configPath string
	jsonOutput bool
	verbose    bool
)

// JSONResult is the output format for --json mode.
type JSONResult struct {
	Status      string      `json:"status"`
	SessionID   string      `json:"session_id"`
	Task        string      `json:"task"`
	Rounds      []JSONRound `json:"rounds"`
	TotalTokens TokenUsage  `json:"total_tokens"`
	Duration    string      `json:"duration"`
	Error       string      `json:"error,omitempty"`
}

// TokenUsage tracks token consumption.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// JSONRound represents a round in JSON output.
type JSONRound struct {
	Number  int        `json:"number"`
	Verdict string     `json:"verdict"`
	Summary string     `json:"summary"`
	Issue   string     `json:"issue,omitempty"`
	Tokens  TokenUsage `json:"tokens"`
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "marshal",
		Short: "Marshal is a loop-first coding agent orchestrator",
		Long:  "Marshal runs an executor-critic feedback loop with real git operations.",
		Run: func(cmd *cobra.Command, args []string) {
			runTask("", false)
		},
	}

	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "marshal.toml", "Path to config file")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")

	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(sessionsCmd())
	rootCmd.AddCommand(pipelineCmd())

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runCmd() *cobra.Command {
	var noTUI bool

	cmd := &cobra.Command{
		Use:   "run [task]",
		Short: "Run a task through the executor-critic loop",
		Long:  "Run a task through the executor-critic loop. If no task is provided, launches the TUI.",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			task := ""
			if len(args) > 0 {
				task = strings.Join(args, " ")
			}
			// If no task and headless mode, error
			if task == "" && noTUI {
				log.Fatal("task required when using --no-tui")
			}
			runTask(task, noTUI)
		},
	}

	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Run without TUI (headless mode)")

	return cmd
}

func sessionsCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "sessions [id]",
		Short: "List sessions or show session details",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := config.Load(configPath)
			if err != nil {
				log.Fatal("config:", err)
			}

			dbPath := cfg.Session.DBPath
			if dbPath == "" {
				dbPath = ".marshal/sessions.db"
			}
			if !filepath.IsAbs(dbPath) {
				dbPath = filepath.Join(".", dbPath)
			}

			s, err := store.New(dbPath)
			if err != nil {
				log.Fatal("store:", err)
			}
			defer s.Close()

			if len(args) == 1 {
				// Show specific session
				sessionID := args[0]
				session, err := s.GetSession(sessionID)
				if err != nil {
					log.Fatal("get session:", err)
				}

				rounds, err := s.GetRounds(sessionID)
				if err != nil {
					log.Fatal("get rounds:", err)
				}

				printSession(session, rounds)
			} else {
				// List sessions
				sessions, err := s.ListSessions(limit)
				if err != nil {
					log.Fatal("list sessions:", err)
				}

				printSessions(sessions)
			}
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Number of sessions to show")

	return cmd
}

func runTask(task string, noTUI bool) {
	// Load config first (needed for both TUI and headless)
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatal("config:", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal("validate:", err)
	}

	// Find git repository root
	repoRoot, err := findGitRoot(".")
	if err != nil {
		log.Fatal("not a git repository:", err)
	}
	cfg.RepoRoot = repoRoot

	// Setup store
	dbPath := cfg.Session.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(repoRoot, ".marshal", "sessions.db")
	} else if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(repoRoot, dbPath)
	}

	s, err := store.New(dbPath)
	if err != nil {
		log.Fatal("store init:", err)
	}
	defer s.Close()

	if noTUI {
		runTaskHeadless(task, cfg, s)
	} else {
		runTaskTUI(task, cfg, s)
	}
}

// resolveEditor returns the editor binary to use, in priority order:
// 1. marshal.toml [ui].editor
// 2. $EDITOR env var
// 3. "vim"
func resolveEditor(cfg *config.Config) string {
	if cfg.UI.Editor != "" {
		return cfg.UI.Editor
	}
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	return "vim"
}

func runTaskTUI(initialTask string, cfg *config.Config, s *store.Store) {
	// Create the loop adapter that bridges TUI messages with the loop engine
	// Pass the initial task so it starts processing immediately
	adapter := tui.NewLoopAdapter(cfg, s, initialTask)

	// Create the TUI model with the adapter wired in
	model := tui.New().
		WithRepoRoot(cfg.RepoRoot).
		WithEditor(resolveEditor(cfg)).
		WithConfig(cfg).
		WithStore(s).
		WithLoopAdapter(adapter)

	// If an initial task was provided via CLI, pre-populate it in the UI
	if initialTask != "" {
		model = model.WithInitialTask(initialTask)
	}

	// Create the program
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Start the adapter in a goroutine to process loop events
	go adapter.Run(p)

	// Run the TUI
	if _, err := p.Run(); err != nil {
		log.Fatal("TUI error:", err)
	}
}

func runTaskHeadless(task string, cfg *config.Config, s *store.Store) {
	startTime := time.Now()

	// Check for dirty working tree before headless execution
	gitImpl, err := git.New(cfg.RepoRoot)
	if err != nil {
		log.Fatal("git init:", err)
	}
	if dirty, _ := gitImpl.IsWorkingTreeDirty(); dirty {
		log.Fatal("working tree has uncommitted changes - stash or commit first")
	}

	// Load skills
	skills, err := loop.LoadSkills(".")
	if err != nil {
		log.Fatal("skills:", err)
	}

	// Create loop
	l := loop.New(cfg, gitImpl, s, skills)

	// Create session record
	session := &store.Session{
		ID:              generateSessionID(),
		RepoRoot:        cfg.RepoRoot,
		Task:            task,
		Status:          "RUNNING",
		BaseBranch:      gitImpl.BaseBranch(),
		IsolationBranch: "",
	}

	// Run with recording
	recorder := loop.NewLoopRecorder(s, l)
	ctx := context.Background()

	fmt.Printf("\n=== Starting Marshal Loop ===\n")
	fmt.Printf("Task: %s\n\n", task)

	result, err := recorder.RunWithRecording(ctx, task, session, nil)

	duration := time.Since(startTime)

	// Output results
	if jsonOutput {
		outputJSON(result, task, duration, err)
	} else {
		outputText(result, duration)
	}

	// Exit with error if loop failed
	if err != nil {
		os.Exit(1)
	}
}

func outputJSON(result *loop.Result, task string, duration time.Duration, runErr error) {
	jr := JSONResult{
		Status:   result.Status,
		Task:     task,
		Duration: duration.Round(time.Millisecond).String(),
	}

	if runErr != nil {
		jr.Error = runErr.Error()
	}

	for _, round := range result.Rounds {
		jr.Rounds = append(jr.Rounds, JSONRound{
			Number:  round.Number,
			Verdict: round.Verdict.Verdict,
			Summary: round.Verdict.Summary,
			Issue:   round.Verdict.Issue,
			Tokens: TokenUsage{
				PromptTokens:     round.Tokens.PromptTokens,
				CompletionTokens: round.Tokens.CompletionTokens,
			},
		})
		jr.TotalTokens.PromptTokens += round.Tokens.PromptTokens
		jr.TotalTokens.CompletionTokens += round.Tokens.CompletionTokens
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(jr)
}

func outputText(result *loop.Result, duration time.Duration) {
	fmt.Printf("\n=== Result ===\n")
	fmt.Printf("Status: %s\n", result.Status)
	if result.FinalVerdict != nil {
		fmt.Printf("Verdict: %s\n", result.FinalVerdict.Verdict)
		fmt.Printf("Summary: %s\n", result.FinalVerdict.Summary)
		if result.FinalVerdict.Issue != "" {
			fmt.Printf("Issue: %s\n", result.FinalVerdict.Issue)
		}
	}
	fmt.Printf("Rounds: %d\n", len(result.Rounds))
	fmt.Printf("Duration: %s\n", duration.Round(time.Millisecond))
}

func printSession(session *store.Session, rounds []store.RoundRecord) {
	fmt.Printf("Session: %s\n", session.ID)
	fmt.Printf("Repo: %s\n", session.RepoRoot)
	fmt.Printf("Task: %s\n", session.Task)
	fmt.Printf("Status: %s\n", session.Status)
	fmt.Printf("Base Branch: %s\n", session.BaseBranch)
	fmt.Printf("Created: %s\n", session.CreatedAt.Format(time.RFC3339))
	if session.CompletedAt != nil {
		fmt.Printf("Completed: %s\n", session.CompletedAt.Format(time.RFC3339))
	}
	fmt.Printf("Tokens: %d prompt, %d completion\n",
		session.PromptTokens, session.CompletionTokens)

	if len(rounds) > 0 {
		fmt.Printf("\nRounds:\n")
		for _, r := range rounds {
			fmt.Printf("  Round %d: %s - %s\n", r.RoundNumber, r.Verdict, r.Summary)
			if r.Issue != "" {
				fmt.Printf("    Issue: %s\n", r.Issue)
			}
		}
	}
}

func printSessions(sessions []store.Session) {
	if len(sessions) == 0 {
		fmt.Println("No sessions found")
		return
	}

	fmt.Printf("%-30s %-12s %-20s %s\n", "ID", "Status", "Created", "Task")
	fmt.Println(strings.Repeat("-", 100))
	for _, s := range sessions {
		task := s.Task
		if len(task) > 40 {
			task = task[:37] + "..."
		}
		fmt.Printf("%-30s %-12s %-20s %s\n",
			s.ID,
			s.Status,
			s.CreatedAt.Format("2006-01-02 15:04"),
			task)
	}
}

func pipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Pipeline management commands",
		Long:  "Plan and execute multi-task pipelines for complex features.",
	}

	// Add subcommands
	cmd.AddCommand(pipelinePlanCmd())
	cmd.AddCommand(pipelineExecuteCmd())

	return cmd
}

func pipelinePlanCmd() *cobra.Command {
	var autoApprove bool

	cmd := &cobra.Command{
		Use:   "plan <feature description>",
		Short: "Plan a feature as a dependency-ordered task graph",
		Long:  "Uses the Planner agent to decompose a feature into tasks, validates the graph for cycles, and stores the plan.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runPipelinePlan(args[0], autoApprove)
		},
	}

	cmd.Flags().BoolVar(&autoApprove, "auto-approve", false, "Skip confirmation prompt and store the plan immediately")

	return cmd
}

func pipelineExecuteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "execute <pipeline-id>",
		Short: "Execute a planned pipeline",
		Long:  "Executes a stored pipeline plan sequentially, respecting task dependencies.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				log.Fatal("invalid pipeline ID:", err)
			}
			runPipelineExecute(id)
		},
	}
}

func runPipelinePlan(feature string, autoApprove bool) {
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatal("config:", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal("validate:", err)
	}

	// Hard-fail if planner is not configured
	if cfg.Planner.Model == "" || cfg.Planner.BaseURL == "" || cfg.Planner.APIKey == "" {
		log.Fatal("planner not configured: add [planner] section to marshal.toml (model, base_url, api_key)")
	}

	repoRoot, err := findGitRoot(".")
	if err != nil {
		log.Fatal("not a git repository:", err)
	}
	cfg.RepoRoot = repoRoot

	dbPath := cfg.Session.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(repoRoot, ".marshal", "sessions.db")
	} else if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(repoRoot, dbPath)
	}

	s, err := store.New(dbPath)
	if err != nil {
		log.Fatal("store init:", err)
	}
	defer s.Close()

	// Build planner backend using factory — always uses JSON output
	be, err := backend.FactoryForAgent(cfg.Planner.Provider, "planner", cfg.Planner.BaseURL, cfg.Planner.APIKey,
		cfg.Planner.Temperature, cfg.Planner.MaxTokens, true, cfg.Planner.ContextWindow)
	if err != nil {
		log.Fatalf("planner backend: %v", err)
	}

	p := planner.New(be, cfg.Planner)

	fmt.Printf("Planning: %s\n\n", feature)

	ctx := context.Background()
	result, err := p.Plan(ctx, feature)
	if err != nil {
		log.Fatalf("planning failed: %v", err)
	}

	// Output plan
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result.Graph)
	} else {
		printPlan(result.Graph)
		fmt.Printf("\nTokens: %d prompt, %d completion\n", result.PromptTokens, result.CompletionTokens)
	}

	// Confirmation prompt (unless --auto-approve or --json)
	if !autoApprove && !jsonOutput {
		fmt.Print("\nProceed? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return
		}
	}

	// Store the pipeline plan
	run := &store.PipelineRun{
		Feature:   feature,
		Status:    "PLANNED",
		PlanJSON:  result.RawJSON,
		CreatedAt: time.Now(),
	}
	if err := s.CreatePipelineRun(run); err != nil {
		log.Fatalf("store pipeline run: %v", err)
	}

	// Store individual tasks
	if err := s.CreatePipelineTasks(run.ID, result.Graph.Tasks); err != nil {
		log.Fatalf("store pipeline tasks: %v", err)
	}

	if !jsonOutput {
		fmt.Printf("Plan stored (id=%d). Execute with `marshal pipeline execute %d`.\n", run.ID, run.ID)
	}
}

func printPlan(g *planner.TaskGraph) {
	fmt.Printf("Feature: %s\n\n", g.Feature)
	fmt.Println("Tasks:")
	for _, t := range g.Tasks {
		deps := "(none)"
		if len(t.DependsOn) > 0 {
			deps = strings.Join(t.DependsOn, ", ")
		}
		files := "(unknown)"
		if len(t.FilesLikelyAffected) > 0 {
			files = strings.Join(t.FilesLikelyAffected, ", ")
		}
		skill := ""
		if t.Skill != "" {
			skill = fmt.Sprintf("  skill: %s", t.Skill)
		}
		fmt.Printf("  [%s] %s\n", t.ID, t.Description)
		fmt.Printf("       files: %s\n", files)
		fmt.Printf("       depends: %s%s\n", deps, skill)
	}

	tiers, err := planner.TopologicalSort(g)
	if err != nil {
		return
	}
	fmt.Println("\nExecution tiers:")
	for i, tier := range tiers {
		ids := make([]string, len(tier))
		for j, t := range tier {
			ids[j] = t.ID
		}
		fmt.Printf("  Tier %d: %s\n", i+1, strings.Join(ids, ", "))
	}
}

func runPipelineExecute(pipelineID int64) {
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatal("config:", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal("validate:", err)
	}

	repoRoot, err := findGitRoot(".")
	if err != nil {
		log.Fatal("not a git repository:", err)
	}
	cfg.RepoRoot = repoRoot

	dbPath := cfg.Session.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(repoRoot, ".marshal", "sessions.db")
	} else if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(repoRoot, dbPath)
	}

	s, err := store.New(dbPath)
	if err != nil {
		log.Fatal("store init:", err)
	}
	defer s.Close()

	// Load the pipeline run
	pipelineRun, err := s.GetPipelineRun(pipelineID)
	if err != nil {
		log.Fatalf("load pipeline run: %v", err)
	}

	if pipelineRun.Status == "DONE" {
		log.Fatal("pipeline already executed")
	}

	fmt.Printf("Executing pipeline %d: %s\n\n", pipelineID, pipelineRun.Feature)

	// Initialize git layer
	gitLayer, err := git.New(repoRoot)
	if err != nil {
		log.Fatal("git init:", err)
	}

	// Initialize Marshal orchestrator
	m := marshal.New(cfg, gitLayer, s, nil)

	// Create pipeline runner
	runner := pipeline.NewRunner(m, s, gitLayer)

	// Execute the pipeline
	ctx := context.Background()
	result, err := runner.Run(ctx, pipelineID, pipeline.ConsoleProgressHandler)

	fmt.Println()
	if err != nil {
		fmt.Printf("Pipeline failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Pipeline completed: %d/%d tasks succeeded in %v\n",
		result.TasksDone, result.TasksTotal, result.Duration)
}

// findGitRoot walks up the directory tree to find the .git directory.
func findGitRoot(start string) (string, error) {
	absPath, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	for {
		gitDir := filepath.Join(absPath, ".git")
		info, err := os.Stat(gitDir)
		if err == nil && info.IsDir() {
			return absPath, nil
		}

		parent := filepath.Dir(absPath)
		if parent == absPath {
			break
		}
		absPath = parent
	}

	return "", fmt.Errorf("no .git directory found starting from %s", start)
}

// generateSessionID creates a unique session identifier.
func generateSessionID() string {
	return fmt.Sprintf("%d-%s", time.Now().Unix(), randomHex(8))
}

// randomHex generates random hex string of given byte length.
func randomHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(time.Now().UnixNano() >> uint(i*8))
	}
	return fmt.Sprintf("%x", b)[:n*2]
}
