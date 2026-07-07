# Novelty-Aware Stall Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the agent loop from force-finalizing legitimate research turns by making stall detection count *repeated* tool calls instead of *read-only* tool calls.

**Architecture:** `progressTracker` (internal/agent/progress.go) gains a seen-set of `(tool, normalizedArgs)` pairs so each recorded call knows whether it is novel. The category-based `readOnlyChurn` heuristic — which hard-stalled after 4 consecutive read/search calls even on distinct files, and after a single prompt-recommended parallel batch of 4 reads — is replaced by duplicate-based churn. Mutating calls (shell/patch/write) reset the seen-set because state changed and re-reads are legitimate again. The soft-stall nudge is rewritten to name the specific repeated call instead of falsely telling the model it is "repeating the same step".

**Tech Stack:** Go, standard library only. Tests use the existing `scriptedProvider` harness in `internal/agent/runner_test.go`.

## Background (diagnosis this plan implements)

Reproduced failure modes (both verified against `RunTask` with scripted providers):

1. A model reading 5 *distinct* files gets a false "you appear to be repeating the same step" nudge after read 3 and is force-finalized after read 4 (`readOnlyChurn(4)` → `assessHardStall` → `finalize()`), never delivering its answer.
2. The system prompt (`prompts.go:98-100`) instructs the model to batch parallel read-only calls in an `actions` array; a single batch of 4 reads records 4 tracker entries and trips the hard stall on the **first** model response.

Once `finalize()` fires mid-investigation, weaker models ignore the no-tools directive and the user gets a salvaged fallback instead of an answer. The working tree already contains an uncommitted fix for that *secondary* symptom (finalize retry-with-correction, raw-JSON stripping, tracker recording on tool errors) — Task 1 commits it. Tasks 2–4 fix the *primary* bug.

## Global Constraints

- Build and test with CGO enabled: `CGO_ENABLED=1 go test ./...` (tree-sitter dependency; see CLAUDE.md).
- Format with `gofmt -w .` and check `go vet ./...` before every commit.
- The TUI renders only — no loop/policy logic may move into `internal/app/tui/`.
- `exactRepeat(3)` (3 identical calls → hard stall) must keep working exactly as today; `TestRunDetectsRepeatedToolCalls` in runner_test.go guards it and must not be modified.
- Do NOT run this plan in a fresh worktree before completing Task 1 — the required finalize fix exists only as uncommitted changes in the current checkout at `/Users/alecpullen/projects/coder-agent`.

---

### Task 1: Commit the in-flight working-tree changes

The working tree has two unrelated uncommitted change sets that must land as separate commits before new work starts.

**Files:**
- Commit (no edits): `internal/agent/finalize.go`, `internal/agent/finalize_test.go`, `internal/agent/runner.go`
- Commit (no edits): `internal/app/tui/model.go`

**Interfaces:**
- Consumes: nothing.
- Produces: a clean working tree; `finalize()` with `maxFinalizeAttempts = 3` retry loop and `extractUsefulProse(raw string) string`, which later tasks' tests coexist with.

- [ ] **Step 1: Verify the in-flight changes pass the full suite**

Run: `CGO_ENABLED=1 go test ./...`
Expected: all packages `ok`. If anything fails, STOP — the in-flight fix is broken and must be repaired before this plan continues.

- [ ] **Step 2: Verify formatting and vet**

Run: `gofmt -l . && go vet ./...`
Expected: no file names printed by gofmt, vet exits clean.

- [ ] **Step 3: Commit the agent finalize fix**

```bash
git add internal/agent/finalize.go internal/agent/finalize_test.go internal/agent/runner.go
git commit -m "fix(agent): retry finalize directive and keep raw tool-call JSON out of salvaged answers"
```

- [ ] **Step 4: Commit the unrelated TUI cursor styling fix**

```bash
git add internal/app/tui/model.go
git commit -m "fix(tui): remove cursor-line and end-of-buffer background artifacts"
```

- [ ] **Step 5: Confirm clean tree**

Run: `git status --short`
Expected: no output.

---

### Task 2: Novelty-aware progress tracker

Replace the category-based churn heuristic with duplicate-based churn in `progressTracker`.

**Files:**
- Modify: `internal/agent/progress.go` (whole file — final content shown below)
- Test: `internal/agent/progress_test.go`

