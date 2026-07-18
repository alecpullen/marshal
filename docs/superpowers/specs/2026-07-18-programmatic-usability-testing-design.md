# Programmatic Usability Testing for Marshal — Design

**Status:** Draft, pending implementation plan.  
**Goal:** Run automated usability studies on Marshal without human beta testers, producing CI regression checks, benchmark reports, and qualitative friction logs.

## 1. Background & Scope

Marshal is a local-first terminal coding agent with a Bubble Tea TUI (`internal/app/tui`), an agent loop (`internal/agent`), and a headless ACP JSON-RPC mode (`internal/acp`). Existing telemetry work (`docs/superpowers/specs/2026-07-07-turn-telemetry-and-evals-design.md`) measures the agent loop; existing TUI work (`docs/superpowers/plans/2026-07-11-tui-usability-overhaul.md`) improves the interface. This design adds a **synthetic-tester harness** that drives the real TUI through a PTY and scores both task success and interface friction.

### In scope

- PTY-based harness that launches the real `marshal` binary.
- Two reusable actor types:
  - **Scripted actors** for deterministic UI walkthroughs.
  - **LLM actors** for open-ended coding tasks on fixture repositories.
- Screen-state parser that extracts actionable TUI signals.
- Scenario definitions, success criteria, and scoring.
- Reporting: CI regression artifacts, benchmark scorecard, friction log.

### Out of scope

- Replacing existing ACP headless evals. The PTY harness is complementary; ACP remains the fast deterministic lane.
- Browser-based testing (Marshal is a terminal app).
- Collecting telemetry from real users.

## 2. Requirements

1. **Real binary, real TUI**: tests run the compiled `marshal` binary inside a PTY so rendering, keybindings, and approval flows are authentic.
2. **Two suites, one harness**: UI walkthroughs and coding tasks share PTY lifecycle and reporting.
3. **Local-first default**: the LLM actor uses local Ollama by default; CI can override via environment.
4. **Deterministic regressions**: scripted tests must be repeatable and CI-friendly.
5. **Benchmark output**: every run produces machine-readable metrics (success rate, duration, keystrokes, approvals, help usage).
6. **Friction logging**: human-readable log with screen snapshots and actor reasoning for failures.
7. **Isolation**: each scenario gets a fresh process, temp directory, and database.

## 3. Architecture

Add a new top-level test harness tree:

```
test/usability/
├── harness/      # PTY lifecycle: start marshal, send keys, capture screen
├── screen/       # ANSI parser → structured TUI state
├── actor/        # actor implementations
│   ├── scripted/ # deterministic UI walkthroughs
│   └── llm/      # LLM-driven coding tasks
├── scenario/     # scenario definitions + success criteria
├── fixtures/     # sample Git repos for coding tasks
└── report/       # metrics aggregation + friction-log output
```

### Relationship to existing code

- `internal/acp/` remains the headless eval path. The PTY harness does not replace it.
- `internal/app/tui/` is the system under test; the harness treats it as a black box.
- `internal/agent/` and `internal/db/` are exercised indirectly through the binary.

## 4. Components

### 4.1 `harness/` — PTY wrapper

```go
type Session struct {
    cmd      *exec.Cmd
    pty      *os.File
    width    int
    height   int
    // output ring buffer for screen parsing
}

func New(cfg Config) (*Session, error)
func (s *Session) Send(text string) error
func (s *Session) SendKey(key string) error      // enter, esc, ctrl+c, etc.
func (s *Session) Snapshot() screen.Screen
func (s *Session) WaitFor(ctx context.Context, pred func(screen.Screen) bool) error
func (s *Session) Close() error
```

- Uses `github.com/creack/pty` to allocate a pseudo-terminal.
- Builds the `marshal` binary from `cmd/marshal` on demand (or accepts a path via `Config.BinaryPath`), then runs it inside the PTY — not `go run`.
- `Send` supports an optional human-like inter-key delay.
- `Snapshot` reads the current terminal contents from the PTY output buffer.

### 4.2 `screen/` — TUI state parser

```go
type Screen struct {
    Width   int
    Height  int
    Content string
    Lines   []string
    State   UIState
}

type UIState struct {
    Mode            string // auto, ask, edit, etc.
    Busy            bool
    HelpOpen        bool
    PendingApproval bool
    PendingQuestion bool
    InputValue      string
    LastAgentMsg    string
    ErrorVisible    bool
}
```

- Strips ANSI escape codes.
- Extracts state with regex/heuristics on known Marshal strings (e.g. "Agent wants to run", "marshal keys", "❯").
- `WaitFor` polls until a predicate holds for two consecutive snapshots (stable-state guard against transient frames).

### 4.3 `actor/scripted/` — deterministic UI walkthroughs

Scenarios are declarative. Example:

```yaml
name: open help and dismiss
steps:
  - send: "?"
    wait_for:
      screen_contains: "marshal keys"
  - send_key: esc
    wait_for:
      state:
        help_open: false
```

