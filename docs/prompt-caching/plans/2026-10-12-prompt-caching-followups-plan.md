# Prompt caching follow-ups — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a turn's first request reuse the previous turn's cached prefix on every provider, Anthropic included. Keep finalize, handoff and plan calls on the loop's cached prefix. Stop the agent roster from silently rewriting the system prompt between turns.

**Architecture:** The runner marks the end of the cross-turn stable region with `schema.ChatMessage.CacheAnchor`. A regression test pins that the region is append-only across turns, and the Anthropic encoder spends its fourth breakpoint there. Tools-disabled calls keep the tool definitions with `tool_choice: "none"`, falling back (latched per provider) to the old tool-less request when a backend cannot honour that. The discovered-models part of the roster is snapshotted once per runner.

**Tech Stack:** Go with cgo (`CGO_ENABLED=1`), standard `testing`.

**Spec:** [`docs/prompt-caching/followups-design.md`](../followups-design.md) (decisions F1–F5). Parent: [`docs/prompt-caching/design.md`](../design.md).

**Base:** `main` at `c2f10349`. Line anchors are as on that commit. Work on branch `prompt-caching-followups` (worktree `.worktrees/prompt-caching-followups`).

**Plan slug:** `prompt-caching-followups`. Commit each task as `prompt-caching-followups: task N — <title>`.

## Global Constraints

- Build and test with `CGO_ENABLED=1`.
- `internal/app/tui/` renders only. Nothing under `web/` changes. No new config keys, no new modules.
- `AGENTS.md` "Cache-stable prompt prefix": nothing may insert or rewrite a message ahead of the transcript mid-turn. `TestPromptPrefixStableAcrossSteps` must keep passing unchanged.
- Anthropic requests carry at most 4 `cache_control` breakpoints, never one on a `Volatile` block.
- JSON-envelope mode (`NativeTools == false`) never sends tool definitions; that stays true.
- Known baseline: on `main`, `go test ./...` fails 17 tests in `internal/app`, `internal/snapshot`, `internal/tools/desktop/browser` and `internal/worktree` (environmental). Any other failure is a regression.

## Review Focus

1. **A backend that ignores `tool_choice` lets the model call a tool during finalize or handoff** (Ollama-native has no such field). The call must still produce prose, and later tools-disabled calls must not re-try `none`. Pinned in Task 3 (`TestToolsDisabledCallFallsBackWhenModelCallsTool`).
2. **A strict backend rejects the request with a 400, naming `tool_choice` or not.** The call retries once without tools and the latch holds for the session; a cancellation is never retried. Pinned in Task 3 (`TestToolsDisabledCallFallsBackWhenToolChoiceRejected`, `…OnGenericRejection`, `…DoesNotRetryCancellation`).
3. **A wire rebuilt by compaction (handoff or rollover) must carry exactly one anchor, before the hint and the goal.** Pinned in Task 1 (`TestHandoffWireAnchorsBeforeHint`).
4. **An empty context pack removes the anchored pack message mid-turn.** The anchor moves to the previous message rather than vanishing or duplicating. Pinned in Task 1 (`TestSetContextPackMessageKeepsAnchor`).
5. **The anchor lands on a leading system message or on the rolling block.** The Anthropic body must not exceed 4 breakpoints or double-count. Pinned in Task 2.

---

### Task 1: Mark the cross-turn stable region and pin it across turns

Implements F1, and the regression guard for follow-up item 1.

**Files:**
- Modify: `internal/llm/schema/chat.go:19-31` (`ChatMessage`)
- Modify: `internal/agent/route.go` (add `markCacheAnchor` next to `setContextPackMessage`; update `setContextPackMessage`, lines 405-436)
- Modify: `internal/agent/runner.go:924-926` (turn start), `:983-985` (plan-first rebuild)
- Modify: `internal/agent/handoff.go:37-38`, `internal/agent/rollover.go:151-152`
- Test: `internal/agent/cacheprefix_test.go` (append)

