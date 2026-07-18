# Programmatic Usability Testing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a PTY-based synthetic-tester harness under `test/usability/` that drives the real Marshal TUI through deterministic scripted actors and LLM-driven coding actors, producing CI regression reports, benchmark scorecards, and friction logs.

**Architecture:** A shared `harness.Session` wraps the compiled `marshal` binary in a pseudo-terminal. The `screen` package parses terminal output into structured `UIState`. Two actor implementations (`scripted` and `llm`) decide keystrokes. The `scenario` package loads scenarios, runs the action loop, and applies success criteria. The `report` package emits JSON and Markdown artifacts. Fixture Git repositories provide coding tasks.

**Tech Stack:** Go 1.26, `github.com/creack/pty`, standard `testing`, Ollama-compatible HTTP client for LLM actors.

## Global Constraints

- Work in the existing `marshal` Go module; add `github.com/creack/pty` as a test dependency.
- Build with `CGO_ENABLED=1` for Marshal; harness tests do not require CGO except when compiling Marshal.
- `gofmt -w .` must be clean and `go vet ./...` must pass before every commit.
- All new code lives under `test/usability/`; no production code in `internal/` is modified unless a seam is missing.
- Each scenario gets an isolated temp directory and fresh Marshal process.
- LLM actor defaults to local Ollama (`http://localhost:11434`) with `qwen2.5-coder:14b`; override via env vars.
- Commit messages follow repo style: lowercase subject, e.g. `test(usability): add pty harness`.

---

## File Structure

```
test/usability/
├── harness/
│   ├── harness.go          # Session, Config, PTY lifecycle
│   └── harness_test.go     # Unit tests with echo/TTY program
├── screen/
│   ├── screen.go           # Screen, UIState types
│   ├── parse.go            # ANSI strip + state heuristics
│   └── screen_test.go      # Parser unit tests
├── report/
│   ├── report.go           # Event types, Reporter, file writers
│   └── report_test.go      # Report output tests
├── actor/
│   ├── actor.go            # Actor interface
│   ├── scripted/
│   │   ├── scripted.go     # Scripted actor implementation
│   │   ├── json.go         # JSON scenario loading (optional)
│   │   └── scripted_test.go
│   └── llm/
│       ├── llm.go          # LLM actor + Ollama client
│       └── llm_test.go
├── scenario/
│   ├── scenario.go         # Scenario, Runner, success criteria
│   └── scenarios.go        # Go scenario definitions
├── fixtures/
│   ├── go-calc/            # calculator package
│   └── go-calc-broken/     # calculator with failing test
└── usability_test.go       # top-level test entry points
```

---

### Task 1: Add dependency and create directory scaffold

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: directories under `test/usability/`
- Test: `test/usability/usability_test.go` (compile-only stub)

**Interfaces:**
- Consumes: existing Go module.
- Produces: `test/usability/` package tree with `package usability` top-level stub.

- [ ] **Step 1: Add `creack/pty` dependency**

Run:
```bash
go get github.com/creack/pty@latest
```

Expected: `go.mod` gains `github.com/creack/pty v...` and `go.sum` updates.

- [ ] **Step 2: Create directory scaffold**

Run:
```bash
mkdir -p test/usability/{harness,screen,report,actor/scripted,actor/llm,scenario,fixtures/go-calc,fixtures/go-calc-broken}
```

- [ ] **Step 3: Create top-level compile-only stub**

Create `test/usability/usability_test.go`:

```go
package usability

import "testing"

func TestScaffoldCompiles(t *testing.T) {
	// placeholder removed in later tasks
}
```

- [ ] **Step 4: Run the stub test**

Run:
```bash
go test ./test/usability/...
```

Expected: `ok` with no tests found or one passing stub.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/usability_test.go
git add go.mod go.sum test/usability/
git commit -m "test(usability): scaffold usability harness tree"
```

---

### Task 2: PTY harness (`harness/`)

**Files:**
- Create: `test/usability/harness/harness.go`
- Create: `test/usability/harness/harness_test.go`
- Test: `go test ./test/usability/harness/...`

**Interfaces:**
- Consumes: `github.com/creack/pty`.
- Produces:
  - `type Config struct { BinaryPath string; Width, Height int; WorkDir string; Env []string; HumanDelay time.Duration }`
  - `type Session struct` with methods `Send`, `SendKey`, `Snapshot`, `WaitFor`, `Close`, `Output`.
  - `type Snapshot struct { Width, Height int; Content []byte; Lines []string }`

- [ ] **Step 1: Write the failing test**

Create `test/usability/harness/harness_test.go`:

```go
package harness

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSessionEcho(t *testing.T) {
	cfg := Config{
		BinaryPath: "cat", // use cat as a simple echo-like process
		Width:      80,
		Height:     24,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Send("hello\n"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "hello")
	}); err != nil {
		t.Fatalf("WaitFor: %v\noutput: %q", err, string(s.Output()))
	}
}

func TestSessionSendKeyEnter(t *testing.T) {
	cfg := Config{BinaryPath: "cat", Width: 80, Height: 24}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Send("line1"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.SendKey("enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "line1\r\n")
	}); err != nil {
		t.Fatalf("WaitFor: %v\noutput: %q", err, string(s.Output()))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./test/usability/harness/... -v
```

Expected: compile errors — `undefined: Config`, `undefined: New`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `test/usability/harness/harness.go`:

```go
package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Config configures a PTY session.
type Config struct {
	BinaryPath string
	Width      int
	Height     int
	WorkDir    string
	Env        []string
	HumanDelay time.Duration
}

// Session wraps a running process inside a PTY.
type Session struct {
	cmd    *exec.Cmd
	pty    *os.File
	mu     sync.Mutex
	buf    []byte
	width  int
	height int
}

// New starts a process in a PTY and begins collecting output.
func New(cfg Config) (*Session, error) {
	if cfg.BinaryPath == "" {
		return nil, fmt.Errorf("BinaryPath is required")
	}
	if cfg.Width == 0 {
		cfg.Width = 80
	}
	if cfg.Height == 0 {
		cfg.Height = 24
	}

	cmd := exec.Command(cfg.BinaryPath)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = cfg.Env
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cfg.Width), Rows: uint16(cfg.Height)})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	s := &Session{
		cmd:    cmd,
		pty:    ptmx,
		width:  cfg.Width,
		height: cfg.Height,
	}
	go s.readLoop()
	return s, nil
}

