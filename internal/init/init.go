// Package initmgr provides repository initialization for Marshal.
// It sets up the .marshal directory, session store, repomap cache, and
// integrates with the git and session systems.
package initmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/repomap"
	"github.com/alecpullen/marshal/internal/session"
)

// Result contains the outcome of an initialization.
type Result struct {
	RepoRoot      string
	MarshalDir    string
	SessionDBPath string
	RepoMapCache  string
	RepoMapPath   string
	RepoMapLines  int
	FilesIndexed  int
	SessionID     string
	ContextPath   string
	GitSession    *git.Session
	GitEnabled    bool
	ConfigCreated bool
	ConfigPath    string
}

// Options controls the behavior of initialization.
type Options struct {
	// SkipGit disables git integration setup.
	SkipGit bool
	// SkipRepoMap skips repository map generation.
	SkipRepoMap bool
	// SkipConfig skips creating a sample config file.
	SkipConfig bool
	// Force recreates existing directories/files.
	Force bool
}

// Init initializes a repository for Marshal use.
// It creates the .marshal directory, sets up the session store,
// generates a repository map, and optionally creates a sample config.
func Init(repoRoot string, opts Options) (*Result, error) {
	result := &Result{
		RepoRoot:   repoRoot,
		MarshalDir: filepath.Join(repoRoot, ".marshal"),
	}

	// 1. Create .marshal directory
	if err := os.MkdirAll(result.MarshalDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating .marshal directory: %w", err)
	}

	// 2. Ensure .marshal is in git exclude
	ensureMarshalExcluded(repoRoot)

	// 3. Open/create session database
	dbPath := filepath.Join(result.MarshalDir, "sessions.db")
	store, err := session.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening session store: %w", err)
	}
	defer store.Close()
	result.SessionDBPath = dbPath

	// 4. Create a new session record
	sessionID := fmt.Sprintf("%x", time.Now().UnixNano()&0xffffffff)
	result.SessionID = sessionID

	sess := &session.Session{
		ID:        sessionID,
		StartedAt: time.Now(),
	}

	// 5. Set up git integration if available and not skipped
	if !opts.SkipGit {
		repo, err := git.New(repoRoot, git.RepoConfig{})
		if err == nil {
			gitSess := git.NewSession(repo, git.SessionOptions{})
			if err := gitSess.Start(); err == nil {
				result.GitSession = gitSess
				result.GitEnabled = true
				sess.TargetBranch = gitSess.TargetBranch
				sess.TargetStartSHA = gitSess.TargetStartSHA
				sess.StagingBranch = gitSess.StagingBranch
			}
		}
	}

	// Insert session record
	if err := store.InsertSession(sess); err != nil {
		return nil, fmt.Errorf("inserting session: %w", err)
	}

	// 6. Generate repository map if not skipped
	if !opts.SkipRepoMap {
		ig, _ := git.LoadMarshalIgnore(repoRoot)
		rm, err := repomap.Build(repoRoot, ig, repomap.Options{
			MaxFiles:          50,
			MaxSymbolsPerFile: 10,
		})
		if err == nil && rm != nil {
			result.RepoMapCache = rm.String()
			result.RepoMapLines = strings.Count(result.RepoMapCache, "\n") + 1
			// Count files in the map
			for _, section := range rm.Sections {
				if len(section.Symbols) > 0 {
					result.FilesIndexed++
				}
			}
			// Persist repo map to disk
			repoMapPath := filepath.Join(result.MarshalDir, "repomap.txt")
			if err := os.WriteFile(repoMapPath, []byte(result.RepoMapCache), 0o644); err == nil {
				result.RepoMapPath = repoMapPath
			}
		}
	}

	// 7. Create session context file for AI awareness
	ctxPath := filepath.Join(result.MarshalDir, "session_context.md")
	if err := createSessionContext(ctxPath, result, sess); err == nil {
		result.ContextPath = ctxPath
	}

	// 8. Create sample config if not skipped and doesn't exist
	if !opts.SkipConfig {
		configPath := filepath.Join(repoRoot, "marshal.toml")
		if opts.Force || !fileExists(configPath) {
			if err := createSampleConfig(configPath); err == nil {
				result.ConfigCreated = true
				result.ConfigPath = configPath
			}
		}
	}

	return result, nil
}

// ensureMarshalExcluded adds ".marshal/" to .git/info/exclude so that git never
// tracks SQLite WAL/SHM files created inside the .marshal/ directory.
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
	if len(data) > 0 && data[len(data)-1] != '\n' {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, ".marshal/")
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// createSampleConfig creates a sample marshal.toml configuration file.
func createSampleConfig(path string) error {
	content := `# Marshal Configuration
# See marshal.toml.example for full documentation

[model.executor]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"
supports_tools = true

[model.critic]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"

[model.marshal]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"

[loop]
max_rounds = 3

[git]
enabled = true
`
	return os.WriteFile(path, []byte(content), 0o644)
}

