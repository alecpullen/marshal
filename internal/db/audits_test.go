package db

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func TestSaveAndGetToolCalls(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "session-audit"
	if err := db.CreateSession(sessionID, projectID, "audit test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	args, err := json.Marshal(map[string]string{"command": "go test"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	exitCode := 0
	event := registry.AuditEvent{
		Timestamp:       time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		AgentRole:       "implementer",
		Model:           "test-model",
		ToolName:        "shell.exec",
		Args:            args,
		Risk:            registry.RiskCommand,
		Approval:        registry.ApprovalApproved,
		ResultSummary:   "tests passed",
		FilesChanged:    []string{"foo.go"},
		CommandExitCode: &exitCode,
		Error:           "",
	}

	if err := db.SaveToolCall(sessionID, event); err != nil {
		t.Fatalf("SaveToolCall failed: %v", err)
	}

	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	got := calls[0]
	if got.ToolName != "shell.exec" || got.Model != "test-model" || got.AgentRole != "implementer" {
		t.Errorf("tool call metadata mismatch: %+v", got)
	}
	if got.Approval != registry.ApprovalApproved {
		t.Errorf("expected approval approved, got %s", got.Approval)
	}
	if string(got.Args) != string(args) {
		t.Errorf("args mismatch: %s vs %s", string(got.Args), string(args))
	}
	if got.CommandExitCode == nil || *got.CommandExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", got.CommandExitCode)
	}
	if got.Error != event.Error {
		t.Errorf("expected error %q, got %q", event.Error, got.Error)
	}
	if len(got.FilesChanged) != 1 || got.FilesChanged[0] != "foo.go" {
		t.Errorf("expected files changed [foo.go], got %v", got.FilesChanged)
	}

	t.Run("with error", func(t *testing.T) {
		errArgs, err := json.Marshal(map[string]string{"command": "go fail"})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		errEvent := registry.AuditEvent{
			Timestamp:     time.Date(2026, 7, 2, 12, 1, 0, 0, time.UTC),
			AgentRole:     "implementer",
			Model:         "test-model",
			ToolName:      "shell.exec",
			Args:          errArgs,
			Risk:          registry.RiskCommand,
			Approval:      registry.ApprovalApproved,
			ResultSummary: "command failed",
			FilesChanged:  []string{},
			Error:         "exit status 1",
		}

		if err := db.SaveToolCall(sessionID, errEvent); err != nil {
			t.Fatalf("SaveToolCall failed: %v", err)
		}

		calls, err := db.GetToolCalls(sessionID)
		if err != nil {
			t.Fatalf("GetToolCalls failed: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("expected 2 tool calls, got %d", len(calls))
		}

		got := calls[1]
		if got.ToolName != "shell.exec" {
			t.Errorf("tool name mismatch: got %q", got.ToolName)
		}
		if got.Error != errEvent.Error {
			t.Errorf("expected error %q, got %q", errEvent.Error, got.Error)
		}
	})
}

func TestSaveAndGetToolCalls_NilFields(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "session-nil-fields"
	if err := db.CreateSession(sessionID, projectID, "nil fields test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	args, err := json.Marshal(map[string]string{"path": "README.md"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	event := registry.AuditEvent{
		Timestamp:       time.Date(2026, 7, 2, 12, 2, 0, 0, time.UTC),
		AgentRole:       "reviewer",
		Model:           "test-model",
		ToolName:        "file.read",
		Args:            args,
		Risk:            registry.RiskReadOnly,
		Approval:        registry.ApprovalNotRequired,
		ResultSummary:   "read file",
		FilesChanged:    nil,
		CommandExitCode: nil,
		Error:           "",
	}

	if err := db.SaveToolCall(sessionID, event); err != nil {
		t.Fatalf("SaveToolCall failed: %v", err)
	}

	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	got := calls[0]
	if got.CommandExitCode != nil {
		t.Errorf("expected nil exit code, got %v", got.CommandExitCode)
	}
	if got.FilesChanged != nil {
		t.Errorf("expected nil files changed, got %v", got.FilesChanged)
	}
	if got.Error != "" {
		t.Errorf("expected empty error, got %q", got.Error)
	}
}

func TestGetToolCalls_LegacyRows(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Simulate a pre-migration tool_calls table that lacks the columns added
	// after the initial schema: command_exit_code, files_changed, and error.
	_, err = db.sqlDB.Exec(`CREATE TABLE tool_calls (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT,
		agent_role TEXT,
		model TEXT,
		tool_name TEXT,
		args_json TEXT,
		result_summary TEXT,
		risk_level TEXT,
		approval_state TEXT,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create legacy tool_calls table: %v", err)
	}

	sessionID := "session-legacy"
	createdAt := time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC).Format(time.RFC3339)
	_, err = db.sqlDB.Exec(
		`INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID,
		"implementer",
		"legacy-model",
		"shell.exec",
		`{"command":"go test"}`,
		"tests passed",
		string(registry.RiskCommand),
		string(registry.ApprovalApproved),
		createdAt,
	)
	if err != nil {
		t.Fatalf("insert legacy tool call: %v", err)
	}

	// Migrate should add the missing columns without touching the existing row.
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	got := calls[0]
	if got.ToolName != "shell.exec" {
		t.Errorf("expected tool name shell.exec, got %q", got.ToolName)
	}
	if got.CommandExitCode != nil {
		t.Errorf("expected nil exit code for legacy row, got %v", got.CommandExitCode)
	}
	if got.FilesChanged != nil {
		t.Errorf("expected nil files changed for legacy row, got %v", got.FilesChanged)
	}
	if got.Error != "" {
		t.Errorf("expected empty error for legacy row, got %q", got.Error)
	}
	if got.OriginalArgs != nil {
		t.Errorf("legacy row: OriginalArgs = %v, want nil", got.OriginalArgs)
	}
	if got.Rewritten {
		t.Errorf("legacy row: Rewritten = true, want false")
	}
}

func TestGetToolCallsPreservesLegacyEnabledFlag(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create the full schema as it exists after migration.
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "session-legacy-enabled"
	if err := db.CreateSession(sessionID, projectID, "legacy enabled test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Simulate a legacy row that existed before the sandbox_enabled column
	// was added. After ALTER TABLE the column defaults to 0, but the row
	// has a non-empty sandbox_backend which implies Enabled=true.
	_, err = db.sqlDB.Exec(
		`INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at,
		                          sandbox_backend, sandbox_enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID,
		"implementer",
		"legacy-model",
		"shell.exec",
		`{"command":"go test"}`,
		"tests passed",
		string(registry.RiskCommand),
		string(registry.ApprovalApproved),
		time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"restricted",
		0, // sandbox_enabled = 0 — legacy default after ALTER TABLE
	)
	if err != nil {
		t.Fatalf("insert legacy sandbox row: %v", err)
	}

	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	got := calls[0]
	if !got.Sandbox.Enabled {
		t.Errorf("expected Enabled=true for legacy row with sandbox_backend='restricted', got Enabled=false")
	}
	if got.Sandbox.Backend != "restricted" {
		t.Errorf("expected Backend='restricted', got %q", got.Sandbox.Backend)
	}
}

func TestSaveAndGetToolCalls_SandboxMeta(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "session-sandbox-meta"
	if err := db.CreateSession(sessionID, projectID, "sandbox meta test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	exitCode := 137
	event := registry.AuditEvent{
		Timestamp:       time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		AgentRole:       "implementer",
		Model:           "test-model",
		ToolName:        "shell.run",
		Args:            argsJSON(`{"command":"go test ./..."}`),
		Risk:            registry.RiskCommand,
		Approval:        registry.ApprovalApproved,
		ResultSummary:   "command \"go test ./...\" killed: timeout",
		CommandExitCode: &exitCode,
		Sandbox: registry.SandboxMeta{
			Enabled:          true,
			Backend:          "container",
			NetworkIsolated:  true,
			ResourceLimits:   true,
			MemoryLimitBytes: 1024 * 1024 * 1024,
			CPUSeconds:       2,
			MaxProcesses:     128,
			KilledReason:     "timeout",
			DurationMS:       4012,
		},
	}
	if err := db.SaveToolCall(sessionID, event); err != nil {
		t.Fatalf("SaveToolCall failed: %v", err)
	}
	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	got := calls[0]
	if !got.Sandbox.Enabled || got.Sandbox.Backend != "container" {
		t.Errorf("sandbox backend not persisted: %+v", got.Sandbox)
	}
	if !got.Sandbox.NetworkIsolated {
		t.Errorf("expected NetworkIsolated=true, got %+v", got.Sandbox)
	}
	if got.Sandbox.KilledReason != "timeout" {
		t.Errorf("expected killed reason timeout, got %q", got.Sandbox.KilledReason)
	}
	if got.Sandbox.DurationMS != 4012 {
		t.Errorf("expected duration 4012ms, got %d", got.Sandbox.DurationMS)
	}
	// Round-trip the structured limit/capability fields that were persisted
	// inside sandbox_limits_json.
	if got.Sandbox.MemoryLimitBytes != event.Sandbox.MemoryLimitBytes {
		t.Errorf("expected MemoryLimitBytes %d, got %d", event.Sandbox.MemoryLimitBytes, got.Sandbox.MemoryLimitBytes)
	}
	if got.Sandbox.CPUSeconds != event.Sandbox.CPUSeconds {
		t.Errorf("expected CPUSeconds %d, got %d", event.Sandbox.CPUSeconds, got.Sandbox.CPUSeconds)
	}
	if got.Sandbox.MaxProcesses != event.Sandbox.MaxProcesses {
		t.Errorf("expected MaxProcesses %d, got %d", event.Sandbox.MaxProcesses, got.Sandbox.MaxProcesses)
	}
}

func TestSaveToolCallRoundTripsSandboxMeta(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	sessionID := "session-rt-sandbox"
	if err := db.CreateSession(sessionID, projectID, "sandbox round-trip test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ev := registry.AuditEvent{
		ToolName:  "shell.run",
		Timestamp: time.Now().UTC(),
		Sandbox: registry.SandboxMeta{
			Enabled:         true,
			Backend:         "restricted",
			NetworkIsolated: true,
			ResourceLimits:  true,
			OutputTruncated: true,
		},
	}
	if err := db.SaveToolCall(sessionID, ev); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rows, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	sm := rows[0].Sandbox
	if !sm.Enabled || sm.Backend != "restricted" {
		t.Errorf("Enabled/Backend not preserved: %+v", sm)
	}
	if !sm.ResourceLimits || !sm.OutputTruncated {
		t.Errorf("ResourceLimits/OutputTruncated not preserved: %+v", sm)
	}
}

func TestSaveToolCallNormalizesFilesChanged(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}

	sessionID := "session-normalize"
	if err := db.CreateSession(sessionID, projectID, "normalize test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	raw := `a\b/c.go`
	ev := registry.AuditEvent{
		ToolName:      "shell.run",
		FilesChanged:  []string{raw},
		Timestamp:     time.Now().UTC(),
		AgentRole:     "implementer",
		Model:         "test-model",
		Risk:          registry.RiskCommand,
		Approval:      registry.ApprovalApproved,
		ResultSummary: "test",
	}
	if err := db.SaveToolCall(sessionID, ev); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rows, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].FilesChanged[0] != filepath.ToSlash(raw) {
		t.Errorf("expected slash-normalized path, got %q", rows[0].FilesChanged[0])
	}
}

func argsJSON(s string) []byte { return []byte(s) }