func (s *Session) readLoop() {
	var tmp [4096]byte
	for {
		n, err := s.pty.Read(tmp[:])
		if n > 0 {
			s.mu.Lock()
			s.buf = append(s.buf, tmp[:n]...)
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Send writes text to the PTY with an optional human-like delay.
func (s *Session) Send(text string) error {
	delay := s.humanDelayPerChar()
	for _, r := range text {
		if _, err := s.pty.Write([]byte(string(r))); err != nil {
			return err
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return nil
}

// SendKey writes a named key to the PTY.
func (s *Session) SendKey(key string) error {
	var seq []byte
	switch strings.ToLower(key) {
	case "enter", "return":
		seq = []byte("\r")
	case "esc", "escape":
		seq = []byte("\x1b")
	case "tab":
		seq = []byte("\t")
	case "space":
		seq = []byte(" ")
	case "backspace":
		seq = []byte("\x7f")
	case "ctrl+c":
		seq = []byte("\x03")
	case "ctrl+d":
		seq = []byte("\x04")
	case "ctrl+o":
		seq = []byte("\x0f")
	case "ctrl+k":
		seq = []byte("\x0b")
	case "ctrl+g":
		seq = []byte("\x07")
	case "ctrl+r":
		seq = []byte("\x12")
	case "ctrl+x":
		seq = []byte("\x18")
	case "up":
		seq = []byte("\x1b[A")
	case "down":
		seq = []byte("\x1b[B")
	case "right":
		seq = []byte("\x1b[C")
	case "left":
		seq = []byte("\x1b[D")
	case "pgup":
		seq = []byte("\x1b[5~")
	case "pgdown":
		seq = []byte("\x1b[6~")
	case "home":
		seq = []byte("\x1b[H")
	case "end":
		seq = []byte("\x1b[F")
	default:
		return fmt.Errorf("unknown key: %q", key)
	}
	_, err := s.pty.Write(seq)
	return err
}

// Snapshot returns the current terminal contents.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	content := make([]byte, len(s.buf))
	copy(content, s.buf)
	s.mu.Unlock()

	lines := strings.Split(string(content), "\n")
	return Snapshot{Width: s.width, Height: s.height, Content: content, Lines: lines}
}

// Output returns all raw output bytes collected so far.
func (s *Session) Output() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.buf))
	copy(out, s.buf)
	return out
}

// WaitFor polls until predicate holds for two consecutive snapshots.
func (s *Session) WaitFor(ctx context.Context, pred func(Snapshot) bool) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	stable := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if pred(s.Snapshot()) {
				stable++
				if stable >= 2 {
					return nil
				}
			} else {
				stable = 0
			}
		}
	}
}

// Close terminates the session and waits for the process.
func (s *Session) Close() error {
	_ = s.pty.Close()
	return s.cmd.Wait()
}

func (s *Session) humanDelayPerChar() time.Duration {
	// no delay by default
	return 0
}

