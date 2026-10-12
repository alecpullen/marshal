# Prompt caching — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make provider prompt caches actually hit. Keep the start of every request byte-stable across a turn, add an Anthropic rolling breakpoint, send `prompt_cache_key` where it is accepted, parse Kimi's cache field, and show the real prompt-cache rate in the TUI.

**Architecture:** The context pack is split at render time. Stable sections stay at message index 1; turn-volatile sections (todos, scratchpad, semantic snippets) become a working-state message that the runner appends at its single dispatch point (`chatOnceAttempt`), marked `schema.ChatMessage.Volatile` and never stored. Providers then add their caching hints (an Anthropic breakpoint before the volatile tail, `prompt_cache_key` on OpenAI and Codex) without any provider-specific logic in the agent loop.

**Tech Stack:** Go 1.x with cgo (`CGO_ENABLED=1`), standard `testing`, `httptest`, Bubble Tea TUI (render-only).

**Spec:** [`docs/prompt-caching/design.md`](../design.md). Decisions D1–D9 are referenced by number below.

**Base:** `main` at `cab5ff06`. Every line anchor below is as on that commit. Branch from `main` (`git switch -c prompt-caching main`); the current `transcript-beats` branch is unrelated docs work.

**Plan slug:** `prompt-caching`. Commit each task as `prompt-caching: task N — <task title>`.

## Global Constraints

- Build and test with `CGO_ENABLED=1` (tree-sitter links C).
- `internal/app/tui/` renders only: no prompt, routing or policy logic there (AGENTS.md).
- Nothing under `web/` changes.
- No new config keys, no new third-party modules.
- Every OpenAI-compatible endpoint other than `api.openai.com` keeps a byte-identical request body, apart from message ordering changes made in the runner.
- Prefer the narrowest `go test` scope while iterating; run `go test ./...` once at the end (Task 8). CI runs the full suite only on release tags.

## Review Focus

1. **A turn whose pack has only volatile sections** (todos exist, no repo card or memories): no index-1 pack message, and the tail still carries the todos. Pinned by Task 2's regression test, which runs with an otherwise empty pack.
2. **The tail must never be stored**: it must not appear in the session transcript, compaction input or history replay, and must not be duplicated on retries. Pinned in Task 2 (transcript assertion and the `withWorkingState` non-mutation test).
3. **Anthropic conversion of unusual transcripts**: an empty assistant message before any user message, or a transcript of only volatile messages, must not panic and must not place a breakpoint on a volatile block. Pinned in Task 4.
4. **`prompt_cache_key` must not leak to other endpoints**: lookalike hosts (`api.openai.com.evil.example`), OpenRouter and local servers get no new field, and no key is sent when there is no session ID. Pinned in Task 5.
5. **A provider that reports no cache data** must show nothing, not "0% cached". Pinned in Task 7.

---

### Task 1: Split context-pack rendering into stable and volatile parts

Implements D1. Pure `internal/contextpack` change; no caller changes yet.

**Files:**
- Modify: `internal/contextpack/render.go`
- Create: `internal/contextpack/render_test.go`

**Interfaces:**
- Consumes: existing `Pack`, `Section`, `SectionKind` constants, `MergeRepoSections`, `MergeTodos`, `MergeScratchpad`.
- Produces:
  - `func IsVolatile(k SectionKind) bool`
  - `func RenderStable(pack Pack) string`: header `"Project context pack:"`, or `""` when there are no stable sections.
  - `func RenderVolatile(pack Pack) string`: header `"Current working state:"`, or `""` when there are no volatile sections and the pack is not truncated.
  - The existing `Render` is unchanged; it is kept for the builder tests.

- [ ] **Step 1: Write the failing tests**

Create `internal/contextpack/render_test.go`:

```go
package contextpack

import (
	"strings"
	"testing"
	"time"
)

func TestIsVolatile(t *testing.T) {
	volatile := []SectionKind{SectionTodos, SectionScratchpad, SectionSemantic, SectionToolOutput}
	stable := []SectionKind{SectionRepoCard, SectionRepoMap, SectionMemory, SectionPlan, SectionFileSnippet, SectionSessionSummaries}
	for _, k := range volatile {
		if !IsVolatile(k) {
			t.Errorf("IsVolatile(%q) = false, want true", k)
		}
	}
	for _, k := range stable {
		if IsVolatile(k) {
			t.Errorf("IsVolatile(%q) = true, want false", k)
		}
	}
}

func TestRenderSplitsSectionsByVolatility(t *testing.T) {
	pack := Pack{Sections: []Section{
		{Kind: SectionRepoCard, Title: "Repo Card", Content: "CARD-MARKER"},
		{Kind: SectionMemory, Title: "Project Memories", Content: "MEMORY-MARKER"},
		{Kind: SectionSemantic, Title: "Relevant Code", Content: "SEMANTIC-MARKER"},
		{Kind: SectionTodos, Title: "Current Todos", Content: "TODO-MARKER"},
		{Kind: SectionScratchpad, Title: "Scratchpad", Content: "SCRATCH-MARKER"},
	}}

	stable := RenderStable(pack)
	volatile := RenderVolatile(pack)

	if !strings.HasPrefix(stable, "Project context pack:\n") {
		t.Fatalf("stable header missing:\n%s", stable)
	}
	if !strings.HasPrefix(volatile, "Current working state:\n") {
		t.Fatalf("volatile header missing:\n%s", volatile)
	}
	for _, want := range []string{"CARD-MARKER", "MEMORY-MARKER"} {
		if !strings.Contains(stable, want) || strings.Contains(volatile, want) {
			t.Errorf("%s must render only in the stable part", want)
		}
	}
	for _, want := range []string{"SEMANTIC-MARKER", "TODO-MARKER", "SCRATCH-MARKER"} {
		if !strings.Contains(volatile, want) || strings.Contains(stable, want) {
			t.Errorf("%s must render only in the volatile part", want)
		}
	}
}

func TestRenderPartsEmptyWhenNothingToShow(t *testing.T) {
	onlyStable := Pack{Sections: []Section{{Kind: SectionRepoCard, Title: "Repo Card", Content: "x"}}}
	if got := RenderVolatile(onlyStable); got != "" {
		t.Errorf("RenderVolatile(stable-only pack) = %q, want empty", got)
	}
	onlyVolatile := Pack{Sections: []Section{{Kind: SectionTodos, Title: "Current Todos", Content: "x"}}}
	if got := RenderStable(onlyVolatile); got != "" {
		t.Errorf("RenderStable(volatile-only pack) = %q, want empty", got)
	}
	if got := RenderStable(Pack{}) + RenderVolatile(Pack{}); got != "" {
		t.Errorf("empty pack rendered %q, want empty", got)
	}
}

// The point of the split: rewriting the todo list or letting the
// scratchpad's relative ages tick must not change a single byte of the
// stable render, even when the pack is over budget and truncated (the
// truncation note's token estimate moves with every volatile change).
func TestRenderStableUnaffectedByVolatileChanges(t *testing.T) {
	clock := time.Unix(10_000, 0)
	now := func() time.Time { return clock }
	const budget = 400
	bigMap := strings.Repeat("internal/some/dir/\n", 400) // forces truncation
	base := MergeRepoSections(Pack{}, "Project: marshal", bigMap, budget, now)
	scratch := []ScratchpadEntry{{Key: "notes", Content: "finding", Format: "text", Updated: clock.UnixMilli()}}

	a := MergeTodos(base, []TodoItem{{Content: "read a", Status: "in_progress"}}, budget, now)
	a = MergeScratchpad(a, scratch, budget, 0, now)

	clock = clock.Add(5 * time.Minute)
	b := MergeTodos(base, []TodoItem{{Content: "read a", Status: "completed"}, {Content: "read b", Status: "in_progress"}}, budget, now)
	b = MergeScratchpad(b, scratch, budget, 0, now)

	if !a.TokenUsage.Truncated || !b.TokenUsage.Truncated {
		t.Fatalf("fixture must truncate the pack (got %v / %v)", a.TokenUsage.Truncated, b.TokenUsage.Truncated)
	}
	if RenderStable(a) != RenderStable(b) {
		t.Fatalf("stable render changed with volatile content:\n--- a\n%s\n--- b\n%s", RenderStable(a), RenderStable(b))
	}
	if RenderVolatile(a) == RenderVolatile(b) {
		t.Fatal("volatile render did not change; the fixture is not exercising anything")
	}
	if !strings.Contains(RenderVolatile(b), "Truncated: true") {
		t.Errorf("truncation note must render in the volatile part:\n%s", RenderVolatile(b))
	}
	if strings.Contains(RenderStable(b), "Estimated tokens") {
		t.Errorf("truncation note must not render in the stable part:\n%s", RenderStable(b))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/contextpack/ -run 'TestIsVolatile|TestRender' -v`
