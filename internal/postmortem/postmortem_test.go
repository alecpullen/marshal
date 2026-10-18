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

// testMetricsDB opens a migrated database with one project and the test
// session already created, so a test can insert turn_metrics rows directly.
func testMetricsDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
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
	return database, projectID
}

func TestBuildParseIssueCarriesSample(t *testing.T) {
	database, projectID := testMetricsDB(t)
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:       projectID,
		SessionID:       testSessionID,
		StartedAt:       time.Unix(3000, 0).UTC(),
		ParseFailures:   1,
		ParseFailKind:   "envelope",
		ParseFailSample: "hello broken json",
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	report, err := Build(testState(t, testConfig(), database), database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want one entry", report.ParseIssues)
	}
	want := ParseIssue{Kind: parseFailureKind, Detail: "envelope", Count: 1, ParseSample: "hello broken json"}
	if report.ParseIssues[0] != want {
		t.Errorf("parse_issues[0] = %+v, want %+v", report.ParseIssues[0], want)
	}
}

// TestBuildParseIssueCarriesTruncatedArgsSample exercises the
// parse_fail_kind="truncated_args" arm of the turn_metrics consumption, which
// the envelope case above does not reach: the output-token-limit site labels
// its sample differently, and postmortem must surface both the label and the
// sample rather than falling through to the legacy turn_metrics detail.
func TestBuildParseIssueCarriesTruncatedArgsSample(t *testing.T) {
	database, projectID := testMetricsDB(t)
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:       projectID,
		SessionID:       testSessionID,
		StartedAt:       time.Unix(3000, 0).UTC(),
		ParseFailures:   1,
		ParseFailKind:   "truncated_args",
		ParseFailSample: `{"path":"a.go"}`,
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	report, err := Build(testState(t, testConfig(), database), database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want one entry", report.ParseIssues)
	}
	want := ParseIssue{Kind: parseFailureKind, Detail: "truncated_args", Count: 1, ParseSample: `{"path":"a.go"}`}
	if report.ParseIssues[0] != want {
		t.Errorf("parse_issues[0] = %+v, want %+v", report.ParseIssues[0], want)
	}
}

// TestBuildUnrecognizedParseKindKeepsCountAndSampleTogether covers a row whose
// parse_fail_kind is neither envelope nor truncated_args (a future kind, or a
// typo). Its count and its sample must land on the same parse_issues entry
// under that kind, not split across two entries.
func TestBuildUnrecognizedParseKindKeepsCountAndSampleTogether(t *testing.T) {
	database, projectID := testMetricsDB(t)
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:       projectID,
		SessionID:       testSessionID,
		StartedAt:       time.Unix(3000, 0).UTC(),
		ParseFailures:   2,
		ParseFailKind:   "something_unexpected",
		ParseFailSample: "unrecognized kind output",
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	report, err := Build(testState(t, testConfig(), database), database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want exactly one entry", report.ParseIssues)
	}
	want := ParseIssue{
		Kind:        parseFailureKind,
		Detail:      "something_unexpected",
		Count:       2,
		ParseSample: "unrecognized kind output",
	}
	if report.ParseIssues[0] != want {
		t.Errorf("parse_issues[0] = %+v, want %+v", report.ParseIssues[0], want)
	}
}

// TestBuildRedactsParseSampleAtReportBoundary proves the report re-redacts a
// turn_metrics sample even when the session's redact_secrets flag is off (so
// clean() would leave it alone): the sample crosses the DB boundary carrying
// whatever the producer wrote, and must not surface a secret verbatim.
func TestBuildRedactsParseSampleAtReportBoundary(t *testing.T) {
	// Build the sigil at runtime so no credential-shaped literal lands in the
	// test source (matching the convention in internal/redact's own tests).
	secret := "ghp_" + strings.Repeat("a", 30)
	database, projectID := testMetricsDB(t)
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:       projectID,
		SessionID:       testSessionID,
		StartedAt:       time.Unix(3000, 0).UTC(),
		ParseFailures:   1,
		ParseFailKind:   "envelope",
		ParseFailSample: "unparseable output for token " + secret,
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	report, err := Build(testState(t, testConfig(), database), database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want one entry", report.ParseIssues)
	}
	sample := report.ParseIssues[0].ParseSample
	if strings.Contains(sample, secret) {
		t.Errorf("parse sample not redacted: %q", sample)
	}
	if !strings.Contains(sample, redact.MaskToken) {
		t.Errorf("parse sample missing the redaction marker: %q", sample)
	}
	if !strings.Contains(sample, "unparseable output for token") {
		t.Errorf("parse sample lost its non-secret context: %q", sample)
	}
}

