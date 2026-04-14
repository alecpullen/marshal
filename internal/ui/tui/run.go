package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/loop"
	"github.com/alecpullen/marshal/internal/prompts"
	"github.com/alecpullen/marshal/internal/session"
	"github.com/alecpullen/marshal/internal/skills"
	"github.com/alecpullen/marshal/internal/watch"
)

// Run starts the interactive TUI. It blocks until the user quits (Ctrl+C /
// Esc). All tasks that pass are on the staging branch; the caller ships them.
func Run(
	ctx context.Context,
	cfg *config.Config,
	repo *git.Repo,
	gitSess *git.Session,
	store *session.Store,
	reg *backend.Registry,
	skillsReg *skills.Registry,
) error {
	sessID, err := insertSession(store, gitSess)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	marshalB, err := reg.For(config.RoleMarshal)
	if err != nil {
		return fmt.Errorf("marshal backend: %w", err)
	}

	// runGate calls the Marshal model and returns (action, text, error).
	// action is "proceed", "chat", or "clarify".
	runGate := func(ctx context.Context, prompt string) (string, string, error) {
		return callMarshalGate(ctx, marshalB, prompt)
	}

	// Get read-only files from context or store
	var readOnlyFiles []string
	if store != nil {
		readOnlyFiles, _ = store.GetReadOnlyFiles(sessID)
	}

	// runEngine builds a fresh Engine for each task so that chatFiles and
	// readOnlyFiles reflect the TUI's current state at submission time.
	runEngine := func(ctx context.Context, prompt string, sink loop.Sink, executorExtra, criticExtra string, chatFiles, roFiles []string, readOnly bool) error {
		eng := loop.New(
			loop.Config{
				MaxRounds:      cfg.Loop.MaxRounds,
				CompactAfter:   cfg.Loop.CompactAfter,
				SessionID:      sessID,
				GitEnabled:     cfg.Git.Enabled,
				ChatFiles:      chatFiles,
				ReadOnlyFiles:  roFiles,
				LinterCfg:      cfg.Linters,
				EditFormat:     cfg.Loop.EditFormat,
				ExecutorExtra:  executorExtra,
				CriticExtra:    criticExtra,
				ReadOnly:       readOnly,
				Permission:     cfg.Loop.Permission,
				ShowDiff:       cfg.Loop.ShowDiff,
				PreApplyReview: cfg.PreApplyReview,
			},
			repo, gitSess, store, reg, sink,
		)
		return eng.Run(ctx, prompt)
	}

	pref := &progRef{}

	// Create watch manager (not started until /watch command)
	var watchMgr *watch.Manager
	if repo != nil {
		ig, _ := git.LoadMarshalIgnore(repo.Root())
		watchMgr, _ = watch.NewManager(repo, ig)
	}

	m := newModel(ctx, runGate, runEngine, skillsReg, store, sessID, repo.Root(), readOnlyFiles, watchMgr, pref, cfg, gitSess, repo, marshalB)

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	pref.p = p // set before p.Run() so startEngine can use it

	_, err = p.Run()
	return err
}

// callMarshalGate calls the Marshal model with prompt and parses its response
// into one of three actions: "proceed", "chat", or "clarify".
//
// The Marshal prompt instructs the model to output:
//   - "PROCEED"          → route to the executor loop
//   - "CHAT: <text>"     → conversational reply, shown directly
//   - anything else      → clarifying question, shown directly
func callMarshalGate(ctx context.Context, b backend.Backend, prompt string) (action, text string, err error) {
	msgs := []backend.Message{
		{Role: backend.MessageRoleSystem, Content: prompts.Marshal},
		{Role: backend.MessageRoleUser, Content: prompt},
	}
	resp, err := b.Complete(ctx, backend.Request{Messages: msgs})
	if err != nil {
		return "", "", err
	}

	raw := strings.TrimSpace(resp.Content)

	if raw == "PROCEED" {
		return "proceed", "", nil
	}
	if strings.HasPrefix(raw, "CHAT:") {
		return "chat", strings.TrimSpace(strings.TrimPrefix(raw, "CHAT:")), nil
	}
	// Clarifying question or anything else unexpected — show it to the user.
	return "clarify", raw, nil
}

// OpenStore opens (or creates) the marshal SQLite database in the repo root.
// It also ensures .marshal/ is in .git/info/exclude.
func OpenStore(repoRoot string) (*session.Store, error) {
	dbDir := filepath.Join(repoRoot, ".marshal")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, err
	}
	ensureExcluded(repoRoot)
	return session.Open(filepath.Join(dbDir, "sessions.db"))
}

// ensureExcluded adds ".marshal/" to .git/info/exclude so that git never
// tracks SQLite WAL/SHM files, which would cause branch-switch failures.
func ensureExcluded(repoRoot string) {
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

func insertSession(store *session.Store, gitSess *git.Session) (string, error) {
	id := fmt.Sprintf("%x", time.Now().UnixNano()&0xffffffff)
	rec := &session.Session{ID: id, StartedAt: time.Now()}
	if gitSess != nil {
		rec.TargetBranch = gitSess.TargetBranch
		rec.TargetStartSHA = gitSess.TargetStartSHA
		rec.StagingBranch = gitSess.StagingBranch
	}
	return id, store.InsertSession(rec)
}