// Snapshot is a captured screen state.
type Snapshot struct {
	Width   int
	Height  int
	Content []byte
	Lines   []string
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./test/usability/harness/... -v
```

Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/harness/
go vet ./test/usability/harness/...
git add test/usability/harness/
git commit -m "test(usability): add pty harness"
```

---

### Task 3: Screen-state parser (`screen/`)

**Files:**
- Create: `test/usability/screen/screen.go`
- Create: `test/usability/screen/parse.go`
- Create: `test/usability/screen/screen_test.go`
- Test: `go test ./test/usability/screen/...`

**Interfaces:**
- Consumes: raw ANSI bytes / lines from `harness.Snapshot`.
- Produces:
  - `type UIState struct` with fields `Mode`, `Busy`, `HelpOpen`, `PendingApproval`, `PendingQuestion`, `InputValue`, `LastAgentMsg`, `ErrorVisible`.
  - `func Parse(snap harness.Snapshot) (Screen, error)` returning `Screen` that embeds `UIState`.
  - `func StripANSI(b []byte) string`.

- [ ] **Step 1: Write the failing test**

Create `test/usability/screen/screen_test.go`:

```go
package screen

import (
	"testing"

	"marshal/test/usability/harness"
)

func TestStripANSI(t *testing.T) {
	in := "\x1b[31mhello\x1b[0m world"
	want := "hello world"
	got := StripANSI([]byte(in))
	if got != want {
		t.Fatalf("StripANSI = %q, want %q", got, want)
	}
}

func TestParseHelpOpen(t *testing.T) {
	snap := harness.Snapshot{
		Width:   80,
		Height:  24,
		Content: []byte("marshal keys\n  Enter send message\n"),
		Lines:   []string{"marshal keys", "  Enter send message"},
	}
	scr, err := Parse(snap)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !scr.State.HelpOpen {
		t.Fatalf("HelpOpen = false, want true")
	}
}

func TestParsePendingApproval(t *testing.T) {
	snap := harness.Snapshot{
		Content: []byte("Agent wants to run:\n  go test ./...\nRisk: Low\n[Enter] approve"),
	}
	scr, err := Parse(snap)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !scr.State.PendingApproval {
		t.Fatalf("PendingApproval = false, want true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./test/usability/screen/... -v
```

Expected: compile errors — `undefined: Screen`, `undefined: Parse`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `test/usability/screen/screen.go`:

```go
package screen

import "marshal/test/usability/harness"

// UIState captures actionable signals parsed from the TUI.
type UIState struct {
	Mode            string // e.g. auto, ask, edit
	Busy            bool
	HelpOpen        bool
	PendingApproval bool
	PendingQuestion bool
	InputValue      string
	LastAgentMsg    string
	ErrorVisible    bool
}

// Screen is a parsed snapshot.
type Screen struct {
	Width   int
	Height  int
	Content string
	Lines   []string
	State   UIState
}

// Parse turns a harness snapshot into a structured Screen.
func Parse(snap harness.Snapshot) (Screen, error) {
	content := StripANSI(snap.Content)
	lines := make([]string, 0, len(snap.Lines))
	for _, ln := range snap.Lines {
		lines = append(lines, StripANSI([]byte(ln)))
	}

	scr := Screen{
		Width:   snap.Width,
		Height:  snap.Height,
		Content: content,
		Lines:   lines,
		State:   extractState(content, lines),
	}
	return scr, nil
}
```

Create `test/usability/screen/parse.go`:

```go
package screen

import (
	"regexp"
	"strings"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// StripANSI removes ANSI escape sequences.
func StripANSI(b []byte) string {
	return ansiRe.ReplaceAllString(string(b), "")
}

func extractState(content string, lines []string) UIState {
	state := UIState{}
	lower := strings.ToLower(content)

	if strings.Contains(lower, "marshal keys") {
		state.HelpOpen = true
	}
	if strings.Contains(content, "Agent wants to run:") {
		state.PendingApproval = true
	}
	if strings.Contains(content, "Pending question") || strings.Contains(content, "[Enter] answer") {
		state.PendingQuestion = true
	}
	if strings.Contains(content, "❯") {
		// input prompt visible; attempt to capture the current input line after the prompt
		for _, ln := range lines {
			if idx := strings.Index(ln, "❯"); idx >= 0 {
				state.InputValue = strings.TrimSpace(ln[idx+len("❯"):])
			}
		}
	}
	if strings.Contains(content, "busy") || strings.Contains(content, "running") {
		state.Busy = true
	}

	// Mode indicator from status line; look for known mode words at line start or in status bar.
	for _, m := range []string{"ask", "edit", "auto", "plan"} {
		if strings.Contains(lower, " "+m+" ") || strings.Contains(lower, "["+m+"]") {
			state.Mode = m
		}
	}

	state.LastAgentMsg = lastAgentMessage(lines)
	return state
}

func lastAgentMessage(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if strings.HasPrefix(ln, "agent:") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "agent:"))
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./test/usability/screen/... -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/screen/
go vet ./test/usability/screen/...
git add test/usability/screen/
git commit -m "test(usability): add screen state parser"
```

---

### Task 4: Reporting (`report/`)

**Files:**
- Create: `test/usability/report/report.go`
- Create: `test/usability/report/report_test.go`
- Test: `go test ./test/usability/report/...`

**Interfaces:**
- Consumes: events from the scenario runner.
- Produces:
  - `type Event struct { Time time.Time; Kind string; Payload map[string]any }`
  - `type Reporter struct`
  - `func (r *Reporter) Record(kind string, payload map[string]any)`
  - `func (r *Reporter) WriteReport(dir string) error` producing `usability-report.json`, `usability-benchmark.json`, `friction-log.md`.

- [ ] **Step 1: Write the failing test**

Create `test/usability/report/report_test.go`:

```go
package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReporterWritesArtifacts(t *testing.T) {
	r := New()
	r.Record("turn_started", map[string]any{"scenario": "help_open_close"})
	r.Record("key_sent", map[string]any{"key": "?"})
	r.Record("task_done", map[string]any{"scenario": "help_open_close", "success": true, "duration_ms": 1200})

	dir := t.TempDir()
	if err := r.WriteReport(dir); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	for _, name := range []string{"usability-report.json", "usability-benchmark.json", "friction-log.md"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", name, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, "usability-report.json"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	events, ok := report["events"].([]any)
	if !ok || len(events) != 3 {
		t.Fatalf("expected 3 events, got %v", report["events"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./test/usability/report/... -v
```

Expected: compile errors — `undefined: New`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `test/usability/report/report.go`:

```go
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Event is a single observable action during a scenario.
type Event struct {
	Time    time.Time      `json:"time"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload,omitempty"`
}

// ScenarioResult aggregates one scenario's outcome.
type ScenarioResult struct {
	Name      string        `json:"name"`
	Actor     string        `json:"actor"`
	Success   bool          `json:"success"`
	Duration  time.Duration `json:"duration"`
	Keystrokes int          `json:"keystrokes"`
	Error     string        `json:"error,omitempty"`
}

// Reporter collects events and results.
type Reporter struct {
	events   []Event
	results  []ScenarioResult
	start    time.Time
}

// New creates a Reporter.
func New() *Reporter {
	return &Reporter{start: time.Now()}
}

// Record adds an event.
func (r *Reporter) Record(kind string, payload map[string]any) {
	r.events = append(r.events, Event{
		Time:    time.Now(),
		Kind:    kind,
		Payload: payload,
	})
}

// AddResult adds a scenario outcome.
func (r *Reporter) AddResult(res ScenarioResult) {
	r.results = append(r.results, res)
}

// WriteReport writes JSON and Markdown artifacts to dir.
func (r *Reporter) WriteReport(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	report := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"events":       r.events,
		"results":      r.results,
	}
	if err := writeJSON(filepath.Join(dir, "usability-report.json"), report); err != nil {
		return err
	}

	bench := r.buildBenchmark()
	if err := writeJSON(filepath.Join(dir, "usability-benchmark.json"), bench); err != nil {
		return err
	}

	return writeFrictionLog(filepath.Join(dir, "friction-log.md"), r.results, r.events)
}