**Interfaces:**
- Consumes: `categorize(toolName string) toolCategory` (unchanged, same file).
- Produces (used by Task 3):
  - `(*progressTracker).record(name, normalizedArgs string)` — same signature as today; call sites in runner.go need no change.
  - `(*progressTracker).assess() assessment` — same signature.
  - `(*progressTracker).lastCall() (name, args string, ok bool)` — new; Task 3's nudge builder consumes it.
  - New semantics: distinct-args calls never count toward churn; `duplicateChurn(3)` → `assessStalling`, `duplicateChurn(5)` → `assessHardStall`, `exactRepeat(3)` → `assessHardStall` (unchanged); mutating calls (`shell.run`, `file.write_patch`) reset novelty.

- [ ] **Step 1: Rewrite the tracker tests to specify the new behavior**

Replace the entire `TestAssess` function in `internal/agent/progress_test.go` with the version below, and add `TestMutating` and `TestLastCall`. Keep `TestCategorize` untouched. The old subtests "three distinct reads is stalling" and "stall persists to hard stall" are deliberately deleted — they encode the bug. Add `"fmt"` to the imports.

```go
func TestAssess(t *testing.T) {
	t.Run("empty is progressing", func(t *testing.T) {
		if got := newProgressTracker().assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})

	t.Run("exact repeat 3x is hard stall", func(t *testing.T) {
		tr := newProgressTracker()
		for i := 0; i < 3; i++ {
			tr.record("file.read", `{"path":"a.go"}`)
		}
		if got := tr.assess(); got != assessHardStall {
			t.Fatalf("assess() = %v, want hardStall", got)
		}
	})

	t.Run("sustained distinct reads are progressing", func(t *testing.T) {
		// Regression: the old readOnlyChurn heuristic nudged after 3 and
		// hard-stalled after 4 consecutive read/search calls even when every
		// call targeted a different file.
		tr := newProgressTracker()
		for i := 0; i < 8; i++ {
			tr.record("file.read", fmt.Sprintf(`{"path":"f%d.go"}`, i))
			if got := tr.assess(); got != assessProgressing {
				t.Fatalf("assess() after %d distinct reads = %v, want progressing", i+1, got)
			}
		}
	})

	t.Run("distinct mixed reads and searches are progressing", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("repo.search", `{"query":"bar"}`)
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})

	t.Run("three trailing duplicates is stalling", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		// Revisit all three previously seen calls.
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		if got := tr.assess(); got != assessStalling {
			t.Fatalf("assess() = %v, want stalling", got)
		}
	})

	t.Run("five trailing duplicates is hard stall", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("file.read", `{"path":"c.go"}`)
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("file.read", `{"path":"c.go"}`)
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		if got := tr.assess(); got != assessHardStall {
			t.Fatalf("assess() = %v, want hardStall", got)
		}
	})

	t.Run("mutation resets novelty so re-reads are progress", func(t *testing.T) {
		// Re-reading a file after patching it is normal verification, not a
		// loop: the write invalidates earlier observations.
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.write_patch", `{"patch":"..."}`)
		tr.record("file.read", `{"path":"a.go"}`)
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})

	t.Run("recent write is progressing", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.write_patch", `{"patch":"..."}`)
		tr.record("shell.run", `{"command":"go test ./..."}`)
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})
}

func TestMutating(t *testing.T) {
	cases := map[toolCategory]bool{
		catShell:  true,
		catWrite:  true,
		catPatch:  true,
		catRead:   false,
		catSearch: false,
		catOther:  false,
	}
	for cat, want := range cases {
		if got := mutating(cat); got != want {
			t.Errorf("mutating(%q) = %v, want %v", cat, got, want)
		}
	}
}

func TestLastCall(t *testing.T) {
	tr := newProgressTracker()
	if _, _, ok := tr.lastCall(); ok {
		t.Fatal("lastCall() on empty tracker reported ok")
	}
	tr.record("file.read", `{"path":"a.go"}`)
	tr.record("repo.search", `{"query":"foo"}`)
	name, args, ok := tr.lastCall()
	if !ok || name != "repo.search" || args != `{"query":"foo"}` {
		t.Fatalf("lastCall() = %q, %q, %v; want repo.search / query foo / true", name, args, ok)
	}
}
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestAssess|TestMutating|TestLastCall' -v`
Expected: compile error (`undefined: mutating`, `tr.lastCall undefined`). A compile failure is the red state here — do not write stubs; proceed straight to Step 3.

