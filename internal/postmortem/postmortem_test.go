package postmortem

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/redact"
	"marshal/internal/tools/registry"
)

const testSessionID = "sess_test"

// testConfig returns the default config with secret redaction off, so the
// extraction tests assert raw values unless they opt into redaction.
func testConfig() config.Config {
	cfg := config.Default()
	cfg.Privacy.RedactSecrets = false
	return cfg
}

// testState seeds a session state the way internal/export does: a real State
// with a fixed clock and an explicit session id, and no database (so there are
// no load/save side effects). The working dir is "/repo" so ProjectSlug is
// deterministic.
func testState(t *testing.T, cfg config.Config, database *db.DB) *session.State {
	t.Helper()
	return session.New(cfg, "/repo", time.Unix(1000, 0).UTC(), session.Persistence{
		DB:        database,
		SessionID: testSessionID,
	})
}

func TestBuildDeduplicatesToolFailures(t *testing.T) {
	state := testState(t, testConfig(), nil)
	base := time.Unix(2000, 0).UTC()
	exitCode := 1
	state.LogToolCall(registry.AuditEvent{
		Timestamp: base,
		ToolName:  "file.read",
		Args:      json.RawMessage(`{"path":"a.go"}`),
		Error:     "too large to read",
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp: base.Add(time.Minute),
		ToolName:  "file.read",
		Args:      json.RawMessage(`{"path":"b.go"}`),
		Error:     "too large to read",
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp:       base.Add(2 * time.Minute),
		ToolName:        "shell.run",
		Args:            json.RawMessage(`{"command":"go build"}`),
		Error:           "exit status 1",
		CommandExitCode: &exitCode,
	})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ToolFailures) != 2 {
		t.Fatalf("len(tool_failures) = %d, want 2 (%+v)", len(report.ToolFailures), report.ToolFailures)
	}

	grouped := report.ToolFailures[0]
	if grouped.Tool != "file.read" || grouped.Count != 2 {
		t.Fatalf("grouped failure = %+v, want file.read count 2", grouped)
	}
	if !grouped.FirstSeen.Equal(base) || !grouped.LastSeen.Equal(base.Add(time.Minute)) {
		t.Errorf("first_seen/last_seen = %v/%v, want %v/%v",
			grouped.FirstSeen, grouped.LastSeen, base, base.Add(time.Minute))
	}
	if grouped.ExitCode != nil {
		t.Errorf("grouped exit_code = %d, want nil", *grouped.ExitCode)
	}

	single := report.ToolFailures[1]
	if single.Tool != "shell.run" || single.Count != 1 {
		t.Fatalf("single failure = %+v, want shell.run count 1", single)
	}
	if single.ExitCode == nil || *single.ExitCode != 1 {
		t.Errorf("single exit_code = %v, want 1", single.ExitCode)
	}
}

func TestBuildCountsParseFailuresFromTurnMetrics(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession(testSessionID, projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:          projectID,
		SessionID:          testSessionID,
		StartedAt:          time.Unix(3000, 0).UTC(),
		Model:              "m1",
		ParseFailures:      3,
		Outcome:            "failed",
		PromptTokens:       100,
		CompletionTokens:   20,
		ReasoningTokens:    5,
		EstimatedCostCents: 7,
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	// Another session's row in the same project must be excluded: the query is
	// project-scoped, the session filter is ours.
	if err := database.CreateSession("sess_other", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession(other): %v", err)
	}
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:        projectID,
		SessionID:        "sess_other",
		StartedAt:        time.Unix(3001, 0).UTC(),
		Model:            "m2",
		ParseFailures:    99,
		Outcome:          "failed",
		PromptTokens:     999,
		CompletionTokens: 999,
	}); err != nil {
		t.Fatalf("InsertTurnMetrics(other): %v", err)
	}

	state := testState(t, testConfig(), database)
	report, err := Build(state, database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var failures int
	for _, issue := range report.ParseIssues {
		if issue.Kind == parseFailureKind {
			failures += issue.Count
		}
	}
	if failures != 3 {
		t.Errorf("parse_failure count = %d, want 3 (%+v)", failures, report.ParseIssues)
	}
	if report.TokenWaste.TokensFailedTurns != 120 {
		t.Errorf("tokens_failed_turns = %d, want 120", report.TokenWaste.TokensFailedTurns)
	}
	if report.TokenWaste.TotalPrompt != 100 || report.TokenWaste.TotalCompletion != 20 || report.TokenWaste.TotalReasoning != 5 {
		t.Errorf("token totals = %+v, want 100/20/5", report.TokenWaste)
	}
	if report.TokenWaste.EstimatedCostCents != 7 {
		t.Errorf("estimated_cost_cents = %d, want 7", report.TokenWaste.EstimatedCostCents)
	}
	if len(report.TurnOutcomes) != 1 || report.TurnOutcomes[0] != (TurnOutcome{Outcome: "failed", Count: 1}) {
		t.Errorf("turn_outcomes = %+v, want one failed entry", report.TurnOutcomes)
	}
	if len(report.Session.ModelsUsed) != 1 || report.Session.ModelsUsed[0] != "m1" {
		t.Errorf("models_used = %v, want [m1]", report.Session.ModelsUsed)
	}
}

