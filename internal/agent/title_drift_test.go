package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
)

func TestParseDriftReply(t *testing.T) {
	tests := []struct {
		name      string
		reply     string
		wantTitle string
		wantDrift bool
	}{
		{name: "continues", reply: "CONTINUES", wantTitle: "", wantDrift: false},
		{name: "new task", reply: "NEW_TASK: Fix auth bug", wantTitle: "Fix auth bug", wantDrift: true},
		{name: "continues with whitespace", reply: "  CONTINUES  ", wantTitle: "", wantDrift: false},
		{name: "empty candidate", reply: "NEW_TASK:", wantTitle: "", wantDrift: false},
		{name: "case sensitive", reply: "new_task: x", wantTitle: "", wantDrift: false},
		{name: "garbage", reply: "garbage", wantTitle: "", wantDrift: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, drifted := parseDriftReply(tt.reply)
			if drifted != tt.wantDrift {
				t.Fatalf("drifted = %v, want %v", drifted, tt.wantDrift)
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

func TestTitleManagerDriftSilentUpdate(t *testing.T) {
	p := &recordingProvider{responses: []string{"NEW_TASK: New task"}}
	state := newTestState(t)
	state.SetTitle("old")
	before := len(state.Messages())
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "unrelated request")

	if got := state.Title(); got != "New task" {
		t.Fatalf("title = %q, want %q", got, "New task")
	}
	if after := len(state.Messages()); after != before {
		t.Fatalf("transcript grew from %d to %d messages on silent update", before, after)
	}
}

func TestTitleManagerDriftProposesOnManual(t *testing.T) {
	p := &recordingProvider{responses: []string{"NEW_TASK: Better"}}
	state := newTestState(t)
	state.SetTitleManual("mine")
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "unrelated request")

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

func TestTitleManagerContinuesKeepsTitle(t *testing.T) {
	p := &recordingProvider{responses: []string{"CONTINUES"}}
	state := newTestState(t)
	state.SetTitle("keep me")
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "more of the same")

	if got := state.Title(); got != "keep me" {
		t.Fatalf("title = %q, want %q", got, "keep me")
	}
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestTitleManagerFailureKeepsTitle(t *testing.T) {
	p := &failProvider{}
	state := newTestState(t)
	state.SetTitle("keep me")
	before := len(state.Messages())
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "unrelated request")

	if got := state.Title(); got != "keep me" {
		t.Fatalf("title = %q, want %q", got, "keep me")
	}
	if after := len(state.Messages()); after != before {
		t.Fatalf("transcript grew from %d to %d messages on failure", before, after)
	}
}

// Ensure the drift request shape reaches the provider unchanged.
func TestTitleManagerDriftRequestCarriesCurrentTitle(t *testing.T) {
	var got schema.ChatRequest
	p := &requestCaptureProvider{recordingProvider: recordingProvider{responses: []string{"CONTINUES"}}, capture: &got}
	state := newTestState(t)
	state.SetTitle("current title")
	tm := NewTitleManager(p, "tiny", state, time.Second)

	tm.OnUserTurn(context.Background(), "new message")

	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	if !strings.Contains(got.Messages[0].Content, "current title") || !strings.Contains(got.Messages[0].Content, "new message") {
		t.Fatalf("drift user message missing title or goal: %q", got.Messages[0].Content)
	}
	if got.Messages[1].Content != driftDirectiveText {
		t.Fatalf("drift system message = %q, want directive", got.Messages[1].Content)
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