- [ ] **Step 3: Rewrite progress.go**

Replace the entire contents of `internal/agent/progress.go` with:

```go
package agent

type toolCategory string

const (
	catRead   toolCategory = "read"
	catSearch toolCategory = "search"
	catShell  toolCategory = "shell"
	catWrite  toolCategory = "write"
	catPatch  toolCategory = "patch"
	catOther  toolCategory = "other"
)

func categorize(toolName string) toolCategory {
	switch toolName {
	case "file.read", "repo.card", "repo.index", "repo.map", "symbols.find":
		return catRead
	case "repo.search":
		return catSearch
	case "shell.run":
		return catShell
	case "file.write_patch":
		return catPatch
	default:
		return catOther
	}
}

// mutating reports whether a category of tool call can change repository or
// system state. After a mutating call, previously gathered observations are
// stale, so repeating an earlier read counts as fresh progress again.
func mutating(cat toolCategory) bool {
	return cat == catShell || cat == catWrite || cat == catPatch
}

type assessment int

const (
	assessProgressing assessment = iota
	assessStalling
	assessHardStall
)

type callEntry struct {
	name string
	args string
	cat  toolCategory
	// novel is true when this (name, args) pair had not been executed since
	// the last mutating call. Novel work is progress by definition; only
	// repeats of already-gathered results count toward churn.
	novel bool
}

type progressTracker struct {
	history []callEntry
	seen    map[string]struct{}
}

func newProgressTracker() *progressTracker {
	return &progressTracker{seen: make(map[string]struct{})}
}

func (t *progressTracker) record(name, normalizedArgs string) {
	cat := categorize(name)
	if mutating(cat) {
		// State changed: earlier reads are stale, future re-reads are novel.
		t.seen = make(map[string]struct{})
	}
	key := name + "\x00" + normalizedArgs
	_, dup := t.seen[key]
	t.seen[key] = struct{}{}
	t.history = append(t.history, callEntry{
		name:  name,
		args:  normalizedArgs,
		cat:   cat,
		novel: !dup,
	})
}

// exactRepeat reports whether the last n entries are the same call
// (name+args). Novelty is deliberately ignored here: in a 3x repeat the
// first occurrence is novel and the rest are not, yet all three are the
// same call.
func (t *progressTracker) exactRepeat(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	last := h[len(h)-1]
	for i := len(h) - n; i < len(h)-1; i++ {
		if h[i].name != last.name || h[i].args != last.args {
			return false
		}
	}
	return true
}

// duplicateChurn reports whether the last n entries all repeat calls whose
// results were already gathered this turn (no novel work).
func (t *progressTracker) duplicateChurn(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	for i := len(h) - n; i < len(h); i++ {
		if h[i].novel {
			return false
		}
	}
	return true
}

// lastCall returns the most recent recorded call so nudge messages can name
// the specific repeated call. ok is false when nothing has been recorded.
func (t *progressTracker) lastCall() (name, args string, ok bool) {
	if len(t.history) == 0 {
		return "", "", false
	}
	last := t.history[len(t.history)-1]
	return last.name, last.args, true
}

func (t *progressTracker) assess() assessment {
	if len(t.history) < 3 {
		return assessProgressing
	}
	if t.exactRepeat(3) {
		return assessHardStall
	}
	if t.duplicateChurn(5) {
		return assessHardStall
	}
	if t.duplicateChurn(3) {
		return assessStalling
	}
	return assessProgressing
}
```

