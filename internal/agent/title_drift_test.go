package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
)

func TestParseNewTaskMessage(t *testing.T) {
	tests := []struct {
		name      string
		goal      string
		wantTitle string
		wantNew   bool
	}{
		{name: "exact prefix", goal: "new task: fix the auth bug", wantTitle: "fix the auth bug", wantNew: true},
		{name: "case insensitive", goal: "New Task: fix the auth bug", wantTitle: "fix the auth bug", wantNew: true},
		{name: "no colon", goal: "new task fix the auth bug", wantTitle: "fix the auth bug", wantNew: true},
		{name: "leading whitespace", goal: "  new task: fix the auth bug", wantTitle: "fix the auth bug", wantNew: true},
		{name: "empty candidate", goal: "new task:", wantTitle: "", wantNew: false},
		{name: "whitespace candidate", goal: "new task:   ", wantTitle: "", wantNew: false},
		{name: "mid-message", goal: "start a new task after this", wantTitle: "", wantNew: false},
		{name: "not a prefix", goal: "a new task would help", wantTitle: "", wantNew: false},
		{name: "ordinary message", goal: "where is the parser?", wantTitle: "", wantNew: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, isNew := parseNewTaskMessage(tt.goal)
			if isNew != tt.wantNew {
				t.Fatalf("isNew = %v, want %v", isNew, tt.wantNew)
			}
			if title != tt.wantTitle {
				t.Fatalf("title = %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

func TestTitleManagerInitialTitle(t *testing.T) {
	p := &recordingProvider{responses: []string{"Fix parser bug"}}
	state := newTestState(t)
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "where is the parser?")

	if got := state.Title(); got != "Fix parser bug" {
		t.Fatalf("title = %q, want %q", got, "Fix parser bug")
	}
}

// The lag fix: an ordinary turn on a titled session must make zero provider
// calls. The old design ran a drift-classification LLM round-trip on every
// message, which delayed every turn.
func TestTitleManagerOrdinaryTurnMakesNoProviderCall(t *testing.T) {
	p := &recordingProvider{responses: []string{"irrelevant"}}
	state := newTestState(t)
	state.SetTitle("keep me")
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "more of the same")

	if got := state.Title(); got != "keep me" {
		t.Fatalf("title = %q, want %q", got, "keep me")
	}
	if got := p.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 (ordinary turns must not call the title model)", got)
	}
}

func TestTitleManagerNewTaskRegenerates(t *testing.T) {
	p := &recordingProvider{responses: []string{"Fix auth bug"}}
	state := newTestState(t)
	state.SetTitle("old")
	before := len(state.Messages())
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "new task: fix the auth flow")

	if got := state.Title(); got != "Fix auth bug" {
		t.Fatalf("title = %q, want %q", got, "Fix auth bug")
	}
	if after := len(state.Messages()); after != before {
		t.Fatalf("transcript grew from %d to %d messages on silent update", before, after)
	}
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestTitleManagerNewTaskProposesOnManual(t *testing.T) {
	p := &recordingProvider{responses: []string{"Better"}}
	state := newTestState(t)
	state.SetTitleManual("mine")
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "new task: fix the auth flow")

	if got := state.Title(); got != "mine" {
		t.Fatalf("manual title overwritten: %q", got)
	}
	found := false
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "Suggested title") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("transcript did not gain a system message suggesting a title")
	}
}

// The re-titling request must follow the same {system, user} convention as
// the initial-title request and the task classifier.
func TestTitleManagerNewTaskRequestOrdering(t *testing.T) {
	var got schema.ChatRequest
	p := &requestCaptureProvider{recordingProvider: recordingProvider{responses: []string{"Fix auth bug"}}, capture: &got}
	state := newTestState(t)
	state.SetTitle("old")
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "new task: fix the auth flow")

	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != schema.RoleSystem || got.Messages[0].Content != titleDirectiveText {
		t.Fatalf("messages[0] = role %v content %q, want {system, titleDirectiveText}", got.Messages[0].Role, got.Messages[0].Content)
	}
	if got.Messages[1].Role != schema.RoleUser || !strings.Contains(got.Messages[1].Content, "fix the auth flow") {
		t.Fatalf("messages[1] = role %v content %q, want {user, new task body}", got.Messages[1].Role, got.Messages[1].Content)
	}
}

// A failed regeneration keeps the old title and adds no transcript noise.
func TestTitleManagerNewTaskFailureKeepsTitle(t *testing.T) {
	p := &failProvider{}
	state := newTestState(t)
	state.SetTitle("keep me")
	before := len(state.Messages())
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "new task: fix the auth flow")

	if got := state.Title(); got != "keep me" {
		t.Fatalf("title = %q, want %q", got, "keep me")
	}
	if after := len(state.Messages()); after != before {
		t.Fatalf("transcript grew from %d to %d messages on failure", before, after)
	}
}

// Ensure the initial-title request shape reaches the provider unchanged.
func TestTitleManagerInitialRequestOrdering(t *testing.T) {
	var got schema.ChatRequest
	p := &requestCaptureProvider{recordingProvider: recordingProvider{responses: []string{"Fix parser bug"}}, capture: &got}
	state := newTestState(t)
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "where is the parser?")

	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != schema.RoleSystem || got.Messages[0].Content != titleDirectiveText {
		t.Fatalf("messages[0] = role %v content %q, want {system, titleDirectiveText}", got.Messages[0].Role, got.Messages[0].Content)
	}
	if got.Messages[1].Role != schema.RoleUser || got.Messages[1].Content != "where is the parser?" {
		t.Fatalf("messages[1] = role %v content %q, want {user, first message}", got.Messages[1].Role, got.Messages[1].Content)
	}
}

type requestCaptureProvider struct {
	recordingProvider
	capture *schema.ChatRequest
}

func (p *requestCaptureProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	*p.capture = req
	return p.recordingProvider.Chat(ctx, req)
}