Expected: build failure, `undefined: IsVolatile` (and `RenderStable`, `RenderVolatile`).

- [ ] **Step 3: Implement**

Append to `internal/contextpack/render.go` (leave `Render` as it is):

```go
// IsVolatile reports whether sections of kind k are rewritten within a
// turn: todo.write after every step, scratchpad.write (whose projection
// also prints relative ages), and mid-turn semantic re-queries. Volatile
// sections render into the per-request working-state message at the end
// of the wire, not the context pack near its start: anything that changes
// near the start invalidates the provider prompt cache for everything
// after it. See docs/prompt-caching/design.md (D1).
func IsVolatile(k SectionKind) bool {
	switch k {
	case SectionTodos, SectionScratchpad, SectionSemantic, SectionToolOutput:
		return true
	}
	return false
}

// RenderStable renders the sections that hold still within a turn, under
// the "Project context pack:" header. Empty when there are none.
//
// Stable sections have lower priority numbers than volatile ones, so the
// greedy budget pass admits them first and their content never depends on
// how large the volatile sections are.
func RenderStable(pack Pack) string {
	return renderPart(pack, "Project context pack:", false)
}

// RenderVolatile renders the turn-volatile sections under the "Current
// working state:" header. The pack's truncation note renders here rather
// than in the stable part: its token estimate moves whenever a volatile
// section does. Empty when there is nothing volatile and no truncation.
func RenderVolatile(pack Pack) string {
	return renderPart(pack, "Current working state:", true)
}

func renderPart(pack Pack, header string, volatile bool) string {
	var body strings.Builder
	for _, section := range pack.Sections {
		if IsVolatile(section.Kind) != volatile {
			continue
		}
		fmt.Fprintf(&body, "\n## %s\n", section.Title)
		if section.Source != "" {
			fmt.Fprintf(&body, "Source: %s\n", section.Source)
		}
		fmt.Fprintf(&body, "\n%s\n", section.Content)
	}
	note := volatile && pack.TokenUsage.Truncated
	if body.Len() == 0 && !note {
		return ""
	}
	var b strings.Builder
	b.WriteString(header + "\n")
	if note {
		fmt.Fprintf(&b, "Estimated tokens: %d/%d\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
		b.WriteString("Truncated: true\n")
	}
	b.WriteString(body.String())
	return strings.TrimRight(b.String(), "\n")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/contextpack/ -v -count=1`
Expected: PASS (new tests and all existing contextpack tests).

If `TestRenderStableUnaffectedByVolatileChanges` fails on the `Truncated` precondition, raise the `strings.Repeat` count until the map overflows `budget/2`. Do not weaken the equality assertion.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/contextpack
git add internal/contextpack/render.go internal/contextpack/render_test.go
git commit -m "prompt-caching: task 1 — split context-pack rendering into stable and volatile parts"
```

---

### Task 2: Working-state tail and the prefix-stability regression test

Implements D2 and D3, plus the regression test from suggestion 4. After this task, a todo update no longer rewrites message index 1.

**Files:**
- Modify: `internal/llm/schema/chat.go:19-24` (`ChatMessage`)
- Modify: `internal/agent/prompts.go:933-939` (`BuildContextPackMessage`; add `BuildWorkingStateMessage`)
- Modify: `internal/agent/route.go` (add `withWorkingState` after `setContextPackMessage`, around line 436)
- Modify: `internal/agent/chat.go:337` (`chatOnceAttempt`)
- Modify: `internal/agent/runner.go:1178-1185` (budget checks)
- Create: `internal/agent/cacheprefix_test.go`

**Interfaces:**
- Consumes: `contextpack.RenderStable`, `contextpack.RenderVolatile` (Task 1).
- Produces:
  - `schema.ChatMessage.Volatile bool`
  - `func BuildWorkingStateMessage(pack contextpack.Pack) (schema.ChatMessage, bool)`, returning a message with `Role: schema.RoleUser, Volatile: true`.
  - `func (r *Runner) withWorkingState(messages []schema.ChatMessage) []schema.ChatMessage`
  - Test helpers `stablePrefix` and `sameMessage` in `cacheprefix_test.go`, reused by Task 3's tests.

- [ ] **Step 1: Write the failing regression test**

Create `internal/agent/cacheprefix_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// stablePrefix drops the trailing Volatile messages: the part of a request
// a provider prompt cache can reuse on the next request.
func stablePrefix(msgs []schema.ChatMessage) []schema.ChatMessage {
	n := len(msgs)
	for n > 0 && msgs[n-1].Volatile {
		n--
	}
	return msgs[:n]
}

func sameMessage(a, b schema.ChatMessage) bool {
	if a.Role != b.Role || a.Content != b.Content || a.ToolCallID != b.ToolCallID || a.Volatile != b.Volatile || len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		x, y := a.ToolCalls[i], b.ToolCalls[i]
		if x.ID != y.ID || x.Name != y.Name || string(x.Args) != string(y.Args) {
			return false
		}
	}
	return true
}

// assertPrefixStable fails when any request rewrites a message that the
// previous request's stable prefix already sent.
func assertPrefixStable(t *testing.T, reqs []schema.ChatRequest) {
	t.Helper()
	for i := 1; i < len(reqs); i++ {
		prev := stablePrefix(reqs[i-1].Messages)
		cur := reqs[i].Messages
		if len(cur) < len(prev) {
			t.Fatalf("request %d has %d messages, fewer than the previous stable prefix (%d)", i, len(cur), len(prev))
		}
		for j := range prev {
			if !sameMessage(prev[j], cur[j]) {
				t.Fatalf("request %d rewrote message %d of the previous request's stable prefix:\nwas: %.300q\nnow: %.300q",
					i, j, prev[j].Content, cur[j].Content)
			}
		}
	}
}

