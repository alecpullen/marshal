package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/db"
)

type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) error
type configLoader func(config.LoadOptions) (config.Config, error)

type options struct {
	now           func() time.Time
	configLoader  configLoader
	programRunner ProgramRunner
}

type Option func(*options)

func WithNow(now func() time.Time) Option {
	return func(opts *options) {
		opts.now = now
	}
}

func WithProgramRunner(runner ProgramRunner) Option {
	return func(opts *options) {
		if runner == nil {
			return
		}
		opts.programRunner = runner
	}
}

func WithConfigLoader(loader configLoader) Option {
	return func(opts *options) {
		if loader == nil {
			return
		}
		opts.configLoader = loader
	}
}

func dbPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "marshal.db")
}

func Run(ctx context.Context, stdout io.Writer, stderr io.Writer, opts ...Option) error {
	if ctx.Err() != nil {
		return nil
	}

	runOpts := options{
		now:           time.Now,
		configLoader:  config.Load,
		programRunner: runProgram,
	}
	for _, opt := range opts {
		opt(&runOpts)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
	}

	cfg, err := runOpts.configLoader(config.LoadOptions{WorkingDir: workingDir})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(workingDir, ".marshal"), 0755); err != nil {
		return fmt.Errorf("create .marshal directory: %w", err)
	}

	database, err := db.Open(dbPath(workingDir))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	projectID, err := database.GetOrCreateProject(workingDir, cfg.Project.Name)
	if err != nil {
		return fmt.Errorf("get or create project: %w", err)
	}

	sessionID := fmt.Sprintf("sess_%d", runOpts.now().UnixNano())
	if err := database.CreateSession(sessionID, projectID, "", runOpts.now()); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	logger := logging.New(stderr, slog.LevelInfo)
	state := session.New(cfg, workingDir, runOpts.now(), session.Persistence{DB: database, SessionID: sessionID, Logger: logger})
	done := make(chan struct{})
	defer close(done)
	defer state.Shutdown()
	go func() {
		select {
		case <-ctx.Done():
			state.Shutdown()
		case <-done:
		}
	}()

	logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

	select {
	case <-ctx.Done():
		return nil
	default:
	}

	return runOpts.programRunner(ctx, tui.New(state), stdout)
}

func runProgram(ctx context.Context, model tea.Model, output io.Writer) error {
	program := tea.NewProgram(model, tea.WithOutput(output), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}