Note what changed vs. the old file: `readOnlyChurn` is gone; `callEntry` gained `novel`; `progressTracker` gained `seen`; `newProgressTracker` initializes the map; `exactRepeat` now compares `name`/`args` fields explicitly (struct equality would break at exactly 3 repeats because the first occurrence's `novel` differs); `mutating`, `duplicateChurn`, `lastCall` are new; `assess` thresholds changed.

- [ ] **Step 4: Run the tracker tests**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestAssess|TestMutating|TestLastCall|TestCategorize' -v`
Expected: all PASS.

- [ ] **Step 5: Run the whole agent package**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...`
Expected: `ok`. In particular `TestRunDetectsRepeatedToolCalls` (exact-repeat hard stall) and `TestExhaustionSalvagesInsteadOfFailing` (distinct reads now exhaust without nudges — it asserts only salvage status, so it still passes) must be green. If either fails, the implementation is wrong; do not edit those tests.

- [ ] **Step 6: Format, vet, commit**

```bash
gofmt -w internal/agent/progress.go internal/agent/progress_test.go
go vet ./internal/agent/...
git add internal/agent/progress.go internal/agent/progress_test.go
git commit -m "fix(agent): stall detection counts repeated calls, not read-only work"
```

---

### Task 3: Nudge names the specific repeated call

Replace the generic (and previously false) "you appear to be repeating the same step" message with one that cites the actual duplicated call, so a compliant model can correct course.

**Files:**
- Modify: `internal/agent/runner.go` (constant block at ~line 22 and `maybeFinalizeOnStall` at ~line 301)
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `(*progressTracker).lastCall() (name, args string, ok bool)` from Task 2.
- Produces: unexported `buildLoopNudge(name, args string) string`. `maybeFinalizeOnStall`'s signature is unchanged.

- [ ] **Step 1: Write the failing runner-level test**

Append to `internal/agent/runner_test.go` (it already imports `fmt`, `strings`, `session`, `registry`, `policy`, `config`):

```go
func TestRunNudgeNamesRepeatedCall(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	read := func(path string) string {
		return fmt.Sprintf(`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":%q}}}`, path)
	}
	// Three novel reads, then the same three again: the 6th call makes the
	// trailing three all duplicates -> soft stall -> nudge; the model then
	// answers normally on the 7th response.
	p := &scriptedProvider{responses: []string{
		read("a.go"), read("b.go"), read("c.go"),
		read("a.go"), read("b.go"), read("c.go"),
		`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "how does pkg work?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" || task.Summary != "Answer." {
		t.Fatalf("task = %+v, want un-salvaged completion with Summary=Answer.", task)
	}
	foundNudge := false
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem &&
			strings.Contains(m.Content, "file.read") &&
			strings.Contains(m.Content, "c.go") {
			foundNudge = true
		}
	}
	if !foundNudge {
		t.Fatal("expected a soft-stall nudge naming the repeated call (file.read c.go)")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestRunNudgeNamesRepeatedCall -v`
Expected: FAIL at "expected a soft-stall nudge naming the repeated call" — the stall fires but the old generic `loopNudgeMessage` doesn't contain the tool name or path.

- [ ] **Step 3: Implement the specific nudge**

In `internal/agent/runner.go`, delete the `loopNudgeMessage` constant from the `const` block at the top of the file (leave the other constants untouched), and change `maybeFinalizeOnStall` to:

```go
func (r *Runner) maybeFinalizeOnStall(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task) (finalized bool, res *Task, err error, nudge string) {
	r.trackerMu.Lock()
	a := r.tracker.assess()
	dupName, dupArgs, _ := r.tracker.lastCall()
	r.trackerMu.Unlock()

	switch a {
	case assessHardStall:
		res, ferr := r.finalize(ctx, p, model, messages, task, reasonStalled)
		return true, res, ferr, ""
	case assessStalling:
		return false, task, nil, buildLoopNudge(dupName, dupArgs)
	}
	return false, task, nil, ""
}

// buildLoopNudge tells the model exactly which call it is repeating. A
// stalling assessment implies at least three recorded calls, the last of
// which is a duplicate, so lastCall's result is always usable here.
func buildLoopNudge(name, args string) string {
	return fmt.Sprintf(
		"You are repeating tool calls you already made — most recently %s with args %s. Those results are in the transcript above; use them instead of calling the tool again. Take a genuinely new action or produce a final answer.",
		name, args,
	)
}
```

Keep the existing doc comment above `maybeFinalizeOnStall` (the one explaining why the nudge is returned rather than appended).

- [ ] **Step 4: Run the test and the package**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...`
Expected: all PASS, including the new `TestRunNudgeNamesRepeatedCall`.

- [ ] **Step 5: Format, vet, commit**

```bash
gofmt -w internal/agent/runner.go internal/agent/runner_test.go
go vet ./internal/agent/...
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): name the repeated call in the soft-stall nudge"
```

---

### Task 4: End-to-end regression tests for the two diagnosed failure modes

These encode the original bug reports as permanent guards. They must pass immediately given Tasks 2–3; if either fails, stop and fix the tracker rather than adjusting the test.

**Files:**
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `scriptedProvider` (runner_test.go), `newTestState` (runner_test.go), Task 2 tracker semantics.
- Produces: nothing consumed later.

- [ ] **Step 1: Add the sequential-research regression test**

Append to `internal/agent/runner_test.go`:

```go
func TestRunAllowsSustainedDistinctReadsBeforeAnswering(t *testing.T) {
	// Regression for the "agent never produces an answer" bug: five distinct
	// file reads used to trip readOnlyChurn(4) and force finalize after the
	// 4th read, cutting research off before the model could answer.
	reg := registry.New()
	var executed []string
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = append(executed, string(call.Args))
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	responses := make([]string, 0, 6)
	for i := 1; i <= 5; i++ {
		responses = append(responses, fmt.Sprintf(
			`{"rationale":"reading file %d of 5","action":{"type":"tool_call","tool":"file.read","args":{"path":"pkg/f%d.go"}}}`, i, i))
	}
	responses = append(responses,
		`{"rationale":"done","action":{"type":"final","content":"THE REAL ANSWER after reading all five files."}}`)

	p := &scriptedProvider{responses: responses}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "how does pkg work?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if len(executed) != 5 {
		t.Fatalf("executed %d reads, want all 5", len(executed))
	}
	if task.Status != TaskStatusCompleted || task.SalvagedReason != "" {
		t.Fatalf("task = %+v, want normal (un-salvaged) completion", task)
	}
	if task.Summary != "THE REAL ANSWER after reading all five files." {
		t.Fatalf("Summary = %q, want the model's own final answer", task.Summary)
	}
	if p.calls != 6 {
		t.Fatalf("provider calls = %d, want 6 (5 reads + 1 final, no finalize calls)", p.calls)
	}
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "repeating") {
			t.Fatalf("distinct reads drew a repetition nudge: %q", m.Content)
		}
	}
}
```

- [ ] **Step 2: Add the parallel-batch regression test**

```go
func TestRunAllowsParallelReadBatchWithoutStalling(t *testing.T) {
	// Regression: a single actions-array batch of four distinct reads — the
	// exact pattern baseOutputFormat recommends — used to record four churn
	// entries and hard-stall on the very first model response.
	reg := registry.New()
	var executed []string
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = append(executed, string(call.Args))
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []string{
		`{"rationale":"read all four relevant files at once","actions":[
			{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}},
			{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}},
			{"type":"tool_call","tool":"file.read","args":{"path":"c.go"}},
			{"type":"tool_call","tool":"file.read","args":{"path":"d.go"}}]}`,
		`{"rationale":"one more file","action":{"type":"tool_call","tool":"file.read","args":{"path":"e.go"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"REAL ANSWER."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "how does pkg work?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if len(executed) != 5 {
		t.Fatalf("executed %d reads, want 5 (batch of 4 + 1 follow-up)", len(executed))
	}
	if task.SalvagedReason != "" || task.Summary != "REAL ANSWER." {
		t.Fatalf("task = %+v, want un-salvaged completion with the model's answer", task)
	}
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (batch, single read, final)", p.calls)
	}
}
```

- [ ] **Step 3: Run both new tests**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestRunAllowsSustainedDistinctReadsBeforeAnswering|TestRunAllowsParallelReadBatchWithoutStalling' -v`
Expected: both PASS on the first run. If either fails, the Task 2/3 implementation is wrong — fix it there; do not weaken these assertions.

- [ ] **Step 4: Run the entire repository suite**

Run: `CGO_ENABLED=1 go test -count=1 ./...`
Expected: every package `ok` (includes `internal/agent/swarm`, which shares the Runner).

- [ ] **Step 5: Format, vet, commit**

```bash
gofmt -w internal/agent/runner_test.go
go vet ./...
git add internal/agent/runner_test.go
git commit -m "test(agent): regression coverage for research reads and parallel batches"
```

---

## Verification of the original complaint

After Task 4, the two reproductions from the diagnosis are permanent tests. Manual smoke check (optional, requires a configured local model): run `go run ./cmd/marshal`, ask a repo question that needs several file reads (e.g. "how does config merging work?"), and confirm the agent reads multiple files without a "repeating the same step" nudge and finishes with its own answer rather than a "stalled" salvage banner.
