package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// THE safety test. Narration is stored non-final precisely so it never
// re-enters the model's context — the identical text is already in the
// messages slice. If it were replayed, it would be duplicated into every
// request for the rest of the turn.
func TestNarrationIsNotReplayedIntoHistory(t *testing.T) {
	s := newTestState(t)
	s.AddMessage(session.RoleUser, "do the thing", session.ContentTypePlain)
	s.AddMessage(session.RoleAssistant, "Checking the guard first.", session.ContentTypeNarration)
	s.AddMessageFinalWithUsage(session.RoleAssistant, "Done.", session.ContentTypeMarkdown, 1, "")

	msgs := buildHistoryMessages(s.Messages(), 8000, s.Generation(), map[int64][]db.ToolAuditEntry{})
	for _, m := range msgs {
		if strings.Contains(m.Content, "Checking the guard first.") {
			t.Fatalf("narration must never be replayed into model context; found in %+v", m)
		}
	}
}

// The final answer must still be replayed — the exclusion is narrow.
func TestFinalAnswerStillReplayed(t *testing.T) {
	s := newTestState(t)
	s.AddMessage(session.RoleUser, "do the thing", session.ContentTypePlain)
	s.AddMessageFinalWithUsage(session.RoleAssistant, "Done.", session.ContentTypeMarkdown, 0, "")

	msgs := buildHistoryMessages(s.Messages(), 8000, s.Generation(), map[int64][]db.ToolAuditEntry{})
	var found bool
	for _, m := range msgs {
		if m.Role == schema.RoleAssistant && strings.Contains(m.Content, "Done.") {
			found = true
		}
	}
	if !found {
		t.Fatal("the final answer must still replay into history")
	}
}

func TestNarrationRecordedForTextWithToolCalls(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"Checking the guard first.", "all done"},
		ToolCalls:     [][]schema.ToolCall{{{ID: "tc1", Name: "noop.tool", Args: json.RawMessage(`{}`)}}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "noop.tool", Description: "does nothing", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var narrationCount int
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeNarration {
			narrationCount++
			if m.Content != "Checking the guard first." {
				t.Fatalf("narration content = %q, want %q", m.Content, "Checking the guard first.")
			}
		}
	}
	if narrationCount != 1 {
		t.Fatalf("narration message count = %d, want 1", narrationCount)
	}
}

// Narration must not swallow the thinking/reasoning summary. AddMessage
// clears inProgress, so LogThinking must run before the narration AddMessage.
func TestNarrationDoesNotSwallowThinking(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Thinking:      []string{"I should check the guard first."},
		Responses:     []string{"Checking the guard first.", "all done"},
		ToolCalls:     [][]schema.ToolCall{{{ID: "tc1", Name: "noop.tool", Args: json.RawMessage(`{}`)}}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "noop.tool", Description: "does nothing", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var thinkText string
	for _, item := range state.Transcript() {
		if item.Kind == session.KindThinking && item.Thinking != nil {
			thinkText = item.Thinking.Text
			break
		}
	}
	if thinkText == "" {
		t.Fatal("thinking entry was swallowed by narration AddMessage")
	}
	if thinkText != "I should check the guard first." {
		t.Fatalf("thinking text = %q, want %q", thinkText, "I should check the guard first.")
	}
}

// Empty text with tool calls must not produce a narration message.
func TestNarrationNotRecordedForEmptyTextWithToolCalls(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "all done"},
		ToolCalls:     [][]schema.ToolCall{{{ID: "tc1", Name: "noop.tool", Args: json.RawMessage(`{}`)}}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "noop.tool", Description: "does nothing", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeNarration {
			t.Fatalf("empty text must not produce narration, found %q", m.Content)
		}
	}
}