// TestPromptPrefixStableAcrossSteps is the regression guard for the cache
// prefix (docs/prompt-caching/design.md). Between steps it does what a real
// turn does: the model rewrites its todo list and wall-clock time passes, so
// the scratchpad projection's relative ages tick. Neither may change any
// message a previous request already sent; both must still reach the model
// in the working-state tail.
func TestPromptPrefixStableAcrossSteps(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	clock := time.Unix(1_000, 0)
	now := func() time.Time { return clock }
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{}, session.WithClock(now))
	if err := state.SetScratchpadEntry("notes", "first finding", "text"); err != nil {
		t.Fatalf("SetScratchpadEntry: %v", err)
	}
	call := func(id, path string) []schema.ToolCall {
		return []schema.ToolCall{{ID: id, Name: "file.read", Args: json.RawMessage(`{"path":"` + path + `"}`)}}
	}
	statuses := []string{"pending", "in_progress", "completed"}
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "", "", "Done."},
		ToolCalls:     [][]schema.ToolCall{call("c1", "a.go"), call("c2", "b.go"), call("c3", "c.go")},
		FinishReasons: []string{"tool_calls", "tool_calls", "tool_calls", "stop"},
		OnChat: func(idx int, _ schema.ChatRequest) {
			clock = clock.Add(2 * time.Minute)
			_ = state.SetTodos([]db.TodoItem{{Content: "read the files", Status: statuses[min(idx, len(statuses)-1)]}})
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.Now = now
	r.SetForceClass(string(ClassEdit))

	if _, err := r.RunTask(context.Background(), "read three files"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 3 {
		t.Fatalf("got %d requests, want at least 3", len(p.Requests))
	}

	assertPrefixStable(t, p.Requests)

	last := p.Requests[len(p.Requests)-1].Messages
	tail := last[len(last)-1]
	if !tail.Volatile || !strings.Contains(tail.Content, "Current Todos") || !strings.Contains(tail.Content, "Scratchpad") {
		t.Fatalf("last message must be the volatile working state with todos and scratchpad, got %+v", tail)
	}
	for i, m := range last[:len(last)-1] {
		if m.Volatile || strings.Contains(m.Content, "Current working state:") {
			t.Fatalf("message %d is a stale working-state copy; exactly one tail is allowed", i)
		}
	}
	// Review focus 2: the tail is wire-only and never reaches the transcript.
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "Current working state:") {
			t.Fatalf("working-state tail leaked into the session transcript: %q", m.Content)
		}
	}
}

func TestWithWorkingStateDoesNotMutateInput(t *testing.T) {
	state := newTestState(t)
	_ = state.SetTodos([]db.TodoItem{{Content: "x", Status: "pending"}})
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.mergeTodos(0)

	in := make([]schema.ChatMessage, 1, 4) // spare capacity: append must not write into it
	in[0] = schema.ChatMessage{Role: schema.RoleSystem, Content: "sys"}
	out := r.withWorkingState(in)

	if len(in) != 1 || len(out) != 2 {
		t.Fatalf("len(in)=%d len(out)=%d, want 1 and 2", len(in), len(out))
	}
	if spare := in[:2][1]; spare.Volatile || spare.Content != "" {
		t.Fatalf("withWorkingState wrote into the caller's backing array: %+v", spare)
	}
	if !out[1].Volatile {
		t.Fatalf("tail not marked Volatile: %+v", out[1])
	}
}
```

Check the helper names this test assumes before running it. `r.mergeTodos(maxTokenOverride int)` is called at `runner.go:1173` as `r.mergeTodos(route.ContextBudget.MaxRepoContextTokens)`. `state.Messages()` is used in `finalize_test.go:32`. If either has drifted, use the current name rather than adding a wrapper.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestPromptPrefixStableAcrossSteps|TestWithWorkingStateDoesNotMutateInput' -count=1`
Expected: build failure, `undefined: withWorkingState` and `unknown field Volatile`. The behavioural failure this task fixes (`request 2 rewrote message 1 of the previous request's stable prefix`, with a "Project context pack:" todo list in `was`) is demonstrated by the mutation check in Step 7.

- [ ] **Step 3: Add the schema field**

In `internal/llm/schema/chat.go`, extend `ChatMessage`:

```go
type ChatMessage struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// Volatile marks a message the runner regenerates for every request
	// (the working-state tail). It is never part of the reusable prompt
	// prefix, so providers that place explicit cache breakpoints
	// (Anthropic cache_control) must place them before it. Wire encoders
	// otherwise send it like any other message.
	Volatile bool
}
```

- [ ] **Step 4: Render the pack in two parts**

In `internal/agent/prompts.go`, replace `BuildContextPackMessage` and add its counterpart:

```go
// BuildContextPackMessage renders the stable part of the context pack, the
// message that sits at index 1 right after the system prompt. Turn-volatile
// sections are left out: they ride in BuildWorkingStateMessage at the end
// of the wire so that rewriting them cannot invalidate the provider's
// prompt cache for the transcript in between.
func BuildContextPackMessage(pack contextpack.Pack) (schema.ChatMessage, bool) {
	rendered := contextpack.RenderStable(pack)
	if rendered == "" {
		return schema.ChatMessage{}, false
	}
	return schema.ChatMessage{Role: schema.RoleUser, Content: rendered}, true
}

// BuildWorkingStateMessage renders the turn-volatile part of the context
// pack (todos, scratchpad projection, semantic snippets) as the
// per-request tail. It is marked Volatile so providers keep cache
// breakpoints in front of it.
func BuildWorkingStateMessage(pack contextpack.Pack) (schema.ChatMessage, bool) {
	rendered := contextpack.RenderVolatile(pack)
	if rendered == "" {
		return schema.ChatMessage{}, false
	}
	return schema.ChatMessage{Role: schema.RoleUser, Content: rendered, Volatile: true}, true
}
```

- [ ] **Step 5: Attach the tail at dispatch**

In `internal/agent/route.go`, directly after `setContextPackMessage`, add:

```go
// withWorkingState returns messages plus this request's working-state
// tail. The tail is rebuilt for every request and never stored in the
// runner's message slice, so it cannot leak into compaction, pruning,
// archives or history replay, and a retry never stacks a second copy.
// The input is not modified.
func (r *Runner) withWorkingState(messages []schema.ChatMessage) []schema.ChatMessage {
	if r.State == nil {
		return messages
	}
	msg, ok := BuildWorkingStateMessage(r.State.ContextPack())
	if !ok {
		return messages
	}
	out := make([]schema.ChatMessage, len(messages), len(messages)+1)
	copy(out, messages)
	return append(out, msg)
}
```

In `internal/agent/chat.go`, make this the first statement of `chatOnceAttempt` (line 337):

```go
func (r *Runner) chatOnceAttempt(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeTools bool) (chatResult, error) {
	// The single dispatch point: every request (loop step, finalize,
	// handoff summary, plan) carries the current working state, and the
	// request-inspection snapshot below records it as sent.
	messages = r.withWorkingState(messages)

	var cancel context.CancelFunc
```

- [ ] **Step 6: Budget against the wire actually sent**

In `internal/agent/runner.go`, the over-budget check (around line 1178) and the pruning check (around line 1185) must include the tail:

```go
		if turnThreshold > 0 && r.calibratedEstimate(r.withWorkingState(messages)) > turnThreshold {
```

```go
			if prunedMsgs, n := pruneStaleToolOutputs(messages, pruneMinSizeDefault); n > 0 && r.calibratedEstimate(r.withWorkingState(prunedMsgs)) <= turnThreshold {
```

- [ ] **Step 7: Run the new tests**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestPromptPrefixStableAcrossSteps|TestWithWorkingStateDoesNotMutateInput' -count=1 -v`
Expected: PASS.

Mutation check (repo practice: see "Verify coverage by mutation"). Temporarily change `chatOnceAttempt`'s new line to `messages = r.setContextPackMessage(messages, r.State.ContextPack())`, so the volatile content goes back to index 1. Confirm `TestPromptPrefixStableAcrossSteps` fails, then revert.

- [ ] **Step 8: Run the affected packages**

Run: `CGO_ENABLED=1 go test ./internal/agent/... ./internal/contextpack/... ./internal/llm/... ./internal/acp/... ./internal/app/... -count=1`
Expected: PASS. A test that looked for todos, scratchpad or semantic text inside the `"Project context pack:"` message is now wrong about where that content lives. Change it to look in the last request message (the `Volatile` tail). Do not move the content back.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal/agent internal/llm/schema
git add internal/llm/schema/chat.go internal/agent/prompts.go internal/agent/route.go internal/agent/chat.go internal/agent/runner.go internal/agent/cacheprefix_test.go
git commit -m "prompt-caching: task 2 — send turn-volatile context as a per-request working-state tail"
```

---

### Task 3: One system-prompt builder; skill hint before the goal

Implements D4 and D5. Fixes rollover and plan-first rebuilds dropping `SystemAccess` and `Narration`, and moves the per-turn hint behind the replayed history.

**Files:**
- Modify: `internal/agent/route.go` (add `systemPromptMessage`; update the `appendSkillHint` doc comment at lines 438-439)
- Modify: `internal/agent/runner.go:905-923` (turn start), `:984-1003` (plan-first rebuild), `:1149-1163` (mid-loop rebuild)
- Modify: `internal/agent/handoff.go:34-54`
- Modify: `internal/agent/rollover.go:149-153`
- Modify: `internal/agent/handoff_test.go:130-131` (stale comment only)
- Test: `internal/agent/cacheprefix_test.go` (append)

**Interfaces:**
- Consumes: `internal/agent/cacheprefix_test.go` (created in Task 2; this task appends to it); `rankFixture`, `fakeSkillEmbedder` (`skillrank_test.go`); `newTestState` (`runner_testhelpers_test.go`).
- Produces: `func (r *Runner) systemPromptMessage() schema.ChatMessage`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/cacheprefix_test.go` (add `"marshal/internal/contextpack"` and `"marshal/internal/skills"` to the imports):

