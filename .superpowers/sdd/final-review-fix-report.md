# Final Review Fix Report

## What I implemented

- Updated `internal/app/app.go` to use `ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) error`.
- Wired the default Bubble Tea runner through `tea.WithContext(ctx)` so app cancellation now propagates into the running TUI.
- Added an early `ctx.Err()` check before any working-directory or config work so already-cancelled runs exit without startup side effects.
- Added a narrow config seam with `type configLoader func(config.LoadOptions) (config.Config, error)` plus `WithConfigLoader(loader configLoader) Option`.
- Switched `Run` to use the injected config loader, defaulting to `config.Load`.
- Guarded `WithProgramRunner(nil)` so a nil option does not replace the active runner.
- Ensured session state is shut down both when the app context is cancelled and when `Run` exits normally.

## What I tested and test results

- `GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app -count=1`
  - Result: PASS
- `GOCACHE=/private/tmp/coder-agent-gocache go test ./... -count=1`
  - Result: PASS

Covered behaviors in `internal/app/app_test.go`:

- cancelled context skips program startup and does not call the config loader
- runner receives the app context and observes cancellation
- `WithProgramRunner(nil)` does not break later runner configuration
- injected config load errors are returned
- existing runner start behavior still works

## TDD Evidence: RED and GREEN commands/output

### RED

Command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app -count=1
```

Output:

```text
# marshal/internal/app [marshal/internal/app.test]
internal/app/app_test.go:24:3: undefined: WithConfigLoader
internal/app/app_test.go:28:21: cannot use func(ctx context.Context, model tea.Model, output io.Writer) error {…} (value of type func(ctx context.Context, model tea.Model, output io.Writer) error) as ProgramRunner value in argument to WithProgramRunner
internal/app/app_test.go:50:3: undefined: WithConfigLoader
internal/app/app_test.go:53:21: cannot use func(ctx context.Context, model tea.Model, output io.Writer) error {…} (value of type func(ctx context.Context, model tea.Model, output io.Writer) error) as ProgramRunner value in argument to WithProgramRunner
internal/app/app_test.go:80:4: undefined: WithConfigLoader
internal/app/app_test.go:83:22: cannot use func(runCtx context.Context, model tea.Model, output io.Writer) error {…} (value of type func(runCtx context.Context, model tea.Model, output io.Writer) error) as ProgramRunner value in argument to WithProgramRunner
internal/app/app_test.go:116:3: undefined: WithConfigLoader
internal/app/app_test.go:120:21: cannot use func(ctx context.Context, model tea.Model, output io.Writer) error {…} (value of type func(ctx context.Context, model tea.Model, output io.Writer) error) as ProgramRunner value in argument to WithProgramRunner
internal/app/app_test.go:138:3: undefined: WithConfigLoader
internal/app/app_test.go:141:21: cannot use func(ctx context.Context, model tea.Model, output io.Writer) error {…} (value of type func(ctx context.Context, model tea.Model, output io.Writer) error) as ProgramRunner value in argument to WithProgramRunner
internal/app/app_test.go:141:21: too many errors
FAIL	marshal/internal/app [build failed]
FAIL
```

### GREEN

Command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app -count=1
```

Output:

```text
ok  	marshal/internal/app	0.670s
```

Final verification command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./... -count=1
```

Output:

```text
?   	marshal/cmd/marshal	[no test files]
ok  	marshal/internal/app	0.285s
ok  	marshal/internal/app/config	0.751s
?   	marshal/internal/app/logging	[no test files]
ok  	marshal/internal/app/session	1.135s
ok  	marshal/internal/app/tui	1.608s
```

## Files changed

- `internal/app/app.go`
- `internal/app/app_test.go`
- `.superpowers/sdd/final-review-fix-report.md`

## Self-review findings

- The cancellation bridge now reaches Bubble Tea through the program context instead of relying only on model quit paths.
- The early cancellation check prevents filesystem and config side effects for cancelled startup, which makes the app test deterministic against user-local config state.
- The new seams stay narrow and local to `internal/app`, which matches the ownership guidance.
- The session shutdown bridge uses a `done` channel so the cancellation goroutine does not outlive normal `Run` completion.

## Any issues or concerns

- No functional concerns from the change set.