// createSessionContext creates a session context file for AI awareness.
func createSessionContext(path string, result *Result, sess *session.Session) error {
	var sb strings.Builder
	sb.WriteString("# Marshal Session Context\n\n")
	sb.WriteString(fmt.Sprintf("**Session ID:** %s\n\n", sess.ID))
	sb.WriteString(fmt.Sprintf("**Started:** %s\n\n", sess.StartedAt.Format(time.RFC3339)))

	if result.GitEnabled && result.GitSession != nil {
		sb.WriteString("## Git Status\n\n")
		sb.WriteString(fmt.Sprintf("- **Target Branch:** %s\n", result.GitSession.TargetBranch))
		sb.WriteString(fmt.Sprintf("- **Staging Branch:** %s\n", result.GitSession.StagingBranch))
		sb.WriteString(fmt.Sprintf("- **Target Start SHA:** %s\n\n", result.GitSession.TargetStartSHA[:8]))
	}

	if result.RepoMapLines > 0 {
		sb.WriteString("## Repository Map\n\n")
		sb.WriteString(fmt.Sprintf("- **Files Indexed:** %d\n", result.FilesIndexed))
		sb.WriteString(fmt.Sprintf("- **Map Location:** `.marshal/repomap.txt`\n"))
		sb.WriteString(fmt.Sprintf("- **Lines:** %d\n\n", result.RepoMapLines))
		sb.WriteString("### Quick Reference\n\n")
		sb.WriteString("The repository map contains a PageRank-ranked list of files and their symbols. ")
		sb.WriteString("Use `/map` to view it in the TUI.\n\n")
	}

	sb.WriteString("## Database\n\n")
	sb.WriteString(fmt.Sprintf("- **Path:** `%s`\n", result.SessionDBPath))
	sb.WriteString("- **Tables:** sessions, tasks, rounds, read_only_files\n\n")

	sb.WriteString("## Available Commands\n\n")
	sb.WriteString("Key commands for session management:\n\n")
	sb.WriteString("- `/session` - Show current session info\n")
	sb.WriteString("- `/history` - Show task history for this session\n")
	sb.WriteString("- `/map` - Show repository map\n")
	sb.WriteString("- `/ship` - Merge staging to target branch\n")
	sb.WriteString("- `/add <file>` - Add file to context\n\n")

	sb.WriteString("---\n\n")
	sb.WriteString("*This file is auto-generated during `/init`. Update with `/map-refresh`.*\n")

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// Status returns a formatted status string of the initialization result.
func (r *Result) Status() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Initialized Marshal in %s\n", r.RepoRoot))
	sb.WriteString(fmt.Sprintf("  Session ID: %s\n", r.SessionID))
	sb.WriteString(fmt.Sprintf("  Database: %s\n", r.SessionDBPath))
	if r.GitEnabled && r.GitSession != nil {
		sb.WriteString(fmt.Sprintf("  Git: enabled\n"))
		sb.WriteString(fmt.Sprintf("    Target:  %s\n", r.GitSession.TargetBranch))
		sb.WriteString(fmt.Sprintf("    Staging: %s\n", r.GitSession.StagingBranch))
	} else {
		sb.WriteString(fmt.Sprintf("  Git: not available\n"))
	}
	if r.RepoMapLines > 0 {
		sb.WriteString(fmt.Sprintf("  Repo map: %d files indexed, %d lines\n", r.FilesIndexed, r.RepoMapLines))
		sb.WriteString(fmt.Sprintf("  Repo map file: .marshal/repomap.txt\n"))
	}
	if r.ContextPath != "" {
		sb.WriteString(fmt.Sprintf("  Session context: .marshal/session_context.md\n"))
	}
	if r.ConfigCreated {
		sb.WriteString(fmt.Sprintf("  Config: created %s\n", r.ConfigPath))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Summary returns a brief one-line summary of the initialization.
func (r *Result) Summary() string {
	parts := []string{
		fmt.Sprintf("session %s", r.SessionID),
	}
	if r.GitEnabled {
		parts = append(parts, "git enabled")
	}
	if r.FilesIndexed > 0 {
		parts = append(parts, fmt.Sprintf("%d files mapped", r.FilesIndexed))
	}
	if r.ConfigCreated {
		parts = append(parts, "config created")
	}
	return fmt.Sprintf("Marshal initialized: %s", strings.Join(parts, ", "))
}