- No LLM calls; fast and deterministic.
- Each step asserts on `screen.Screen` or `UIState`.
- Failures produce a screen snapshot and the failing step.

### 4.4 `actor/llm/` — agentic coding tasks

```go
type LLMActor struct {
    client llm.Client // local Ollama by default
    goal   string
}

func (a *LLMActor) Act(ctx context.Context, s screen.Screen) (Action, error)
```

- System prompt describes Marshal's keybindings, the current goal, and the expected JSON action format:
  - `{"action":"type","text":"..."}`
  - `{"action":"key","key":"enter"}`
  - `{"action":"done","success":true,"notes":"..."}`
- Observes the last N lines of terminal output plus `UIState`.
- Uses a local model (e.g. `qwen2.5-coder:14b`) by default.

### 4.5 `scenario/` — definitions and scoring

```go
type Scenario struct {
    Name        string
    Fixture     string           // optional fixture repo
    Actor       string           // scripted or llm
    Steps       []scripted.Step  // for scripted actors
    Goal        string           // for LLM actors
    Success     SuccessCriterion
    MaxDuration time.Duration
}
```

- **Scripted success**: all assertions pass.
- **LLM success**: a separate judge phase inspects the fixture repo after the actor finishes (tests pass, file exists, diff matches expectation). The judge can be another LLM call or a deterministic check.
- **Frustration heuristics**: count help openings, approval denials, idle loops, repeated identical inputs.

### 4.6 `report/` — outputs

Emits three artifacts on every run:

1. `usability-report.json` — per-scenario outcomes, durations, keystrokes, approvals, error flags.
2. `usability-benchmark.json` — aggregated scorecard: success rate, mean time-to-complete, friction rate.
3. `friction-log.md` — human-readable narrative with screen snapshots for failures.

CI reads `usability-report.json` to fail the build on regressions.

## 5. Data Flow

1. **Fixture setup**: copy a fixture repo to a temp dir under `t.TempDir()`.
2. **Marshal launch**: `harness.Session` starts `marshal` in the fixture directory inside a PTY with a known size (default 120×40).
3. **Scenario load**: load either a scripted YAML or an LLM actor with a task description.
4. **Action loop**:
   - Actor observes `screen.UIState` + last terminal lines.
   - Actor decides the next input or calls `done`.
   - `harness.Send` writes to the PTY.
   - `screen` updates from PTY output.
   - `report` records the event.
5. **Completion**: scenario ends when the actor calls `done`, a timeout fires, or a max-turn limit is reached.
6. **Scoring**:
   - Scripted: assertion results.
   - LLM: judge phase inspects repo state and possibly the friction log.
7. **Report generation**: write JSON and Markdown artifacts.

## 6. Error Handling & Robustness

- **Total and per-action timeouts**: prevent runaway scenarios.
- **Stable-state guard**: `WaitFor` requires two consecutive matching snapshots before proceeding.
- **Crash capture**: if Marshal exits, save stderr/stdout and mark scenario failed.
- **Determinism**: fixed terminal size, fixed keystroke pacing, fixed model seed/temperature where possible.
- **Isolation**: fresh process, temp dir, and SQLite DB per scenario.
- **Local-first default**: LLM actor defaults to local Ollama; remote judge optional.

## 7. Initial Scenarios

### UI Walkthrough Suite

| Scenario | Goal |
|---|---|
| `help_open_close` | Open `?` help, verify content, close with Esc. |
| `change_profile` | Open command palette, switch model profile, verify status bar. |
| `approve_shell` | Trigger a safe shell command, approve it, verify output appears. |
| `deny_shell` | Trigger a shell command, deny it, verify turn cancels cleanly. |
| `settings_open` | Open settings with Ctrl+O, navigate, exit without changes. |

### Coding Task Suite

| Scenario | Fixture | Goal |
|---|---|---|
| `add_test` | `fixtures/go-calc` | Add a table-driven test for an existing calculator package. |
| `fix_failing_test` | `fixtures/go-calc-broken` | Run tests, diagnose failure, patch code so tests pass. |
| `refactor_rename` | `fixtures/go-hello` | Rename a function and update all call sites. |

## 8. Testing the Harness Itself

- Unit tests for `screen` parser against captured Marshal output samples.
- Unit tests for `harness` using a simple echo/TTY program before testing Marshal.
- Smoke test: one scripted scenario runs against the current build.

## 9. Dependencies

- `github.com/creack/pty` for PTY allocation (add to `go.mod`).
- Existing LLM provider code for local Ollama calls, or a thin client in the harness.
- Existing `internal/acp/` is not required but remains available for separate headless evals.

## 10. Out of Scope / Future Work

- A/B testing of TUI layouts.
- Recording and replaying real user sessions.
- Browser-based or GUI automation.
- Hosted analytics/telemetry collection.

## 11. Success Criteria for This Design

- A `go test ./test/usability/...` run executes scripted scenarios and reports pass/fail.
- A `go test -run LLM ./test/usability/...` run executes at least one coding task via an LLM actor.
- Every run writes the three report artifacts.
- CI can fail on regression using `usability-report.json`.