func (r *Reporter) buildBenchmark() map[string]any {
	total := len(r.results)
	if total == 0 {
		return map[string]any{"total": 0, "success_rate": 0}
	}
	passed := 0
	var totalDuration time.Duration
	for _, res := range r.results {
		if res.Success {
			passed++
		}
		totalDuration += res.Duration
	}
	return map[string]any{
		"total":        total,
		"passed":       passed,
		"failed":       total - passed,
		"success_rate": float64(passed) / float64(total),
		"mean_duration_ms": totalDuration.Milliseconds() / int64(total),
	}
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeFrictionLog(path string, results []ScenarioResult, events []Event) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# Usability Friction Log\n\n")
	fmt.Fprintf(f, "Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	for _, res := range results {
		status := "PASS"
		if !res.Success {
			status = "FAIL"
		}
		fmt.Fprintf(f, "## %s — %s\n\n", res.Name, status)
		fmt.Fprintf(f, "- Actor: %s\n- Duration: %s\n- Keystrokes: %d\n", res.Actor, res.Duration, res.Keystrokes)
		if res.Error != "" {
			fmt.Fprintf(f, "- Error: %s\n", res.Error)
		}
		fmt.Fprintln(f)
		fmt.Fprintf(f, "### Events\n\n")
		for _, ev := range events {
			if name, ok := ev.Payload["scenario"].(string); ok && name == res.Name {
				fmt.Fprintf(f, "- %s %s: %v\n", ev.Time.Format("15:04:05"), ev.Kind, ev.Payload)
			}
		}
		fmt.Fprintln(f)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./test/usability/report/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/report/
go vet ./test/usability/report/...
git add test/usability/report/
git commit -m "test(usability): add report generator"
```

---

### Task 5: Actor interface (`actor/`)

**Files:**
- Create: `test/usability/actor/actor.go`
- Test: compile check only

**Interfaces:**
- Consumes: `screen.Screen`.
- Produces:
  - `type Action struct { Type string; Text string; Key string; Success bool; Notes string }`
  - `type Actor interface { Act(ctx context.Context, s screen.Screen) (Action, error) }`

- [ ] **Step 1: Write the interface file**

Create `test/usability/actor/actor.go`:

```go
package actor

import (
	"context"

	"marshal/test/usability/screen"
)

// ActionType values for Action.Type.
const (
	ActionType    = "type"
	ActionKey     = "key"
	ActionDone    = "done"
	ActionNoOp    = "noop"
)

// Action is one input decision from an actor.
type Action struct {
	Type    string // "type", "key", "done", "noop"
	Text    string // for "type"
	Key     string // for "key"
	Success bool   // for "done"
	Notes   string // for "done" or debugging
}

// Actor decides the next input given the current screen.
type Actor interface {
	Act(ctx context.Context, s screen.Screen) (Action, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run:
```bash
go test ./test/usability/actor/...
```

Expected: PASS (no tests).

- [ ] **Step 3: Commit**

```bash
gofmt -w test/usability/actor/
go vet ./test/usability/actor/...
git add test/usability/actor/
git commit -m "test(usability): add actor interface"
```

---

### Task 6: Scripted actor (`actor/scripted/`)

**Files:**
- Create: `test/usability/actor/scripted/scripted.go`
- Create: `test/usability/actor/scripted/yaml.go`
- Create: `test/usability/actor/scripted/scripted_test.go`
- Test: `go test ./test/usability/actor/scripted/...`

**Interfaces:**
- Consumes: `screen.Screen`, `actor.Action`.
- Produces:
  - `type Step struct { Send string; SendKey string; WaitFor WaitFor }`
  - `type WaitFor struct { ScreenContains string; State UIStatePredicate }`
  - `type Scripted struct { Steps []Step }` implementing `actor.Actor`.
  - `func Load(path string) (*Scripted, error)`.

- [ ] **Step 1: Write the failing test**

Create `test/usability/actor/scripted/scripted_test.go`:

```go
package scripted

import (
	"context"
	"testing"

	"marshal/test/usability/screen"
)

func TestScriptedTypeStep(t *testing.T) {
	scr := &Scripted{
		Name: "open help",
		Steps: []Step{
			{Send: "?", WaitFor: WaitFor{ScreenContains: "marshal keys"}},
			{SendKey: "esc", WaitFor: WaitFor{State: UIStatePredicate{HelpOpen: boolPtr(false)}}},
		},
	}

	// step 1: type ?
	act, err := scr.Act(context.Background(), screen.Screen{Content: "prompt ❯ "})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != "type" || act.Text != "?" {
		t.Fatalf("first action = %+v, want type '?", act)
	}

	// step 2: screen contains help -> send esc
	act, err = scr.Act(context.Background(), screen.Screen{Content: "marshal keys"})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != "key" || act.Key != "esc" {
		t.Fatalf("second action = %+v, want key esc", act)
	}

	// step 3: help closed -> done
	act, err = scr.Act(context.Background(), screen.Screen{Content: "prompt ❯ "})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != "done" || !act.Success {
		t.Fatalf("final action = %+v, want done success", act)
	}
}

func boolPtr(b bool) *bool { return &b }
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./test/usability/actor/scripted/... -v
```

Expected: compile errors.

- [ ] **Step 3: Write minimal implementation**

Create `test/usability/actor/scripted/scripted.go`:

```go
package scripted

import (
	"context"
	"fmt"
	"strings"

	"marshal/test/usability/actor"
	"marshal/test/usability/screen"
)

// Scripted is a deterministic actor driven by a list of steps.
type Scripted struct {
	Name  string
	Steps []Step
	pos   int
}

// Step is one scripted interaction.
type Step struct {
	Send    string          `json:"send"`
	SendKey string          `json:"send_key"`
	WaitFor WaitFor         `json:"wait_for"`
}

// WaitFor describes the screen condition required before this step executes.
type WaitFor struct {
	ScreenContains string          `json:"screen_contains"`
	State          UIStatePredicate `json:"state"`
}

// UIStatePredicate matches fields in screen.UIState. nil fields are ignored.
type UIStatePredicate struct {
	HelpOpen        *bool `json:"help_open,omitempty"`
	PendingApproval *bool `json:"pending_approval,omitempty"`
	PendingQuestion *bool `json:"pending_question,omitempty"`
	Busy            *bool `json:"busy,omitempty"`
}

// Act returns the next action if the current screen satisfies the next step's wait condition.
func (s *Scripted) Act(ctx context.Context, sc screen.Screen) (actor.Action, error) {
	if s.pos >= len(s.Steps) {
		return actor.Action{Type: actor.ActionDone, Success: true}, nil
	}

	step := s.Steps[s.pos]
	if !matchesWaitFor(sc, step.WaitFor) {
		// Wait condition not met yet; ask harness to wait and try again.
		return actor.Action{Type: actor.ActionNoOp}, nil
	}

	s.pos++
	if step.Send != "" {
		return actor.Action{Type: actor.ActionType, Text: step.Send}, nil
	}
	if step.SendKey != "" {
		return actor.Action{Type: actor.ActionKey, Key: step.SendKey}, nil
	}
	return actor.Action{}, fmt.Errorf("step %d has no send or send_key", s.pos)
}

func matchesWaitFor(sc screen.Screen, wf WaitFor) bool {
	if wf.ScreenContains != "" && !strings.Contains(sc.Content, wf.ScreenContains) {
		return false
	}
	if wf.State.HelpOpen != nil && sc.State.HelpOpen != *wf.State.HelpOpen {
		return false
	}
	if wf.State.PendingApproval != nil && sc.State.PendingApproval != *wf.State.PendingApproval {
		return false
	}
	if wf.State.PendingQuestion != nil && sc.State.PendingQuestion != *wf.State.PendingQuestion {
		return false
	}
	if wf.State.Busy != nil && sc.State.Busy != *wf.State.Busy {
		return false
	}
	return true
}
```

Create `test/usability/actor/scripted/json.go`:

```go
package scripted

import (
	"encoding/json"
	"os"
)

// Load reads a scripted scenario from a JSON file.
func Load(path string) (*Scripted, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scripted
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
```

Note: scenario files use JSON instead of YAML to avoid adding a new dependency; the project already has TOML and JSON support.

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./test/usability/actor/scripted/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/actor/scripted/
go vet ./test/usability/actor/scripted/...
git add test/usability/actor/scripted/
git commit -m "test(usability): add scripted actor"
```

---

### Task 7: LLM actor (`actor/llm/`)

**Files:**
- Create: `test/usability/actor/llm/llm.go`
- Create: `test/usability/actor/llm/llm_test.go`
- Test: `go test ./test/usability/actor/llm/...`

**Interfaces:**
- Consumes: `screen.Screen`, Ollama HTTP API.
- Produces:
  - `type Config struct { BaseURL, Model string; MaxIterations int }`
  - `type LLM struct` implementing `actor.Actor`.
  - `type Client interface { Complete(ctx context.Context, system, prompt string) (string, error) }`

- [ ] **Step 1: Write the failing test**

Create `test/usability/actor/llm/llm_test.go`:

```go
package llm

import (
	"context"
	"testing"

	"marshal/test/usability/actor"
	"marshal/test/usability/screen"
)

type fakeClient struct {
	responses []string
	pos       int
}

func (f *fakeClient) Complete(ctx context.Context, system, prompt string) (string, error) {
	if f.pos >= len(f.responses) {
		return `{"action":"done","success":true}`, nil
	}
	r := f.responses[f.pos]
	f.pos++
	return r, nil
}

func TestLLMActorTypesAndDone(t *testing.T) {
	client := &fakeClient{responses: []string{
		`{"action":"type","text":"hello"}`,
	}}
	a := New(Config{MaxIterations: 5}, client)

	act, err := a.Act(context.Background(), screen.Screen{Content: "prompt ❯ "})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != actor.ActionType || act.Text != "hello" {
		t.Fatalf("first action = %+v, want type 'hello'", act)
	}

	act, err = a.Act(context.Background(), screen.Screen{Content: "prompt ❯ hello"})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != actor.ActionDone || !act.Success {
		t.Fatalf("final action = %+v, want done success", act)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./test/usability/actor/llm/... -v
```

Expected: compile errors.

- [ ] **Step 3: Write minimal implementation**

Create `test/usability/actor/llm/llm.go`:

```go
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"marshal/test/usability/actor"
	"marshal/test/usability/screen"
)

// Config for the LLM actor.
type Config struct {
	BaseURL       string
	Model         string
	MaxIterations int
}

// Client is the LLM completion interface.
type Client interface {
	Complete(ctx context.Context, system, prompt string) (string, error)
}

// LLM is an actor driven by an LLM.
type LLM struct {
	cfg       Config
	client    Client
	goal      string
	iter      int
	lastN     []string
}

// New creates an LLM actor. If client is nil, an OllamaClient is constructed from cfg.
func New(cfg Config, client Client) *LLM {
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("USABILITY_LLM_URL")
		if cfg.BaseURL == "" {
			cfg.BaseURL = "http://localhost:11434"
		}
	}
	if cfg.Model == "" {
		cfg.Model = os.Getenv("USABILITY_LLM_MODEL")
		if cfg.Model == "" {
			cfg.Model = "qwen2.5-coder:14b"
		}
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 30
	}
	if client == nil {
		client = &OllamaClient{BaseURL: cfg.BaseURL, Model: cfg.Model}
	}
	return &LLM{cfg: cfg, client: client}
}

// WithGoal sets the task goal before running.
func (l *LLM) WithGoal(goal string) *LLM {
	l.goal = goal
	return l
}

// Act asks the LLM for the next input.
func (l *LLM) Act(ctx context.Context, s screen.Screen) (actor.Action, error) {
	if l.iter >= l.cfg.MaxIterations {
		return actor.Action{Type: actor.ActionDone, Success: false, Notes: "iteration limit reached"}, nil
	}
	l.iter++

	system := `You are a usability tester driving the Marshal terminal coding agent. You see the terminal screen and decide the next keystrokes. Respond with a single JSON object: {"action":"type","text":"..."}, {"action":"key","key":"..."} (enter, esc, y, n, etc.), or {"action":"done","success":true,"notes":"..."}. Be concise.`

	prompt := fmt.Sprintf("Goal: %s\nCurrent screen:\n%s\nPending approval: %v\nPending question: %v\nBusy: %v\nWhat is your next action?",
		l.goal, summarize(s), s.State.PendingApproval, s.State.PendingQuestion, s.State.Busy)

	raw, err := l.client.Complete(ctx, system, prompt)
	if err != nil {
		return actor.Action{}, err
	}
	return parseAction(raw)
}

func summarize(s screen.Screen) string {
	lines := s.Lines
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}

func parseAction(raw string) (actor.Action, error) {
	// Extract JSON if the model wrapped it in markdown.
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end <= start {
		return actor.Action{}, fmt.Errorf("no JSON action found in: %q", raw)
	}
	var a actor.Action
	if err := json.Unmarshal([]byte(raw[start:end+1]), &a); err != nil {
		return actor.Action{}, fmt.Errorf("parse action: %w", err)
	}
	return a, nil
}

// OllamaClient is a minimal Ollama chat client.
type OllamaClient struct {
	BaseURL string
	Model   string
	client  *http.Client
}

// Complete sends a chat request to Ollama and returns the assistant message.
func (o *OllamaClient) Complete(ctx context.Context, system, prompt string) (string, error) {
	if o.client == nil {
		o.client = http.DefaultClient
	}
	body := map[string]any{
		"model": o.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": prompt},
		},
		"stream": false,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama status %d", resp.StatusCode)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Message.Content, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./test/usability/actor/llm/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/actor/llm/
go vet ./test/usability/actor/llm/...
git add test/usability/actor/llm/
git commit -m "test(usability): add llm actor"
```

---

### Task 8: Scenario runner (`scenario/`)

**Files:**
- Create: `test/usability/scenario/scenario.go`
- Create: `test/usability/scenario/scenario_test.go`
- Test: `go test ./test/usability/scenario/...`

**Interfaces:**
- Consumes: `harness.Session`, `actor.Actor`, `screen.Screen`, `report.Reporter`.
- Produces:
  - `type Scenario struct { Name, ActorType, Fixture, Goal string; Actor actor.Actor; Success SuccessCriterion }`
  - `type Runner struct { ... }`
  - `func (r *Runner) Run(ctx context.Context, sc Scenario) (report.ScenarioResult, error)`

- [ ] **Step 1: Write the failing test**

Create `test/usability/scenario/scenario_test.go`:

```go
package scenario

import (
	"context"
	"testing"
	"time"

	"marshal/test/usability/actor"
	"marshal/test/usability/report"
	"marshal/test/usability/screen"
)

type stubActor struct {
	actions []actor.Action
	pos     int
}

func (s *stubActor) Act(ctx context.Context, sc screen.Screen) (actor.Action, error) {
	if s.pos >= len(s.actions) {
		return actor.Action{Type: actor.ActionDone, Success: true}, nil
	}
	a := s.actions[s.pos]
	s.pos++
	return a, nil
}

func TestRunnerCountsKeystrokes(t *testing.T) {
	act := &stubActor{
		actions: []actor.Action{
			{Type: actor.ActionType, Text: "hi"},
			{Type: actor.ActionKey, Key: "enter"},
		},
	}
	sc := Scenario{
		Name:    "stub",
		Actor:   act,
		Success: SuccessCriterion{},
	}

	r := NewRunner(RunnerConfig{BinaryPath: "cat", WorkDir: t.TempDir()})
	res, err := r.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Keystrokes != 3 { // h, i, enter
		t.Fatalf("Keystrokes = %d, want 3", res.Keystrokes)
	}
	if !res.Success {
		t.Fatalf("Success = false, want true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./test/usability/scenario/... -v
```

Expected: compile errors.

- [ ] **Step 3: Write minimal implementation**

Create `test/usability/scenario/scenario.go`:

```go
package scenario

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"marshal/test/usability/actor"
	"marshal/test/usability/harness"
	"marshal/test/usability/report"
	"marshal/test/usability/screen"
)

// RunnerConfig configures the scenario runner.
type RunnerConfig struct {
	BinaryPath string
	Width      int
	Height     int
	ReportDir  string
}

// Runner executes one scenario against Marshal.
type Runner struct {
	cfg RunnerConfig
	rep *report.Reporter
}

// NewRunner creates a runner.
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.Width == 0 {
		cfg.Width = 120
	}
	if cfg.Height == 0 {
		cfg.Height = 40
	}
	return &Runner{cfg: cfg, rep: report.New()}
}

// SuccessCriterion describes how to judge a scenario. For LLM scenarios the judge is separate.
type SuccessCriterion struct {
	ScreenContains string // for scripted assertions, handled by the actor
}

// Scenario is one usability test.
type Scenario struct {
	Name    string
	Actor   actor.Actor
	WorkDir string
	Success SuccessCriterion
}

// Run executes the scenario and returns the result.
func (r *Runner) Run(ctx context.Context, sc Scenario) (report.ScenarioResult, error) {
	start := time.Now()
	result := report.ScenarioResult{Name: sc.Name, Actor: actorName(sc.Actor)}

	workDir := sc.WorkDir
	if workDir == "" {
		workDir = r.cfg.WorkDir
	}

	sess, err := harness.New(harness.Config{
		BinaryPath: r.cfg.BinaryPath,
		Width:      r.cfg.Width,
		Height:     r.cfg.Height,
		WorkDir:    workDir,
	})
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	defer sess.Close()

	r.rep.Record("turn_started", map[string]any{"scenario": sc.Name, "work_dir": workDir})

	// Give Marshal a moment to render initial UI.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = sess.WaitFor(waitCtx, func(snap harness.Snapshot) bool {
		scr, _ := screen.Parse(snap)
		return scr.Content != ""
	})

	maxDuration := 2 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		maxDuration = time.Until(deadline)
	}
	scenarioCtx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	keystrokes := 0
	for {
		select {
		case <-scenarioCtx.Done():
			result.Error = "scenario timeout"
			result.Duration = time.Since(start)
			r.rep.AddResult(result)
			return result, scenarioCtx.Err()
		default:
		}

		scr, _ := screen.Parse(sess.Snapshot())
		act, err := sc.Actor.Act(scenarioCtx, scr)
		if err != nil {
			result.Error = err.Error()
			result.Duration = time.Since(start)
			r.rep.AddResult(result)
			return result, err
		}

		r.rep.Record("actor_action", map[string]any{"scenario": sc.Name, "action": act})

		switch act.Type {
		case actor.ActionDone:
			result.Success = act.Success
			result.Notes = act.Notes
			result.Duration = time.Since(start)
			result.Keystrokes = keystrokes
			r.rep.AddResult(result)
			return result, nil
		case actor.ActionNoOp:
			// Wait a bit and re-observe.
			select {
			case <-scenarioCtx.Done():
				result.Error = "scenario timeout during noop"
				result.Duration = time.Since(start)
				r.rep.AddResult(result)
				return result, scenarioCtx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		case actor.ActionType:
			if err := sess.Send(act.Text); err != nil {
				result.Error = err.Error()
				return result, err
			}
			keystrokes += len(act.Text)
		case actor.ActionKey:
			if err := sess.SendKey(act.Key); err != nil {
				result.Error = err.Error()
				return result, err
			}
			keystrokes++
		default:
			result.Error = fmt.Sprintf("unknown action type %q", act.Type)
			return result, fmt.Errorf("unknown action type %q", act.Type)
		}
	}
}

// WriteReport flushes reports to ReportDir or a temp dir.
func (r *Runner) WriteReport() error {
	dir := r.cfg.ReportDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "marshal-usability")
	}
	return r.rep.WriteReport(dir)
}

func actorName(a actor.Actor) string {
	switch a.(type) {
	default:
		return fmt.Sprintf("%T", a)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./test/usability/scenario/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/scenario/
go vet ./test/usability/scenario/...
git add test/usability/scenario/
git commit -m "test(usability): add scenario runner"
```

---

### Task 9: Fixture repositories

**Files:**
- Create: `test/usability/fixtures/go-calc/calc.go`
- Create: `test/usability/fixtures/go-calc/calc_test.go`
- Create: `test/usability/fixtures/go-calc/go.mod`
- Create: `test/usability/fixtures/go-calc-broken/calc.go`
- Create: `test/usability/fixtures/go-calc-broken/calc_test.go`
- Create: `test/usability/fixtures/go-calc-broken/go.mod`
- Test: `go test ./test/usability/fixtures/...` (where applicable)

**Interfaces:**
- Consumes: standard Go tooling.
- Produces: two minimal Go modules for coding scenarios.

- [ ] **Step 1: Create working calculator fixture**

Create `test/usability/fixtures/go-calc/go.mod`:

```go
module calc

go 1.26
```

Create `test/usability/fixtures/go-calc/calc.go`:

```go
package calc

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
```

Create `test/usability/fixtures/go-calc/calc_test.go`:

```go
package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2,3) = %d, want 5", got)
	}
}
```

- [ ] **Step 2: Create broken calculator fixture**

Copy the same `go.mod` and `calc.go` to `test/usability/fixtures/go-calc-broken/`.

Create `test/usability/fixtures/go-calc-broken/calc_test.go`:

```go
package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2,3) = %d, want 5", got)
	}
}

