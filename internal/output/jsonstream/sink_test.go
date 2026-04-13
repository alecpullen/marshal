package jsonstream

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNDJSONSink_EmitsSessionStart(t *testing.T) {
	var buf bytes.Buffer
	_ = NewSink(&buf, "sess-123", "fix the bug")

	// Check that session_start was emitted
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 1 {
		t.Fatal("expected at least one line of output")
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if event["event"] != "session_start" {
		t.Errorf("expected event=session_start, got %v", event["event"])
	}
	if event["session_id"] != "sess-123" {
		t.Errorf("expected session_id=sess-123, got %v", event["session_id"])
	}
	if event["prompt"] != "fix the bug" {
		t.Errorf("expected prompt='fix the bug', got %v", event["prompt"])
	}
}

func TestNDJSONSink_RoundLifecycle(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink(&buf, "sess-123", "fix the bug")

	// Simulate a round
	sink.RoundStart("task-456", 1, 3)
	sink.Token("package main")
	sink.Token("\n\nfunc main() {}")
	sink.VerdictBadge("task-456", "PASS", "looks good")
	sink.RoundEnd("task-456", 1, 100, 50)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}

	// Parse round_start
	var roundStart map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &roundStart); err != nil {
		t.Fatalf("invalid round_start JSON: %v", err)
	}
	if roundStart["event"] != "round_start" {
		t.Errorf("expected event=round_start, got %v", roundStart["event"])
	}
	if roundStart["task_id"] != "task-456" {
		t.Errorf("expected task_id=task-456, got %v", roundStart["task_id"])
	}

	// Parse round_end
	var roundEnd map[string]interface{}
	if err := json.Unmarshal([]byte(lines[2]), &roundEnd); err != nil {
		t.Fatalf("invalid round_end JSON: %v", err)
	}
	if roundEnd["event"] != "round_end" {
		t.Errorf("expected event=round_end, got %v", roundEnd["event"])
	}
	if roundEnd["verdict"] != "PASS" {
		t.Errorf("expected verdict=PASS, got %v", roundEnd["verdict"])
	}
	if roundEnd["prompt_tokens"] != 100.0 { // JSON numbers are float64
		t.Errorf("expected prompt_tokens=100, got %v", roundEnd["prompt_tokens"])
	}
	if roundEnd["completion_tokens"] != 50.0 {
		t.Errorf("expected completion_tokens=50, got %v", roundEnd["completion_tokens"])
	}

	// Response should include accumulated tokens
	response, ok := roundEnd["response"].(string)
	if !ok || !strings.Contains(response, "package main") {
		t.Errorf("expected response to contain 'package main', got %v", roundEnd["response"])
	}
}

func TestNDJSONSink_SessionSuccess(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink(&buf, "sess-123", "fix the bug")

	sink.RoundStart("task-456", 1, 1)
	sink.Token("done")
	sink.VerdictBadge("task-456", "PASS", "completed")
	sink.RoundEnd("task-456", 1, 50, 25)
	sink.TaskMerged("task-456", "abc123")
	sink.SessionSuccess("abc123")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 lines, got %d", len(lines))
	}

	// Parse session_end
	var sessionEnd map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sessionEnd); err != nil {
		t.Fatalf("invalid session_end JSON: %v", err)
	}
	if sessionEnd["event"] != "session_end" {
		t.Errorf("expected event=session_end, got %v", sessionEnd["event"])
	}
	if sessionEnd["verdict"] != "PASS" {
		t.Errorf("expected verdict=PASS, got %v", sessionEnd["verdict"])
	}
	if sessionEnd["prompt_tokens"] != 50.0 {
		t.Errorf("expected prompt_tokens=50, got %v", sessionEnd["prompt_tokens"])
	}
	if sessionEnd["completion_tokens"] != 25.0 {
		t.Errorf("expected completion_tokens=25, got %v", sessionEnd["completion_tokens"])
	}
	if sessionEnd["total_tokens"] != 75.0 {
		t.Errorf("expected total_tokens=75, got %v", sessionEnd["total_tokens"])
	}
}

func TestNDJSONSink_TaskFailed(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink(&buf, "sess-123", "fix the bug")

	sink.RoundStart("task-456", 1, 1)
	sink.VerdictBadge("task-456", "FAIL", "broken")
	sink.RoundEnd("task-456", 1, 50, 25)
	sink.TaskFailed("task-456", "could not fix the issue")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}

	// Parse session_end
	var sessionEnd map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sessionEnd); err != nil {
		t.Fatalf("invalid session_end JSON: %v", err)
	}
	if sessionEnd["event"] != "session_end" {
		t.Errorf("expected event=session_end, got %v", sessionEnd["event"])
	}
	if sessionEnd["verdict"] != "FAIL" {
		t.Errorf("expected verdict=FAIL, got %v", sessionEnd["verdict"])
	}
	if sessionEnd["error"] != "could not fix the issue" {
		t.Errorf("expected error message, got %v", sessionEnd["error"])
	}
}

func TestNDJSONSink_TokenAccumulation(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink(&buf, "sess-123", "fix the bug")

	// Multiple rounds
	sink.RoundStart("task-456", 1, 2)
	sink.Token("first")
	sink.RoundEnd("task-456", 1, 10, 20)

	sink.RoundStart("task-456", 2, 2)
	sink.Token("second")
	sink.RoundEnd("task-456", 2, 30, 40)

	sink.SessionSuccess("")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")

	// Parse final session_end
	var sessionEnd map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sessionEnd); err != nil {
		t.Fatalf("invalid session_end JSON: %v", err)
	}

	// Total tokens should accumulate across rounds
	if sessionEnd["prompt_tokens"] != 40.0 { // 10 + 30
		t.Errorf("expected total prompt_tokens=40, got %v", sessionEnd["prompt_tokens"])
	}
	if sessionEnd["completion_tokens"] != 60.0 { // 20 + 40
		t.Errorf("expected total completion_tokens=60, got %v", sessionEnd["completion_tokens"])
	}
	if sessionEnd["total_tokens"] != 100.0 {
		t.Errorf("expected total_tokens=100, got %v", sessionEnd["total_tokens"])
	}
}

func TestNDJSONSink_MultipleRounds(t *testing.T) {
	var buf bytes.Buffer
	sink := NewSink(&buf, "sess-123", "fix the bug")

	// First round (FAIL)
	sink.RoundStart("task-456", 1, 2)
	sink.Token("attempt 1")
	sink.VerdictBadge("task-456", "FAIL", "needs work")
	sink.RoundEnd("task-456", 1, 10, 20)

	// Second round (PASS)
	sink.RoundStart("task-456", 2, 2)
	sink.Token("attempt 2")
	sink.VerdictBadge("task-456", "PASS", "good now")
	sink.RoundEnd("task-456", 2, 15, 25)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")

	// Find the round_end events
	var roundEnds []map[string]interface{}
	for _, line := range lines {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event["event"] == "round_end" {
			roundEnds = append(roundEnds, event)
		}
	}

	if len(roundEnds) != 2 {
		t.Fatalf("expected 2 round_end events, got %d", len(roundEnds))
	}

	if roundEnds[0]["verdict"] != "FAIL" {
		t.Errorf("expected first round verdict=FAIL, got %v", roundEnds[0]["verdict"])
	}
	if roundEnds[1]["verdict"] != "PASS" {
		t.Errorf("expected second round verdict=PASS, got %v", roundEnds[1]["verdict"])
	}
}