func TestBuildRedactsWhenConfigured(t *testing.T) {
	cfg := testConfig()
	cfg.Privacy.RedactSecrets = true
	state := testState(t, cfg, nil)

	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	state.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run",
		Args:     json.RawMessage(`{"command":"curl -H 'Authorization: Bearer ` + secret + `'"}`),
		Error:    "auth failed for token " + secret,
	})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ToolFailures) != 1 {
		t.Fatalf("len(tool_failures) = %d, want 1", len(report.ToolFailures))
	}
	failure := report.ToolFailures[0]
	if strings.Contains(failure.Args, secret) || !strings.Contains(failure.Args, redact.MaskToken) {
		t.Errorf("args not redacted: %q", failure.Args)
	}
	if strings.Contains(failure.Error, secret) || !strings.Contains(failure.Error, redact.MaskToken) {
		t.Errorf("error not redacted: %q", failure.Error)
	}
}

func TestBuildAgentObservationsNullByDefault(t *testing.T) {
	state := testState(t, testConfig(), nil)
	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if report.AgentObservations != nil {
		t.Fatalf("AgentObservations = %q, want nil", *report.AgentObservations)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"agent_observations":null`) {
		t.Errorf("agent_observations must marshal as null:\n%s", data)
	}
	if report.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", report.SchemaVersion, SchemaVersion)
	}
}

func TestBuildMapsRunEventKinds(t *testing.T) {
	state := testState(t, testConfig(), nil)
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventVerifyFailed, Title: "go test ./..."})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventVerifyFailed, Title: "go test ./..."})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventGateSkipped, Title: "no command configured"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventReview, Title: "finding text"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, Title: "a3f9e21", Detail: "plan: task 1"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventRetry, Detail: "transient"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventConcern, Title: "no coverage"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventTaskDone, Detail: "task 7"})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	got := make(map[string]int, len(report.RunEvents))
	for _, entry := range report.RunEvents {
		got[entry.Kind] = entry.Count
	}
	want := map[string]int{
		"verify_failure": 2,
		"gate_skipped":   1,
		"review":         1,
		"commit":         1,
		"retry":          1,
		"concern":        1,
		"task_done":      1,
	}
	if len(got) != len(want) {
		t.Fatalf("run_events kinds = %+v, want %+v", got, want)
	}
	for kind, count := range want {
		if got[kind] != count {
			t.Errorf("run_events[%q] = %d, want %d", kind, got[kind], count)
		}
	}

	var verifyDetail string
	for _, entry := range report.RunEvents {
		if entry.Kind == "verify_failure" {
			verifyDetail = entry.Detail
		}
	}
	if verifyDetail != "go test ./..." {
		t.Errorf("verify_failure detail = %q, want the failing command", verifyDetail)
	}
}

func TestBuildCountsUserTurnsAndTruncatedCalls(t *testing.T) {
	state := testState(t, testConfig(), nil)
	state.AddMessage(session.RoleUser, "first", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "ok", session.ContentTypeMarkdown)
	state.AddMessage(session.RoleUser, "second", session.ContentTypePlain)
	state.LogToolCall(registry.AuditEvent{ToolName: "file.write_patch", FinishReason: "length"})
	state.LogToolCall(registry.AuditEvent{ToolName: "file.write_patch", FinishReason: "length"})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if report.Session.Turns != 2 {
		t.Errorf("turns = %d, want 2", report.Session.Turns)
	}
	if report.Session.Project != "repo" {
		t.Errorf("project = %q, want repo", report.Session.Project)
	}
	if len(report.TruncatedCalls) != 1 || report.TruncatedCalls[0].Count != 2 {
		t.Fatalf("truncated_calls = %+v, want one entry with count 2", report.TruncatedCalls)
	}
	if report.TruncatedCalls[0].FinishReason != "length" {
		t.Errorf("finish_reason = %q, want length", report.TruncatedCalls[0].FinishReason)
	}
}

func TestBuildGroupsApprovalDenials(t *testing.T) {
	state := testState(t, testConfig(), nil)
	event := registry.AuditEvent{
		ToolName: "shell.run",
		Args:     json.RawMessage(`{"command":"rm -rf /"}`),
		Error:    "denied by user",
		Approval: registry.ApprovalDenied,
	}
	state.LogToolCall(event)
	state.LogToolCall(event)

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ApprovalDenials) != 1 || report.ApprovalDenials[0].Count != 2 {
		t.Fatalf("approval_denials = %+v, want one entry with count 2", report.ApprovalDenials)
	}
	if len(report.ToolFailures) != 0 {
		t.Errorf("denied calls must not also count as tool failures: %+v", report.ToolFailures)
	}
}

func TestBuildReadsPatchRepairNotes(t *testing.T) {
	state := testState(t, testConfig(), nil)
	state.LogToolCall(registry.AuditEvent{
		ToolName: "file.write_patch",
		ResultContent: "--- a.go\n+++ a.go\n@@ -1 +1 @@\n-old\n+new\n\n" +
			"Note — " + repairNoteMarker + "\n- a REPLACE block was left open at end of input\n" +
			"Close every REPLACE block with \">>>>>>> REPLACE\".\n\nNo diagnostics for a.go",
	})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want one repair entry", report.ParseIssues)
	}
	issue := report.ParseIssues[0]
	if issue.Kind != repairKind || issue.Count != 1 {
		t.Fatalf("issue = %+v, want kind repair count 1", issue)
	}
	if !strings.Contains(issue.Detail, "left open at end of input") {
		t.Errorf("detail = %q, want the repair note", issue.Detail)
	}
	if strings.Contains(issue.Detail, "No diagnostics") || strings.Contains(issue.Detail, "--- a.go") {
		t.Errorf("detail = %q, want the diff and diagnostics excluded", issue.Detail)
	}
}

func TestBuildRedactsSalvageReason(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession(testSessionID, projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:     projectID,
		SessionID:     testSessionID,
		StartedAt:     time.Unix(3000, 0).UTC(),
		Outcome:       outcomeSalvaged,
		SalvageReason: "recovered after token " + secret,
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	cfg := testConfig()
	cfg.Privacy.RedactSecrets = true
	state := testState(t, cfg, database)

	report, err := Build(state, database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.TurnOutcomes) != 1 {
		t.Fatalf("turn_outcomes = %+v, want one entry", report.TurnOutcomes)
	}
	reason := report.TurnOutcomes[0].SalvageReason
	if strings.Contains(reason, secret) {
		t.Errorf("salvage_reason not redacted: %q", reason)
	}
	if !strings.Contains(reason, redact.MaskToken) {
		t.Errorf("salvage_reason = %q, want the redaction mask", reason)
	}
}

func TestBuildCleanCapsDetailAtFieldCap(t *testing.T) {
	state := testState(t, testConfig(), nil)
	state.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run",
		Error:    strings.Repeat("x", 250),
	})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ToolFailures) != 1 {
		t.Fatalf("len(tool_failures) = %d, want 1", len(report.ToolFailures))
	}
	detail := report.ToolFailures[0].Error
	runes := []rune(detail)
	if len(runes) != fieldCap {
		t.Errorf("len(runes) = %d, want %d", len(runes), fieldCap)
	}
	if !strings.HasSuffix(detail, "…") {
		t.Errorf("detail = %q, want a trailing ellipsis", detail)
	}
}