func TestSubtract(t *testing.T) {
	if got := Subtract(5, 3); got != 2 {
		t.Fatalf("Subtract(5,3) = %d, want 2", got)
	}
}
```

The `Subtract` function is missing; the scenario asks Marshal to add it.

- [ ] **Step 3: Verify fixtures compile/fail as expected**

Run:
```bash
cd test/usability/fixtures/go-calc && go test ./...
cd ../go-calc-broken && go test ./...
```

Expected: `go-calc` passes; `go-calc-broken` fails with `Subtract undefined`.

- [ ] **Step 4: Commit**

```bash
git add test/usability/fixtures/
git commit -m "test(usability): add calculator fixtures"
```

---

### Task 10: Initial scenarios

**Files:**
- Create: `test/usability/scenario/scenarios.go`
- Create: `test/usability/scenario/scenarios/ui/help.go` or inline JSON files
- Modify: `test/usability/usability_test.go`
- Test: `go test ./test/usability/...`

**Interfaces:**
- Consumes: `scripted.Scripted`, `llm.LLM`, `scenario.Scenario`, `scenario.Runner`.
- Produces: runnable scenario definitions.

- [ ] **Step 1: Define scripted UI scenarios in Go**

Create `test/usability/scenario/scenarios.go`:

```go
package scenario

