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
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
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
// turn does: the model rewrites its todo list and wall-clock time passes.
// Neither may change any message a previous request already sent; both must
// still reach the model in the working-state tail.
func TestPromptPrefixStableAcrossSteps(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	clock := time.Unix(1_000, 0)
	now := func() time.Time { return clock }
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{}, session.WithClock(now))
	// A stable pack so the run exercises stable + volatile sections together.
	state.SetContextPack(contextpack.Pack{
		Sections:   []contextpack.Section{{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal"}},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000},
	})
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

	// Message 1 is the stable pack: byte-identical on every request, with
	// no volatile section in it.
	for i, req := range p.Requests {
		if len(req.Messages) < 2 {
			t.Fatalf("request %d has %d messages, want at least 2", i, len(req.Messages))
		}
		m := req.Messages[1]
		if !strings.Contains(m.Content, "Project context pack:") || !strings.Contains(m.Content, "Project: marshal") {
			t.Fatalf("request %d message 1 is not the stable context pack: %.300q", i, m.Content)
		}
		if strings.Contains(m.Content, "Current Todos") {
			t.Fatalf("request %d stable pack contains the volatile todos: %.300q", i, m.Content)
		}
		if m.Content != p.Requests[0].Messages[1].Content {
			t.Fatalf("request %d message 1 differs from request 0:\nwas: %.300q\nnow: %.300q", i, p.Requests[0].Messages[1].Content, m.Content)
		}
	}

	last := p.Requests[len(p.Requests)-1].Messages
	tail := last[len(last)-1]
	if !tail.Volatile || !strings.Contains(tail.Content, "Current Todos") {
		t.Fatalf("last message must be the volatile working state with todos, got %+v", tail)
	}
	for i, m := range last[:len(last)-1] {
		if m.Volatile || strings.Contains(m.Content, "Current working state") {
			t.Fatalf("message %d is a stale working-state copy; exactly one tail is allowed", i)
		}
	}
	// Review focus 2: the tail is wire-only and never reaches the transcript.
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "Current working state") {
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

// skillFixture is a small installed-skill roster. It carries no embedder:
// skill hints are gone, so nothing ranks these against the goal.
func skillFixture() []skills.Skill {
	return []skills.Skill{
		{Name: "tdd", Description: "test-driven development workflow"},
		{Name: "cooking", Description: "recipes for dinner"},
		{Name: "lint", Description: "bulk lint fixing"},
	}
}

// No request may carry a per-turn skill suggestion message. Edit-class
// turns used to get TDD/verification suggestions from class defaults even
// without an embedder; this pins that the whole mechanism is gone and the
// goal directly follows the cache anchor.
func TestNoSkillHintMessageOnWire(t *testing.T) {
	idx := skills.NewIndex()
	for _, c := range skillFixture() {
		idx.Set(c.Name, c)
	}
	idx.Set("test-driven-development", skills.Skill{Name: "test-driven-development", Description: "tests first"})
	idx.Set("verification-before-completion", skills.Skill{Name: "verification-before-completion", Description: "verify"})
	p := &agenttest.ScriptedProvider{Responses: []string{"First answer.", "Second answer."}, FinishReasons: []string{"stop", "stop"}}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SkillIndex = idx
	r.SetForceClass(string(ClassEdit))

	const goal = "fix the flaky test"
	for i := 0; i < 2; i++ {
		if err := r.Run(context.Background(), goal); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	for k, req := range p.Requests {
		for _, m := range req.Messages {
			if strings.HasPrefix(m.Content, "Skill suggestions for this request") {
				t.Fatalf("request %d carried a skill hint message", k)
			}
		}
	}
	first := p.Requests[len(p.Requests)-1].Messages
	a := anchorIndex(t, first)
	if a+1 >= len(first) || first[a+1].Role != schema.RoleUser || first[a+1].Content != goal {
		t.Fatalf("goal must directly follow the anchor; got %#v", first[a:])
	}
}

// TestCrossTurnStableRegionIsAppendOnly pins follow-up item 1 (design
// followups-design.md §1). Everything up to and including turn N's
// CacheAnchor (system prompt, stable pack, skill bodies, replayed history)
// must open turn N+1's first request unchanged. That is what lets an
// automatic prefix cache, and Anthropic's anchor breakpoint, reuse a turn's
// history on the next turn.
func TestCrossTurnStableRegionIsAppendOnly(t *testing.T) {
	candidates := skillFixture()
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
	r.SetForceClass(string(ClassQuestion))

	const goal = "fix the flaky test"
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
		if a+1 >= len(req.Messages) || req.Messages[a+1].Content != goal {
			t.Fatalf("turn %d: the goal must directly follow the anchor (index %d)", k+1, a)
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
// with the goal directly after it.
func TestHandoffWireGoalFollowsAnchor(t *testing.T) {
	idx := skills.NewIndex()
	for _, c := range skillFixture() {
		idx.Set(c.Name, c)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{"progress summary"}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.SkillIndex = idx

	old := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "fix the bug"},
	}
	fresh, err := r.summarizeAndContinue(context.Background(), p, "test-model", old, "fix the bug", nil)
	if err != nil {
		t.Fatalf("summarizeAndContinue: %v", err)
	}
	a := anchorIndex(t, fresh)
	if a+1 >= len(fresh) || fresh[a+1].Content != "fix the bug" {
		t.Fatalf("goal must directly follow the anchor (index %d): %#v", a, fresh[a:])
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
