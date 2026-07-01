package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
)

type options struct {
	now func() time.Time
}

type Option func(*options)

func WithNow(now func() time.Time) Option {
	return func(opts *options) {
		opts.now = now
	}
}

func Run(ctx context.Context, stdout io.Writer, stderr io.Writer, opts ...Option) error {
	runOpts := options{now: time.Now}
	for _, opt := range opts {
		opt(&runOpts)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
	}

	cfg, err := config.Load(config.LoadOptions{WorkingDir: workingDir})
	if err != nil {
		return err
	}

	logger := logging.New(stderr, slog.LevelInfo)
	state := session.New(cfg, workingDir, runOpts.now())
	defer state.Shutdown()

	logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

	select {
	case <-ctx.Done():
		return nil
	case <-state.Done():
		return nil
	default:
		_, _ = fmt.Fprintln(stdout, "Marshal")
		return nil
	}
}
