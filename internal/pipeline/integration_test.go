package pipeline_test

import (
	"context"
	"testing"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/pipeline"
)

func TestIntegrationCritic_Review_EmptyDiff(t *testing.T) {
	// For empty diff, Review should return PASS without calling backend
	mock := &mockBackend{}
	critic := pipeline.NewIntegrationCritic(mock)

	verdict, err := critic.Review(context.Background(), "", []string{})
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}
	if verdict.Verdict != "PASS" {
		t.Errorf("expected PASS for empty diff, got %s", verdict.Verdict)
	}
}

// mockBackend satisfies the backend.Backend interface for testing
type mockBackend struct{}

func (m *mockBackend) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	return backend.Response{Content: `{"verdict":"PASS","summary":"ok"}`}, nil
}
func (m *mockBackend) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) {
	ch := make(chan backend.Chunk, 1)
	ch <- backend.Chunk{Content: "ok"}
	close(ch)
	return ch, nil
}
func (m *mockBackend) TokenCount(messages []backend.Message) (int, error) { return 0, nil }
func (m *mockBackend) SupportsTools() bool                             { return false }
func (m *mockBackend) SupportsJSONMode() bool                            { return true }
func (m *mockBackend) Model() string                                      { return "test-model" }
