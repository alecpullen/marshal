What I implemented

- Added `internal/app/logging/logging.go` with `logging.New(w io.Writer, level slog.Level) *slog.Logger` using `slog.NewTextHandler`.
- Added `internal/app/app.go` with `app.Run(ctx context.Context, stdout io.Writer, stderr io.Writer, opts ...Option) error`.
- Added `WithNow` option support to inject time for tests.
- Wired `app.Run` to:
  - install signal-aware context cancellation,
  - resolve the working directory,
  - load config with `config.Load`,
  - create a logger with `logging.New`,
  - create and shut down session state with `session.New`,
  - log startup metadata,
  - return cleanly when context is cancelled or the session is done,
  - otherwise print `Marshal` to stdout.
- Added `internal/app/app_test.go` covering immediate return on cancelled context.
- Updated `cmd/marshal/main.go` to delegate to `app.Run` and print `marshal: <error>` to stderr on failure before exiting 1.

What I tested and test results

- `GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app -run TestRunReturnsWhenContextIsCancelled -count=1`
  - RED: failed as expected when `Run` and `WithNow` were undefined.
  - GREEN: passed after implementation.
- `GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app ./cmd/marshal -count=1`
  - Passed.
- `GOCACHE=/private/tmp/coder-agent-gocache go test ./... -count=1`
  - Passed across the repository.

TDD Evidence: RED and GREEN commands/output

RED 1

Command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app -run TestRunReturnsWhenContextIsCancelled -count=1
```

Output:

```text
# marshal/internal/app [marshal/internal/app.test]
internal/app/app_test.go:6:2: "io" imported and not used
internal/app/app_test.go:15:9: undefined: Run
internal/app/app_test.go:15:62: undefined: WithNow
FAIL	marshal/internal/app [build failed]
FAIL
```

Note: the brief's initial test snippet included an unused `io` import, so I removed that test-only issue to get a clean failing signal for the missing production API.

RED 2

Command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app -run TestRunReturnsWhenContextIsCancelled -count=1
```

Output:

```text
# marshal/internal/app [marshal/internal/app.test]
internal/app/app_test.go:14:9: undefined: Run
internal/app/app_test.go:14:62: undefined: WithNow
FAIL	marshal/internal/app [build failed]
FAIL
```

GREEN 1

Command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app -run TestRunReturnsWhenContextIsCancelled -count=1
```

Output:

```text
ok  	marshal/internal/app	0.819s
```

GREEN 2

Command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app ./cmd/marshal -count=1
```

Output:

```text
ok  	marshal/internal/app	0.599s
?   	marshal/cmd/marshal	[no test files]
```

Final verification

Command:

```bash
GOCACHE=/private/tmp/coder-agent-gocache go test ./... -count=1
```

Output:

```text
?   	marshal/cmd/marshal	[no test files]
ok  	marshal/internal/app	0.202s
ok  	marshal/internal/app/config	0.644s
?   	marshal/internal/app/logging	[no test files]
ok  	marshal/internal/app/session	1.160s
```

Files changed

- `internal/app/logging/logging.go`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `cmd/marshal/main.go`
- `.superpowers/sdd/task-5-report.md`

Self-review findings

- The implementation matches the brief’s requested API and control flow.
- The app test is intentionally narrow and only asserts the cancelled-context case, which is exactly what the brief specified.
- `Run` loads config before checking the already-cancelled context because the brief’s implementation order does so; this means startup side effects still occur before the early return path.

Any issues or concerns

- The task brief’s starter test snippet had an unused `io` import. I removed it so the RED step would fail for the intended missing symbols rather than an unrelated compile error.
