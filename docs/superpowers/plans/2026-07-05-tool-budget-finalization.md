# Tool-Budget Finalization & Anti-Spiral Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the agent from exhausting its tool-iteration budget with no answer — salvage a best-effort final answer on exhaustion, detect churn by category and escalate to a forced conclusion, and surface budget state in the TUI.

**Architecture:** All three runtime changes converge on one primitive, `Runner.finalize`, which makes a single no-tools model call that must produce a final answer (Approach A from the spec). A new `progressTracker` classifies each executed tool call and reports `progressing` / `stalling` / `hardStall`. The loop injects soft pressure near the budget end, nudges on `stalling`, calls `finalize` on `hardStall`, and calls `finalize` on exhaustion instead of bare-failing. A salvaged answer is a `Completed` task carrying a `SalvagedReason`, and the TUI shows a `tools N/Max` counter plus a salvage marker.

**Tech Stack:** Go, Bubble Tea (TUI). Tests are standard `go test` table tests in `_test.go` files alongside sources.

## Global Constraints

- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter). Build with `go build ./cmd/marshal`.
- Run all tests with `go test ./...`; a single package with e.g. `go test ./internal/agent/...`.
- Format with `gofmt -w .` before every commit; vet with `go vet ./...`.
- The TUI renders only — no routing/policy/prompt logic in `internal/app/tui`.
- Default budget stays **16** (`DefaultMaxToolIterations`, `runner.go:23`). Do not change it.
- Budget state reaches the model only qualitatively (pressure/nudge text). Never inject a numeric counter into `messages`.
- A salvaged turn is `TaskStatusCompleted` (never returns `ErrMaxIterationsExceeded`) unless the salvage model call itself errors.
- Package is `marshal`; module-relative imports like `marshal/internal/agent`.

---

## File Structure

- `internal/agent/progress.go` (**new**) — `progressTracker`, `toolCategory`, `assessment`, and the `assess`/`record` logic. One responsibility: classify tool-call history into a stall assessment.
- `internal/agent/progress_test.go` (**new**) — unit tests for the tracker.
- `internal/agent/finalize.go` (**new**) — `Runner.finalize`, the `finalizeReason` type, and the finalization directive prompt text + synthesized fallback builder. One responsibility: force a no-tools final answer.
- `internal/agent/finalize_test.go` (**new**) — unit tests for `finalize`.
- `internal/agent/runner.go` (**modify**) — replace `callHistory`/`shouldNudgeLoop`/`loopNudgeSent` with the tracker; add soft-pressure injection, hard-stall handling, and exhaustion salvage; update `State.ToolBudget` each iteration.
- `internal/agent/prompts.go` (**modify**) — tighten `baseRules`; export the finalization directive constant used by `finalize.go`.
- `internal/agent/task.go` (**modify**) — add `SalvagedReason string` to `Task`.
- `internal/app/session/session.go` (**modify**) — add `ToolBudget` type + field + `SetToolBudget`/`ToolBudget` accessors; add `Salvaged` marker on the final `Message` and a `SetSalvage`/state field, plus an `AddMessageSalvaged` helper (or a variant of `AddMessageFinal`).
- `internal/app/tui/view.go` (**modify**) — render `tools N/Max` in the activity strip and a salvage marker.
- Test files: `runner_test.go`, `session_test.go`, `view_test.go` updated alongside.

**Already done (verify-only):** `max_tool_iterations` is already a TUI settings field (`internal/app/tui/settings/model.go:124-128`) and already round-trips through `config.go`/`save.go`. Spec Component 5's "tunable" half needs no new work — Task 8 only verifies it.

---

## Task 1: `progressTracker` — category classification & assessment

**Files:**
- Create: `internal/agent/progress.go`
- Test: `internal/agent/progress_test.go`

**Interfaces:**
- Consumes: `registry.RiskLevel` (`registry.RiskReadOnly` etc.), `normalizeArgs` from `runner.go`.
- Produces:
  - `type toolCategory string` with consts `catRead`, `catSearch`, `catShell`, `catWrite`, `catPatch`, `catOther`.
  - `func categorize(toolName string) toolCategory`
  - `type assessment int` with consts `assessProgressing`, `assessStalling`, `assessHardStall`.
  - `type progressTracker struct{...}` with:
    - `func newProgressTracker() *progressTracker`
    - `func (t *progressTracker) record(name, normalizedArgs string)` — appends one executed call.
    - `func (t *progressTracker) assess() assessment` — classifies current history.