**Interfaces:**
- Produces: `schema.ChatMessage.CacheAnchor bool`; `func markCacheAnchor(messages []schema.ChatMessage)` (package `agent`).
- Consumes: the existing test helpers `sameMessage` (cacheprefix_test.go), `rankFixture` (skillrank_test.go), `newTestState` (runner_testhelpers_test.go), `registerFakeRead` (metrics_test.go).

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/cacheprefix_test.go`. It already imports `contextpack`, `skills`, `schema`, `agenttest`, `config`, `policy`, `registry`, `strings` and `context`. Add any that are missing.

```go
// anchorIndex returns the index of the single CacheAnchor message, failing
// the test when there is not exactly one.
func anchorIndex(t *testing.T, msgs []schema.ChatMessage) int {
	t.Helper()
	idx, n := -1, 0
	for i, m := range msgs {
		if m.CacheAnchor {
			idx, n = i, n+1
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one CacheAnchor message, got %d", n)
	}
	return idx
}

func hintIndex(msgs []schema.ChatMessage) int {
	for i, m := range msgs {
		if strings.HasPrefix(m.Content, "Skill suggestions for this request") {
			return i
		}
	}
	return -1
}

// TestCrossTurnStableRegionIsAppendOnly pins follow-up item 1 (design
// followups-design.md §1). Everything up to and including turn N's
// CacheAnchor (system prompt, stable pack, skill bodies, replayed history)
// must open turn N+1's first request unchanged. That is what lets an
// automatic prefix cache, and Anthropic's anchor breakpoint, reuse a turn's
// history on the next turn.
func TestCrossTurnStableRegionIsAppendOnly(t *testing.T) {
	e, candidates := rankFixture()
	idx := skills.NewIndex()
	for _, c := range candidates {
		idx.Set(c.Name, c)
	}
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections:   []contextpack.Section{{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4}},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"Answer one.", "Answer two.", "Answer three."},
		FinishReasons: []string{"stop", "stop", "stop"},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SkillIndex = idx
	r.SkillEmbedder = e
	r.SetForceClass(string(ClassQuestion))

	const goal = "fix the flaky test" // rankFixture keys its vectors on this text
	var firsts []schema.ChatRequest
	for i := 0; i < 3; i++ {
		start := len(p.Requests)
		if err := r.Run(context.Background(), goal); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		firsts = append(firsts, p.Requests[start])
	}

	for k, req := range firsts {
		a := anchorIndex(t, req.Messages)
		if h := hintIndex(req.Messages); h >= 0 && h <= a {
			t.Fatalf("turn %d: skill hint (index %d) must come after the anchor (index %d)", k+1, h, a)
		}
	}
	for k := 1; k < len(firsts); k++ {
		prev, cur := firsts[k-1].Messages, firsts[k].Messages
		pa, ca := anchorIndex(t, prev), anchorIndex(t, cur)
		if ca <= pa {
			t.Fatalf("turn %d anchor (index %d) must sit after turn %d's (index %d): the region grows each turn", k+1, ca, k, pa)
		}
		for j := 0; j <= pa; j++ {
			if !sameMessage(prev[j], cur[j]) {
				t.Fatalf("turn %d rewrote message %d of turn %d's stable region:\nwas: %.200q\nnow: %.200q", k+1, j, k, prev[j].Content, cur[j].Content)
			}
		}
		if len(firsts[k].Tools) != len(firsts[k-1].Tools) {
			t.Fatalf("tool definitions changed between turns %d and %d", k, k+1)
		}
		for i := range firsts[k].Tools {
			if firsts[k].Tools[i].Name != firsts[k-1].Tools[i].Name || string(firsts[k].Tools[i].Parameters) != string(firsts[k-1].Tools[i].Parameters) {
				t.Fatalf("tool definition %d changed between turns %d and %d", i, k, k+1)
			}
		}
	}
}

// Review focus 3: a wire rebuilt by compaction carries exactly one anchor,
// before the hint and the goal.
func TestHandoffWireAnchorsBeforeHint(t *testing.T) {
	_, candidates := rankFixture()
	idx := skills.NewIndex()
	for _, c := range candidates {
		idx.Set(c.Name, c)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{"progress summary"}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.SkillIndex = idx
	r.skillHints = []string{"tdd"}

	old := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "fix the bug"},
	}
	fresh, err := r.summarizeAndContinue(context.Background(), p, "test-model", old, "fix the bug", nil)
	if err != nil {
		t.Fatalf("summarizeAndContinue: %v", err)
	}
	a := anchorIndex(t, fresh)
	h := hintIndex(fresh)
	if h < 0 || h <= a {
		t.Fatalf("hint (index %d) must follow the anchor (index %d)", h, a)
	}
}

