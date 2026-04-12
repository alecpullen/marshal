package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/config"
	"github.com/alec/marshal/internal/git"
	"github.com/alec/marshal/internal/loop"
	"github.com/alec/marshal/internal/prompts"
	"github.com/alec/marshal/internal/session"
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

	runEngine := func(ctx context.Context, prompt string, sink loop.Sink) error {
		eng := loop.New(
			loop.Config{
				MaxRounds:  cfg.Loop.MaxRounds,
				SessionID:  sessID,
				GitEnabled: cfg.Git.Enabled,
			},
			repo, gitSess, store, reg, sink,
		)
		return eng.Run(ctx, prompt)
	}

	pref := &progRef{}
	m := newModel(ctx, runGate, runEngine, pref)

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