func TestBuildLegacyParseIssueKeepsTurnMetricsDetail(t *testing.T) {
	database, projectID := testMetricsDB(t)
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:     projectID,
		SessionID:     testSessionID,
		StartedAt:     time.Unix(3000, 0).UTC(),
		ParseFailures: 2,
		// ParseFailKind deliberately empty: a row written before the kind
		// column existed.
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	report, err := Build(testState(t, testConfig(), database), database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want one entry", report.ParseIssues)
	}
	want := ParseIssue{Kind: parseFailureKind, Detail: turnMetricsParseDetail, Count: 2}
	if report.ParseIssues[0] != want {
		t.Errorf("parse_issues[0] = %+v, want %+v", report.ParseIssues[0], want)
	}
}

func TestBuildParseSamplesKeepFirst(t *testing.T) {
	database, projectID := testMetricsDB(t)
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:       projectID,
		SessionID:       testSessionID,
		StartedAt:       time.Unix(3000, 0).UTC(),
		ParseFailures:   1,
		ParseFailKind:   "envelope",
		ParseFailSample: "first sample",
	}); err != nil {
		t.Fatalf("InsertTurnMetrics(first): %v", err)
	}
	// A later row with no sample must not blank the sample already carried.
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:     projectID,
		SessionID:     testSessionID,
		StartedAt:     time.Unix(3001, 0).UTC(),
		ParseFailures: 4,
		ParseFailKind: "envelope",
	}); err != nil {
		t.Fatalf("InsertTurnMetrics(second): %v", err)
	}

	report, err := Build(testState(t, testConfig(), database), database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want one entry", report.ParseIssues)
	}
	want := ParseIssue{Kind: parseFailureKind, Detail: "envelope", Count: 5, ParseSample: "first sample"}
	if report.ParseIssues[0] != want {
		t.Errorf("parse_issues[0] = %+v, want %+v", report.ParseIssues[0], want)
	}
}

