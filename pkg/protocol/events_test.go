package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventKind_IsValid(t *testing.T) {
	tests := []struct {
		kind  EventKind
		valid bool
	}{
		{EventTaskSpawned, true},
		{EventTaskProgress, true},
		{EventToolCall, true},
		{EventToolResult, true},
		{EventModelCall, true},
		{EventTaskCompleted, true},
		{EventTaskFailed, true},
		{EventUserInterrupt, true},
		{EventGraphMutation, true},
		{EventKnowledgeQuery, true},
		{EventKBMutation, true},
		{EventEditProposed, true},
		{EventEditApplied, true},
		{EventEditRejected, true},
		{EventProfileChanged, true},
		{EventCostUpdate, true},
		{EventKind("unknown"), false},
		{EventKind(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			got := tt.kind.IsValid()
			if got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestNewEvent(t *testing.T) {
	sessionID := "sess_123"
	event := NewEvent(EventTaskSpawned, sessionID)

	if event.Kind != EventTaskSpawned {
		t.Errorf("Kind = %v, want %v", event.Kind, EventTaskSpawned)
	}
	if event.SessionID != sessionID {
		t.Errorf("SessionID = %v, want %v", event.SessionID, sessionID)
	}
	if event.ID == "" {
		t.Error("ID should not be empty")
	}
	if event.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestSwarmEvent_WithPayload(t *testing.T) {
	event := NewEvent(EventTaskSpawned, "sess_123")
	payload := TaskSpawnedPayload{
		TaskID: "task_456",
		Role:   "codegen",
		Goal:   "Add feature",
	}

	event, err := event.WithPayload(payload)
	if err != nil {
		t.Fatalf("WithPayload() error = %v", err)
	}

	if len(event.Payload) == 0 {
		t.Error("Payload should not be empty after WithPayload")
	}

	// Verify we can unmarshal it back
	var got TaskSpawnedPayload
	if err := event.UnmarshalPayload(&got); err != nil {
		t.Fatalf("UnmarshalPayload() error = %v", err)
	}

	if got.TaskID != payload.TaskID {
		t.Errorf("TaskID = %v, want %v", got.TaskID, payload.TaskID)
	}
	if got.Role != payload.Role {
		t.Errorf("Role = %v, want %v", got.Role, payload.Role)
	}
}

func TestSwarmEvent_JSONRoundTrip(t *testing.T) {
	event := NewEvent(EventTaskProgress, "sess_123")
	event.AgentID = "agent_789"
	event.TaskID = "task_456"
	event.ParentID = "parent_000"

	payload := TaskProgressPayload{
		TaskID:   "task_456",
		Status:   "running",
		Message:  "Processing",
		Progress: 0.5,
	}
	event, _ = event.WithPayload(payload)

	// Marshal to JSON
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	// Unmarshal back
	var got SwarmEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got.Kind != event.Kind {
		t.Errorf("Kind = %v, want %v", got.Kind, event.Kind)
	}
	if got.SessionID != event.SessionID {
		t.Errorf("SessionID = %v, want %v", got.SessionID, event.SessionID)
	}
	if got.AgentID != event.AgentID {
		t.Errorf("AgentID = %v, want %v", got.AgentID, event.AgentID)
	}
	if got.TaskID != event.TaskID {
		t.Errorf("TaskID = %v, want %v", got.TaskID, event.TaskID)
	}

	// Verify payload survived round trip
	var gotPayload TaskProgressPayload
	if err := got.UnmarshalPayload(&gotPayload); err != nil {
		t.Fatalf("UnmarshalPayload() error = %v", err)
	}
	if gotPayload.Status != payload.Status {
		t.Errorf("Payload.Status = %v, want %v", gotPayload.Status, payload.Status)
	}
	if gotPayload.Progress != payload.Progress {
		t.Errorf("Payload.Progress = %v, want %v", gotPayload.Progress, payload.Progress)
	}
}

func TestAllEventPayloads_MarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		kind    EventKind
		payload interface{}
	}{
		{
			name: "TaskSpawned",
			kind: EventTaskSpawned,
			payload: TaskSpawnedPayload{
				TaskID: "t1",
				Role:   "codegen",
				Goal:   "Add feature",
				ParentIDs: []string{"p1", "p2"},
			},
		},
		{
			name: "TaskProgress",
			kind: EventTaskProgress,
			payload: TaskProgressPayload{
				TaskID:   "t1",
				Status:   "running",
				Message:  "Working...",
				Progress: 0.75,
			},
		},
		{
			name: "TaskCompleted",
			kind: EventTaskCompleted,
			payload: TaskCompletedPayload{
				TaskID:   "t1",
				Duration: 5000,
				Cost: CostInfo{
					InputTokens:  100,
					OutputTokens: 50,
					TotalTokens:  150,
				},
			},
		},
		{
			name: "TaskFailed",
			kind: EventTaskFailed,
			payload: TaskFailedPayload{
				TaskID:    "t1",
				Error:     "Something went wrong",
				ErrorCode: "ERR_001",
				Retryable: true,
			},
		},
		{
			name: "ToolCall",
			kind: EventToolCall,
			payload: ToolCallPayload{
				CallID:    "call_1",
				ToolName:  "read_file",
				Arguments: json.RawMessage(`{"path": "test.go"}`),
			},
		},
		{
			name: "ToolResult",
			kind: EventToolResult,
			payload: ToolResultPayload{
				CallID: "call_1",
				Status: "success",
				Result: json.RawMessage(`{"content": "file contents"}`),
				NewRefs: []ContextRef{
					"files/test.go@sha256:abc123",
				},
			},
		},
		{
			name: "ModelCall",
			kind: EventModelCall,
			payload: ModelCallPayload{
				CallID:       "call_1",
				Model:        "gpt-4",
				Provider:     "openai",
				MessageCount: 5,
			},
		},
		{
			name: "ModelChunk",
			kind: EventModelChunk,
			payload: ModelChunkPayload{
				CallID:  "call_1",
				Content: "partial output",
				Done:    false,
			},
		},
		{
			name: "UserInterrupt",
			kind: EventUserInterrupt,
			payload: UserInterruptPayload{
				Reason: "cancel",
				Input:  "stop",
			},
		},
		{
			name: "GraphMutation",
			kind: EventGraphMutation,
			payload: GraphMutationPayload{
				GraphVersion: 1,
				MutationType: "add",
				TasksAdded:   []string{"t1"},
				EdgesChanged: []EdgeChange{
					{From: "root", To: "t1", Action: "add"},
				},
			},
		},
		{
			name: "KnowledgeQuery",
			kind: EventKnowledgeQuery,
			payload: KnowledgeQueryPayload{
				Query:      "What is the auth pattern?",
				Mode:       "llm",
				Answer:     "Use JWT tokens",
				Confidence: "high",
			},
		},
		{
			name: "KBMutation",
			kind: EventKBMutation,
			payload: KBMutationPayload{
				EntryType:   "symbol",
				Action:      "add",
				Key:         "pkg.Foo",
				ContentHash: "sha256:abc",
			},
		},
		{
			name: "EditProposed",
			kind: EventEditProposed,
			payload: EditProposedPayload{
				ProposalID:   "prop_1",
				Path:         "test.go",
				ExpectedHash: "sha256:old",
				Operations:   []string{"replace"},
				Rationale:    "Fix bug",
			},
		},
		{
			name: "EditApplied",
			kind: EventEditApplied,
			payload: EditAppliedPayload{
				ProposalID: "prop_1",
				NewHash:    "sha256:new",
			},
		},
		{
			name: "EditRejected",
			kind: EventEditRejected,
			payload: EditRejectedPayload{
				ProposalID: "prop_1",
				Reason:     "Stale hash",
				Hint:       "Re-read the file",
			},
		},
		{
			name: "ProfileChanged",
			kind: EventProfileChanged,
			payload: ProfileChangedPayload{
				OldProfile: "local-only",
				NewProfile: "balanced",
				BindingDelta: map[string]string{
					"codegen": "anthropic/claude",
				},
			},
		},
		{
			name: "CostUpdate",
			kind: EventCostUpdate,
			payload: CostUpdatePayload{
				SessionCost: 0.05,
				DailyBudget: 5.0,
				DailySpent:  0.5,
				KBCost:      0.01,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := NewEvent(tt.kind, "sess_test")
			event, err := event.WithPayload(tt.payload)
			if err != nil {
				t.Fatalf("WithPayload() error = %v", err)
			}

			// Marshal to JSON
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			// Unmarshal back
			var got SwarmEvent
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}

			if got.Kind != tt.kind {
				t.Errorf("Kind = %v, want %v", got.Kind, tt.kind)
			}
			if got.SessionID != "sess_test" {
				t.Errorf("SessionID = %v, want sess_test", got.SessionID)
			}
		})
	}
}

func TestEventIDs_AreUnique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		event := NewEvent(EventTaskSpawned, "sess_123")
		if ids[event.ID] {
			t.Errorf("Duplicate event ID: %s", event.ID)
		}
		ids[event.ID] = true
	}
}

func TestEvent_TimestampIsUTC(t *testing.T) {
	event := NewEvent(EventTaskSpawned, "sess_123")
	if event.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp location = %v, want UTC", event.Timestamp.Location())
	}
}