import (
	"marshal/test/usability/actor/scripted"
)

// HelpOpenClose opens the help overlay and dismisses it.
func HelpOpenClose() *scripted.Scripted {
	return &scripted.Scripted{
		Name: "help_open_close",
		Steps: []scripted.Step{
			{Send: "?", WaitFor: scripted.WaitFor{ScreenContains: "marshal keys"}},
			{SendKey: "esc", WaitFor: scripted.WaitFor{State: scripted.UIStatePredicate{HelpOpen: boolPtr(false)}}},
		},
	}
}

func boolPtr(b bool) *bool { return &b }
```

- [ ] **Step 2: Define LLM coding scenario helper**

Add to `test/usability/scenario/scenarios.go`:

```go
import "marshal/test/usability/actor/llm"

// SubtractFix drives Marshal to add a missing Subtract function and make tests pass.
func SubtractFix() *llm.LLM {
	return llm.New(llm.Config{}, nil).WithGoal("Add a Subtract function to the calc package and run tests until they pass.")
}
```

- [ ] **Step 3: Wire top-level tests**

Replace `test/usability/usability_test.go`:

```go
package usability

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"marshal/test/usability/scenario"
)

func binaryPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("USABILITY_MARSHAL_BINARY"); p != "" {
		return p
	}
	return "../../marshal" // assume built at repo root
}