**Category rules (`categorize`):**
- `file.read`, `repo.card`, `repo.index`, `repo.map`, `symbols.find` → `catRead`
- `repo.search` → `catSearch`
- `shell.run` → `catShell`
- `file.write_patch` → `catPatch`
- anything else → `catOther`
(There is no distinct `catWrite` tool today; keep the const for future write tools but map nothing to it now.)

**Assessment rules (`assess`), evaluated on the recorded history:**
- Fewer than 3 calls recorded → `assessProgressing`.
- **Exact-repeat 3×**: last three entries have identical `{name, normalizedArgs}` → `assessHardStall` (preserves today's `shouldNudgeLoop` behavior as the strongest signal).
- **Stalling**: the last 3 calls are all in `{catRead, catSearch}` **and** no `catPatch`/`catWrite`/`catShell` call has occurred since (i.e. among the last 3) → `assessStalling`.
- **Hard stall by persistence**: `assessStalling` conditions hold across the last **4** calls (stall persisted an extra iteration) → `assessHardStall`.
- Otherwise → `assessProgressing`.

- [ ] **Step 1: Write the failing test**

```go
package agent

import "testing"

func TestCategorize(t *testing.T) {
	cases := map[string]toolCategory{
		"file.read":       catRead,
		"symbols.find":    catRead,
		"repo.search":     catSearch,
		"shell.run":       catShell,
		"file.write_patch": catPatch,
		"mystery.tool":    catOther,
	}
	for name, want := range cases {
		if got := categorize(name); got != want {
			t.Errorf("categorize(%q) = %q, want %q", name, got, want)
		}
	}
}

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

	t.Run("three distinct reads is stalling", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		if got := tr.assess(); got != assessStalling {
			t.Fatalf("assess() = %v, want stalling", got)
		}
	})

	t.Run("stall persists to hard stall", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("repo.search", `{"query":"bar"}`)
		if got := tr.assess(); got != assessHardStall {
			t.Fatalf("assess() = %v, want hardStall", got)
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run 'TestCategorize|TestAssess' -v`
Expected: FAIL — `undefined: categorize`, `undefined: newProgressTracker`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/progress.go`:

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
}

type progressTracker struct {
	history []callEntry
}

func newProgressTracker() *progressTracker {
	return &progressTracker{}
}

func (t *progressTracker) record(name, normalizedArgs string) {
	t.history = append(t.history, callEntry{
		name: name,
		args: normalizedArgs,
		cat:  categorize(name),
	})
}

// exactRepeat reports whether the last n entries are byte-identical.
func (t *progressTracker) exactRepeat(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	last := h[len(h)-1]
	for i := len(h) - n; i < len(h)-1; i++ {
		if h[i] != last {
			return false
		}
	}
	return true
}

// readOnlyChurn reports whether the last n entries are all read/search.
func (t *progressTracker) readOnlyChurn(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	for i := len(h) - n; i < len(h); i++ {
		if h[i].cat != catRead && h[i].cat != catSearch {
			return false
		}
	}
	return true
}

func (t *progressTracker) assess() assessment {
	if len(t.history) < 3 {
		return assessProgressing
	}
	if t.exactRepeat(3) {
		return assessHardStall
	}
	if t.readOnlyChurn(4) {
		return assessHardStall
	}
	if t.readOnlyChurn(3) {
		return assessStalling
	}
	return assessProgressing
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run 'TestCategorize|TestAssess' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/progress.go internal/agent/progress_test.go
git add internal/agent/progress.go internal/agent/progress_test.go
git commit -m "feat(agent): add progressTracker for category-based stall detection"
```

---

## Task 2: `Task.SalvagedReason` field

**Files:**
- Modify: `internal/agent/task.go:29-36`
- Test: `internal/agent/task.go` covered indirectly; add a compile-level assertion in `internal/agent/finalize_test.go` in Task 4. No standalone test here — this is a one-field struct change folded into Task 4's cycle.

**Interfaces:**
- Produces: `Task.SalvagedReason string` — empty for clean completions, set to a short reason (`"exhausted"` / `"stalled"`) for salvaged ones.

- [ ] **Step 1: Add the field**

In `internal/agent/task.go`, add to the `Task` struct (after `StartedAt`):

```go
type Task struct {
	Goal           string
	Class          TaskClass
	Status         TaskStatus
	Plan           []string
	Summary        string
	StartedAt      time.Time
	SalvagedReason string // non-empty when the final answer was forced under budget pressure
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/agent/`
Expected: builds cleanly (no test yet; exercised in Task 4).

- [ ] **Step 3: Commit**

```bash
gofmt -w internal/agent/task.go
git add internal/agent/task.go
git commit -m "feat(agent): add Task.SalvagedReason"
```

---

## Task 3: session `ToolBudget` + salvage marker

**Files:**
- Modify: `internal/app/session/session.go` (struct at `:118`, consts near `:53`, accessors near `:365`)
- Test: `internal/app/session/session_test.go`

**Interfaces:**
- Produces:
  - `type ToolBudget struct { Used, Max int }`
  - `func (s *State) SetToolBudget(b ToolBudget)`
  - `func (s *State) ToolBudget() ToolBudget`
  - `func (s *State) AddMessageSalvaged(role Role, content string, contentType ContentType, reason string)` — like `AddMessageFinal` but sets `Message.Salvaged = true` and records `reason`.
  - `Message.Salvaged bool` and `Message.SalvageReason string` fields.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/session/session_test.go`:

```go
func TestToolBudget(t *testing.T) {
	s := NewState() // use the same constructor other tests in this file use
	if b := s.ToolBudget(); b.Used != 0 || b.Max != 0 {
		t.Fatalf("zero value = %+v, want {0 0}", b)
	}
	s.SetToolBudget(ToolBudget{Used: 5, Max: 16})
	if b := s.ToolBudget(); b.Used != 5 || b.Max != 16 {
		t.Fatalf("ToolBudget() = %+v, want {5 16}", b)
	}
}

func TestAddMessageSalvaged(t *testing.T) {
	s := NewState()
	s.AddMessageSalvaged(RoleAssistant, "best effort answer", ContentTypeMarkdown, "exhausted")
	msgs := s.Messages()
	last := msgs[len(msgs)-1]
	if !last.Final || !last.Salvaged || last.SalvageReason != "exhausted" {
		t.Fatalf("last message = %+v, want Final+Salvaged+reason=exhausted", last)
	}
}
```

> Note: confirm the actual `State` constructor name used in `session_test.go` (e.g. `NewState(...)`) and match it; the file already constructs `State` in existing tests.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/session/ -run 'TestToolBudget|TestAddMessageSalvaged' -v`
Expected: FAIL — `undefined: (*State).ToolBudget`, `undefined: ToolBudget`, `Message.Salvaged` unknown field.

- [ ] **Step 3: Write minimal implementation**

In `internal/app/session/session.go`:

Add to the `Message` struct (`:53-61`):

```go
type Message struct {
	Role          Role
	Content       string
	ContentType   ContentType
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
	Final         bool
	Salvaged      bool
	SalvageReason string
}
```

Add a `ToolBudget` type near the other small types and a field on `State` (add `toolBudget ToolBudget` inside `type State struct` at `:118`):

```go
type ToolBudget struct {
	Used int
	Max  int
}
```

Add accessors (near `SetActivity`/`Activity` at `:365`):

```go
func (s *State) SetToolBudget(b ToolBudget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolBudget = b
}

func (s *State) ToolBudget() ToolBudget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolBudget
}
```

Add `AddMessageSalvaged` by copying `AddMessageFinal` (`:198-227`) and setting the two new fields. Keep the persistence call identical (the DB signature is unchanged; `Salvaged` is display-only and not persisted):

```go
func (s *State) AddMessageSalvaged(role Role, content string, contentType ContentType, reason string) {
	s.mu.Lock()
	reasoning := s.inProgress.Reasoning
	var thinkDuration time.Duration
	if reasoning != "" {
		thinkDuration = time.Since(s.inProgress.StartedAt)
		if thinkDuration <= 0 {
			thinkDuration = time.Millisecond
		}
	}
	s.inProgress = InProgressMessage{}

	msg := Message{
		Role:          role,
		Content:       content,
		ContentType:   contentType,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     time.Now(),
		Final:         true,
		Salvaged:      true,
		SalvageReason: reason,
	}
	s.messages = append(s.messages, msg)
	s.mu.Unlock()

	if s.persistenceEnabled() {
		if err := s.db.SaveMessage(s.sessionID, string(role), content, string(contentType), msg.CreatedAt, reasoning, thinkDuration, true); err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/session/ -run 'TestToolBudget|TestAddMessageSalvaged' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/session/session.go internal/app/session/session_test.go
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): add ToolBudget state and salvaged message marker"
```

---

## Task 4: `Runner.finalize` primitive

**Files:**
- Create: `internal/agent/finalize.go`
- Modify: `internal/agent/prompts.go` (add `FinalizationDirective` constant)
- Test: `internal/agent/finalize_test.go`

**Interfaces:**
- Consumes: `Runner.chatWithRetry` (`runner.go:343`), `ParseAction` (`protocol.go:53`), `ActionAnswer`/`ActionFinal`, `session.State.AddMessageSalvaged` (Task 3), `Task.SalvagedReason` (Task 2), `schema.ChatMessage`, `schema.RoleSystem`.
- Produces:
  - `type finalizeReason string` with consts `reasonExhausted finalizeReason = "exhausted"`, `reasonStalled finalizeReason = "stalled"`.
  - `func (r *Runner) finalize(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, reason finalizeReason) (*Task, error)`
  - Returns `(task, nil)` with `task.Status = TaskStatusCompleted` and `task.SalvagedReason = string(reason)` on success (including the synthesized-fallback path); returns `(task, err)` only when the underlying `chatWithRetry` errors.

**Behavior:**
1. Append a system directive message (`FinalizationDirective`) to a copy of `messages`.
2. One `chatWithRetry` call with the turn's `p`/`model`.
3. `ParseAction(raw)`:
   - `ActionAnswer`/`ActionFinal` → record via `AddMessageSalvaged`, complete.
   - Anything else (tool call, patch, parse error) → build a synthesized fallback from `task.Plan` + `raw` prose and record it via `AddMessageSalvaged`, complete.
4. `chatWithRetry` error → return it.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/finalize_test.go`. Reuse the existing scripted fake provider pattern from `runner_test.go` (see the fake around `runner_test.go:35`). This test drives `finalize` directly:

```go
package agent

import (
	"context"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
)

func TestFinalizeProducesFlaggedCompletion(t *testing.T) {
	state := session.NewState() // match constructor used elsewhere in runner_test.go
	// scriptedProvider returns the given raw strings in order; see runner_test.go.
	prov := newScriptedProvider(`{"rationale":"done","action":{"type":"final","content":"Here is my best answer."}}`)
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonExhausted)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.SalvagedReason != "exhausted" {
		t.Fatalf("SalvagedReason = %q, want exhausted", got.SalvagedReason)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content != "Here is my best answer." {
		t.Fatalf("final message = %+v, want salvaged answer", last)
	}
}

func TestFinalizeSynthesizesWhenModelIgnoresDirective(t *testing.T) {
	state := session.NewState()
	// Model keeps trying to call a tool instead of answering.
	prov := newScriptedProvider(`{"rationale":"one more read","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`)
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read x.go", "Patch it"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted || got.SalvagedReason != "stalled" {
		t.Fatalf("task = %+v, want completed+stalled", got)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content == "" {
		t.Fatalf("expected non-empty synthesized salvage message, got %+v", last)
	}
}
```

> If `newScriptedProvider` / `session.NewState` have different names in this repo, match the existing helpers in `runner_test.go` and `session_test.go` exactly. Do not invent new fakes.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestFinalize -v`
Expected: FAIL — `undefined: (*Runner).finalize`, `undefined: reasonExhausted`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/agent/prompts.go`:

```go
const FinalizationDirective = `You are being asked to stop using tools and conclude this turn. Produce the best final answer you can from the transcript, context pack, and tool results already gathered. Do NOT call tools. If a required fact is genuinely missing, state what you would check next and give your best partial answer. Respond with a single action of type "final".`
```

Create `internal/agent/finalize.go`:

```go
package agent

import (
	"context"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

type finalizeReason string

const (
	reasonExhausted finalizeReason = "exhausted"
	reasonStalled   finalizeReason = "stalled"
)

// finalize makes one no-tools model call that must produce a final answer, then
// records it as a salvaged (flagged) completion. It never returns an
// ErrMaxIterationsExceeded-style failure: the only error path is a transport
// failure from chatWithRetry. A model that ignores the directive and tries to
// call a tool anyway is handled by synthesizing a fallback answer.
func (r *Runner) finalize(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, reason finalizeReason) (*Task, error) {
	final := append(append([]schema.ChatMessage{}, messages...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: FinalizationDirective})

	raw, err := r.chatWithRetry(ctx, p, model, final)
	if err != nil {
		return task, err
	}

	content := ""
	if action, parseErr := ParseAction(raw); parseErr == nil &&
		(action.Type == ActionAnswer || action.Type == ActionFinal) {
		content = action.Content
	}
	if strings.TrimSpace(content) == "" {
		content = synthesizeFallback(task, raw)
	}

	task.Summary = content
	task.Status = TaskStatusCompleted
	task.SalvagedReason = string(reason)
	r.State.AddMessageSalvaged(session.RoleAssistant, content, session.ContentTypeMarkdown, string(reason))
	return task, nil
}

// synthesizeFallback builds a best-effort answer when the model refuses to
// conclude. It stitches together any prose the model emitted plus the plan so
// the user is never left with nothing.
func synthesizeFallback(task *Task, raw string) string {
	var b strings.Builder
	b.WriteString("I ran out of tool budget before fully finishing. Here is my best summary of progress.\n\n")
	if len(task.Plan) > 0 {
		b.WriteString("Plan I was following:\n")
		for _, step := range task.Plan {
			fmt.Fprintf(&b, "- %s\n", step)
		}
		b.WriteString("\n")
	}
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		b.WriteString("Latest model output:\n")
		b.WriteString(trimmed)
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestFinalize -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/finalize.go internal/agent/prompts.go internal/agent/finalize_test.go
git add internal/agent/finalize.go internal/agent/prompts.go internal/agent/finalize_test.go
git commit -m "feat(agent): add finalize primitive for forced final answers"
```

---

## Task 5: Wire the tracker + soft pressure + hard-stall into the loop

**Files:**
- Modify: `internal/agent/runner.go` — struct fields (`:106-110`), `RunTask` reset (`:156-159`), the `for` loop (`:205-253`), `executeToolCall` nudge sites (`:442-445`, `:519-522`), and `recordToolCall`/`shouldNudgeLoop` (`:526-547`).
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `newProgressTracker`, `assessProgressing/Stalling/HardStall` (Task 1); `finalize`, `reasonStalled` (Task 4); `session.State.SetToolBudget` (Task 3).
- Produces: no new exported API; changes loop behavior.

**Design notes:**
- Replace the `callHistory []toolCallKey`, `callHistoryMu`, and `loopNudgeSent` fields with `tracker *progressTracker` and its mutex. Delete `toolCallKey`, `recordToolCall`, and `shouldNudgeLoop`; the tracker subsumes them.
- `executeToolCall` currently both records history and decides the nudge inline. Move that decision to the loop: `executeToolCall` still records into the tracker (via `r.tracker.record`) but no longer appends the nudge message itself. The loop inspects `r.tracker.assess()` after each execution and injects the nudge or calls `finalize`.
- Add a package const near `:22`: `finalizePressureThreshold = 2`.
- The soft-pressure message is a new const, e.g.:
  `finalizePressureMessage = "You are near the tool budget. Unless one specific missing fact is required, produce a final answer now using the results you already have."`
- Track a per-turn `pressureSent bool` local in `RunTask` so soft pressure is injected at most once.
- Update `State.SetToolBudget(session.ToolBudget{Used: iteration, Max: r.MaxToolIterations})` at the top of each loop iteration.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/runner_test.go`. These lean on the existing scripted provider + `Runner` construction already used by `TestRunTask...` cases (see `runner_test.go:35` and the exhaustion test at `:394`).

```go
func TestExhaustionSalvagesInsteadOfFailing(t *testing.T) {
	// Model always calls a tool, never answers -> would exhaust the budget.
	// After the loop, finalize (scripted to answer) must salvage.
	state := session.NewState()
	prov := newLoopingThenFinalProvider(
		`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`, // repeated during loop
		`{"rationale":"done","action":{"type":"final","content":"Salvaged answer."}}`,                  // finalize call
	)
	r := newTestRunner(prov, state)
	r.MaxToolIterations = 3
	r.ForceClass = string(ClassQuestion) // skip planning for a tighter test

	task, err := r.RunTask(context.Background(), "inspect a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (salvaged)", err)
	}
	if task.Status != TaskStatusCompleted || task.SalvagedReason == "" {
		t.Fatalf("task = %+v, want completed+salvaged", task)
	}
}

func TestExhaustionSalvageFailureReturnsError(t *testing.T) {
	// finalize's own model call errors -> original error semantics preserved.
	state := session.NewState()
	prov := newLoopThenErrorProvider(
		`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
	)
	r := newTestRunner(prov, state)
	r.MaxToolIterations = 3
	r.ForceClass = string(ClassQuestion)

	_, err := r.RunTask(context.Background(), "inspect a.go")
	if !errors.Is(err, ErrMaxIterationsExceeded) {
		t.Fatalf("err = %v, want ErrMaxIterationsExceeded", err)
	}
}
```

> Adapt `newLoopingThenFinalProvider` / `newLoopThenErrorProvider` / `newTestRunner` to whatever scripted-provider and runner-builder helpers already exist in `runner_test.go`. If the existing fake returns a fixed script by index, express "loop N times then answer" using that. Do not add real tools — `file.read` needs a registry; if the existing tests register a fake registry, reuse it, otherwise script the provider so the tool call is against a registered read-only fake tool the existing tests already set up.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestExhaustion -v`
Expected: FAIL — salvage not implemented; `RunTask` returns `ErrMaxIterationsExceeded` in the first test.

- [ ] **Step 3: Implement the loop changes**

In `internal/agent/runner.go`:

3a. Replace fields (`:106-110`):

```go
	tracker   *progressTracker
	trackerMu sync.Mutex
```

3b. Add consts (near `:22-27`):

```go
	finalizePressureThreshold = 2
	finalizePressureMessage   = "You are near the tool budget. Unless one specific missing fact is required, produce a final answer now using the results you already have."
```

3c. Reset in `RunTask` (replace the `callHistory`/`loopNudgeSent` reset at `:156-159`):

```go
	r.trackerMu.Lock()
	r.tracker = newProgressTracker()
	r.trackerMu.Unlock()
```

3d. In the loop, at the top of each iteration (after `:205`), update budget and maybe inject soft pressure:

```go
	for iteration := 0; iteration < r.MaxToolIterations; iteration++ {
		r.State.SetToolBudget(session.ToolBudget{Used: iteration, Max: r.MaxToolIterations})

		if !pressureSent && r.MaxToolIterations-iteration <= finalizePressureThreshold {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: finalizePressureMessage})
			r.State.AddMessage(session.RoleSystem, finalizePressureMessage, session.ContentTypePlain)
			pressureSent = true
		}
		// ... existing skills refresh + chatWithRetry ...
```

Declare `pressureSent := false` just before the loop.

3e. After a successful tool execution in the loop (both the single `executeToolCall` branch at `:244-249` and the `executeActions` branch at `:230-235`), assess and act. Add a helper called after `messages = append(messages, resultMsgs...)`:

```go
		if done, res, ferr := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); done {
			return res, ferr
		}
```

where:

```go
// maybeFinalizeOnStall inspects the tracker after a tool execution. On a hard
// stall it forces a final answer; on a soft stall it appends an advisory nudge
// to messages in place. Returns done=true only when it has finalized the turn.
func (r *Runner) maybeFinalizeOnStall(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task) (bool, *Task, error) {
	r.trackerMu.Lock()
	a := r.tracker.assess()
	r.trackerMu.Unlock()

	switch a {
	case assessHardStall:
		res, err := r.finalize(ctx, p, model, messages, task, reasonStalled)
		return true, res, err
	case assessStalling:
		messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: loopNudgeMessage})
		r.State.AddMessage(session.RoleSystem, loopNudgeMessage, session.ContentTypePlain)
		return false, task, nil
	}
	return false, task, nil
}
```

> Note: because `messages` is a slice passed by value, the nudge append in `maybeFinalizeOnStall` won't propagate to the caller's slice. To keep the nudge, have the loop append it instead: return an optional `nudge string` from the helper and append in the loop. Simplest concrete form — change the helper to return `(finalizeResult *Task, finalizeErr error, finalized bool, nudge string)` and in the loop do `if finalized { return ... }; if nudge != "" { messages = append(messages, ...) }`. Implement that concrete signature; do not leave the by-value bug in place.

3f. In `executeToolCall`, replace the two nudge blocks (`:442-445` and `:519-522`) with just recording into the tracker:

```go
	r.trackerMu.Lock()
	r.tracker.record(toolName, string(normalizedArgs))
	r.trackerMu.Unlock()
```

Remove the `msgs = append(msgs, ...loopNudgeMessage...)` code from both sites; the loop now owns nudging.

3g. Replace the exhaustion block (`:255-257`) with salvage:

```go
	res, err := r.finalize(ctx, turnProvider, turnModel, messages, task, reasonExhausted)
	if err != nil {
		task.Status = TaskStatusFailed
		r.State.AddMessage(session.RoleSystem, "Agent stopped: exceeded max tool iterations without a final answer.", session.ContentTypePlain)
		return task, ErrMaxIterationsExceeded
	}
	return res, nil
```

3h. Delete `toolCallKey` type (`:67-70`), `recordToolCall` (`:526-530`), and `shouldNudgeLoop` (`:532-547`). Keep `loopNudgeMessage` (still used for the soft nudge).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/ -v`
Expected: PASS, including the updated exhaustion tests. Fix the pre-existing exhaustion test at `runner_test.go:394` if it now expects a bare failure — it should assert salvage (completed+`SalvagedReason`) for the answering-provider case, and keep `ErrMaxIterationsExceeded` only for the salvage-also-fails case.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/runner.go internal/agent/runner_test.go
go vet ./internal/agent/
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): finalization pressure, stall escalation, and exhaustion salvage"
```

---

## Task 6: Tighten `baseRules`

**Files:**
- Modify: `internal/agent/prompts.go` (`baseRules` const)
- Test: `internal/agent/prompts_test.go`

**Interfaces:** none new — string content change.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/prompts_test.go`:

```go
func TestBaseRulesEncourageEarlyFinal(t *testing.T) {
	for _, want := range []string{
		"only to obtain facts",
		"produce a final answer",
		"Stop after validation",
	} {
		if !strings.Contains(baseRules, want) {
			t.Errorf("baseRules missing %q", want)
		}
	}
}
```

(Ensure `strings` is imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestBaseRulesEncourageEarlyFinal -v`
Expected: FAIL — substrings absent.

- [ ] **Step 3: Edit `baseRules`**

In `internal/agent/prompts.go`, add these three bullet lines to the `baseRules` const (before the closing backtick):

```
- Use tools only to obtain facts you don't already have in the transcript or context pack.
- Once the requested change is made and validated, produce a final answer — do not keep exploring.
- Stop after validation succeeds; do not re-verify work that already passed.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestBaseRulesEncourageEarlyFinal -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/prompts.go internal/agent/prompts_test.go
git add internal/agent/prompts.go internal/agent/prompts_test.go
git commit -m "feat(agent): tighten base rules to prefer early finalization"
```

---

## Task 7: TUI budget counter + salvage marker

**Files:**
- Modify: `internal/app/tui/view.go` (`renderActivityStrip` at `:71-90`)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: `session.State.ToolBudget()` (Task 3), `Message.Salvaged` (Task 3).

**Design:** append a `tools N/Max` segment to the activity strip label when `Max > 0` and the agent is active. The strip already builds a `label`; append the budget suffix. Keep it inside the existing single status row (`statusLineRows = 1`).

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/view_test.go` (match the existing model-construction helper used by other view tests):

```go
func TestActivityStripShowsToolBudget(t *testing.T) {
	m := newTestModel() // whatever helper view_test.go already uses
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "file.read", StartedAt: m.now()})
	m.state.SetToolBudget(session.ToolBudget{Used: 13, Max: 16})

	out := m.renderActivityStrip()
	if !strings.Contains(out, "tools 13/16") {
		t.Fatalf("activity strip = %q, want to contain %q", out, "tools 13/16")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestActivityStripShowsToolBudget -v`
Expected: FAIL — no `tools 13/16` substring.

- [ ] **Step 3: Implement the suffix**

In `internal/app/tui/view.go`, in `renderActivityStrip`, after the `switch` builds `label` and before rendering, append the budget:

```go
	if b := m.state.ToolBudget(); b.Max > 0 && label != "" {
		label = fmt.Sprintf("%s · tools %d/%d", label, b.Used, b.Max)
	}
```

(`fmt` is already imported in this file.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/ -run TestActivityStripShowsToolBudget -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/view.go internal/app/tui/view_test.go
git add internal/app/tui/view.go internal/app/tui/view_test.go
git commit -m "feat(tui): show tool budget counter in activity strip"
```

---

## Task 8: Verify existing settings exposure + full-suite green

**Files:**
- Verify only: `internal/app/tui/settings/model.go:124-128`

**Interfaces:** none.

- [ ] **Step 1: Confirm the setting already exists**

Run: `grep -n "Max tool iterations" internal/app/tui/settings/model.go`
Expected: matches line ~125 binding `&m.cfg.Agent.MaxToolIterations`. No code change needed — spec Component 5's "tunable" requirement is already satisfied.

- [ ] **Step 2: Run the whole suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 3: Vet and build**

Run: `go vet ./... && CGO_ENABLED=1 go build ./cmd/marshal`
Expected: clean build.

- [ ] **Step 4: Commit any formatting**

```bash
gofmt -w .
git add -A
git commit -m "chore: gofmt after tool-budget finalization work" || echo "nothing to commit"
```

---

## Self-Review

**Spec coverage:**
- #1 Finalization pressure → Task 5 (soft pressure at `finalizePressureThreshold`).
- #2 Salvage pass → Task 4 (`finalize`) + Task 5 (exhaustion path).
- #3 Progress detection + escalation → Task 1 (`progressTracker`) + Task 5 (nudge → hard-stall finalize).
- #4 Prompt tightening → Task 6.
- #5 TUI budget display → Task 7; tunable setting → Task 8 (already implemented, verify-only).
- Salvage = Completed+flagged → Tasks 2, 3, 4 (`SalvagedReason`, `AddMessageSalvaged`).
- Model sees budget qualitatively only → Task 5 injects text, never a numeric counter; numeric counter is TUI-only (Task 7).

**Placeholder scan:** No TBD/TODO. The two "match the existing helper" notes (scripted provider names, `NewState` constructor) are deliberate — the repo's test fakes already exist and the implementer must reuse them rather than inventing new ones; exact names are discoverable in `runner_test.go`/`session_test.go` in the same package.

**Type consistency:** `finalize` signature, `finalizeReason` consts (`reasonExhausted`/`reasonStalled`), `assessment` consts, `ToolBudget{Used,Max}`, and `AddMessageSalvaged(role, content, contentType, reason)` are used identically across Tasks 3–7. `SalvagedReason` (Task 2) is the field set by `finalize` (Task 4) and read by tests (Task 5).

**Known caution flagged in-plan:** Task 5 step 3e calls out the slice-by-value pitfall for the nudge append and mandates the concrete return-a-nudge signature so it isn't silently dropped.