```go
// Plan-first rebuilds the wire after the planning call. The rebuilt system
// prompt must be byte-identical to the one the planning call sent: the
// rebuild used BuildSystemPromptWithAddendum, which silently dropped the
// narration addendum and the system-access rules.
func TestPlanFirstRebuildKeepsSystemPrompt(t *testing.T) {
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections:   []contextpack.Section{{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4}},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"1. look\n2. answer", "Done."},
		FinishReasons: []string{"stop", "stop"},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.NarrationPrompt = true
	r.PlanFirst = true
	r.SetForceClass(string(ClassEdit))

	if _, err := r.RunTask(context.Background(), "tidy the readme"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("got %d requests, want plan + execution", len(p.Requests))
	}
	plan, exec := p.Requests[0].Messages[0], p.Requests[1].Messages[0]
	if plan.Content != exec.Content {
		t.Fatalf("system prompt changed between the planning call and execution:\nplan: %.200q\nexec: %.200q", plan.Content, exec.Content)
	}
}

// The skill hint changes every turn. It must sit after the replayed
// history, directly before the new goal, so the history stays inside the
// reusable prefix on a turn's first request.
func TestSkillHintFollowsReplayedHistory(t *testing.T) {
	e, candidates := rankFixture()
	idx := skills.NewIndex()
	for _, c := range candidates {
		idx.Set(c.Name, c)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{"First answer.", "Second answer."}, FinishReasons: []string{"stop", "stop"}}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SkillIndex = idx
	r.SkillEmbedder = e
	r.SetForceClass(string(ClassQuestion))

	const goal = "fix the flaky test" // rankFixture keys its vectors on this text
	for i := 0; i < 2; i++ {
		// Run, not RunTask: Run records the user message, which is what
		// the second turn's history replay reads back.
		if err := r.Run(context.Background(), goal); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}

	msgs := p.Requests[len(p.Requests)-1].Messages
	hint := -1
	for i, m := range msgs {
		if strings.HasPrefix(m.Content, "Skill suggestions for this request") {
			hint = i
		}
	}
	if hint < 0 {
		t.Fatalf("second turn carried no skill hint: %#v", msgs)
	}
	if hint+1 >= len(msgs) || msgs[hint+1].Role != schema.RoleUser || msgs[hint+1].Content != goal {
		t.Fatalf("hint must sit immediately before the new goal; got %#v", msgs[hint:])
	}
	// History replay may wrap the old goal, so match by substring, and
	// insist the replay is really there: without it the ordering check
	// below would pass vacuously.
	var goals []int
	for i, m := range msgs {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, goal) {
			goals = append(goals, i)
		}
	}
	if len(goals) < 2 {
		t.Fatalf("history replay did not carry the first turn's goal (fixture problem, not the hint): %#v", msgs)
	}
	if goals[0] >= hint {
		t.Fatalf("replayed first-turn goal (index %d) must precede the hint (index %d)", goals[0], hint)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestPlanFirstRebuildKeepsSystemPrompt|TestSkillHintFollowsReplayedHistory' -count=1 -v`
Expected: both FAIL. The first fails with "system prompt changed between the planning call and execution". The second fails with "hint must sit immediately before the new goal" (today the hint sits right after the context pack, ahead of the history).

If `TestPlanFirstRebuildKeepsSystemPrompt` passes, check that native mode with `NarrationPrompt` really adds text in `buildSystemPrompt` (`prompts.go:825`). Then set `state.SetSystemAccess(true)` as a second differing option; do not drop the test.

- [ ] **Step 3: Add the shared builder**

In `internal/agent/route.go`, add:

```go
// systemPromptMessage builds messages[0] from the runner's live state.
// Every build site goes through here. The system prompt heads the
// provider's cache prefix, so two sites that disagree on an option produce
// different bytes and miss the cache. They also send the model a different
// prompt: rollover and plan-first used to drop SystemAccess and Narration.
func (r *Runner) systemPromptMessage() schema.ChatMessage {
	return BuildSystemPromptWithSystem(SystemPromptOptions{
		Role:         r.role(),
		Tools:        r.Registry.List(),
		Deferred:     r.Registry.ListDeferred(),
		SkillIndex:   r.SkillIndex,
		ActiveSkills: r.State.ActiveSkills(),
		NativeTools:  r.NativeTools,
		Mode:         r.Policy.ApprovalMode(),
		Addendum:     r.SystemPromptAddendum,
		WorkingDir:   r.State.WorkingDir,
		Roster:       r.agentRoster(),
		LoadedNames:  r.State.LoadedToolNames(),
		SystemAccess: r.State.SystemAccess(),
		Narration:    r.NarrationPrompt,
	})
}
```

Replace the `appendSkillHint` doc comment (lines 438-439) with:

```go
// appendSkillHint appends this turn's skill suggestion message, if any.
// Call it immediately before appending the turn's goal: the hint changes
// every turn, so it must come after the replayed history and skill bodies,
// or a turn's first request re-pays that history at full price.
```

- [ ] **Step 4: Use it at every build site and move the hint**

`internal/agent/runner.go`, turn start (lines 905-942): replace the `messages := []schema.ChatMessage{ BuildSystemPromptWithSystem(...) }` literal and the three calls after it, then move the hint to just before the goal:

```go
	messages := []schema.ChatMessage{r.systemPromptMessage()}
	messages = r.setContextPackMessage(messages, r.State.ContextPack())
	messages = r.appendSkillBodies(messages)
	if r.role() == RoleGeneral {
		// ... existing ledger + historyBudget + buildHistoryMessages block, unchanged ...
	}
	messages = r.appendSkillHint(messages)
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
```

Plan-first rebuild (lines 986-1003), same shape:

```go
			messages = []schema.ChatMessage{r.systemPromptMessage()}
			messages = r.setContextPackMessage(messages, updatedPack)
			messages = r.appendSkillBodies(messages)
			if r.role() == RoleGeneral {
				// ... existing block, unchanged ...
			}
			messages = r.appendSkillHint(messages)
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
```

Mid-loop rebuild (lines 1149-1163): replace the `BuildSystemPromptWithSystem(SystemPromptOptions{...})` literal with

```go
			messages[0] = r.systemPromptMessage()
```

and keep the three `lastRendered* = current*` assignments after it.

`internal/agent/handoff.go` (lines 34-54) and `internal/agent/rollover.go` (lines 149-153), the same in both:

```go
	fresh := []schema.ChatMessage{r.systemPromptMessage()}
	fresh = r.setContextPackMessage(fresh, r.State.ContextPack())
	fresh = r.appendSkillBodies(fresh)
	fresh = r.appendSkillHint(fresh)
	fresh = append(fresh,
		// ... existing goal / progress summary / continue messages, unchanged ...
	)
```

Then confirm no production call site is left:

Run: `grep -n 'BuildSystemPromptWithAddendum\|BuildSystemPromptWithSystem(SystemPromptOptions' internal/agent/*.go | grep -v _test.go`
Expected: only `route.go` (inside `systemPromptMessage`) and the wrapper definitions in `prompts.go`.