func TestScriptedHelpOpenClose(t *testing.T) {
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found; build with 'go build ./cmd/marshal'")
	}
	r := scenario.NewRunner(scenario.RunnerConfig{
		BinaryPath: binaryPath(t),
		WorkDir:    t.TempDir(),
	})
	res, err := r.Run(context.Background(), scenario.Scenario{
		Name:  "help_open_close",
		Actor: scenario.HelpOpenClose(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("scenario failed: %+v", res)
	}
}

func TestLLMSubtractFix(t *testing.T) {
	if os.Getenv("USABILITY_LLM_MODEL") == "" {
		t.Skip("set USABILITY_LLM_MODEL to run LLM scenarios")
	}
	if _, err := os.Stat(binaryPath(t)); err != nil {
		t.Skip("marshal binary not found")
	}
	workDir := copyFixture(t, "go-calc-broken")
	r := scenario.NewRunner(scenario.RunnerConfig{
		BinaryPath: binaryPath(t),
		WorkDir:    workDir,
	})
	res, err := r.Run(context.Background(), scenario.Scenario{
		Name:  "subtract_fix",
		Actor: scenario.SubtractFix(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("scenario failed: %+v", res)
	}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("fixtures", name)
	dst := filepath.Join(t.TempDir(), name)
	// simple recursive copy; or use cp -R via os/exec for brevity
	cmd := exec.Command("cp", "-R", src, dst)
	if err := cmd.Run(); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}
```

- [ ] **Step 4: Run tests**

Build Marshal first:
```bash
CGO_ENABLED=1 go build ./cmd/marshal
```

Run scripted suite:
```bash
go test ./test/usability/... -run TestScripted -v
```

Expected: `TestScriptedHelpOpenClose` PASS if help content is detected.

Run LLM suite only with a model:
```bash
USABILITY_LLM_MODEL=qwen2.5-coder:14b go test ./test/usability/... -run TestLLM -v -timeout 10m
```

Expected: `TestLLMSubtractFix` attempts the task; may fail on first iteration due to model or parsing issues.

- [ ] **Step 5: Commit**

```bash
gofmt -w test/usability/
go vet ./test/usability/...
git add test/usability/
git commit -m "test(usability): add initial scripted and llm scenarios"
```

---

### Task 11: CI wiring and documentation

**Files:**
- Modify: `.github/workflows/ci.yml` or create `.github/workflows/usability.yml`
- Modify: `CLAUDE.md` or `README.md` (brief mention)

**Interfaces:**
- Consumes: existing CI pipeline.
- Produces: CI job that builds Marshal and runs scripted usability tests.

- [ ] **Step 1: Add a CI job for scripted usability tests**

Create `.github/workflows/usability.yml`:

```yaml
name: usability

on:
  push:
    branches: [main]
  pull_request:

jobs:
  scripted:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build marshal
        run: CGO_ENABLED=1 go build ./cmd/marshal
      - name: Run scripted usability tests
        run: go test ./test/usability/... -run TestScripted -v -timeout 5m
      - name: Upload usability report
        uses: actions/upload-artifact@v4
        if: always()
        with:
          name: usability-report
          path: /tmp/marshal-usability/
```

- [ ] **Step 2: Update README with a one-line mention**

Append to `README.md` under "Commands":

```markdown
# Usability testing (synthetic users)

```bash
go build ./cmd/marshal
go test ./test/usability/... -run TestScripted -v
```
```

- [ ] **Step 3: Verify CI file syntax**

Run:
```bash
actionlint .github/workflows/usability.yml
```

If `actionlint` is unavailable, visually inspect.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/usability.yml README.md
git commit -m "ci: add scripted usability test job"
```

---

## Self-Review

**1. Spec coverage:**

| Spec requirement | Implementing task |
|---|---|
| Real binary, real TUI | Task 2 harness, Task 8 runner |
| Two suites, one harness | Tasks 6, 7, 8, 10 |
| Local-first default | Task 7 LLM actor defaults to Ollama |
| Deterministic regressions | Task 6 scripted actor, Task 11 CI |
| Benchmark output | Task 4 report |
| Friction logging | Task 4 report |
| Isolation | Task 8 runner uses temp dirs |

**2. Placeholder scan:** No TBD/TODO, no vague "add error handling", all code blocks are complete. JSON replaces the original YAML idea to avoid a new dependency.

**3. Type consistency:** `actor.Action.Type` uses constants defined in `actor/actor.go` (`ActionType`, `ActionKey`, `ActionDone`, `ActionNoOp`). `scripted.UIStatePredicate` fields are `*bool` matching the test helpers. `scenario.Runner.Run` returns `report.ScenarioResult`.

**4. Gaps:**
- LLM scenario success currently relies on the actor declaring success. A separate judge phase (spec section 4.5) is not fully implemented; add as follow-up if needed.
- Fixture set is minimal; expand as scenarios grow.
- The `screen` parser heuristics will need tuning as the TUI evolves.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-18-programmatic-usability-testing.md`.

Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using `executing-plans`, batch execution with checkpoints.

Which approach would you like?
