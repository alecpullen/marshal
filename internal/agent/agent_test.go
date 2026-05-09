package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	agtctx "github.com/alecpullen/marshal/internal/agent/context"
	agttools "github.com/alecpullen/marshal/internal/agent/tools"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// Mock implementations for testing

type mockTool struct {
	name        string
	description string
	schema      json.RawMessage
	isRead      bool
	isMutating  bool
	result      *agttools.Result
	err         error
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return m.description }
func (m *mockTool) Schema() json.RawMessage {
	if m.schema == nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return m.schema
}
func (m *mockTool) IsReadOperation() bool        { return m.isRead }
func (m *mockTool) IsMutating() bool             { return m.isMutating }
func (m *mockTool) RequiresReadBeforeEdit() bool { return m.isMutating }
func (m *mockTool) Invoke(ctx context.Context, args json.RawMessage) (*agttools.Result, error) {
	return m.result, m.err
}

type mockEventPublisher struct {
	events []string
}

func (m *mockEventPublisher) AgentStarted(agentID, role, goal string) {
	m.events = append(m.events, "agent_started")
}
func (m *mockEventPublisher) AgentCompleted(agentID, output string) {
	m.events = append(m.events, "agent_completed")
}
func (m *mockEventPublisher) AgentFailed(agentID, reason string) {
	m.events = append(m.events, "agent_failed")
}
func (m *mockEventPublisher) RoundStart(agentID string, round, maxRounds int) {
	m.events = append(m.events, "round_start")
}
func (m *mockEventPublisher) RoundEnd(agentID string, round int, usage any) {
	m.events = append(m.events, "round_end")
}
func (m *mockEventPublisher) Token(agentID, content string)                 {}
func (m *mockEventPublisher) ThinkBlock(agentID, content string)             {}
func (m *mockEventPublisher) ToolCall(agentID, name string, args json.RawMessage) {}
func (m *mockEventPublisher) ToolResult(agentID, name, content string, isError bool) {}
func (m *mockEventPublisher) SubAgentSpawned(parentID, childID, role string) {}
func (m *mockEventPublisher) SubAgentCompleted(parentID, childID, output string) {}

// Tests

func TestManifest_LoadAndDefaults(t *testing.T) {
	// Create a temporary manifest file
	content := `
role: test-agent
model_binding: test
system_prompt: "Test prompt"
tools:
  - read_file
`
	tmpFile, err := os.CreateTemp("", "manifest-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	manifest, err := LoadManifest(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if manifest.Role != "test-agent" {
		t.Errorf("Expected role 'test-agent', got %s", manifest.Role)
	}

	// Check defaults
	if manifest.MaxIterations != 3 {
		t.Errorf("Expected default MaxIterations 3, got %d", manifest.MaxIterations)
	}

	if manifest.Timeout != 5*time.Minute {
		t.Errorf("Expected default Timeout 5m, got %v", manifest.Timeout)
	}
}