In `internal/agent/handoff_test.go`, update the stale comment at lines 130-131 to: `// The hint message must be present (after the skill bodies, before the goal).`

- [ ] **Step 5: Run the tests**

Run: `CGO_ENABLED=1 go test ./internal/agent/... -count=1`
Expected: PASS, including Task 2's `TestPromptPrefixStableAcrossSteps`.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/agent
git add internal/agent/route.go internal/agent/runner.go internal/agent/handoff.go internal/agent/rollover.go internal/agent/handoff_test.go internal/agent/cacheprefix_test.go
git commit -m "prompt-caching: task 3 — one system-prompt builder; skill hint before the goal"
```

---

### Task 4: Anthropic — keep mid-conversation system messages in place, add a rolling breakpoint

Implements D6. Applies to the native Anthropic provider and the OpenCode Go `/messages` path, which share `buildAnthropicRequestBody`.

**Files:**
- Modify: `internal/llm/provider/anthropic.go:161-198` (`toAnthropicMessages`), `:278-295` (breakpoints)
- Test: `internal/llm/provider/anthropic_test.go` (append)

**Interfaces:**
- Consumes: `schema.ChatMessage.Volatile` (Task 2).
- Produces: `toAnthropicMessages(msgs) (system []anthropicContentBlock, out []anthropicMessage, lastStable anthropicBlockRef)` (package-private) and `type anthropicBlockRef struct { msg, block int; ok bool }`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/llm/provider/anthropic_test.go`:

```go
func decodeAnthropicBody(t *testing.T, req schema.ChatRequest) anthropicRequest {
	t.Helper()
	raw, err := buildAnthropicRequestBody(req, 0, 0)
	if err != nil {
		t.Fatalf("buildAnthropicRequestBody: %v", err)
	}
	var body anthropicRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func countBreakpoints(body anthropicRequest) int {
	n := 0
	for _, b := range body.System {
		if b.CacheControl != nil {
			n++
		}
	}
	for _, tool := range body.Tools {
		if tool.CacheControl != nil {
			n++
		}
	}
	for _, m := range body.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	return n
}

func toolLoopMessages(tail ...schema.ChatMessage) []schema.ChatMessage {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "harness prompt"},
		{Role: schema.RoleSystem, Content: "repo instructions"},
		{Role: schema.RoleUser, Content: "fix it"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"p":"a"}`)}}},
		{Role: schema.RoleTool, ToolCallID: "c1", Content: "file body"},
	}
	return append(msgs, tail...)
}

// Only the leading system run becomes the system field. A later system
// message (nudge, hint, finalize directive) stays where it was, wrapped as
// a system reminder, so it neither rewrites the cached system prefix
// mid-turn nor loses its position.
func TestAnthropicKeepsMidConversationSystemInPlace(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{
		Model:    "claude-test",
		Messages: toolLoopMessages(schema.ChatMessage{Role: schema.RoleSystem, Content: "Call a tool or give a final answer."}),
	})

	if len(body.System) != 2 || body.System[0].Text != "harness prompt" || body.System[1].Text != "repo instructions" {
		t.Fatalf("system = %+v, want exactly the two leading system messages", body.System)
	}
	if body.System[1].CacheControl == nil {
		t.Error("the last leading system block must keep its breakpoint")
	}
	last := body.Messages[len(body.Messages)-1]
	if last.Role != "user" || len(last.Content) != 2 {
		t.Fatalf("last message = %+v, want one user message holding the tool result and the reminder", last)
	}
	want := "<system-reminder>\nCall a tool or give a final answer.\n</system-reminder>"
	if last.Content[1].Type != "text" || last.Content[1].Text != want {
		t.Fatalf("reminder block = %+v, want text %q", last.Content[1], want)
	}
}

// The rolling breakpoint goes on the last stable block, never on the
// volatile working-state tail, which changes every request.
func TestAnthropicRollingBreakpointSkipsVolatileTail(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{
		Model: "claude-test",
		Tools: []schema.ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Messages: toolLoopMessages(schema.ChatMessage{
			Role: schema.RoleUser, Content: "Current working state:\n\n## Current Todos\n\n- [~] read", Volatile: true,
		}),
	})

	last := body.Messages[len(body.Messages)-1]
	if len(last.Content) != 2 || last.Content[0].Type != "tool_result" {
		t.Fatalf("last message = %+v, want [tool_result, working-state text]", last)
	}
	if last.Content[0].CacheControl == nil {
		t.Error("tool_result (last stable block) must carry the rolling breakpoint")
	}
	if last.Content[1].CacheControl != nil {
		t.Error("the volatile tail must not carry a breakpoint")
	}
	if got := countBreakpoints(body); got != 3 {
		t.Errorf("breakpoints = %d, want 3 (tools, system, rolling); the API allows at most 4", got)
	}
}

func TestAnthropicRollingBreakpointOnLastBlockWithoutTail(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: toolLoopMessages()})
	last := body.Messages[len(body.Messages)-1]
	if last.Content[len(last.Content)-1].CacheControl == nil {
		t.Fatalf("last block must carry the rolling breakpoint: %+v", last)
	}
}

// Review focus 3: odd transcripts must neither panic nor mark a volatile block.
func TestAnthropicBreakpointEdgeCases(t *testing.T) {
	// An empty assistant message before any user message contributes no
	// block; the stable-block tracker must not index out of range.
	body := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleAssistant},
		{Role: schema.RoleUser, Content: "hi"},
	}})
	if body.Messages[0].Content[0].CacheControl == nil {
		t.Errorf("the user block is the last stable block: %+v", body.Messages)
	}

	// Only a volatile message after the system prompt: no message breakpoint.
	body = decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "state", Volatile: true},
	}})
	if body.Messages[0].Content[0].CacheControl != nil {
		t.Error("a volatile-only transcript must not get a message breakpoint")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -run 'TestAnthropic(KeepsMid|Rolling|BreakpointEdge)' -count=1 -v`
Expected: FAIL. `system = [... 3 blocks ...]` (the nudge is hoisted), and "must carry the rolling breakpoint".

- [ ] **Step 3: Implement the converter**

Replace `toAnthropicMessages` in `internal/llm/provider/anthropic.go`:

```go
// anthropicBlockRef locates one content block in a converted message list.
type anthropicBlockRef struct {
	msg, block int
	ok         bool
}

// toAnthropicMessages converts the transcript to Messages API shape.
//
// Only the leading run of system messages becomes the top-level system
// field. A system message later in the transcript (a skill hint, a
// mid-turn nudge, a finalize directive) stays where it is, as a user text
// block wrapped in <system-reminder> tags. Hoisting it into system would
// rewrite the cached system prefix mid-turn and strip it of its position
// next to the exchange it is about. Consecutive tool-result and user
// blocks merge into one user message, as the API requires.
//
// lastStable is the final block contributed by a non-Volatile message:
// where the rolling cache breakpoint goes.
func toAnthropicMessages(msgs []schema.ChatMessage) (system []anthropicContentBlock, out []anthropicMessage, lastStable anthropicBlockRef) {
	leading := true
	for _, m := range msgs {
		if m.Role != schema.RoleSystem {
			leading = false
		}
		switch m.Role {
		case schema.RoleSystem:
			if m.Content == "" {
				continue
			}
			if leading {
				system = append(system, anthropicContentBlock{Type: "text", Text: m.Content})
				continue
			}
			out = appendAnthropicUserBlock(out, anthropicContentBlock{
				Type: "text",
				Text: "<system-reminder>\n" + m.Content + "\n</system-reminder>",
			})
		case schema.RoleUser:
			out = appendAnthropicUserBlock(out, anthropicContentBlock{Type: "text", Text: m.Content})
		case schema.RoleTool:
			out = appendAnthropicUserBlock(out, anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			})
		case schema.RoleAssistant:
			blocks := make([]anthropicContentBlock, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				input := call.Args
				if len(input) == 0 || !json.Valid(input) {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_use", ID: call.ID, Name: call.Name, Input: input,
				})
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		default:
			continue
		}
		if !m.Volatile && len(out) > 0 {
			last := len(out) - 1
			lastStable = anthropicBlockRef{msg: last, block: len(out[last].Content) - 1, ok: true}
		}
	}
	return system, out, lastStable
}
```

In `buildAnthropicRequestBody`, replace the conversion line and the breakpoint comment (lines 278-283):

```go
	system, messages, lastStable := toAnthropicMessages(req.Messages)

	// Prompt caching breakpoints. The API allows four; this uses three:
	//   - the tool definitions and the system prompt, the large and stable
	//     head of the prefix;
	//   - a rolling breakpoint on the last block of the stable transcript.
	//     Each request reads the entry the previous request wrote (the API
	//     looks back about 20 blocks from a breakpoint) and writes a longer
	//     one, so a tool loop pays full price only for what is new. The
	//     Volatile working-state tail sits after it: it changes every
	//     request and would make the entry unreadable.
	if len(system) > 0 {
		system[len(system)-1].CacheControl = anthropicEphemeralCache
	}
	if lastStable.ok {
		messages[lastStable.msg].Content[lastStable.block].CacheControl = anthropicEphemeralCache
	}
