package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/pipeline"
)

// Test integration critic with FAIL verdict
func TestIntegrationCritic_Review_Fail(t *testing.T) {
	mock := &mockBackendFail{}
	critic := pipeline.NewIntegrationCritic(mock)

	verdict, err := critic.Review(context.Background(), "some diff", []string{"task-1"})
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}
	if verdict.Verdict != "FAIL" {
		t.Errorf("expected FAIL, got %s", verdict.Verdict)
	}
	if verdict.Summary != "interface mismatch" {
		t.Errorf("unexpected summary: %s", verdict.Summary)
	}
	if len(verdict.Implicated) != 1 || verdict.Implicated[0] != "task-1" {
		t.Errorf("unexpected implicated tasks: %v", verdict.Implicated)
	}
}

// Test integration critic with backend error
func TestIntegrationCritic_Review_BackendError(t *testing.T) {
	mock := &mockBackendError{}
	critic := pipeline.NewIntegrationCritic(mock)

	_, err := critic.Review(context.Background(), "some diff", []string{"task-1"})
	if err == nil {
		t.Error("expected error from backend failure")
	}
}

// Test integration critic with malformed JSON (should fallback to PASS)
func TestIntegrationCritic_Review_MalformedJSON(t *testing.T) {
	mock := &mockBackendMalformed{}
	critic := pipeline.NewIntegrationCritic(mock)

	verdict, err := critic.Review(context.Background(), "some diff", []string{"task-1"})
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}
	if verdict.Verdict != "PASS" {
		t.Errorf("expected PASS for malformed JSON, got %s", verdict.Verdict)
	}
	if verdict.Issue == "" {
		t.Error("expected issue field to contain parse error")
	}
}

// Test integration critic strips thinking blocks
func TestIntegrationCritic_Review_ThinkingBlocks(t *testing.T) {
	mock := &mockBackendThinking{}
	critic := pipeline.NewIntegrationCritic(mock)

	verdict, err := critic.Review(context.Background(), "some diff", []string{"task-1"})
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}
	if verdict.Verdict != "PASS" {
		t.Errorf("expected PASS, got %s", verdict.Verdict)
	}
}

// Mock backends for different scenarios

type mockBackendFail struct{}

func (m *mockBackendFail) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	v := pipeline.IntegrationVerdict{
		Verdict:    "FAIL",
		Summary:    "interface mismatch",
		Issue:      "missing parameter",
		Fix:        "add parameter",
		Implicated: []string{"task-1"},
	}
	content, _ := json.Marshal(v)
	return backend.Response{Content: string(content)}, nil
}
func (m *mockBackendFail) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) { return nil, nil }
func (m *mockBackendFail) TokenCount(messages []backend.Message) (int, error) { return 0, nil }
func (m *mockBackendFail) SupportsTools() bool { return false }
func (m *mockBackendFail) SupportsJSONMode() bool { return true }
func (m *mockBackendFail) Model() string { return "test-fail" }

type mockBackendError struct{}

func (m *mockBackendError) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	return backend.Response{}, errors.New("backend failure")
}
func (m *mockBackendError) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) { return nil, nil }
func (m *mockBackendError) TokenCount(messages []backend.Message) (int, error) { return 0, nil }
func (m *mockBackendError) SupportsTools() bool { return false }
func (m *mockBackendError) SupportsJSONMode() bool { return true }
func (m *mockBackendError) Model() string { return "test-error" }

type mockBackendMalformed struct{}

func (m *mockBackendMalformed) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	return backend.Response{Content: "not valid json"}, nil
}
func (m *mockBackendMalformed) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) { return nil, nil }
func (m *mockBackendMalformed) TokenCount(messages []backend.Message) (int, error) { return 0, nil }
func (m *mockBackendMalformed) SupportsTools() bool { return false }
func (m *mockBackendMalformed) SupportsJSONMode() bool { return true }
func (m *mockBackendMalformed) Model() string { return "test-malformed" }

type mockBackendThinking struct{}

func (m *mockBackendThinking) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	content := `<thinking>
Let me analyze this diff carefully.
The changes look compatible.
</thinking>
{"verdict":"PASS","summary":"all good"}`
	return backend.Response{Content: content}, nil
}
func (m *mockBackendThinking) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) { return nil, nil }
func (m *mockBackendThinking) TokenCount(messages []backend.Message) (int, error) { return 0, nil }
func (m *mockBackendThinking) SupportsTools() bool { return false }
func (m *mockBackendThinking) SupportsJSONMode() bool { return true }
func (m *mockBackendThinking) Model() string { return "test-thinking" }
