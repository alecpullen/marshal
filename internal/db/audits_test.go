package db

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"marshal/internal/progress"
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

func TestGetToolCallsSandboxLimitsFilesystemIsolated(t *testing.T) {
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
	sessionID := "session-sandbox-fs-iso"
	if err := db.CreateSession(sessionID, projectID, "fs isolation test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	event := registry.AuditEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  "shell.run",
		Args:      argsJSON(`{"command":"make"}`),
		Sandbox: registry.SandboxMeta{
			Enabled:            true,
			Backend:            "container",
			FilesystemIsolated: true,
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
	if !calls[0].Sandbox.FilesystemIsolated {
		t.Errorf("FilesystemIsolated not round-tripped: %+v", calls[0].Sandbox)
	}
}

func argsJSON(s string) []byte { return []byte(s) }

// TestSaveAndGetToolCalls_Notice covers the durable ToolNotice coaching field:
// an event carrying a Notice round-trips Kind, Text, and Data.
//
// The Data map is encoded through JSON, so numeric values come back as float64
// (encoding/json decodes every number into float64 for map[string]any). This
// test documents that behaviour rather than asserting a byte-for-byte equal
// map, because the equality does not hold.
func TestSaveAndGetToolCalls_Notice(t *testing.T) {
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
	sessionID := "session-notice"
	if err := db.CreateSession(sessionID, projectID, "notice test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	event := registry.AuditEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  "file.read",
		Args:      argsJSON(`{"path":"big.go"}`),
		Notice: &registry.ToolNotice{
			Kind: registry.NoticeOversizeFallback,
			Text: "file too large; showing head",
			Data: map[string]any{"total_bytes": 123456, "path": "big.go"},
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
	got := calls[0].Notice
	if got == nil {
		t.Fatal("Notice round-tripped to nil")
	}
	if got.Kind != registry.NoticeOversizeFallback {
		t.Errorf("Kind = %q, want %q", got.Kind, registry.NoticeOversizeFallback)
	}
	if got.Text != "file too large; showing head" {
		t.Errorf("Text = %q", got.Text)
	}
	// A JSON round-trip turns numbers into float64; assert the actual type.
	if v := got.Data["total_bytes"]; v != float64(123456) {
		t.Errorf("Data[total_bytes] = %#v (%T), want float64(123456)", v, v)
	}
	if v := got.Data["path"]; v != "big.go" {
		t.Errorf("Data[path] = %#v, want %q", v, "big.go")
	}
}

// TestSaveAndGetToolCalls_NilNotice pins that a nil Notice persists as SQL NULL
// (not "{}" or the literal "null") and reads back as nil, so existing rows and
// existing consumers keep working.
func TestSaveAndGetToolCalls_NilNotice(t *testing.T) {
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
	sessionID := "session-nil-notice"
	if err := db.CreateSession(sessionID, projectID, "nil notice test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	event := registry.AuditEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  "grep",
		Args:      argsJSON(`{"query":"x"}`),
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
	if calls[0].Notice != nil {
		t.Errorf("Notice = %+v, want nil", calls[0].Notice)
	}

	var stored sql.NullString
	if err := db.sqlDB.QueryRow(`SELECT notice_json FROM tool_calls WHERE session_id = ?`, sessionID).Scan(&stored); err != nil {
		t.Fatalf("select notice_json: %v", err)
	}
	if stored.Valid {
		t.Errorf("notice_json = %q, want SQL NULL", stored.String)
	}
}

// TestGetToolCalls_LegacyRowLacksNotice follows the legacy-table pattern used
// by TestGetToolCalls_LegacyRows: a pre-migration tool_calls table (no
// notice_json) is upgraded by Migrate, and the old row reads back with a nil
// Notice rather than an error.
func TestGetToolCalls_LegacyRowLacksNotice(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

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

	sessionID := "session-legacy-notice"
	_, err = db.sqlDB.Exec(
		`INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, "implementer", "legacy-model", "shell.exec", `{"command":"go test"}`,
		"tests passed", string(registry.RiskCommand), string(registry.ApprovalApproved),
		time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC).Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert legacy tool call: %v", err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	cols, err := db.tableColumns("tool_calls")
	if err != nil {
		t.Fatalf("tableColumns failed: %v", err)
	}
	if !cols["notice_json"] {
		t.Fatal("Migrate did not add notice_json to tool_calls")
	}

	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Notice != nil {
		t.Errorf("legacy row Notice = %+v, want nil", calls[0].Notice)
	}
}

// TestSaveAndGetToolCalls_CommandExecution covers the structured
// command-execution payload: it survives the JSON round trip through
// notice_json and decodes from the *persisted* map form, where numbers have
// become float64 and timestamps their RFC3339 text.
func TestSaveAndGetToolCalls_CommandExecution(t *testing.T) {
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
	const sessionID = "session-exec"
	if err := db.CreateSession(sessionID, projectID, "exec test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	code := 7
	started := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	ended := started.Add(90 * time.Second)
	ex := registry.CommandExecution{
		ExecutionID:     "job=job-3|conv=session-exec|call=c1",
		DisplayID:       "job-3",
		ConversationID:  "session-exec",
		Actor:           "implementer",
		TurnID:          "turn-1",
		StepID:          "step-2",
		ToolCallID:      "c1",
		Cwd:             "/repo",
		Generation:      4,
		PID:             4242,
		StartedAt:       started,
		EndedAt:         ended,
		State:           "failed",
		Outcome:         "nonzero_exit",
		Wait:            "10s",
		Attachment:      registry.ExecutionAttachmentBackground,
		DetachReason:    "wait expired",
		DetachedAt:      started.Add(10 * time.Second),
		StopCause:       "turn stop",
		StopRequestedAt: ended.Add(-time.Second),
		ExitCode:        &code,
		Truncated:       true,
	}
	notice, err := registry.CommandExecutionNotice(ex)
	if err != nil {
		t.Fatalf("CommandExecutionNotice failed: %v", err)
	}
	if notice.Text != "" {
		t.Fatalf("an execution record is metadata, not coaching: text = %q", notice.Text)
	}

	event := registry.AuditEvent{
		Timestamp:     started,
		ToolName:      "shell.run",
		Args:          argsJSON(`{"command":"go test","background":true}`),
		ResultSummary: "started background job job-3",
		Notice:        notice,
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
	got := calls[0].Notice
	if got == nil || got.Kind != registry.NoticeCommandExecution {
		t.Fatalf("Notice = %+v, want a command-execution record", got)
	}
	if _, isMap := got.Data[registry.CommandExecutionKey].(map[string]any); !isMap {
		t.Fatalf("persisted payload = %T, want the JSON map form",
			got.Data[registry.CommandExecutionKey])
	}
	decoded, ok := registry.DecodeCommandExecution(got)
	if !ok {
		t.Fatalf("persisted payload did not decode: %#v", got.Data)
	}
	if decoded.Version != registry.CommandExecutionVersion {
		t.Errorf("Version = %d, want %d", decoded.Version, registry.CommandExecutionVersion)
	}
	if decoded.ExecutionID != ex.ExecutionID || decoded.DisplayID != ex.DisplayID {
		t.Errorf("identity = %q / %q, want %q / %q", decoded.ExecutionID, decoded.DisplayID, ex.ExecutionID, ex.DisplayID)
	}
	if decoded.ConversationID != ex.ConversationID || decoded.Actor != ex.Actor ||
		decoded.TurnID != ex.TurnID || decoded.StepID != ex.StepID || decoded.ToolCallID != ex.ToolCallID {
		t.Errorf("origin lost: %+v", decoded)
	}
	if decoded.Cwd != ex.Cwd || decoded.Generation != ex.Generation || decoded.PID != ex.PID {
		t.Errorf("captured facts lost: cwd %q gen %d pid %d", decoded.Cwd, decoded.Generation, decoded.PID)
	}
	if !decoded.StartedAt.Equal(started) || !decoded.EndedAt.Equal(ended) || !decoded.DetachedAt.Equal(ex.DetachedAt) ||
		!decoded.StopRequestedAt.Equal(ex.StopRequestedAt) {
		t.Errorf("times lost: %+v", decoded)
	}
	if decoded.State != ex.State || decoded.Outcome != ex.Outcome || decoded.Wait != ex.Wait {
		t.Errorf("state/outcome/wait = %q/%q/%q", decoded.State, decoded.Outcome, decoded.Wait)
	}
	if decoded.Attachment != registry.ExecutionAttachmentBackground || decoded.DetachReason != ex.DetachReason {
		t.Errorf("attachment evidence = %q/%q", decoded.Attachment, decoded.DetachReason)
	}
	if decoded.StopCause != ex.StopCause {
		t.Errorf("StopCause = %q, want %q", decoded.StopCause, ex.StopCause)
	}
	if decoded.ExitCode == nil || *decoded.ExitCode != code {
		t.Errorf("ExitCode = %v, want %d", decoded.ExitCode, code)
	}
	if !decoded.Truncated {
		t.Error("Truncated = false, want the payload to remember the cap")
	}
}

// TestDecodeCommandExecutionRejectsForeignVersions pins that an unknown payload
// version is not guessed at: a reader falls back rather than misreading a shape
// it does not know.
func TestDecodeCommandExecutionRejectsForeignVersions(t *testing.T) {
	for _, payload := range []any{
		map[string]any{"version": float64(99), "execution_id": "e1"},
		map[string]any{"version": float64(99), "display_id": "job-1"},
		"not a payload",
		42,
	} {
		n := &registry.ToolNotice{Kind: registry.NoticeCommandExecution,
			Data: map[string]any{registry.CommandExecutionKey: payload}}
		if ex, ok := registry.DecodeCommandExecution(n); ok {
			t.Errorf("payload %#v decoded as %+v, want a refusal", payload, ex)
		}
	}
	// A payload with no version at all is still readable: it predates the
	// version field, and its shape is the one we know.
	n := &registry.ToolNotice{Kind: registry.NoticeCommandExecution,
		Data: map[string]any{registry.CommandExecutionKey: map[string]any{"execution_id": "e1", "pid": float64(11)}}}
	ex, ok := registry.DecodeCommandExecution(n)
	if !ok || ex.ExecutionID != "e1" || ex.PID != 11 {
		t.Fatalf("version-less payload = %+v, ok = %v", ex, ok)
	}
}

// TestNewAuditEventDeepCopiesExecutionEvidence pins the deep copy. A
// continuation rewrites the payload the caller still holds — that is how a
// running execution reports its state — so an audit already recorded must not
// change with it.
func TestNewAuditEventDeepCopiesExecutionEvidence(t *testing.T) {
	code := 1
	notice := registry.NewCommandExecutionNotice(registry.CommandExecution{
		ExecutionID: "e1", DisplayID: "job-1", ExitCode: &code,
		Attachment: registry.ExecutionAttachmentForeground,
	})
	event := registry.NewAuditEvent(time.Now(),
		registry.Tool{Name: "shell.run", Risk: registry.RiskCommand},
		registry.ToolCall{Name: "shell.run", Args: argsJSON(`{"command":"go test"}`)},
		registry.ToolResult{Summary: "ok", Notice: notice},
		registry.ApprovalNotRequired, nil)

	// The same object the caller handed over is rewritten, as a handoff does.
	notice.WithExecution(registry.CommandExecution{
		ExecutionID: "e1", DisplayID: "job-1", State: "completed",
		Attachment: registry.ExecutionAttachmentBackground,
	})
	notice.Text = "coaching added later"

	decoded, ok := event.Notice.Execution()
	if !ok {
		t.Fatal("the audit lost the execution payload")
	}
	if decoded.State != "" || decoded.Attachment != registry.ExecutionAttachmentForeground {
		t.Fatalf("the audit payload followed the caller's rewrite: %+v", decoded)
	}
	if decoded.ExitCode == nil || *decoded.ExitCode != 1 {
		t.Fatalf("exit = %v, want the recorded 1", decoded.ExitCode)
	}
	if event.Notice.Text != "" {
		t.Fatalf("the audit text followed the caller: %q", event.Notice.Text)
	}

	// The copy owns the exit-code pointer as well.
	code = 9
	again, _ := event.Notice.Execution()
	if again.ExitCode == nil || *again.ExitCode != 1 {
		t.Fatalf("the copy aliases the caller's pointer: %v", again.ExitCode)
	}

	// A payload in its map form is copied deeply too, nested maps included.
	original := map[string]any{"execution_id": "e2", "nested": map[string]any{"k": "v"}}
	mapNotice := &registry.ToolNotice{Kind: registry.NoticeCommandExecution,
		Data: map[string]any{registry.CommandExecutionKey: original}}
	mapEvent := registry.NewAuditEvent(time.Now(),
		registry.Tool{Name: "shell.run", Risk: registry.RiskCommand},
		registry.ToolCall{Name: "shell.run"}, registry.ToolResult{Notice: mapNotice},
		registry.ApprovalNotRequired, nil)
	original["nested"].(map[string]any)["k"] = "changed"
	original["execution_id"] = "changed"
	got := mapEvent.Notice.Data[registry.CommandExecutionKey].(map[string]any)
	if got["execution_id"] != "e2" {
		t.Fatalf("execution_id = %#v, want the recorded e2", got["execution_id"])
	}
	if nested, _ := got["nested"].(map[string]any); nested["k"] != "v" {
		t.Fatalf("nested metadata aliased the caller: %#v", got["nested"])
	}
}

// TestJobExitRecordsRoundTripStructuredEvidence covers the additive fields on
// the stored job-exit record: they survive session_state's JSON while the
// original six fields keep their meaning.
func TestJobExitRecordsRoundTripStructuredEvidence(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	code := 4
	started := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	record := JobExitRecord{
		ID: "job-2", Command: "go test", ExitCode: 4, DurationMs: 5000, Output: "FAIL", At: started.Add(time.Minute),
		ExecutionID: "e2", ActualExitCode: &code, ConversationID: "s1", Actor: "implementer",
		TurnID: "turn-1", StepID: "step-2", ToolCallID: "c1", Cwd: "/repo", Generation: 3, PID: 77,
		StartedAt: started, EndedAt: started.Add(time.Minute),
		State: "failed", Outcome: "nonzero_exit", Attachment: "background", Wait: "10s",
		DetachReason: "wait expired", DetachedAt: started.Add(10 * time.Second),
		StopCause: "turn stop", StopRequestedAt: started.Add(50 * time.Second), Truncated: true,
	}
	if err := db.SaveJobExits("exit-sess", []JobExitRecord{record}); err != nil {
		t.Fatalf("SaveJobExits failed: %v", err)
	}
	got, err := db.LoadJobExits("exit-sess")
	if err != nil {
		t.Fatalf("LoadJobExits failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0], record) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got[0], record)
	}
}

// TestLoadJobExitsLegacyRowStaysLegacy pins the compatibility rule: a record
// written before the structured fields existed loads with them empty — unknown,
// not zero-valued facts — and its -1 keeps meaning "no exit was observed"
// rather than success.
func TestLoadJobExitsLegacyRowStaysLegacy(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Exactly what the pre-Task-4 writer produced.
	const legacy = `[{"id":"job-1","command":"go test","exit_code":-1,"at":"2026-01-02T03:04:05Z"}]`
	if _, err := db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'job_exits', ?)`,
		"legacy-sess", legacy,
	); err != nil {
		t.Fatalf("insert legacy job exits: %v", err)
	}

	got, err := db.LoadJobExits("legacy-sess")
	if err != nil {
		t.Fatalf("LoadJobExits failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	r := got[0]
	if r.ID != "job-1" || r.ExitCode != -1 || r.Command != "go test" {
		t.Fatalf("legacy fields changed: %+v", r)
	}
	if r.ExecutionID != "" || r.ActualExitCode != nil || r.State != "" || r.Outcome != "" ||
		r.Attachment != "" || r.Wait != "" || r.StopCause != "" || r.Truncated ||
		r.PID != 0 || r.Generation != 0 || !r.StartedAt.IsZero() || !r.EndedAt.IsZero() {
		t.Fatalf("a legacy row invented structured facts: %+v", r)
	}
}

// TestWorkViewRoundTripVersionAndIsolation pins the work-view index: the whole
// versioned record round-trips through session_state's JSON, and one session's
// view never leaks into another's.
func TestWorkViewRoundTripVersionAndIsolation(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	started := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	record := WorkViewRecord{
		Version: 1,
		WatchResults: []WorkResultRecord{{
			TaskKey: "work:watch", SourceID: "src-1", RuntimeEpoch: "epoch-1",
			Kind: "watch", Label: "build", Owner: "watch", TurnID: "turn-1",
			StepID: "step-1", ToolCallID: "call-1", State: "done", Outcome: "exit_zero",
			StartedAt: started, EndedAt: started.Add(2 * time.Second),
			ResultRevision: "rev-1", Detail: "3 passed",
			Reference: WorkReference{MessageID: 42, NodeKey: "node-1", ChildSessionID: "child-1"},
		}},
		AgentResults: []WorkResultRecord{{
			TaskKey: "work:agent", SourceID: "agent-1", RuntimeEpoch: "epoch-1",
			Kind: "agent", Label: "reviewer", State: "done",
			StartedAt: started, EndedAt: started.Add(time.Minute),
			ResultRevision: "rev-2", Reference: WorkReference{MessageID: 7},
		}},
		Acknowledged: []WorkAcknowledgment{{TaskKey: "work:watch", ResultRevision: "rev-1"}},
	}
	if err := db.SaveWorkView("sess-a", record); err != nil {
		t.Fatalf("SaveWorkView failed: %v", err)
	}
	got, err := db.LoadWorkView("sess-a")
	if err != nil {
		t.Fatalf("LoadWorkView failed: %v", err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, record)
	}

	// A different session must not see sess-a's stored view.
	other, err := db.LoadWorkView("sess-b")
	if err != nil {
		t.Fatalf("LoadWorkView(sess-b) failed: %v", err)
	}
	if !reflect.DeepEqual(other, WorkViewRecord{Version: 1}) {
		t.Fatalf("sess-b view = %+v, want empty version-1 record", other)
	}
}

// TestWorkViewMissingAndFutureVersion pins the two read edges: an absent row
// is an empty version-1 record with no error, while an unknown stored version
// is an explicit error that leaves the stored data untouched.
func TestWorkViewMissingAndFutureVersion(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	missing, err := db.LoadWorkView("never-written")
	if err != nil {
		t.Fatalf("LoadWorkView(missing) failed: %v", err)
	}
	if !reflect.DeepEqual(missing, WorkViewRecord{Version: 1}) {
		t.Fatalf("missing view = %+v, want empty version-1 record", missing)
	}

	const future = `{"version":2,"watchResults":[]}`
	if _, err := db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'work_view_v1', ?)`,
		"future-sess", future,
	); err != nil {
		t.Fatalf("insert future work view: %v", err)
	}
	if _, err := db.LoadWorkView("future-sess"); err == nil {
		t.Fatal("LoadWorkView on a future version returned a blank success, want an error")
	}
	// The failed read must not have overwritten the stored data.
	var raw string
	if err := db.sqlDB.QueryRow(
		`SELECT value FROM session_state WHERE session_id=? AND key='work_view_v1'`,
		"future-sess").Scan(&raw); err != nil {
		t.Fatalf("read stored work view: %v", err)
	}
	if raw != future {
		t.Fatalf("stored work view changed:\n got %s\nwant %s", raw, future)
	}

	// Saving a non-1 version is refused, never silently coerced.
	if err := db.SaveWorkView("future-sess", WorkViewRecord{Version: 2}); err == nil {
		t.Fatal("SaveWorkView(version 2) succeeded, want an error")
	}
}

// TestJobExitProgressRoundTripLegacy pins the additive progress evidence on
// the job-exit record: the captured config and final snapshot round-trip, and
// a record written before these fields existed still decodes with them absent.
func TestJobExitProgressRoundTripLegacy(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	started := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	full := JobExitRecord{
		ID: "job-1", Command: "go test", ExitCode: 0, At: started.Add(time.Minute),
		State: "completed", Outcome: "exit_zero",
		ProgressConfig: &progress.Config{Profile: "tests", Source: "go-test-json", Title: "go test"},
		Progress: &progress.Snapshot{
			Frame:      progress.Frame{Version: 1, Sequence: 4, Detail: "go test"},
			ObservedAt: started,
			MeasuredAt: started,
		},
		ResultRevision: "rev-9", SourceEpoch: "epoch-9",
	}
	if err := db.SaveJobExits("progress-sess", []JobExitRecord{full}); err != nil {
		t.Fatalf("SaveJobExits failed: %v", err)
	}
	got, err := db.LoadJobExits("progress-sess")
	if err != nil {
		t.Fatalf("LoadJobExits failed: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], full) {
		t.Fatalf("progress round trip:\n got %+v\nwant %+v", got, full)
	}

	// A pre-progress record still decodes with the additive fields empty.
	const legacy = `[{"id":"job-legacy","command":"go build","exit_code":0,"at":"2026-01-02T03:04:05Z"}]`
	if _, err := db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'job_exits', ?)`,
		"legacy-progress-sess", legacy,
	); err != nil {
		t.Fatalf("insert legacy job exits: %v", err)
	}
	legacyGot, err := db.LoadJobExits("legacy-progress-sess")
	if err != nil {
		t.Fatalf("LoadJobExits(legacy) failed: %v", err)
	}
	if len(legacyGot) != 1 {
		t.Fatalf("legacy records = %d, want 1", len(legacyGot))
	}
	lr := legacyGot[0]
	if lr.ProgressConfig != nil || lr.Progress != nil || lr.ResultRevision != "" || lr.SourceEpoch != "" {
		t.Fatalf("a legacy row invented progress evidence: %+v", lr)
	}
}
