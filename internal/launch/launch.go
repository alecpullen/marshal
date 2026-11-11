// Package launch is embedded mode's composition root (P3 R1): it runs the
// TUI over the agent host on an in-memory pipe, and owns the loop around it.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/app"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/tui"
)

// ProgramResult is the value returned by a ProgramRunner. It carries the
// program's outcome plus an optional request to resume another session.
type ProgramResult struct {
	Err           error
	ResumeSession string // non-empty => tear down and restart WithExistingSession
	Detached      string // the session a detach left running; "" otherwise
}

// ProgramRunner runs the TUI program. The default runs a tea.Program; tests
// inject a runner that returns a ProgramResult directly.
type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult

type options struct {
	programRunner ProgramRunner
	appOpts       []app.Option
	// agentConnWrap wraps the agent's end of each pass's pipe; tests use it to
	// close the connection under the program.
	agentConnWrap func(io.ReadWriteCloser) io.ReadWriteCloser
	// onServed hands each pass's service pair to the test before the program
	// runs.
	onServed func(*service.Host, *service.Session)

	// daemonSocket is the local daemon Run tries first; "" runs embedded.
	daemonSocket string
	// daemon replaces the client built for the socket; tests inject it.
	daemon daemonClient
	// workingDir replaces os.Getwd for daemon mode's project lookup.
	workingDir string
	// agentVersion replaces acp.AgentVersion as this build's version.
	agentVersion string
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

	// P4 D5: a local daemon that answers serves the TUI over its relay, and
	// the project needs no trust question to be served by it (decision 2).
	// Anything else runs embedded, exactly as it did before.
	if runOpts.daemonSocket != "" {
		if _, _, onDaemon := chooseDaemon(ctx, &runOpts); onDaemon {
			return runDaemon(ctx, stdout, &runOpts, "")
		}
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

		// The pair is built from the runtime's own service deps plus the
		// startup trust prompt and its decision, which belong to the
		// embedding. The TUI reaches it through marshal acp's agent host on
		// one end of an in-memory pipe, as one ACP client of its own agent.
		seamHost, seamSession := service.New(embedded.SessionDeps(ctx))
		if runOpts.onServed != nil {
			runOpts.onServed(seamHost, seamSession)
		}
		conn, err := dialAgent(ctx, rt, seamHost, seamSession, runOpts.agentConnWrap)
		if err != nil {
			return err
		}

		var tuiOpts []tui.Option
		// F18: eager-seed the @file completion popup with the repo file
		// index, read through the session so the popup and the lazy load
		// share one path. Failures (no DB, empty index) are non-fatal — the
		// TUI falls back to a lazy load on the first @-keystroke.
		if filePaths, ferr := conn.sess.FileIndex(ctx); ferr == nil && len(filePaths) > 0 {
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
			closeCtx, closeCancel := context.WithTimeout(context.Background(), serverStopTimeout)
			_ = conn.close(closeCtx)
			closeCancel()
			return nil
		default:
		}

		// The program's context ends when the agent's connection does, so a
		// connection that closes ends the program with the agent's error (R11).
		progCtx, progCancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-conn.lost():
				progCancel()
			case <-progCtx.Done():
			}
		}()

		progResult := runOpts.programRunner(progCtx, tui.New(conn.host, conn.sess, tuiOpts...), stdout)
		progCancel()

		// The service is released before the runtime closes, as the ACP host
		// closes a session (decision 7).
		closeCtx, closeCancel := context.WithTimeout(context.Background(), serverStopTimeout)
		if cerr := conn.close(closeCtx); cerr != nil {
			loggerOf(rt).Warn("launch: closing the connection", "error", cerr)
		}
		closeCancel()

		// Read the loss after the close: close also records a connection that
		// died in the window between a look beforehand and its own claim, which
		// a look beforehand would miss. A shutdown the TUI asked for is never a
		// loss, because close claims the connection before it closes it.
		if conn.connectionLost() {
			agentErr := conn.err()
			if agentErr == nil {
				agentErr = errors.New("the connection closed")
			}
			return embedded.Finish(errors.Join(progResult.Err, fmt.Errorf("launch: the agent's connection closed: %w", agentErr)))
		}

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
	if detacher, ok := finalModel.(interface{ Detached() string }); ok {
		res.Detached = detacher.Detached()
	}
	return res
}