func TestBuildParseRepairsSurfaceAsEnvelope(t *testing.T) {
	database, projectID := testMetricsDB(t)
	if _, err := database.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID:    projectID,
		SessionID:    testSessionID,
		StartedAt:    time.Unix(3000, 0).UTC(),
		ParseRepairs: 2,
	}); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	report, err := Build(testState(t, testConfig(), database), database)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ParseIssues) != 1 {
		t.Fatalf("parse_issues = %+v, want one entry", report.ParseIssues)
	}
	want := ParseIssue{Kind: repairKind, Detail: "envelope", Count: 2}
	if report.ParseIssues[0] != want {
		t.Errorf("parse_issues[0] = %+v, want %+v", report.ParseIssues[0], want)
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

func TestBuildCoverageDefaults(t *testing.T) {
	state := testState(t, testConfig(), nil)
	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !report.Coverage.ExtractionPass {
		t.Errorf("Coverage.ExtractionPass = false, want true")
	}
	if report.Coverage.AgentPass {
		t.Errorf("Coverage.AgentPass = true, want false")
	}
	if report.Coverage.AgentPassReason != "not_run" {
		t.Errorf("Coverage.AgentPassReason = %q, want %q", report.Coverage.AgentPassReason, "not_run")
	}
}

func TestAgentPassFlipsCoverage(t *testing.T) {
	state := testState(t, testConfig(), nil)
	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	MarkAgentPass(&report)
	if !report.Coverage.AgentPass {
		t.Errorf("Coverage.AgentPass = false, want true")
	}
	if report.Coverage.AgentPassReason != "completed" {
		t.Errorf("Coverage.AgentPassReason = %q, want %q", report.Coverage.AgentPassReason, "completed")
	}

	MarkAgentPass(nil) // must not panic
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

// executionNotice builds the audit a managed command records when it settles.
func executionNotice(tool string, ex registry.CommandExecution) registry.AuditEvent {
	return registry.AuditEvent{ToolName: tool, Notice: registry.NewCommandExecutionNotice(ex)}
}

// A foreground command's own record is its final record, so one audit yields
// one evidence entry carrying the ownership and the observed outcome.
func TestBuildExtractsForegroundExecutionEvidence(t *testing.T) {
	state := testState(t, testConfig(), nil)
	code := 0
	state.LogToolCall(executionNotice("shell.run", registry.CommandExecution{
		ExecutionID: "exec-1", DisplayID: "job-1", ConversationID: "conv-1", Actor: "implementer",
		TurnID: "turn-1", StepID: "2", ToolCallID: "call-1", Cwd: "/repo", Generation: 4, PID: 99,
		State: "completed", Outcome: "completed", Attachment: registry.ExecutionAttachmentForeground,
		Wait: "10s", ExitCode: &code,
	}))

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ExecutionEvidence) != 1 {
		t.Fatalf("execution_evidence = %+v, want one entry", report.ExecutionEvidence)
	}
	e := report.ExecutionEvidence[0]
	if e.Identity != "exec:exec-1" || e.ExecutionID != "exec-1" || e.DisplayID != "job-1" {
		t.Fatalf("identity = %+v", e)
	}
	if e.ConversationID != "conv-1" || e.Actor != "implementer" || e.TurnID != "turn-1" ||
		e.StepID != "2" || e.ToolCallID != "call-1" || e.Cwd != "/repo" || e.Generation != 4 || e.PID != 99 {
		t.Fatalf("ownership = %+v", e)
	}
	if e.Attachment != "foreground" || e.Wait != "10s" || e.Tool != "shell.run" {
		t.Fatalf("attachment/wait/tool = %+v", e)
	}
	if e.Exit == nil || *e.Exit != 0 {
		t.Fatalf("exit = %v, want the observed 0", e.Exit)
	}
	if e.Failed {
		t.Fatalf("a natural success was recorded as failure evidence: %+v", e)
	}
}

// The cases the section exists for: each settled failure cause is failure
// evidence with its own name, and a continuation — a wait that ended while the
// command kept running — is not a failure at all.
func TestBuildClassifiesExecutionOutcomes(t *testing.T) {
	nonzero := 3
	stopped := -1
	cases := []struct {
		name       string
		ex         registry.CommandExecution
		wantFailed bool
	}{
		{
			name: "nonzero exit",
			ex: registry.CommandExecution{ExecutionID: "e1", State: "failed", Outcome: "nonzero_exit",
				Attachment: registry.ExecutionAttachmentForeground, ExitCode: &nonzero},
			wantFailed: true,
		},
		{
			name: "startup failure with no exit",
			ex: registry.CommandExecution{ExecutionID: "e2", State: "failed", Outcome: "startup_failure",
				Attachment: registry.ExecutionAttachmentForeground},
			wantFailed: true,
		},
		{
			name: "resource limit",
			ex: registry.CommandExecution{ExecutionID: "e3", State: "killed", Outcome: "resource_limit",
				Attachment: registry.ExecutionAttachmentForeground, ExitCode: &stopped},
			wantFailed: true,
		},
		{
			name: "explicit stop",
			ex: registry.CommandExecution{ExecutionID: "e4", State: "killed", Outcome: "stopped",
				Attachment: registry.ExecutionAttachmentForeground, StopCause: "turn stop requested"},
			wantFailed: true,
		},
		{
			name: "continuation after a wait expired",
			ex: registry.CommandExecution{ExecutionID: "e5", State: "running",
				Attachment: registry.ExecutionAttachmentBackground, Wait: "10s",
				DetachReason: "wait expired"},
			wantFailed: false,
		},
	}
	state := testState(t, testConfig(), nil)
	for _, c := range cases {
		state.LogToolCall(executionNotice("shell.run", c.ex))
	}

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ExecutionEvidence) != len(cases) {
		t.Fatalf("execution_evidence = %d entries, want %d", len(report.ExecutionEvidence), len(cases))
	}
	byID := map[string]ExecutionEvidence{}
	for _, e := range report.ExecutionEvidence {
		byID[e.ExecutionID] = e
	}
	for _, c := range cases {
		e, ok := byID[c.ex.ExecutionID]
		if !ok {
			t.Fatalf("%s: no evidence entry", c.name)
		}
		if e.Failed != c.wantFailed {
			t.Errorf("%s: Failed = %v, want %v (%+v)", c.name, e.Failed, c.wantFailed, e)
		}
		// A start-up failure has no exit; the report must say so by omission
		// rather than by a zero a reader would take for success.
		if c.ex.Outcome == "startup_failure" && e.Exit != nil {
			t.Errorf("%s: Exit = %v, want unknown", c.name, *e.Exit)
		}
	}
}