```

Keep the existing tools loop and its `tools[len(tools)-1].CacheControl` line unchanged.

- [ ] **Step 4: Run the provider tests**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -count=1`
Expected: PASS. If an existing test compares a full Anthropic or `/messages` request body, its expected JSON now needs `"cache_control":{"type":"ephemeral"}` on the last stable message block. Update the expectation. Do not remove the breakpoint.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/provider
git add internal/llm/provider/anthropic.go internal/llm/provider/anthropic_test.go
git commit -m "prompt-caching: task 4 — anthropic keeps mid-conversation system messages in place and caches the transcript"
```

---

### Task 5: Send `prompt_cache_key` to OpenAI and Codex

Implements D7.

**Files:**
- Modify: `internal/llm/schema/chat.go:55-76` (`ChatRequest`)
- Create: `internal/llm/provider/prompt_cache_key.go`
- Modify: `internal/llm/provider/openai_compatible_wire.go:61-75` (`chatCompletionRequestBody`)
- Modify: `internal/llm/provider/openai_compatible.go:213` (`Chat`), `:380-394` (body literal)
- Modify: `internal/llm/provider/openai_compatible_responses.go:66-83` (`responsesRequestBody`), `:296-310` (body literal)
- Modify: `internal/llm/provider/openai_codex.go:63-98` (struct and options), `:128` (constructor literal), `:204-209` (`post`)
- Modify: `internal/llm/provider/factory.go:138-147` (Codex construction)
- Create: `internal/llm/provider/prompt_cache_key_test.go`

**Interfaces:**
- Consumes: `OpenAICompatible.sessionID` (already set by `NewFromConfigWithSession`); the `codexTestEngine`, `codexTestToken` and `codexSSE` test helpers.
- Produces: `schema.ChatRequest.PromptCacheKey string`; `func promptCacheKey(sessionID string) string`; `func (p *OpenAICompatible) sendsPromptCacheKey() bool`; `CodexOptions.SessionID string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/provider/prompt_cache_key_test.go`:

```go
package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func TestPromptCacheKeyStableAndOpaque(t *testing.T) {
	a, b := promptCacheKey("sess_123"), promptCacheKey("sess_123")
	if a == "" || a != b {
		t.Fatalf("keys for one session = %q / %q, want equal and non-empty", a, b)
	}
	if promptCacheKey("sess_456") == a {
		t.Fatal("different sessions produced the same key")
	}
	if strings.Contains(a, "sess_123") {
		t.Fatalf("key %q leaks the session id", a)
	}
	if len(a) > 64 {
		t.Fatalf("key length %d, want <= 64", len(a))
	}
	if promptCacheKey("") != "" {
		t.Fatal("empty session must produce no key")
	}
}

func TestSendsPromptCacheKeyDetection(t *testing.T) {
	cases := []struct {
		baseURL, session string
		want             bool
	}{
		{"https://api.openai.com/v1", "s", true},
		{"https://api.openai.com/v1", "", false},
		{"https://api.openai.com.evil.example/v1", "s", false},
		{"https://openrouter.ai/api/v1", "s", false},
		{"http://localhost:1234/v1", "s", false},
	}
	for _, tc := range cases {
		p, err := NewOpenAICompatible(Options{Name: "x", BaseURL: tc.baseURL, SessionID: tc.session})
		if err != nil {
			t.Fatalf("NewOpenAICompatible: %v", err)
		}
		if got := p.sendsPromptCacheKey(); got != tc.want {
			t.Errorf("sendsPromptCacheKey(%q, session=%q) = %v, want %v", tc.baseURL, tc.session, got, tc.want)
		}
	}
}

// rewriteTransport sends every request to target, keeping the path, so a
// provider can be configured with a real hostname but served by httptest.
type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func captureChatBody(t *testing.T, baseURL string, viaRewrite bool) map[string]any {
	t.Helper()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)
	opts := Options{Name: "x", BaseURL: baseURL, SessionID: "sess_123"}
	if viaRewrite {
		target, _ := url.Parse(server.URL)
		opts.HTTPClient = &http.Client{Transport: rewriteTransport{target: target}}
	} else {
		opts.BaseURL = server.URL
	}
	p, err := NewOpenAICompatible(opts)
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
	return body
}

func TestOpenAIChatSendsPromptCacheKey(t *testing.T) {
	body := captureChatBody(t, "https://api.openai.com/v1", true)
	if got := body["prompt_cache_key"]; got != promptCacheKey("sess_123") {
		t.Fatalf("prompt_cache_key = %v, want %q", got, promptCacheKey("sess_123"))
	}
}

// Review focus 4: strict OpenAI-compatible servers reject unknown fields.
func TestOpenAICompatibleOmitsPromptCacheKeyElsewhere(t *testing.T) {
	body := captureChatBody(t, "", false)
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("prompt_cache_key sent to a non-OpenAI endpoint: %v", body)
	}
}

