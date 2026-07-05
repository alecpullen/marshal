# Native Text Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore native terminal text selection in the Marshal TUI by removing application-level mouse capture.

**Architecture:** Bubble Tea is currently started with `tea.WithMouseCellMotion()`, which forwards mouse events to the app and prevents the terminal emulator from handling click-drag selection. Removing that option returns mouse control to the terminal. The now-unused `tea.MouseMsg` branch in the TUI model is also removed as cleanup.

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbletea`).

## Global Constraints

- No config changes.
- No new keybindings.
- No new dependencies.
- Keep `tea.WithAltScreen()` unchanged.
- Build requires `CGO_ENABLED=1` (per `CLAUDE.md`).

---

### Task 1: Remove Mouse Capture from Program Options

**Files:**
- Modify: `internal/app/app.go:437-445`

**Interfaces:**
- Consumes: Bubble Tea program options.
- Produces: `runProgram` no longer requests mouse cell motion.

- [ ] **Step 1: Remove the mouse option**

  In `internal/app/app.go`, edit `runProgram` so the program is created without `tea.WithMouseCellMotion()`:

  ```go
  func runProgram(ctx context.Context, model tea.Model, output io.Writer) error {
  	program := tea.NewProgram(model,
  		tea.WithOutput(output),
  		tea.WithContext(ctx),
  		tea.WithAltScreen(),
  	)
  	_, err := program.Run()
  	return err
  }
  ```

- [ ] **Step 2: Verify the file still compiles**

  Run: `go build ./internal/app`
  Expected: success.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/app/app.go
  git commit -m "tui: remove mouse capture so terminal handles selection"
  ```

---

### Task 2: Remove Dead Mouse Message Handling

**Files:**
- Modify: `internal/app/tui/model.go:247-250`

**Interfaces:**
- Consumes: TUI update loop in `Model.Update`.
- Produces: `tea.MouseMsg` branch removed; no other code depends on it.

- [ ] **Step 1: Delete the MouseMsg case**

  In `internal/app/tui/model.go`, remove the `tea.MouseMsg` branch from the `Update` switch:

  ```go
  case tea.MouseMsg:
  	var vpCmd tea.Cmd
  	m.viewport, vpCmd = m.viewport.Update(msg)
  	return m, vpCmd
  ```

  After removal, the surrounding switch should flow directly from `case memory.ClosedMsg:` to `case tea.KeyMsg:`.

- [ ] **Step 2: Run TUI package tests**

  Run: `go test ./internal/app/tui/...`
  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/app/tui/model.go
  git commit -m "tui: remove dead MouseMsg handler"
  ```

---

### Task 3: Verify Full Build and Test Suite

**Files:**
- None (verification only).

**Interfaces:**
- Consumes: Changes from Task 1 and Task 2.
- Produces: Green CI/local build signal.

- [ ] **Step 1: Build the binary**

  Run: `CGO_ENABLED=1 go build ./cmd/marshal`
  Expected: success.

- [ ] **Step 2: Run all tests**

  Run: `go test ./...`
  Expected: PASS.

- [ ] **Step 3: Format and vet**

  Run: `gofmt -w . && go vet ./...`
  Expected: no output/errors.

- [ ] **Step 4: Commit any formatting changes**

  ```bash
  git add -A
  git commit -m "chore: format and vet after mouse removal" || true
  ```

---

## Self-Review Checklist

- [ ] Spec coverage: both changes from the spec (remove `WithMouseCellMotion` and remove `MouseMsg` branch) are represented by tasks.
- [ ] Placeholder scan: no TBD, TODO, or vague steps.
- [ ] Type consistency: no new types or signatures introduced; only deletions.
