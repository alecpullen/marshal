package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestLastCompleteLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"no newline", ""},
		{"one line\n", "one line"},
		{"one line\npartial", "one line"},
		{"first\nsecond\npartial", "second"},
		{"first\n\nsecond\n", "second"},
		{"first\n   \npartial", "first"},
		{"  padded  \n", "padded"},
	}
	for _, tc := range cases {
		if got := lastCompleteLine(tc.in); got != tc.want {
			t.Errorf("lastCompleteLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestClassifyContextOverflow pins the classifier that turns a provider's
// "prompt too large" rejection into a recoverable error. Matching requires
// BOTH a 400/413 status and a known wording, so an unrelated 400 (a bad API
// key, a malformed body) is never misread as an overflow and never triggers
// a pointless compaction.
func TestClassifyContextOverflow(t *testing.T) {
	for _, status := range []int{400, 413} {
		for _, pattern := range contextOverflowPatterns {
			body := `[{"error":{"message":"` + pattern + `: 90000 tokens > 65536"}}]`
			err := &provider.ProviderError{Provider: "test", StatusCode: status, Body: body}
			got := classifyContextOverflow(err)
			if !errors.Is(got, errContextOverflow) {
				t.Errorf("status %d body %q: errors.Is(errContextOverflow) = false, want true (got %v)", status, pattern, got)
			}
			// The original provider error must stay in the chain, or
			// isRetryableChatError stops seeing the 4xx and the retry ladder
			// resends an identical oversized prompt.
			var pe *provider.ProviderError
			if !errors.As(got, &pe) || pe.StatusCode != status {
				t.Errorf("status %d body %q: provider error dropped from the chain (got %v)", status, pattern, got)
			}
			if isRetryableChatError(got) {
				t.Errorf("status %d body %q: classified overflow must stay non-retryable", status, pattern)
			}
		}
	}

	notOverflow := []struct {
		name string
		err  error
	}{
		{"same body at 500", &provider.ProviderError{Provider: "test", StatusCode: 500, Body: "context_length_exceeded"}},
		{"unrelated 400", &provider.ProviderError{Provider: "test", StatusCode: 400, Body: "invalid api key"}},
		{"empty body", &provider.ProviderError{Provider: "test", StatusCode: 400, Body: ""}},
		{"non-provider error", errors.New("context_length_exceeded")},
		{"nil", nil},
	}
	for _, tc := range notOverflow {
		if got := classifyContextOverflow(tc.err); errors.Is(got, errContextOverflow) {
			t.Errorf("%s: classified as overflow, want unchanged (got %v)", tc.name, got)
		}
	}
}

// TestChatWithRetryDoesNotRetryOverflow is the guard that recovery belongs to
// the runner, not the retry ladder. The generic ladder must stop after one
// attempt: resending the same too-large prompt burns the retry budget on a
// request that cannot succeed, and the sentinel must survive whatever
// wrapping the ladder adds.
func TestChatWithRetryDoesNotRetryOverflow(t *testing.T) {
	overflowErr := classifyContextOverflow(&provider.ProviderError{
		Provider:   "test",
		StatusCode: 400,
		Body:       `{"error":{"message":"prompt is too long: 90000 tokens > 65536"}}`,
	})
	if !errors.Is(overflowErr, errContextOverflow) {
		t.Fatalf("fixture: error was not classified (got %v)", overflowErr)
	}

	p := &agenttest.ScriptedProvider{Errs: []error{overflowErr, overflowErr, overflowErr}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.MaxRetries = 2

	_, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if p.Calls != 1 {
		t.Fatalf("expected 1 call for an overflow rejection, got %d", p.Calls)
	}
	if !errors.Is(err, errContextOverflow) {
		t.Fatalf("errContextOverflow did not survive the retry ladder: %v", err)
	}
	if !strings.Contains(err.Error(), "prompt is too long") {
		t.Fatalf("classified error lost the provider detail: %v", err)
	}
}