// A detached final record is the only trace of a command that outlived its
// call, and a lost one is unknown: it is reported as lost, never as a failure
// or a success.
func TestBuildExtractsDetachedAndLostRecords(t *testing.T) {
	state := testState(t, testConfig(), nil)
	code := 1
	if err := state.AddJobExit(session.JobExit{
		ID: "job-7", ExecutionID: "exec-7", ConversationID: "conv-1", Actor: "implementer",
		TurnID: "turn-2", ToolCallID: "call-7", Cwd: "/repo", Generation: 2, PID: 1234,
		State: "failed", Outcome: "nonzero_exit", Attachment: "background", Wait: "10s",
		DetachReason: "wait expired", DetachedAt: time.Unix(50, 0).UTC(),
		StopCause: "conversation ended", StopRequestedAt: time.Unix(60, 0).UTC(),
		Exit: &code, Truncated: true, At: time.Unix(70, 0).UTC(),
	}); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}
	if err := state.AddJobExit(session.JobExit{
		ID: "job-8", ExecutionID: "exec-8", Lost: true, State: "", Outcome: "",
		ExitCode: -1, Attachment: "background", At: time.Unix(80, 0).UTC(),
	}); err != nil {
		t.Fatalf("AddJobExit (lost): %v", err)
	}

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byID := map[string]ExecutionEvidence{}
	for _, e := range report.ExecutionEvidence {
		byID[e.ExecutionID] = e
	}
	settled, ok := byID["exec-7"]
	if !ok {
		t.Fatalf("the detached completion is missing: %+v", report.ExecutionEvidence)
	}
	if settled.Attachment != "background" || settled.Wait != "10s" || settled.DetachReason != "wait expired" {
		t.Fatalf("handoff evidence = %+v", settled)
	}
	if settled.StopCause != "conversation ended" || settled.StopRequestedAt.IsZero() {
		t.Fatalf("stop evidence = %+v", settled)
	}
	if !settled.Failed || settled.Exit == nil || *settled.Exit != 1 || !settled.Truncated {
		t.Fatalf("outcome evidence = %+v", settled)
	}

	lost, ok := byID["exec-8"]
	if !ok {
		t.Fatalf("the lost record is missing: %+v", report.ExecutionEvidence)
	}
	if !lost.Lost {
		t.Fatalf("lost = %+v, want it marked lost", lost)
	}
	if lost.Failed {
		t.Fatalf("a lost record was reported as failure evidence: %+v", lost)
	}
	if lost.Exit != nil {
		t.Fatalf("a lost record carried an exit %v; its outcome is unknown, not zero", lost.Exit)
	}
}

