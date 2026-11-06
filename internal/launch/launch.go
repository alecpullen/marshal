// Package launch is embedded mode's composition root (P3 R1): it runs the
// TUI over the agent side, in process, and owns the loop around it.
package launch

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/app"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/tui"
)

// ProgramResult is the value returned by a ProgramRunner. It carries the
// program's outcome plus an optional request to resume another session.
type ProgramResult struct {
	Err           error
	ResumeSession string // non-empty => tear down and restart WithExistingSession
}

// ProgramRunner runs the TUI program. The default runs a tea.Program; tests
// inject a runner that returns a ProgramResult directly.
type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult

type options struct {
	programRunner ProgramRunner
	appOpts       []app.Option
}

type Option func(*options)

// WithProgramRunner replaces the program runner. A nil runner is ignored.
func WithProgramRunner(runner ProgramRunner) Option {
	return func(opts *options) {
		if runner == nil {
			return
		}
		opts.programRunner = runner
	}
}

// WithApp passes runtime options through to app.NewEmbedded.
func WithApp(opts ...app.Option) Option {
	return func(o *options) {
		o.appOpts = append(o.appOpts, opts...)
	}
}

// Run runs embedded marshal until the user quits.
func Run(ctx context.Context, stdout io.Writer, opts ...Option) error {
	if ctx.Err() != nil {
		return nil
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Run's own program runner: the caller's, defaulting to the Bubble Tea
	// program. NewEmbedded applies the app options exactly once.
	runOpts := options{programRunner: runProgram}
	for _, opt := range opts {
		opt(&runOpts)
	}

	embedded, err := app.NewEmbedded(ctx, runOpts.appOpts...)
	if err != nil {
		return err
	}
	defer embedded.Close(context.Background())

	for {
		rt, err := embedded.Start(ctx)
		if err != nil {
			return err
		}

		// The seam is built from the runtime's own service deps plus the
		// startup trust prompt and its decision, which belong to the
		// embedding.
		seamHost, seamSession := inproc.New(embedded.SessionDeps(ctx))

		var tuiOpts []tui.Option
		// F18: eager-seed the @file completion popup with the repo file
		// index, read through the session so the popup and the lazy load
		// share one path. Failures (no DB, empty index) are non-fatal — the
		// TUI falls back to a lazy load on the first @-keystroke.
		if filePaths, ferr := seamSession.FileIndex(ctx); ferr == nil && len(filePaths) > 0 {
			tuiOpts = append(tuiOpts, tui.WithFileIndex(filePaths))
		}
		if embedded.FirstRun() {
			tuiOpts = append(tuiOpts, tui.WithOpenConnectOnStart())
		}
		// Config reloading stays wired even when the initial provider build
		// failed: /settings, /set and /connect are the recovery path, and they
		// reach the reload through deps.ReloadConfig above. A successful
		// reload rebuilds the runtime in place, and the session reads the
		// runtime's current runner from the next turn on, so a rebuilt runner
		// needs no adoption here.
		tuiOpts = append(tuiOpts, tui.WithHomeDir(embedded.HomeDir()))
		tuiOpts = append(tuiOpts, tui.WithWorkingDir(embedded.WorkingDir()))
		// Copy prefers a local clipboard helper and falls back to OSC 52 over
		// SSH or when no helper resolves.
		tuiOpts = append(tuiOpts, tui.WithClipboard(
			&clipboard.LocalWriter{},
			func() bool { return clipboard.Remote(os.Getenv) },
		))
		tuiOpts = append(tuiOpts, tui.WithContext(ctx))
		tuiOpts = append(tuiOpts, tui.WithLogger(rt.State.Logger()))

		select {
		case <-ctx.Done():
			return nil
		default:
		}

		progResult := runOpts.programRunner(ctx, tui.New(seamHost, seamSession, tuiOpts...), stdout)

		if progResult.ResumeSession != "" && progResult.Err == nil {
			// User chose to resume a different session. Tear down the
			// current runtime (the current transcript is already persisted,
			// and that session is marked ended when it is eventually exited)
			// and restart startRuntime in
			// existing-session mode.
			embedded.Resume(progResult.ResumeSession)
			continue
		}

		if embedded.TakeTrustReload() {
			continue
		}
		return embedded.Finish(progResult.Err)
	}
}

func runProgram(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
	program := tea.NewProgram(model,
		tea.WithOutput(output),
		tea.WithContext(ctx),
	)
	finalModel, err := program.Run()
	res := ProgramResult{Err: err}
	if signaller, ok := finalModel.(interface{ ResumeSession() string }); ok {
		res.ResumeSession = signaller.ResumeSession()
	}
	return res
}
