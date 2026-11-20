package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// volatileTailCount counts the volatile messages after wire that mention
// marker. It is the shape assertion the workspace guidance tests share: a
// request carries exactly one current workspace message, never a stack of
// them.
func volatileTailCount(wire []schema.ChatMessage, marker string) int {
	n := 0
	for _, m := range wire {
		if m.Volatile && strings.Contains(m.Content, marker) {
			n++
		}
	}
	return n
}

// TestWorkspaceRebindOnlyChangesTail pins the cache-prefix rule for a mid-turn
// workspace change: the interactive parent can move the session from one
// worktree to another between requests, and that change must reach the model
// through the volatile tail only. Every byte the previous request already sent
// ahead of the tail stays identical, and the wire carries exactly one current
// root message.
func TestWorkspaceRebindOnlyChangesTail(t *testing.T) {
	state := newTestState(t)
	projectRoot := state.WorkingDir
	first := t.TempDir()
	second := t.TempDir()
	state.SetWorkspace(session.Workspace{ProjectRoot: projectRoot, ActiveRoot: first, Branch: "feat/first"})

	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.pinPromptAccess()

	history := []schema.ChatMessage{
		r.systemPromptMessage(),
		{Role: schema.RoleUser, Content: "goal"},
		{Role: schema.RoleAssistant, Content: "first step"},
	}
	before := append([]schema.ChatMessage(nil), history...)
	firstRequest := r.withWorkingState(history)

	state.SetWorkspace(session.Workspace{ProjectRoot: projectRoot, ActiveRoot: second, Branch: "feat/second"})
	secondRequest := r.withWorkingState(history)

	if len(firstRequest) != len(secondRequest) {
		t.Fatalf("tail width changed across a rebind: first=%d second=%d", len(firstRequest), len(secondRequest))
	}
	// The prefix and every replayed message ahead of the tail are byte-identical
	// to what the previous request sent, and to the caller's own slice.
	for i := range history {
		if !sameMessage(firstRequest[i], before[i]) || !sameMessage(secondRequest[i], before[i]) {
			t.Fatalf("message %d changed across a workspace rebind:\nbefore: %.300q\nfirst:  %.300q\nsecond: %.300q",
				i, before[i].Content, firstRequest[i].Content, secondRequest[i].Content)
		}
	}
	forcedInput := []schema.ChatMessage{{Role: schema.RoleUser, Content: "goal"}}
	forced := r.withWorkingState(forcedInput)
	for _, req := range []struct {
		wire []schema.ChatMessage
		base int
	}{
		{firstRequest, len(history)},
		{secondRequest, len(history)},
		{forced, len(forcedInput)},
	} {
		if len(req.wire) <= req.base {
			t.Fatalf("no tail appended: wire=%d base=%d", len(req.wire), req.base)
		}
		for _, m := range req.wire[req.base:] {
			if !m.Volatile {
				t.Fatalf("tail message is not volatile: %+v", m)
			}
		}
	}

	// Exactly one current root message per request, naming the new root.
	if n := volatileTailCount(secondRequest, "root="+second); n != 1 {
		t.Fatalf("want exactly one volatile message naming the new root %q, got %d: %+v", second, n, secondRequest)
	}
	if n := volatileTailCount(secondRequest, "root="+first); n != 0 {
		t.Fatalf("rebound request still names the previous root %q in %d tail messages", first, n)
	}
}

// TestWorkspaceTailSurvivesCompactionAndRetry pins that a compacted wire is
// rebuilt around the current binding and that the workspace guidance is a
// per-request tail, never stored history: a retry rebuilds one copy instead of
// stacking a second, and the transcript keeps none.
func TestWorkspaceTailSurvivesCompactionAndRetry(t *testing.T) {
	state := newTestState(t)
	projectRoot := state.WorkingDir
	worktree := t.TempDir()
	state.SetWorkspace(session.Workspace{ProjectRoot: projectRoot, ActiveRoot: worktree, Branch: "feat/compacted"})

	p := &agenttest.ScriptedProvider{Responses: []string{"progress summary"}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")

	old := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "fix the bug"},
	}
	fresh, err := r.summarizeAndContinue(context.Background(), p, "test-model", old, "fix the bug", nil)
	if err != nil {
		t.Fatalf("summarizeAndContinue: %v", err)
	}
	// The rebuilt system prompt names the binding in force now, not the
	// directory the session was launched from.
	if !strings.Contains(fresh[0].Content, "The workspace root is "+worktree) {
		t.Fatalf("compacted system prompt does not name the bound workspace %q:\n%s", worktree, fresh[0].Content)
	}

	retryA := r.withWorkingState(fresh)
	retryB := r.withWorkingState(fresh)
	for i, wire := range [][]schema.ChatMessage{retryA, retryB} {
		if n := volatileTailCount(wire[len(fresh):], "root="+worktree); n != 1 {
			t.Fatalf("retry %d carries %d current-root messages, want exactly 1: %+v", i, n, wire[len(fresh):])
		}
		if n := volatileTailCount(wire, "root="+projectRoot); n != 0 {
			t.Fatalf("retry %d still names the launch root %q", i, projectRoot)
		}
	}
	if len(retryA) != len(retryB) {
		t.Fatalf("a retry changed the tail width: %d vs %d", len(retryA), len(retryB))
	}

	// The tail is wire-only: it never reaches the stored transcript.
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "root="+worktree) {
			t.Fatalf("workspace tail leaked into the session transcript: %q", m.Content)
		}
	}
}