// Review focus 4: replacing the pack keeps its anchor; removing it moves the
// anchor to the previous message.
func TestSetContextPackMessageKeepsAnchor(t *testing.T) {
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.contextPackMsgIndex = -1
	stable := contextpack.Pack{Sections: []contextpack.Section{{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal"}}}

	msgs := r.setContextPackMessage([]schema.ChatMessage{{Role: schema.RoleSystem, Content: "sys"}}, stable)
	markCacheAnchor(msgs)
	if !msgs[1].CacheAnchor {
		t.Fatalf("fixture: anchor should be on the pack message, got %+v", msgs)
	}
	msgs = r.setContextPackMessage(msgs, stable)
	if !msgs[1].CacheAnchor {
		t.Fatal("replacing the pack message dropped the anchor")
	}
	msgs = r.setContextPackMessage(msgs, contextpack.Pack{})
	if len(msgs) != 1 || !msgs[0].CacheAnchor {
		t.Fatalf("removing the anchored pack must move the anchor to the previous message, got %+v", msgs)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestCrossTurnStableRegionIsAppendOnly|TestHandoffWireAnchorsBeforeHint|TestSetContextPackMessageKeepsAnchor' -count=1`
Expected: build failure (`unknown field CacheAnchor`, `undefined: markCacheAnchor`).

- [ ] **Step 3: Add the field**

In `internal/llm/schema/chat.go`, add after `Volatile`:

```go
	// CacheAnchor marks the last message of the cross-turn stable region:
	// system prompt, stable context pack, skill bodies and replayed
	// history. Everything up to and including it opens the next turn's
	// first request unchanged, so providers with explicit cache
	// breakpoints (Anthropic) place one here. The runner sets exactly one
	// per freshly built wire.
	CacheAnchor bool
```

- [ ] **Step 4: Implement the marker and keep it through pack updates**

In `internal/agent/route.go`, add directly above `setContextPackMessage`:

```go
// markCacheAnchor flags the last message as the end of the cross-turn
// stable region and clears any earlier flag, so a wire carries exactly one
// anchor. Call it when a wire is built from scratch, after the replayed
// history and skill bodies and before the skill hint and the goal: those
// change every turn and must sit after the anchor. See
// docs/prompt-caching/followups-design.md (F1).
func markCacheAnchor(messages []schema.ChatMessage) {
	for i := range messages {
		messages[i].CacheAnchor = false
	}
	if n := len(messages); n > 0 {
		messages[n-1].CacheAnchor = true
	}
}
```

In `setContextPackMessage`, carry the flag. Replace the removal branch and the replacement branch:

```go
	if !ok {
		// Empty pack: remove the tracked message if it still exists. An
		// anchor it carried moves to the message before it, so the wire
		// keeps exactly one.
		if i := r.contextPackMsgIndex; i >= 0 && i < len(messages) {
			anchored := messages[i].CacheAnchor
			messages = append(messages[:i], messages[i+1:]...)
			if anchored && i > 0 {
				messages[i-1].CacheAnchor = true
			}
		}
		r.contextPackMsgIndex = -1
		return messages
	}

	if r.contextPackMsgIndex >= 0 && r.contextPackMsgIndex < len(messages) {
		msg.CacheAnchor = messages[r.contextPackMsgIndex].CacheAnchor
		messages[r.contextPackMsgIndex] = msg
		return messages
	}
```

- [ ] **Step 5: Mark the anchor at every build site**

`internal/agent/runner.go`, turn start. Between the closing brace of the `if r.role() == RoleGeneral { … buildHistoryMessages … }` block (line ~925) and `messages = r.appendSkillHint(messages)` (line 926):

```go
	markCacheAnchor(messages)
	messages = r.appendSkillHint(messages)
```

Make the same insertion in the plan-first rebuild (lines ~984-985), between the history block's closing brace and `messages = r.appendSkillHint(messages)`.

`internal/agent/handoff.go` and `internal/agent/rollover.go`, in both:

```go
	fresh = r.appendSkillBodies(fresh)
	markCacheAnchor(fresh)
	fresh = r.appendSkillHint(fresh)
```

- [ ] **Step 6: Run the tests**

Run: `CGO_ENABLED=1 go test ./internal/agent/... -count=1`
Expected: PASS, including `TestPromptPrefixStableAcrossSteps` and `TestSkillHintFollowsReplayedHistory` unchanged.

Mutation check: move the turn-start `markCacheAnchor(messages)` to after `appendSkillHint`. `TestCrossTurnStableRegionIsAppendOnly` must fail (the hint inside the anchored region). Revert.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/agent internal/llm/schema
git add internal/llm/schema/chat.go internal/agent/route.go internal/agent/runner.go internal/agent/handoff.go internal/agent/rollover.go internal/agent/cacheprefix_test.go
git commit -m "prompt-caching-followups: task 1 — mark the cross-turn stable region and pin it across turns"
```

---

### Task 2: Anthropic — fourth breakpoint on the cache anchor

Implements F2 (follow-up item 3).

**Files:**
- Modify: `internal/llm/provider/anthropic.go:167-232` (`toAnthropicMessages`), `:308-327` (breakpoints)
- Test: `internal/llm/provider/anthropic_test.go` (append)

**Interfaces:**
- Consumes: `schema.ChatMessage.CacheAnchor` (Task 1); the test helpers `decodeAnthropicBody` and `countBreakpoints` already in `anthropic_test.go`.
- Produces: `toAnthropicMessages` returns a fourth value, `anchor anthropicBlockRef`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/llm/provider/anthropic_test.go`:

```go
// The anchor's last block carries the fourth breakpoint, so a turn's first
// request reads the previous turn's history instead of rewriting it.
func TestAnthropicAnchorBreakpoint(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{
		Model: "claude-test",
		Tools: []schema.ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Messages: []schema.ChatMessage{
			{Role: schema.RoleSystem, Content: "harness prompt"},
			{Role: schema.RoleUser, Content: "Project context pack:\n\n## Repo Card\n\nProject: marshal"},
			{Role: schema.RoleUser, Content: "first question"},
			{Role: schema.RoleAssistant, Content: "Answer one.", CacheAnchor: true},
			{Role: schema.RoleUser, Content: "second question"},
			{Role: schema.RoleUser, Content: "Current working state (reference only)", Volatile: true},
		},
	})

	// Wire: user[pack, q1], assistant[ans1], user[q2, tail]
	if got := body.Messages[1].Content[0]; got.CacheControl == nil {
		t.Errorf("anchor block (assistant answer) must carry a breakpoint: %+v", got)
	}
	last := body.Messages[2]
	if last.Content[0].CacheControl == nil {
		t.Error("the rolling breakpoint must stay on the last stable block")
	}
	if last.Content[1].CacheControl != nil {
		t.Error("the volatile tail must not carry a breakpoint")
	}
	if got := countBreakpoints(body); got != 4 {
		t.Errorf("breakpoints = %d, want 4 (tools, system, anchor, rolling)", got)
	}
}

// Review focus 5: an anchor on a leading system message or on the rolling
// block adds nothing.
func TestAnthropicAnchorNeverExceedsFour(t *testing.T) {
	onSystem := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys", CacheAnchor: true},
		{Role: schema.RoleUser, Content: "hi"},
	}})
	if got := countBreakpoints(onSystem); got != 2 {
		t.Errorf("anchor on the leading system message: breakpoints = %d, want 2 (system, rolling)", got)
	}

	onRolling := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "hi", CacheAnchor: true},
	}})
	if got := countBreakpoints(onRolling); got != 2 {
		t.Errorf("anchor on the rolling block: breakpoints = %d, want 2", got)
	}

	onVolatile := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "q"},
		{Role: schema.RoleUser, Content: "state", Volatile: true, CacheAnchor: true},
	}})
	if onVolatile.Messages[0].Content[1].CacheControl != nil {
		t.Error("a volatile message must never carry a breakpoint, anchor or not")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -run 'TestAnthropicAnchor' -count=1`
Expected: FAIL. "anchor block (assistant answer) must carry a breakpoint", and breakpoints = 3, want 4.

- [ ] **Step 3: Implement**

In `toAnthropicMessages`, change the signature and doc tail:

```go
// lastStable is the final block contributed by a non-Volatile message:
// where the rolling cache breakpoint goes. anchor is the final block of the
// CacheAnchor message, the end of the cross-turn stable region, unless that
// message is a leading system message (the system block already carries a
// breakpoint) or Volatile.
func toAnthropicMessages(msgs []schema.ChatMessage) (system []anthropicContentBlock, out []anthropicMessage, lastStable, anchor anthropicBlockRef) {
```

Replace the trailing `lastStable` update and the return:

```go
		if !m.Volatile && len(out) > 0 {
			last := len(out) - 1
			ref := anthropicBlockRef{msg: last, block: len(out[last].Content) - 1, ok: true}
			lastStable = ref
			if m.CacheAnchor {
				anchor = ref
			}
		}
	}
	return system, out, lastStable, anchor
```

In `buildAnthropicRequestBody`, change the call and add the breakpoint after the rolling one, then update the comment:

```go
	system, messages, lastStable, anchor := toAnthropicMessages(req.Messages)

	// Prompt caching breakpoints, at most the API's four:
	//   - the tool definitions and the system prompt, the large and stable
	//     head of the prefix;
	//   - the cache anchor, the end of the cross-turn stable region (stable
	//     pack, skill bodies, replayed history). The next turn's anchor
	//     sits a few blocks later, within the API's ~20-block lookback, so
	//     each turn's first request reads the previous turn's history;
	//   - a rolling breakpoint on the last block of the stable transcript.
	//     Each request reads the entry the previous request wrote and
	//     writes a longer one, so a tool loop pays full price only for what
	//     is new. The Volatile working-state tail sits after it: it changes
	//     every request and would make the entry unreadable.
	if len(system) > 0 {
		system[len(system)-1].CacheControl = anthropicEphemeralCache
	}
	if lastStable.ok {
		messages[lastStable.msg].Content[lastStable.block].CacheControl = anthropicEphemeralCache
	}
	if anchor.ok {
		messages[anchor.msg].Content[anchor.block].CacheControl = anthropicEphemeralCache
	}
```

Run `grep -n 'toAnthropicMessages(' internal/llm/provider/*.go`. Any other caller, including tests, must accept the fourth return value.

- [ ] **Step 4: Run the tests**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -count=1`
Expected: PASS, including the existing `TestAnthropicRollingBreakpointSkipsVolatileTail` (3 breakpoints, no anchor in its fixture).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/provider
git add internal/llm/provider/anthropic.go internal/llm/provider/anthropic_test.go
git commit -m "prompt-caching-followups: task 2 — anthropic places its fourth breakpoint on the cache anchor"
```

---

### Task 3: Tools-disabled calls keep the tool definitions

Implements F3 (follow-up item 2).

**Files:**
- Modify: `internal/agent/chat.go:79-81` (`chatWithRetryNoNativeTools`), `:352-357` and the `schema.ChatRequest{…}` literal at `:390-401` (`chatOnceAttempt`)
- Modify: `internal/agent/runner.go` (two fields on `Runner`, next to the other per-session caches around line 490)
- Test: create `internal/agent/toolchoice_test.go`

**Interfaces:**
- Consumes: `provider.ProviderError` (`internal/llm/provider/errors.go:7`: fields `Provider`, `StatusCode`, `Body`; `Error()` includes the body).
- Produces: `func (r *Runner) toolChoiceNoneRejected(providerName string) bool`; `func (r *Runner) rejectToolChoiceNone(providerName string)`; `func isCancellation(err error) bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/toolchoice_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func toolsDisabledRunner(t *testing.T, p *agenttest.ScriptedProvider, native bool) *Runner {
	t.Helper()
	reg := registry.New()
	registerFakeRead(t, reg, false)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.NativeTools = native
	r.MaxRetries = 0
	return r
}

var toolsDisabledMsgs = []schema.ChatMessage{
	{Role: schema.RoleSystem, Content: "sys"},
	{Role: schema.RoleUser, Content: "summarize"},
}

// Finalize, handoff and plan calls send the loop's tool definitions with
// tool_choice "none", so their prompt prefix matches the loop's cache.
func TestToolsDisabledCallKeepsDefinitions(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"summary"}}
	r := toolsDisabledRunner(t, p, true)

	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	req := p.Requests[0]
	if req.ToolChoice != "none" {
		t.Fatalf("ToolChoice = %q, want none", req.ToolChoice)
	}
	want := r.buildToolDefinitions()
	if len(want) == 0 || len(req.Tools) != len(want) {
		t.Fatalf("tools = %d, want the loop's %d definitions", len(req.Tools), len(want))
	}
	for i := range want {
		if req.Tools[i].Name != want[i].Name {
			t.Fatalf("tool %d = %q, want %q", i, req.Tools[i].Name, want[i].Name)
		}
	}
}

// The loop's own calls are unchanged: tools on, no tool_choice.
func TestLoopCallSendsNoToolChoice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"ok"}}
	r := toolsDisabledRunner(t, p, true)
	if _, err := r.chatWithRetry(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err != nil {
		t.Fatal(err)
	}
	if p.Requests[0].ToolChoice != "" || len(p.Requests[0].Tools) == 0 {
		t.Fatalf("loop call: ToolChoice=%q tools=%d, want \"\" and >0", p.Requests[0].ToolChoice, len(p.Requests[0].Tools))
	}
}

// Review focus 2: a backend that rejects tool_choice gets one tool-less
// retry, and the latch holds for later tools-disabled calls.
func TestToolsDisabledCallFallsBackWhenToolChoiceRejected(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs:      []error{&provider.ProviderError{Provider: "scripted", StatusCode: 400, Body: `{"error":{"message":"tool_choice none is not supported"}}`}},
		Responses: []string{"", "summary", "again"},
	}
	r := toolsDisabledRunner(t, p, true)

	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(p.Requests) != 2 || p.Requests[1].Tools != nil || p.Requests[1].ToolChoice != "" {
		t.Fatalf("want one tool-less retry; requests=%d retry tools=%d choice=%q", len(p.Requests), len(p.Requests[len(p.Requests)-1].Tools), p.Requests[len(p.Requests)-1].ToolChoice)
	}
	if _, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.Requests) != 3 || p.Requests[2].Tools != nil {
		t.Fatalf("latched provider must not be sent tools again; requests=%d", len(p.Requests))
	}
}

// Strict servers do not always name the field they reject. A generic 400
// on a call that sent tools must still fall back rather than fail
// finalize or the handoff summary.
func TestToolsDisabledCallFallsBackOnGenericRejection(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs:      []error{&provider.ProviderError{Provider: "scripted", StatusCode: 400, Body: `{"error":"invalid request"}`}},
		Responses: []string{"", "summary"},
	}
	r := toolsDisabledRunner(t, p, true)
	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" || len(p.Requests) != 2 || p.Requests[1].Tools != nil {
		t.Fatalf("res=%+v err=%v requests=%d", res, err, len(p.Requests))
	}
}

// A cancelled call is never retried.
func TestToolsDisabledCallDoesNotRetryCancellation(t *testing.T) {
	p := &agenttest.ScriptedProvider{Errs: []error{context.Canceled}, Responses: []string{"", "summary"}}
	r := toolsDisabledRunner(t, p, true)
	if _, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err == nil {
		t.Fatal("want the cancellation error back")
	}
	if len(p.Requests) != 1 || r.toolChoiceNoneRejected(p.Name()) {
		t.Fatalf("cancellation must not retry or latch; requests=%d", len(p.Requests))
	}
}

// Review focus 1: a backend that ignores tool_choice (Ollama-native has no
// such field) lets the model call a tool. Retry once without tools.
func TestToolsDisabledCallFallsBackWhenModelCallsTool(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{"", "summary"},
		ToolCalls: [][]schema.ToolCall{{{ID: "c1", Name: "file.read", Args: json.RawMessage(`{"path":"a.go"}`)}}},
	}
	r := toolsDisabledRunner(t, p, true)

	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" || len(res.ToolCalls) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(p.Requests) != 2 || p.Requests[1].Tools != nil {
		t.Fatalf("want one tool-less retry; requests=%d", len(p.Requests))
	}
}

// JSON-envelope mode never sends tool definitions; that is unchanged.
func TestToolsDisabledCallInEnvelopeModeSendsNoTools(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"summary"}}
	r := toolsDisabledRunner(t, p, false)
	if _, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err != nil {
		t.Fatal(err)
	}
	if p.Requests[0].Tools != nil || p.Requests[0].ToolChoice != "" {
		t.Fatalf("envelope mode: tools=%d choice=%q, want none", len(p.Requests[0].Tools), p.Requests[0].ToolChoice)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestToolsDisabled|TestLoopCallSendsNoToolChoice' -count=1`
Expected: build failure, `undefined: (*Runner).toolChoiceNoneRejected` (used by `TestToolsDisabledCallDoesNotRetryCancellation`). To see the behavioural RED, temporarily comment that one assertion out. `TestToolsDisabledCallKeepsDefinitions` then fails (`ToolChoice = "", want none`), and the three fallback tests fail on their request counts. `TestLoopCallSendsNoToolChoice` and `TestToolsDisabledCallInEnvelopeModeSendsNoTools` pass already; they guard behaviour that must not change. Restore the assertion before Step 3.

- [ ] **Step 3: Add the latch**

On the `Runner` struct in `internal/agent/runner.go`, next to the other unexported per-session caches (around `contextPackMsgIndex`, line ~490), add:

```go
	// toolChoiceNoneOff records providers that cannot honour tool_choice
	// "none": they rejected it, or the model called a tool anyway.
	// Tools-disabled calls to them omit the tool definitions for the rest
	// of this runner's life. See chatWithRetryNoNativeTools.
	toolChoiceMu      sync.Mutex
	toolChoiceNoneOff map[string]bool
```

(`sync` is already imported in runner.go; add it if not.)

In `internal/agent/chat.go`, add near `chatWithRetryNoNativeTools`:

```go
func (r *Runner) toolChoiceNoneRejected(providerName string) bool {
	r.toolChoiceMu.Lock()
	defer r.toolChoiceMu.Unlock()
	return r.toolChoiceNoneOff[providerName]
}

func (r *Runner) rejectToolChoiceNone(providerName string) {
	r.toolChoiceMu.Lock()
	defer r.toolChoiceMu.Unlock()
	if r.toolChoiceNoneOff == nil {
		r.toolChoiceNoneOff = make(map[string]bool)
	}
	if !r.toolChoiceNoneOff[providerName] {
		r.toolChoiceNoneOff[providerName] = true
		if r.State != nil {
			r.State.Logger().Info("tool_choice none not honoured; tools-disabled calls will omit tool definitions", "provider", providerName)
		}
	}
}

// isCancellation reports whether err ended the call on the caller's side
// (Esc, turn cancel, deadline), which must never be retried.
func isCancellation(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}
```

- [ ] **Step 4: Send the tools with tool_choice "none", and fall back**

Replace `chatWithRetryNoNativeTools`:

```go
// chatWithRetryNoNativeTools runs a call that must not use tools: finalize,
// the handoff summary, the plan-first plan. In native-tools mode it still
// sends the loop's tool definitions, with tool_choice "none". The
// definitions head every provider's prompt cache, so dropping them made
// these calls, which include the largest request of a long session, miss
// the cache entirely. A provider that rejects tool_choice, or whose model
// calls a tool anyway (Ollama-native has no tool_choice field), gets one
// retry without tools and is latched to tool-less calls from then on.
// See docs/prompt-caching/followups-design.md (F3).
func (r *Runner) chatWithRetryNoNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat) (chatResult, error) {
	sentTools := r.NativeTools && !r.toolChoiceNoneRejected(p.Name())
	res, err := r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, false)
	if !sentTools || isCancellation(err) || (err == nil && len(res.ToolCalls) == 0) {
		return res, err
	}
	// The provider failed the request, or the model called a tool
	// despite "none". Strict servers do not always name the offending
	// field, so any failure counts: the worst a wrong latch costs is the
	// cache benefit, never the call. Transient network failures do not
	// reach here (chatWithRetryWithNativeTools waits them out).
	r.rejectToolChoiceNone(p.Name())
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, false)
}
```

In `chatOnceAttempt`, replace the tools block:

```go
	var tools []schema.ToolDefinition
	toolChoice := ""
	if r.NativeTools {
		switch {
		case includeNativeTools:
			tools = r.buildToolDefinitions()
		case !r.toolChoiceNoneRejected(p.Name()):
			// A tools-disabled call keeps the definitions so its prefix
			// matches the loop's; tool_choice "none" forbids calls. See
			// chatWithRetryNoNativeTools for the fallback.
			tools = r.buildToolDefinitions()
			toolChoice = "none"
		}
	}
```

and add `ToolChoice: toolChoice,` to the `schema.ChatRequest{…}` literal right after `Tools: tools,`.

- [ ] **Step 5: Run the tests**

Run: `CGO_ENABLED=1 go test ./internal/agent/... ./internal/llm/... -count=1`
Expected: PASS. If an existing finalize or handoff test asserts the tools-disabled request has no tools, update it to expect the loop's definitions plus `ToolChoice: "none"` (that is the change). Any other failure: investigate.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/agent
git add internal/agent/chat.go internal/agent/runner.go internal/agent/toolchoice_test.go
git commit -m "prompt-caching-followups: task 3 — tools-disabled calls keep the tool definitions with tool_choice none"
```

---

### Task 4: Snapshot the discovered models once per runner

Implements F4 (follow-up item 4).

**Files:**
- Modify: `internal/agent/prompts.go:288-290` (`agentRoster`)
- Modify: `internal/agent/runner.go` (two fields on `Runner`, next to the Task 3 fields)
- Test: `internal/agent/prompts_test.go` (append)

**Interfaces:**
- Consumes: `modelcache.Save(dataDir string, c modelcache.Cache) error`, `modelcache.Cache{Providers map[string]modelcache.Entry}`, `modelcache.Entry{ConfigHash, Models, FetchedAt}`, `modelcache.HashProvider(pc)`, `(*session.State).SetModelCacheDir(dir)`.
- Produces: no new exported names.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/prompts_test.go` (add imports `marshal/internal/llm/provider/modelcache`, `marshal/internal/app/session` and `marshal/internal/agent/agenttest` if missing):

```go
// The roster heads the system prompt, which heads every provider's prompt
// cache. Its discovered-models part comes from an on-disk cache that a
// background refresh rewrites and a 24h TTL expires, so it must be read
// once per runner. Otherwise the system prompt changes between turns with
// no user action.
func TestAgentRosterSnapshotsDiscoveredModels(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "openai_compatible", BaseURL: "http://localhost:1234/v1"},
	}
	save := func(id string) {
		t.Helper()
		err := modelcache.Save(dir, modelcache.Cache{Providers: map[string]modelcache.Entry{
			"local": {
				ConfigHash: modelcache.HashProvider(cfg.Providers["local"]),
				Models:     []schema.ModelInfo{{ID: id}},
				FetchedAt:  time.Now(),
			},
		}})
		if err != nil {
			t.Fatalf("modelcache.Save: %v", err)
		}
	}
	save("alpha-model")
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.SetModelCacheDir(dir)
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&cfg, nil), state, "test-model")

	first := r.agentRoster()
	if !strings.Contains(first, "alpha-model") {
		t.Fatalf("fixture: roster should list the discovered model, got %q", first)
	}
	save("beta-model") // a background refresh between turns
	if got := r.agentRoster(); got != first {
		t.Fatalf("roster changed mid-session:\nwas: %q\nnow: %q", first, got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestAgentRosterSnapshotsDiscoveredModels -count=1`
Expected: FAIL, "roster changed mid-session" (the second call lists `beta-model`).

If the fixture check fails instead, the roster render is filtering the entry. Check `RenderAgentRosterWithDiscovered` (prompts.go ~356) and adjust the fixture, not the assertion.

- [ ] **Step 3: Implement**

Add to the `Runner` struct, next to the Task 3 fields:

```go
	// discoveredModels is the model-discovery snapshot the agent roster
	// renders from, read once per runner (one session). See agentRoster.
	rosterMu         sync.Mutex
	discoveredModels map[string][]schema.ModelInfo
```

Replace `agentRoster` in `internal/agent/prompts.go`:

```go
// agentRoster renders the roster for the system prompt. The discovered-
// models part is snapshotted on first use and reused for the runner's
// lifetime (one session). It comes from an on-disk cache that a background
// refresh rewrites and a 24h TTL expires against the wall clock, so
// re-reading it on every build let the system prompt, the head of every
// provider's prompt cache, change between turns with no user action. The
// config-derived parts are rendered every time, so a preset added
// mid-session still appears; newly discovered models appear next session.
func (r *Runner) agentRoster() string {
	r.rosterMu.Lock()
	if r.discoveredModels == nil {
		r.discoveredModels = DiscoveredModelsFromCache(r.State.Config, r.State.ModelCacheDir(), time.Now())
	}
	discovered := r.discoveredModels
	r.rosterMu.Unlock()
	return RenderAgentRosterWithDiscovered(r.State.Config, discovered)
}
```

`DiscoveredModelsFromCache` returns a non-nil map (`out := map[string][]schema.ModelInfo{}`), so an empty discovery result is still a snapshot and is not re-read.

- [ ] **Step 4: Run the tests**

Run: `CGO_ENABLED=1 go test ./internal/agent/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent
git add internal/agent/prompts.go internal/agent/runner.go internal/agent/prompts_test.go
git commit -m "prompt-caching-followups: task 4 — snapshot discovered models once per runner"
```

---

### Task 5: Docs and full verification

**Files:**
- Modify: `docs/ARCHITECTURE.md` (the "Prompt layout and caching" section)
- Modify: `docs/prompt-caching/design.md` §4 (point the follow-ups at the new design)
- Modify: `docs/prompt-caching/followups-design.md` (append the status to §4)

- [ ] **Step 1: Full suite**

Run: `CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./... -count=1`
Expected: the only failures are the 17 baseline tests listed in Global Constraints. For any other failure, check whether it also fails on `c2f10349`, using a temporary `git worktree add` under the session scratchpad and removing it afterwards. A failure that also occurs on the base is baseline; anything else is a regression to report, not to fix here.

Run: `gofmt -l internal/`
Expected: no output.

- [ ] **Step 2: Docs**

In `docs/ARCHITECTURE.md`, replace the layout block in "Prompt layout and caching" with:

````markdown
```
[0] system prompt               stable within a session (rebuilt only when skills,
                                loaded tools or system access change; the agent
                                roster's discovered models are snapshotted per session)
[1] context pack (stable part)  repo card, map, memories, summaries, plan, pins
    skill bodies
    replayed history            ← CacheAnchor: end of the cross-turn stable region
    skill hint                  per-turn; directly before the goal
    goal, then the turn's tool loop (append-only)
[n] working state (Volatile)    todos, scratchpad, semantic snippets; rebuilt
                                for every request, never stored
```
````

Then replace the paragraph after it with:

```markdown
The working state is appended in `chatOnceAttempt`, the single dispatch
point. Everything up to the `CacheAnchor` opens the next turn's first
request unchanged (`TestCrossTurnStableRegionIsAppendOnly`); within a turn
the prefix only grows (`TestPromptPrefixStableAcrossSteps`). Anthropic
places cache breakpoints on the tools, the system prompt, the anchor and
the last non-`Volatile` block. OpenAI and Codex requests carry a
session-derived `prompt_cache_key`. Tools-disabled calls (finalize, handoff
summary, plan) keep the tool definitions with `tool_choice: none` so they
share the loop's prefix. Loading a deferred tool with `tools.select` costs
one cache miss, because tools head every provider's prefix. Design:
`docs/prompt-caching/design.md`, `docs/prompt-caching/followups-design.md`.
```

In `docs/prompt-caching/design.md` §4, append to the "Tool-less finalize and handoff calls" bullet and to the "Cross-turn history" bullet: ` Taken up in [followups-design.md](followups-design.md).`

Append to `docs/prompt-caching/followups-design.md` §4:

```markdown
**Status (unit):** verified on the `prompt-caching-followups` branch. Live checks pending alongside the parent design's.
```

- [ ] **Step 3: Commit**

```bash
git add docs/ARCHITECTURE.md docs/prompt-caching/design.md docs/prompt-caching/followups-design.md
git commit -m "prompt-caching-followups: task 5 — document the cache anchor and tools-disabled calls"
```