// One command must produce one entry: the call's own record and the job's final
// record are the same execution, and identity is what proves it.
func TestBuildDeduplicatesExecutionEvidenceByIdentity(t *testing.T) {
	state := testState(t, testConfig(), nil)
	code := 0
	ex := registry.CommandExecution{
		ExecutionID: "exec-9", DisplayID: "job-9", ConversationID: "conv-1", Actor: "implementer",
		State: "running", Attachment: registry.ExecutionAttachmentBackground, Wait: "10s",
	}
	state.LogToolCall(executionNotice("shell.run", ex))
	// The completion reaches the transcript with the same identity and the
	// settled facts the notice could not know.
	if err := state.AddJobExit(session.JobExit{
		ID: "job-9", ExecutionID: "exec-9", ConversationID: "conv-1", Actor: "implementer",
		State: "completed", Outcome: "completed", Attachment: "background", Wait: "10s",
		Exit: &code, At: time.Unix(90, 0).UTC(),
	}); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ExecutionEvidence) != 1 {
		t.Fatalf("execution_evidence = %+v, want one entry for one execution", report.ExecutionEvidence)
	}
	e := report.ExecutionEvidence[0]
	if e.ExecutionID != "exec-9" || e.Identity != "exec:exec-9" {
		t.Fatalf("identity = %+v", e)
	}
	// The settled facts win; the earlier record supplies what it had.
	if e.State != "completed" || e.Outcome != "completed" {
		t.Fatalf("state/outcome = %+v, want the settled completion", e)
	}
	if e.Exit == nil || *e.Exit != 0 || e.Failed {
		t.Fatalf("exit/failed = %+v", e)
	}
	if e.Wait != "10s" || e.Attachment != "background" {
		t.Fatalf("wait/attachment = %+v", e)
	}
}

// A legacy record has no execution identity at all. It is still evidence, and
// it is reported with the legacy identity so a reader can tell it apart from a
// structured record — never silently upgraded into one.
func TestBuildKeepsLegacyRecords(t *testing.T) {
	state := testState(t, testConfig(), nil)
	if err := state.AddJobExit(session.JobExit{
		ID: "job-legacy", ExitCode: 0, At: time.Unix(100, 0).UTC(),
	}); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ExecutionEvidence) != 1 {
		t.Fatalf("execution_evidence = %+v, want one entry", report.ExecutionEvidence)
	}
	e := report.ExecutionEvidence[0]
	if e.ExecutionID != "" {
		t.Fatalf("ExecutionID = %q, want empty for a legacy record", e.ExecutionID)
	}
	if !strings.HasPrefix(e.Identity, "legacy:") || !strings.Contains(e.Identity, "job-legacy") {
		t.Fatalf("Identity = %q, want a legacy identity naming the display id", e.Identity)
	}
	if e.State != "" || e.Outcome != "" {
		t.Fatalf("state/outcome = %+v, want them empty on a legacy record", e)
	}
	if e.Failed {
		t.Fatalf("a legacy record with unknown status was called failure evidence: %+v", e)
	}
}

// The section is additive: the schema version stays put, an empty report omits
// the field entirely, and the failure evidence that already existed is still
// produced from the same audit.
func TestExecutionEvidenceIsAdditive(t *testing.T) {
	state := testState(t, testConfig(), nil)
	state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Error: "too large to read"})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if report.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", report.SchemaVersion, SchemaVersion)
	}
	if len(report.ExecutionEvidence) != 0 {
		t.Fatalf("execution_evidence = %+v, want none for a session with no managed commands", report.ExecutionEvidence)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "execution_evidence") {
		t.Fatalf("an empty section was written: %s", raw)
	}
	if len(report.ToolFailures) != 1 {
		t.Fatalf("tool_failures = %+v, want the existing failure evidence preserved", report.ToolFailures)
	}
}

// Execution evidence is display data a run produced, so it travels through the
// same redaction path as every other report string.
func TestBuildRedactsExecutionEvidence(t *testing.T) {
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	cfg := testConfig()
	cfg.Privacy.RedactSecrets = true
	state := testState(t, cfg, nil)
	state.LogToolCall(executionNotice("shell.run", registry.CommandExecution{
		ExecutionID: "exec-secret", State: "killed", Outcome: "stopped",
		Attachment: registry.ExecutionAttachmentForeground,
		Cwd:        "/repo",
		StopCause:  "turn stop requested with TOKEN=" + secret,
	}))

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.ExecutionEvidence) != 1 {
		t.Fatalf("execution_evidence = %+v, want one entry", report.ExecutionEvidence)
	}
	cause := report.ExecutionEvidence[0].StopCause
	if strings.Contains(cause, secret) {
		t.Errorf("stop_cause not redacted: %q", cause)
	}
	if !strings.Contains(cause, redact.MaskToken) {
		t.Errorf("stop_cause = %q, want the redaction mask", cause)
	}
}