func TestCodexSendsPromptCacheKey(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()
	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	caps := schema.ProviderCapabilities{ToolCalling: true, StructuredOutput: true, Reasoning: true}
	p, err := NewOpenAICodex(CodexOptions{
		Name: "codex", BaseURL: server.URL, Engine: engine, Capabilities: &caps,
		StaticModels: []string{"gpt-5.6-luna"}, SessionID: "sess_123",
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
	if got := body["prompt_cache_key"]; got != promptCacheKey("sess_123") {
		t.Fatalf("prompt_cache_key = %v, want %q", got, promptCacheKey("sess_123"))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -run 'PromptCacheKey' -count=1`
Expected: build failure, `undefined: promptCacheKey` / `SessionID` in `CodexOptions`.

- [ ] **Step 3: Implement**

`internal/llm/schema/chat.go`, add to `ChatRequest` after `ThinkingOptions`:

```go
	// PromptCacheKey routes requests that share a prompt prefix to the
	// same provider cache (OpenAI's prompt_cache_key). Providers fill it
	// from their own session identity on endpoints known to accept it;
	// callers leave it empty. Empty is never sent.
	PromptCacheKey string
```

Create `internal/llm/provider/prompt_cache_key.go`:

```go
package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
)

// promptCacheKey derives the prompt_cache_key for a conversation. OpenAI
// routes requests that share a key and a prompt prefix to the same cache,
// so a session's tool loop keeps hitting it instead of landing on a cold
// machine. The session ID is hashed: the key only has to be stable and
// distinct per conversation, and the provider has no use for marshal's
// own identifiers. Empty sessions get no key.
func promptCacheKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return "marshal-" + hex.EncodeToString(sum[:12])
}

// sendsPromptCacheKey reports whether this endpoint accepts
// prompt_cache_key. Only OpenAI's own API is known to; strict
// OpenAI-compatible servers reject unknown fields, so every other
// endpoint keeps its existing wire.
func (p *OpenAICompatible) sendsPromptCacheKey() bool {
	if p.sessionID == "" {
		return false
	}
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "api.openai.com"
}
```

`internal/llm/provider/openai_compatible_wire.go`, add to `chatCompletionRequestBody` after `Reasoning`:

```go
	PromptCacheKey  string                 `json:"prompt_cache_key,omitempty"`
```

and in the `chatCompletionRequestBody{...}` literal in `buildChatRequestBody` (`openai_compatible.go:380`) add `PromptCacheKey: req.PromptCacheKey,`.

`internal/llm/provider/openai_compatible_responses.go`, add to `responsesRequestBody` after `Include`:

```go
	PromptCacheKey  string               `json:"prompt_cache_key,omitempty"`
```

and in the `responsesRequestBody{...}` literal (line 296) add `PromptCacheKey: req.PromptCacheKey,`.

`internal/llm/provider/openai_compatible.go`, at the top of `Chat` (line 213), before the `ThinkingOptions` lookup:

```go
	if p.sendsPromptCacheKey() {
		req.PromptCacheKey = promptCacheKey(p.sessionID)
	}
```

`internal/llm/provider/openai_codex.go`:
- add `sessionID string` to the `OpenAICodex` struct;
- add to `CodexOptions`:
  ```go
  	// SessionID is the stable per-conversation identifier; it seeds
  	// prompt_cache_key so a session's requests reach a warm cache.
  	SessionID string
  ```
- add `sessionID: opts.SessionID,` to the `&OpenAICodex{...}` literal in `NewOpenAICodex`;
- in `post`, after `req.Temperature = nil`:
  ```go
  	// The Codex backend keys server-side prompt caching on
  	// prompt_cache_key (docs/codex-spike-findings-2026-09-14.md §3), as
  	// the codex CLI does with its conversation id.
  	req.PromptCacheKey = promptCacheKey(p.sessionID)
  ```

`internal/llm/provider/factory.go`, in the `NewOpenAICodex(CodexOptions{...})` literal add `SessionID: sessionID,`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/llm/... -count=1`
Expected: PASS. Existing request-shape tests must be unaffected: they use `httptest` hosts and either no session ID or a non-OpenAI host.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm
git add internal/llm/schema/chat.go internal/llm/provider/prompt_cache_key.go internal/llm/provider/prompt_cache_key_test.go internal/llm/provider/openai_compatible_wire.go internal/llm/provider/openai_compatible.go internal/llm/provider/openai_compatible_responses.go internal/llm/provider/openai_codex.go internal/llm/provider/factory.go
git commit -m "prompt-caching: task 5 — send prompt_cache_key to OpenAI and the Codex backend"
```

---

### Task 6: Parse Kimi's flat `usage.cached_tokens`

Implements D8.

**Files:**
- Modify: `internal/llm/provider/openai_compatible_wire.go:35-47` (`usageBody`)
- Modify: `internal/llm/provider/openai_compatible.go:475-500` (`tokenUsageFrom`)
- Test: `internal/llm/provider/openai_compatible_test.go` (append)

**Interfaces:**
- Consumes: nothing new.
- Produces: `usageBody.CachedTokens *int`.

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/provider/openai_compatible_test.go`:

```go
// Moonshot (Kimi) reports prompt cache hits as a flat usage field. The
// OpenAI detail object and DeepSeek's field still take precedence.
func TestTokenUsageFromReadsFlatCachedTokens(t *testing.T) {
	cases := []struct {
		name, raw string
		want      int
	}{
		{"kimi flat", `{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"cached_tokens":768}`, 768},
		{"openai details win", `{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":512},"cached_tokens":768}`, 512},
		{"deepseek wins over flat", `{"prompt_tokens":1000,"prompt_cache_hit_tokens":256,"cached_tokens":768}`, 256},
		{"absent", `{"prompt_tokens":1000}`, 0},
	}
	for _, tc := range cases {
		var u usageBody
		if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := tokenUsageFrom(&u).CacheReadTokens; got != tc.want {
			t.Errorf("%s: CacheReadTokens = %d, want %d", tc.name, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -run TestTokenUsageFromReadsFlatCachedTokens -count=1`
Expected: FAIL on `kimi flat` (`CacheReadTokens = 0, want 768`).

- [ ] **Step 3: Implement**

In `usageBody`, after `PromptCacheMissTokens`:

```go
	// Moonshot (Kimi) reports prompt cache hits as a flat usage field
	// rather than under prompt_tokens_details. Pointer for the same
	// absent-versus-zero reason as the DeepSeek fields.
	CachedTokens *int `json:"cached_tokens,omitempty"`
```

In `tokenUsageFrom`, after the DeepSeek `PromptCacheHitTokens` block:

```go
	if out.CacheReadTokens == 0 && u.CachedTokens != nil {
		out.CacheReadTokens = *u.CachedTokens
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/provider
git add internal/llm/provider/openai_compatible_wire.go internal/llm/provider/openai_compatible.go internal/llm/provider/openai_compatible_test.go
git commit -m "prompt-caching: task 6 — read Kimi's flat usage.cached_tokens"
```

The live confirmation of the field name is in Task 8, step 4.

---

### Task 7: Show the real prompt-cache rate in the TUI

Implements D9. Rendering only; the data is already in `turn_metrics` and `db.UsageTotals`.

**Files:**
- Modify: `internal/app/tui/sessionsheet/telemetry.go:44-81`
- Modify: `internal/app/tui/sessionsheet/section_context.go:91-93`
- Modify: `internal/app/tui/sessionsheet/section_session.go:71-80` (`Render`)
- Modify: `internal/app/tui/sessionsheet/telemetry_test.go:32-62`
- Test: `internal/app/tui/sessionsheet/section_session_test.go` (append)

**Interfaces:**
- Consumes: `db.TurnMetricsRow.PromptTokens` / `.CacheReadTokens` (int), `db.UsageTotals.PromptTokens` / `.CacheReadTokens` (int64).
- Produces: `TelemetryStats.PromptCachePct int`, which replaces `CacheHitPct`; `func cachedShare(t db.UsageTotals) int`.

- [ ] **Step 1: Write the failing tests**

In `internal/app/tui/sessionsheet/telemetry_test.go`, replace the three `TestTelemetry` fixture rows and the `CacheHitPct` assertion:

```go
	turns := []db.TurnMetricsRow{
		// most recent first
		{StartedAt: now.Add(-1 * time.Minute), PromptTokens: 1000, CompletionTokens: 200, CacheReadTokens: 900},
		{StartedAt: now.Add(-3 * time.Minute), PromptTokens: 800, CompletionTokens: 200, CacheReadTokens: 0},
		{StartedAt: now.Add(-5 * time.Minute), PromptTokens: 700, CompletionTokens: 100, CacheReadTokens: 350},
	}
```

```go
	// (900 + 0 + 350) cached of (1000 + 800 + 700) prompt tokens = 50%
	if got.PromptCachePct != 50 {
		t.Errorf("PromptCachePct = %d, want 50", got.PromptCachePct)
	}
```

In `TestTelemetryEmpty`, change `got.CacheHitPct != 0` to `got.PromptCachePct != 0`.

Append to `section_session_test.go`:

```go
func TestSessionSectionShowsPromptCacheShare(t *testing.T) {
	d := sessionData(time.Now())
	d.Totals.CacheReadTokens = 16_840 // 40% of 42_100
	joined := StripANSI(strings.Join(SessionSection{}.Render(d, 40, 0), "\n"))
	if !strings.Contains(joined, "40% cached") {
		t.Errorf("want the prompt-cache share, got %q", joined)
	}
}

// Review focus 5: a provider that reports no cache data shows nothing,
// not a misleading "0% cached".
func TestSessionSectionOmitsCacheShareWithoutCacheData(t *testing.T) {
	joined := StripANSI(strings.Join(SessionSection{}.Render(sessionData(time.Now()), 40, 0), "\n"))
	if strings.Contains(joined, "cached") {
		t.Errorf("no cache reads were reported; want no cache label, got %q", joined)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/app/tui/sessionsheet/ -count=1`
Expected: build failure, `got.PromptCachePct undefined`.

- [ ] **Step 3: Implement**

`telemetry.go`: in `TelemetryStats`, replace `CacheHitPct int` with:

```go
	// PromptCachePct is the share of prompt tokens the provider served
	// from its prompt cache across these turns. 0 when none were reported.
	// (The old CacheHitPct counted turns with a tool-result cache hit,
	// which read as a prompt-cache rate next to token stats.)
	PromptCachePct int
```

Rewrite the accumulation in `Telemetry`:

```go
	series := make([]int, len(turns))
	total, prompt, cached := 0, 0, 0
	for i, t := range turns {
		n := t.PromptTokens + t.CompletionTokens
		total += n
		prompt += t.PromptTokens
		cached += t.CacheReadTokens
		// Reverse into oldest-first order for left-to-right rendering.
		series[len(turns)-1-i] = n
	}

	stats := TelemetryStats{
		AvgTokens: total / len(turns),
		Series:    series,
	}
	if prompt > 0 {
		stats.PromptCachePct = cached * 100 / prompt
	}
```

`section_context.go` lines 91-93:

```go
		if stats.PromptCachePct > 0 {
			detail += fmt.Sprintf(" · %d%% cached", stats.PromptCachePct)
		}
```

`section_session.go`: add the helper and use it in `Render`'s token row:

```go
// cachedShare is the percentage of the session's prompt tokens served from
// the provider's prompt cache; 0 when nothing was reported.
func cachedShare(t db.UsageTotals) int {
	if t.PromptTokens <= 0 || t.CacheReadTokens <= 0 {
		return 0
	}
	return int(t.CacheReadTokens * 100 / t.PromptTokens)
}
```

```go
	tokens := fmt.Sprintf(" %s in · %s out",
		strutil.CompactTokens(int(d.Totals.PromptTokens)),
		strutil.CompactTokens(int(d.Totals.CompletionTokens)))
	if pct := cachedShare(d.Totals); pct > 0 {
		tokens += fmt.Sprintf(" · %d%% cached", pct)
	}
	rows := []string{
		ansi.Truncate(fmt.Sprintf(" turn %d · %s", d.Totals.Turns, shortDuration(sessionElapsed(d))), width, "…"),
		ansi.Truncate(tokens, width, "…"),
	}
```

Add `"marshal/internal/db"` to `section_session.go`'s imports if it is not already there.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/app/tui/... -count=1`
Expected: PASS. Run `grep -rn CacheHitPct internal/` and expect no matches.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/sessionsheet
git add internal/app/tui/sessionsheet
git commit -m "prompt-caching: task 7 — show the prompt-cache share instead of the tool-cache ratio"
```

---

### Task 8: Full verification, live checks and docs

**Files:**
- Modify: `docs/ARCHITECTURE.md` (new section after "Data flow of a single turn", around line 112)
- Modify: `AGENTS.md` ("Design constraints" list)
- Modify: `docs/prompt-caching/design.md` (record live results in §5)

- [ ] **Step 1: Full suite**

Run: `CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./... -count=1`
Expected: PASS. A failure outside the packages touched here is a regression from this branch until shown otherwise: check it against `main` before dismissing it.

Run: `gofmt -l . | grep -v '^web/ui/node_modules'`
Expected: no output.

- [ ] **Step 2: Live prefix check (any provider)**

```bash
CGO_ENABLED=1 go build -o marshal ./cmd/marshal
export MARSHAL_WIRE_CAPTURE_REQUESTS="$(mktemp -d)"
./marshal
```

In the TUI, give a task that takes at least four tool steps and writes todos (for example: "read three files under internal/contextpack and summarise each, tracking progress with todos"). Quit. Pick two consecutive main-loop captures, `OLD` and `NEW`: same provider, the newer one with more messages. Skip the small title and summary requests that land in the same directory. Then:

```bash
D="$MARSHAL_WIRE_CAPTURE_REQUESTS"; ls -t "$D"/*.request.json | head -6   # choose OLD and NEW
F=messages   # use F=input for Responses-path providers (Codex, OpenCode /responses)
n=$(jq ".$F | length - 1" "$OLD")   # everything before OLD's working-state tail
# del(cache_control): Anthropic's rolling breakpoint moves by design.
diff <(jq -c "del(.. | .cache_control?) | .$F[:$n]" "$OLD") \
     <(jq -c "del(.. | .cache_control?) | .$F[:$n]" "$NEW") && echo PREFIX-STABLE
```

Expected: `PREFIX-STABLE` (every message of the older request except its working-state tail opens the newer request unchanged). Delete the capture directory afterwards: it holds conversation content.

- [ ] **Step 3: Live cache-hit check (ollama-cloud, then Codex)**

Run a fresh multi-step turn on each, then:

```bash
sqlite3 -readonly .marshal/marshal.db "select provider, model, iterations, prompt_tokens, cache_read_tokens from turn_metrics order by id desc limit 3"
```

Expected: `cache_read_tokens > 0` on multi-iteration turns for both. If Codex still reports 0 with a stable prefix, record it in the design doc §5 as "Codex backend does not report or honour prompt_cache_key", and do not add more Codex changes in this branch.

- [ ] **Step 4: Live Kimi field check**

```bash
export MARSHAL_WIRE_CAPTURE="$(mktemp -d)"
./marshal   # one multi-step turn on the kimi provider
grep -o '"usage":{[^}]*}' "$MARSHAL_WIRE_CAPTURE"/kimi-*.stream | tail -3
```

Expected: a usage object whose cache field is either `cached_tokens` at the top level (Task 6 covers it) or `prompt_tokens_details.cached_tokens` (already covered). If it is a different name, add that field the way Task 6 did, with a test case using the captured JSON. Record the observed shape in the design doc §5, then delete the capture directory.

- [ ] **Step 5: Docs**

`docs/ARCHITECTURE.md`, add after the data-flow block:

````markdown
## Prompt layout and caching

Every request is laid out so that provider prompt caches can reuse
everything except what changed since the previous request:

```
[0] system prompt               stable within a turn (rebuilt only when skills,
                                loaded tools or system access change)
[1] context pack (stable part)  repo card, map, memories, summaries, plan, pins
    skill bodies
    replayed history
    skill hint                  per-turn; directly before the goal
    goal, then the turn's tool loop (append-only)
[n] working state (Volatile)    todos, scratchpad, semantic snippets; rebuilt
                                for every request, never stored
```

The working state is appended in `chatOnceAttempt`, the single dispatch
point. Anthropic places cache breakpoints on the tools, the system prompt and
the last non-`Volatile` block; OpenAI and Codex requests carry a
session-derived `prompt_cache_key`. `TestPromptPrefixStableAcrossSteps`
guards the layout. Design: `docs/prompt-caching/design.md`.
````

`AGENTS.md`, add to "Design constraints":

```markdown
- **Cache-stable prompt prefix.** Anything that changes within a turn
  (todos, scratchpad, retrieval snippets, per-turn hints) goes after the
  history: in the working-state tail (`contextpack.IsVolatile`) or
  directly before the goal. Never insert or rewrite a message ahead of
  the transcript mid-turn; `TestPromptPrefixStableAcrossSteps` fails if
  you do.
```

- [ ] **Step 6: Commit**

```bash
git add docs/ARCHITECTURE.md AGENTS.md docs/prompt-caching/design.md
git commit -m "prompt-caching: task 8 — document the cache-stable prompt layout and live results"
```