func TestManifest_Validate(t *testing.T) {
	tests := []struct {
		name      string
		manifest  Manifest
		wantError bool
	}{
		{
			name: "valid manifest",
			manifest: Manifest{
				Role:         "test",
				ModelBinding: "test-binding",
				SystemPrompt: "test prompt",
				Tools:        []string{"read_file"},
			},
			wantError: false,
		},
		{
			name: "missing role",
			manifest: Manifest{
				ModelBinding: "test-binding",
				SystemPrompt: "test prompt",
				Tools:        []string{"read_file"},
			},
			wantError: true,
		},
		{
			name: "missing system_prompt",
			manifest: Manifest{
				Role:         "test",
				ModelBinding: "test-binding",
				Tools:        []string{"read_file"},
			},
			wantError: true,
		},
		{
			name: "no tools",
			manifest: Manifest{
				Role:         "test",
				ModelBinding: "test-binding",
				SystemPrompt: "test prompt",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.manifest.Validate()
			if tt.wantError && err == nil {
				t.Error("Expected error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestReadSet_RecordAndVerify(t *testing.T) {
	rs := NewReadSet()

	content := []byte("test content")
	hash := hashBytes(content)

	rs.RecordRead("test.txt", hash, "read_file")

	if !rs.HasRead("test.txt") {
		t.Error("Expected HasRead to return true")
	}

	storedHash, ok := rs.GetHash("test.txt")
	if !ok {
		t.Error("Expected to get hash")
	}
	if storedHash != hash {
		t.Errorf("Expected hash %s, got %s", hash, storedHash)
	}
}

func TestReadSet_VerifyHash(t *testing.T) {
	rs := NewReadSet()

	content := []byte("original content")
	hash := hashBytes(content)

	rs.RecordRead("test.txt", hash, "read_file")

	// Should pass with same content
	err := rs.VerifyHash("test.txt", content)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// Should fail with different content
	newContent := []byte("modified content")
	err = rs.VerifyHash("test.txt", newContent)
	if err == nil {
		t.Error("Expected staleness error, got nil")
	}

	if !IsStalenessError(err) {
		t.Errorf("Expected StalenessError, got %T", err)
	}
}

func TestReadSet_NeverRead(t *testing.T) {
	rs := NewReadSet()

	err := rs.VerifyHash("never-read.txt", []byte("content"))
	if err == nil {
		t.Error("Expected error for unread file")
	}
}

func TestReadSet_ExtractHashFromRef(t *testing.T) {
	ref := protocol.ContextRef("files/test.txt@blake3:abc123def456")
	hash := extractHashFromRef(ref)
	
	if hash != "abc123def456" {
		t.Errorf("Expected hash 'abc123def456', got '%s'", hash)
	}
}

func TestToolError_Classification(t *testing.T) {
	tests := []struct {
		code      string
		critical  bool
		retryable bool
	}{
		{"read_before_edit", false, true},
		{"stale_hash", false, true},
		{"file_not_found", false, true},
		{"permission_denied", true, false},
		{"out_of_disk_space", true, false},
		{"unknown", false, true}, // Unknown errors are retryable by default (not critical, so retryable)
	}

	for _, tt := range tests {
		err := &agttools.ToolError{
			Code:    tt.code,
			Message: "test",
		}

		if err.IsCriticalError() != tt.critical {
			t.Errorf("Code %s: expected critical=%v, got %v", tt.code, tt.critical, err.IsCriticalError())
		}

		if err.IsRetryableError() != tt.retryable {
			t.Errorf("Code %s: expected retryable=%v, got %v", tt.code, tt.retryable, err.IsRetryableError())
		}
	}
}

func TestAgent_CanSpawnSubAgents(t *testing.T) {
	manifest := &Manifest{
		Role:              "test",
		CanSpawnAgents:    true,
		MaxConcurrentSubs: 2,
		AllowedSubRoles:   []string{"helper"},
	}

	agent := &Agent{
		Manifest:  manifest,
		SubAgents: make(map[string]*Agent),
	}

	if !agent.CanSpawnSubAgents() {
		t.Error("Expected CanSpawnSubAgents to be true initially")
	}

	// Simulate spawning
	agent.SubAgents["sub1"] = &Agent{ID: "sub1"}
	agent.SubAgents["sub2"] = &Agent{ID: "sub2"}

	if agent.CanSpawnSubAgents() {
		t.Error("Expected CanSpawnSubAgents to be false at max capacity")
	}
}

func TestAgent_CanSpawnRole(t *testing.T) {
	manifest := &Manifest{
		Role:              "test",
		CanSpawnAgents:    true,
		AllowedSubRoles:   []string{"helper", "reviewer"},
		MaxConcurrentSubs: 3,
	}

	if !manifest.CanSpawnRole("helper") {
		t.Error("Expected CanSpawnRole('helper') to be true")
	}

	if !manifest.CanSpawnRole("reviewer") {
		t.Error("Expected CanSpawnRole('reviewer') to be true")
	}

	if manifest.CanSpawnRole("unauthorized") {
		t.Error("Expected CanSpawnRole('unauthorized') to be false")
	}
}

func TestResult_ValidateOutput(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"summary": {"type": "string"}
		},
		"required": ["summary"]
	}`)

	tests := []struct {
		name      string
		output    string
		wantError bool
	}{
		{
			name:      "valid JSON",
			output:    `{"summary": "Task completed"}`,
			wantError: false,
		},
		{
			name:      "valid JSON in markdown",
			output:    "```json\n{\"summary\": \"Done\"}\n```",
			wantError: false,
		},
		{
			name:      "missing required field",
			output:    `{"other": "value"}`,
			wantError: true,
		},
		{
			name:      "invalid JSON",
			output:    "not json",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{
				Status: ResultStatusSuccess,
				Output: tt.output,
			}

			err := result.ValidateOutput(schema)
			if tt.wantError && err == nil {
				t.Error("Expected error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestContextEntry_TotalTokens(t *testing.T) {
	entries := []agtctx.ContextEntry{
		{Tokens: 100},
		{Tokens: 200},
		{Tokens: 300},
	}

	total := agtctx.TotalTokens(entries)
	if total != 600 {
		t.Errorf("Expected total 600, got %d", total)
	}
}
