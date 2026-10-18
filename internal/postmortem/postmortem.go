// Package postmortem extracts a per-session report of harness friction into a
// versioned JSON document: tool failures, JSON parse issues, approval
// denials, output-truncated calls, wasted tokens, turn outcomes, and run
// events.
//
// Extraction is mechanical and read-only. It never synthesises conclusions,
// rankings, or recommendations — the report is raw, deduplicated signal for
// someone else to analyse. The optional agent pass appends semantic notes to
// Report.AgentObservations, which extraction always leaves nil.
//
// The package imports only the session state, the project database, and small
// pure helpers (strutil, redact) so the TUI and agent layers can depend on it
// without an import cycle.
package postmortem

import (
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/redact"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// SchemaVersion is the postmortem report schema version. It is bumped when a
// field is renamed or removed; additive changes keep the same version so a
// cross-session aggregator can merge reports from different releases.
const SchemaVersion = 1

// fieldCap bounds args, error, and detail strings in the report. Full payloads
// stay out of the report on purpose: it is meant to be skimmed and aggregated,
// and an unbounded error string can be an entire file.
const fieldCap = 200

// recentTurnMetricsLimit bounds how many project-scoped turn_metrics rows are
// read before the current session's rows are selected.
const recentTurnMetricsLimit = 200

// Parse-issue kinds. Entries are (kind, detail) pairs so an aggregator can
// group them without parsing prose.
const (
	parseFailureKind = "parse_failure"
	repairKind       = "repair"
)

// turnMetricsParseDetail labels parse failures that came from the
// project-scoped turn_metrics table rather than from a tool audit event. Those
// rows carry no tool attribution, so the detail is a provenance label instead
// of an error string.
const turnMetricsParseDetail = "turn_metrics"

// Execution-outcome vocabulary the report classifies on. The values are the
// ones the command service records (internal/tools/native); naming them here
// keeps the classification a closed set rather than a string comparison buried
// in a loop.
const (
	// execOutcomeNonZeroExit: a natural exit with a non-zero code.
	execOutcomeNonZeroExit = "nonzero_exit"
	// execOutcomeStartupFailure: the runner never started a process.
	execOutcomeStartupFailure = "startup_failure"
	// execOutcomeStopped: an accepted explicit stop request terminated it.
	execOutcomeStopped = "stopped"
	// execOutcomeResourceLimit: a backend enforced and named a resource limit.
	execOutcomeResourceLimit = "resource_limit"
)

// repairNoteMarker is the header file.write_patch appends to a successful
// result when its parser healed a malformed proposal in place. It is the only
// observable trace of a repaired tool envelope in the audit log (the agent's
// own envelope repairs are logged, not audited), so the report mines it to
// surface "the model keeps getting the format wrong" without synthesis.
const repairNoteMarker = "the proposal's format was repaired before applying:"

// Coverage records which optional passes ran on this report so a nil
// section is diagnosable: 'extraction' always runs during Build;
// 'agent_pass' only runs when the postmortem skill dispatches it.
type Coverage struct {
	ExtractionPass  bool   `json:"extraction_pass"`
	AgentPass       bool   `json:"agent_pass"`
	AgentPassReason string `json:"agent_pass_reason,omitempty"` // "not_run" | "completed"
}

// Report is the versioned, machine-readable postmortem for one session.
// Field names and nesting are the aggregation contract: renaming one is a
// SchemaVersion bump.
type Report struct {
	SchemaVersion   int              `json:"schema_version"`
	Session         SessionInfo      `json:"session"`
	ToolFailures    []ToolFailure    `json:"tool_failures"`
	ParseIssues     []ParseIssue     `json:"parse_issues"`
	ApprovalDenials []ApprovalDenial `json:"approval_denials"`
	TruncatedCalls  []TruncatedCall  `json:"truncated_calls"`
	TokenWaste      TokenWaste       `json:"token_waste"`
	TurnOutcomes    []TurnOutcome    `json:"turn_outcomes"`
	RunEvents       []RunEventEntry  `json:"run_events"`
	// ExecutionEvidence explains managed command execution: who owned each
	// command, how long the caller effectively waited, whether it handed off
	// to the background, whether a stop was requested and how it actually
	// ended. Additive: the schema version stays put, and an aggregator that
	// ignores this field still sees the failure evidence above.
	ExecutionEvidence []ExecutionEvidence `json:"execution_evidence,omitempty"`
	// AgentObservations is nil until the optional agent pass appends semantic
	// observations. Extraction never fabricates it.
	AgentObservations *string  `json:"agent_observations"`
	Coverage          Coverage `json:"coverage"`
}

// SessionInfo identifies the session the report describes.
type SessionInfo struct {
	ID         string    `json:"id"`
	Project    string    `json:"project"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Turns      int       `json:"turns"`
	ModelsUsed []string  `json:"models_used"`
}

// ToolFailure is one deduplicated failed tool call, grouped by tool and
// normalized error so four identical failures read as one entry with count 4.
type ToolFailure struct {
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	Error     string    `json:"error"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	ExitCode  *int      `json:"exit_code"`
}

// ParseIssue is one deduplicated envelope/format problem.
type ParseIssue struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Count  int    `json:"count"`
	// ParseSample carries the redacted head of one offending model output
	// when Detail came from a turn_metrics row with parse_fail_sample set.
	// Omitted empty.
	//
	// It is intentionally exempt from the fieldCap truncation applied to
	// other report strings: it is diagnostic output already redacted and
	// bounded to parseFailSampleCap (4096 bytes) at the producer, and Build
	// re-redacts it as defense in depth rather than shortening it.
	ParseSample string `json:"parse_sample,omitempty"`
}

// ApprovalDenial is one deduplicated denied tool call.
type ApprovalDenial struct {
	Tool  string `json:"tool"`
	Args  string `json:"args"`
	Count int    `json:"count"`
}

// TruncatedCall is one deduplicated call whose model response hit the output
// token limit, so its arguments may have been silently truncated.
type TruncatedCall struct {
	Tool         string `json:"tool"`
	FinishReason string `json:"finish_reason"`
	Count        int    `json:"count"`
}

// TokenWaste is purely mechanical token accounting: totals for the session
// plus the tokens spent on turns that failed outright (salvaged turns produce
// an answer, so they are not counted).
type TokenWaste struct {
	TotalPrompt        int   `json:"total_prompt"`
	TotalCompletion    int   `json:"total_completion"`
	TotalReasoning     int   `json:"total_reasoning"`
	EstimatedCostCents int64 `json:"estimated_cost_cents"`
	// TokensFailedTurns sums prompt+completion for failed turns. Reasoning
	// tokens are deliberately excluded from this figure (they are still
	// counted in TotalReasoning): a failed turn's spend is what the caller
	// paid for the attempt, and the asymmetry is intentional rather than an
	// oversight.
	TokensFailedTurns int `json:"tokens_failed_turns"`
}

// TurnOutcome is one deduplicated turn result.
type TurnOutcome struct {
	Outcome       string `json:"outcome"`
	SalvageReason string `json:"salvage_reason"`
	Count         int    `json:"count"`
}

// ExecutionEvidence is one deduplicated managed command execution: the
// ownership, effective wait, handoff, stop request and observed outcome that
// explain why a command ran, waited, handed off or stopped.
//
// It is deliberately evidence rather than a verdict. A continuation or an await
// expiry is NOT a failed command — the same process keeps running — so the
// report states the lifecycle the service recorded and leaves the judgement to
// its reader. An absent Exit means no exit code was ever observed (a start-up
// failure, a still-running command, a lost record) and must never be read as
// success; Outcome "startup_failure" and Lost are the cases where that matters
// most.
type ExecutionEvidence struct {
	// Identity pairs this entry with the call and the completion it came
	// from, across resumes where a display job ID repeats. It is the
	// execution id when the record has one, else a legacy identity.
	Identity string `json:"identity"`
	// ExecutionID is empty on a legacy record, which is exactly why it has a
	// separate legacy identity rather than a fabricated execution id.
	ExecutionID string `json:"execution_id,omitempty"`
	DisplayID   string `json:"display_id,omitempty"`
	// ConversationID, Actor, TurnID, StepID and ToolCallID are the ownership
	// the command recorded: which conversation owned it, which actor and turn
	// launched it, and which call it belonged to.
	ConversationID string `json:"conversation_id,omitempty"`
	Actor          string `json:"actor,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	StepID         string `json:"step_id,omitempty"`
	ToolCallID     string `json:"tool_call_id,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Generation     int    `json:"generation,omitempty"`
	PID            int    `json:"pid,omitempty"`
	// Tool names the tool that launched the command ("shell.run").
	Tool string `json:"tool,omitempty"`
	// State is the lifecycle status ("running", "stopping", "completed",
	// "failed", "killed", "timed_out"); Outcome is the finer cause
	// ("nonzero_exit", "startup_failure", "stopped", "resource_limit", …).
	State   string `json:"state,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	// Attachment is how the launching caller related to the execution:
	// "foreground" while it waited, "background" once it detached.
	Attachment string `json:"attachment,omitempty"`
	// Wait is the effective foreground wait the launch used ("10s"). It is
	// evidence about the WAIT, not the command's lifetime: a short wait is
	// what hands a command off, not what ends it.
	Wait string `json:"wait,omitempty"`
	// DetachReason and DetachedAt record why and when a wait handed off.
	DetachReason string    `json:"detach_reason,omitempty"`
	DetachedAt   time.Time `json:"detached_at,omitempty"`
	// StopCause and StopRequestedAt record an accepted explicit stop request,
	// separately from the outcome it produced.
	StopCause       string    `json:"stop_cause,omitempty"`
	StopRequestedAt time.Time `json:"stop_requested_at,omitempty"`
	// Exit is the observed exit code; absent means none was observed and the
	// reader must treat it as unknown, never as zero.
	Exit *int `json:"exit_code,omitempty"`
	// Truncated reports whether the retained output hit its cap.
	Truncated bool `json:"truncated,omitempty"`
	// Lost marks a command whose owning process ended before its outcome was
	// recorded. Its Outcome is unknown — the session died with the process —
	// so it is reported as lost rather than as a failure or a success.
	Lost bool `json:"lost,omitempty"`
	// Failed reports whether this settlement is failure evidence: a settled
	// nonzero exit, start-up failure, resource limit or stop. A continuation,
	// an await expiry, a natural success and a lost record are all false.
	Failed    bool      `json:"failed"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// RunEventEntry is one deduplicated plan-run event. Run events are in-memory
// only, so this field is the only durable record of them.
type RunEventEntry struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Count  int    `json:"count"`
}

// MarkAgentPass records that the optional agent pass ran and appended
// observations to the report. The agent pass is performed by the model
// editing the report JSON directly (see the postmortem skill), so this is
// the Go seam that keeps the coverage contract testable.
func MarkAgentPass(report *Report) {
	if report == nil {
		return
	}
	report.Coverage.AgentPass = true
	report.Coverage.AgentPassReason = "completed"
}

// Build extracts a report from the live session state and, when database is
// non-nil, the project-scoped turn_metrics table filtered to this session.
//
// Extraction is best-effort: a missing or failing database degrades the report
// to live state only and never fails the caller. The only error returned is a
// programming error (no state).
func Build(state *session.State, database *db.DB) (Report, error) {
	if state == nil {
		return Report{}, errors.New("postmortem: nil session state")
	}
	redactOn := state.Config.Privacy.RedactSecrets

	report := Report{
		SchemaVersion:     SchemaVersion,
		ToolFailures:      []ToolFailure{},
		ParseIssues:       []ParseIssue{},
		ApprovalDenials:   []ApprovalDenial{},
		TruncatedCalls:    []TruncatedCall{},
		TurnOutcomes:      []TurnOutcome{},
		RunEvents:         []RunEventEntry{},
		AgentObservations: nil,
		Coverage:          Coverage{ExtractionPass: true, AgentPass: false, AgentPassReason: "not_run"},
	}
	report.Session = SessionInfo{
		ID:         state.SessionID(),
		Project:    ProjectSlug(state.WorkingDir),
		StartedAt:  state.StartedAt,
		EndedAt:    time.Now(),
		Turns:      countUserTurns(state.Messages()),
		ModelsUsed: []string{},
	}

	// clean redacts then truncates. Redaction runs first so a masked token is
	// never cut in half by truncation (a half-masked secret is still a leak).
	clean := func(s string) string {
		if redactOn {
			s = redact.Secrets(s)
		}
		// fieldCap-1 leaves room for the appended ellipsis, so the result is at
		// most fieldCap runes rather than fieldCap+1.
		return strutil.Truncate(s, fieldCap-1, true)
	}

	models := make(map[string]bool)

	failureIdx := make(map[string]int)
	denialIdx := make(map[string]int)
	truncIdx := make(map[string]int)
	parseIdx := make(map[string]int)
	execIdx := make(map[string]int)

	// addFailure groups on (tool, first line of the error, truncated).
	addFailure := func(tool, args, errText string, at time.Time, exit *int) {
		key := tool + "\x00" + clean(firstLine(errText))
		if i, ok := failureIdx[key]; ok {
			entry := &report.ToolFailures[i]
			entry.Count++
			if at.After(entry.LastSeen) {
				entry.LastSeen = at
			}
			if at.Before(entry.FirstSeen) {
				entry.FirstSeen = at
			}
			if exit != nil {
				entry.ExitCode = exit
			}
			return
		}
		failureIdx[key] = len(report.ToolFailures)
		report.ToolFailures = append(report.ToolFailures, ToolFailure{
			Tool:      tool,
			Args:      clean(args),
			Error:     clean(errText),
			Count:     1,
			FirstSeen: at,
			LastSeen:  at,
			ExitCode:  exit,
		})
	}

	// addParse groups on (kind, detail). n lets a turn_metrics row contribute
	// its whole ParseFailures tally in one call.
	addParse := func(kind, detail string, n int) {
		if n <= 0 {
			return
		}
		key := kind + "\x00" + detail
		if i, ok := parseIdx[key]; ok {
			report.ParseIssues[i].Count += n
			return
		}
		parseIdx[key] = len(report.ParseIssues)
		report.ParseIssues = append(report.ParseIssues, ParseIssue{Kind: kind, Detail: detail, Count: n})
	}

	// addExecution records one command execution's evidence, deduplicated by
	// execution identity. The identity is what pairs a foreground call with
	// the detached final record of the same command — display IDs repeat in
	// every process, and a completion can reach the transcript before the
	// audit row of the call that started it — so a record already present is
	// filled in rather than appended a second time.
	addExecution := func(e ExecutionEvidence) {
		e.Cwd = clean(firstLine(e.Cwd))
		e.DetachReason = clean(firstLine(e.DetachReason))
		e.StopCause = clean(firstLine(e.StopCause))
		if i, ok := execIdx[e.Identity]; ok {
			report.ExecutionEvidence[i] = mergeExecutionEvidence(report.ExecutionEvidence[i], e)
			return
		}
		execIdx[e.Identity] = len(report.ExecutionEvidence)
		report.ExecutionEvidence = append(report.ExecutionEvidence, e)
	}

	for _, ev := range state.AuditLog() {
		if ev.Model != "" {
			models[ev.Model] = true
		}

		// Execution evidence rides in the call's notice, so it is read from the
		// same audit pass: a foreground command's own record is its final
		// record, which is why a settled foreground call needs no second
		// source.
		if ex, ok := ev.Notice.Execution(); ok {
			addExecution(executionEvidenceFromNotice(ev.ToolName, ex))
		}

		switch {
		case ev.Approval == registry.ApprovalDenied:
			// Denials are their own signal list: a denied call never ran, so
			// folding it into tool_failures would conflate "the user said no"
			// with "the tool broke".
			args := clean(string(ev.Args))
			key := ev.ToolName + "\x00" + args
			if i, ok := denialIdx[key]; ok {
				report.ApprovalDenials[i].Count++
			} else {
				denialIdx[key] = len(report.ApprovalDenials)
				report.ApprovalDenials = append(report.ApprovalDenials, ApprovalDenial{
					Tool:  ev.ToolName,
					Args:  args,
					Count: 1,
				})
			}
		case ev.Error != "":
			addFailure(ev.ToolName, string(ev.Args), ev.Error, ev.Timestamp, copyIntPtr(ev.CommandExitCode))
			if isParseFailureError(ev.Error) {
				addParse(parseFailureKind, clean(firstLine(ev.Error)), 1)
			}
		}

		// Truncation risk is independent of success: a call can fail for an
		// unrelated reason and still have been cut off at the limit.
		if isLengthFinish(ev.FinishReason) {
			key := ev.ToolName + "\x00" + ev.FinishReason
			if i, ok := truncIdx[key]; ok {
				report.TruncatedCalls[i].Count++
			} else {
				truncIdx[key] = len(report.TruncatedCalls)
				report.TruncatedCalls = append(report.TruncatedCalls, TruncatedCall{
					Tool:         ev.ToolName,
					FinishReason: ev.FinishReason,
					Count:        1,
				})
			}
		}

		for _, note := range patchRepairNotes(ev) {
			addParse(repairKind, clean(note), 1)
		}
	}

	// Detached final records: a background completion is the only record of a
	// command that outlived the call that launched it, so it is read from the
	// session's transcript — the same exported projection a card's completion
	// is built from. Deduplication is by identity, so a command whose call
	// already contributed a notice is filled in rather than duplicated — and a
	// lost record stays lost rather than becoming a failure.
	for _, item := range state.Transcript() {
		if item.JobExit == nil {
			continue
		}
		addExecution(executionEvidenceFromJobExit(*item.JobExit))
	}

	runIdx := make(map[string]int)
	for _, ev := range state.RunEvents() {
		kind := runEventKindName(ev.Kind)
		// Title carries the actionable text for every kind that has one (a
		// failing command, a finding, a commit SHA); Detail is the fallback.
		detail := clean(firstNonEmpty(ev.Title, ev.Detail))
		key := kind + "\x00" + detail
		if i, ok := runIdx[key]; ok {
			report.RunEvents[i].Count++
			continue
		}
		runIdx[key] = len(report.RunEvents)
		report.RunEvents = append(report.RunEvents, RunEventEntry{Kind: kind, Detail: detail, Count: 1})
	}

	collectTurnMetrics(state, database, &report, models, clean, parseIdx, addParse)

	for model := range models {
		report.Session.ModelsUsed = append(report.Session.ModelsUsed, model)
	}
	sort.Strings(report.Session.ModelsUsed)

	return report, nil
}

// collectTurnMetrics folds the session's rows out of the turn_metrics table
// into the report. The session id is pushed into SQL when it is known, so a
// long session's older rows are never crowded out of the limit window by
// other sessions in the same project. Every failure here degrades to
// live-state-only extraction: a postmortem must never block a session close.
func collectTurnMetrics(
	state *session.State,
	database *db.DB,
	report *Report,
	models map[string]bool,
	clean func(string) string,
	parseIdx map[string]int,
	addParse func(kind, detail string, n int),
) {
	if database == nil {
		return
	}
	projectID, err := database.GetOrCreateProject(state.WorkingDir, filepath.Base(state.WorkingDir))
	if err != nil {
		state.Logger().Warn("postmortem: resolve project failed", "error", err, "root", state.WorkingDir)
		return
	}

	sessionID := state.SessionID()
	var rows []db.TurnMetricsRow
	if sessionID != "" {
		rows, err = database.RecentTurnMetricsForSession(projectID, sessionID, recentTurnMetricsLimit)
	} else {
		// No session id to scope by, so fall back to the project-scoped window
		// and filter in Go. This path can under-report a very long session once
		// other sessions crowd the window, but there is nothing to filter on.
		rows, err = database.RecentTurnMetrics(projectID, recentTurnMetricsLimit)
	}
	if err != nil {
		state.Logger().Warn("postmortem: read turn metrics failed", "error", err)
		return
	}

	outcomeIdx := make(map[string]int)
	for _, row := range rows {
		// On the project-scoped fallback the session filter is ours.
		if sessionID == "" && row.SessionID != sessionID {
			continue
		}
		if row.Model != "" {
			models[row.Model] = true
		}
		report.TokenWaste.TotalPrompt += row.PromptTokens
		report.TokenWaste.TotalCompletion += row.CompletionTokens
		report.TokenWaste.TotalReasoning += row.ReasoningTokens
		report.TokenWaste.EstimatedCostCents += row.EstimatedCostCents
		if row.Outcome == "failed" {
			report.TokenWaste.TokensFailedTurns += row.PromptTokens + row.CompletionTokens
		}
		if row.ParseFailures > 0 {
			// Resolve the detail once and use that same value for both the
			// count bucket and the sample lookup key: an unrecognized but
			// non-empty kind (a future kind, or a typo) must not split its
			// count from its sample.
			detail := row.ParseFailKind
			if detail == "" {
				// Legacy rows (pre-migration) have empty kind; keep the
				// historical provenance label for one release cycle.
				detail = turnMetricsParseDetail
			}
			addParse(parseFailureKind, detail, row.ParseFailures)
			// Attach the sample to the detail entry just written when present.
			// addParse's grouping key is (kind, detail); lookup the entry and
			// set ParseSample if not already carried by an earlier row.
			if row.ParseFailSample != "" {
				// Re-redact at the report boundary as defense in depth. The
				// producer redacts unconditionally, so on a well-behaved row
				// this is a deliberate no-op double-redact. The sample is
				// deliberately exempt from clean/fieldCap: it is diagnostic
				// output already bounded to parseFailSampleCap at the producer
				// (see the ParseSample field comment).
				sample := redact.Secrets(row.ParseFailSample)
				key := parseFailureKind + "\x00" + detail
				if i, ok := parseIdx[key]; ok && report.ParseIssues[i].ParseSample == "" {
					report.ParseIssues[i].ParseSample = sample
				}
			}
		}
		if row.ParseRepairs > 0 {
			addParse(repairKind, "envelope", row.ParseRepairs)
		}
		if row.Outcome == "" {
			continue
		}
		// clean the reason before keying on it so two reasons that differ only
		// past the truncation cap group into one entry, and so a secret-bearing
		// reason is masked in the report.
		reason := clean(row.SalvageReason)
		key := row.Outcome + "\x00" + reason
		if i, ok := outcomeIdx[key]; ok {
			report.TurnOutcomes[i].Count++
			continue
		}
		outcomeIdx[key] = len(report.TurnOutcomes)
		report.TurnOutcomes = append(report.TurnOutcomes, TurnOutcome{
			Outcome:       row.Outcome,
			SalvageReason: reason,
			Count:         1,
		})
	}
}

// executionEvidenceFromNotice builds the evidence for one structured execution
// notice. The tool name is the call's, so a reader can tell which tool launched
// the command.
func executionEvidenceFromNotice(tool string, ex registry.CommandExecution) ExecutionEvidence {
	return ExecutionEvidence{
		Identity:        executionIdentity(ex.ExecutionID, ex.DisplayID, ex.StartedAt),
		ExecutionID:     ex.ExecutionID,
		DisplayID:       ex.DisplayID,
		ConversationID:  ex.ConversationID,
		Actor:           ex.Actor,
		TurnID:          ex.TurnID,
		StepID:          ex.StepID,
		ToolCallID:      ex.ToolCallID,
		Cwd:             ex.Cwd,
		Generation:      ex.Generation,
		PID:             ex.PID,
		Tool:            tool,
		State:           ex.State,
		Outcome:         ex.Outcome,
		Attachment:      string(ex.Attachment),
		Wait:            ex.Wait,
		DetachReason:    ex.DetachReason,
		DetachedAt:      ex.DetachedAt,
		StopCause:       ex.StopCause,
		StopRequestedAt: ex.StopRequestedAt,
		Exit:            copyIntPtr(ex.ExitCode),
		Truncated:       ex.Truncated,
		Failed:          executionFailed(ex.State, ex.Outcome, ex.ExitCode, false),
		StartedAt:       ex.StartedAt,
		EndedAt:         ex.EndedAt,
	}
}

// executionEvidenceFromJobExit builds the evidence for one detached final
// record. A lost record's outcome is unknown — the process that would have
// recorded it is gone — so it is reported as lost and never as a failure or a
// success.
func executionEvidenceFromJobExit(e session.JobExit) ExecutionEvidence {
	return ExecutionEvidence{
		Identity:        e.Identity(),
		ExecutionID:     e.ExecutionID,
		DisplayID:       e.ID,
		ConversationID:  e.ConversationID,
		Actor:           e.Actor,
		TurnID:          e.TurnID,
		StepID:          e.StepID,
		ToolCallID:      e.ToolCallID,
		Cwd:             e.Cwd,
		Generation:      e.Generation,
		PID:             e.PID,
		State:           e.State,
		Outcome:         e.Outcome,
		Attachment:      e.Attachment,
		Wait:            e.Wait,
		DetachReason:    e.DetachReason,
		DetachedAt:      e.DetachedAt,
		StopCause:       e.StopCause,
		StopRequestedAt: e.StopRequestedAt,
		Exit:            copyIntPtr(e.Exit),
		Truncated:       e.Truncated,
		Lost:            e.Lost,
		// A lost record is never failure evidence: nothing was observed. This
		// also keeps a lost record from reading as success, because Failed is
		// false and Exit is nil, so a reader sees "unknown" either way.
		Failed:    !e.Lost && executionFailed(e.State, e.Outcome, e.Exit, false),
		StartedAt: e.StartedAt,
		EndedAt:   e.EndedAt,
	}
}

// executionIdentity is the key an execution's evidence is deduplicated by. A
// structured record has a cross-resume execution id; a legacy record has none,
// so its display ID and start time stand in — the same fallback pairing a card
// uses, which is the point: an identity that does not match across the two
// sources would duplicate one command instead of merging it.
func executionIdentity(executionID, displayID string, startedAt time.Time) string {
	if id := strings.TrimSpace(executionID); id != "" {
		return "exec:" + id
	}
	if startedAt.IsZero() {
		return "legacy:" + displayID
	}
	return "legacy:" + displayID + "@" + startedAt.UTC().Format(time.RFC3339Nano)
}

// executionSettled reports whether a lifecycle status is terminal, i.e. the
// execution will not change state again. The set is the command service's
// ("completed", "failed", "killed", "timed_out"); "running" and "stopping" are
// deliberately not settled, which is what lets a settled record supersede a
// running one during a merge.
func executionSettled(state string) bool {
	switch state {
	case "completed", "failed", "killed", "timed_out":
		return true
	}
	return false
}

// executionFailed classifies whether a settlement is failure evidence.
//
// It is deliberately narrow, because mislabelling a wait as a failure is
// exactly the confusion this section exists to remove: a continuation — the
// caller stopped waiting while the command kept running — is not a failed
// command, an await expiry is not a failed command, and a natural success is
// not a failure. Only a settlement the service actually observed to fail
// counts: a nonzero exit, a start-up failure, an enforced resource limit, or an
// explicit stop.
func executionFailed(state, outcome string, exit *int, lost bool) bool {
	if lost {
		return false
	}
	switch outcome {
	case execOutcomeNonZeroExit, execOutcomeStartupFailure,
		execOutcomeStopped, execOutcomeResourceLimit:
		return true
	}
	// A record with no structured outcome falls back to the lifecycle state
	// plus the observed exit: "failed"/"killed" settled as a failure, and a
	// nonzero exit is one whether or not the state was recorded. A "running" or
	// "stopping" record has not settled, so it is not failure evidence yet.
	switch state {
	case "failed", "killed":
		return true
	case "running", "stopping", "":
		return exit != nil && *exit != 0
	}
	return exit != nil && *exit != 0
}

// mergeExecutionEvidence folds a freshly extracted record into one already in
// the report. Both sources describe the same execution — a call's notice and
// the job's final record — so the merge prefers whichever side observed a fact
// and keeps the identity, which is the same on both by construction.
func mergeExecutionEvidence(base, next ExecutionEvidence) ExecutionEvidence {
	base.Identity = firstNonEmpty(base.Identity, next.Identity)
	base.ExecutionID = firstNonEmpty(base.ExecutionID, next.ExecutionID)
	base.DisplayID = firstNonEmpty(base.DisplayID, next.DisplayID)
	base.ConversationID = firstNonEmpty(base.ConversationID, next.ConversationID)
	base.Actor = firstNonEmpty(base.Actor, next.Actor)
	base.TurnID = firstNonEmpty(base.TurnID, next.TurnID)
	base.StepID = firstNonEmpty(base.StepID, next.StepID)
	base.ToolCallID = firstNonEmpty(base.ToolCallID, next.ToolCallID)
	base.Cwd = firstNonEmpty(base.Cwd, next.Cwd)
	base.Tool = firstNonEmpty(base.Tool, next.Tool)
	// State and Outcome describe one lifecycle, so they are taken together: a
	// settled record supersedes an unsettled one, because the earlier source can
	// only have observed the execution while it was still running.
	if executionSettled(next.State) && !executionSettled(base.State) {
		base.State, base.Outcome = next.State, next.Outcome
	} else {
		base.State = firstNonEmpty(base.State, next.State)
		base.Outcome = firstNonEmpty(base.Outcome, next.Outcome)
	}
	base.Attachment = firstNonEmpty(base.Attachment, next.Attachment)
	base.Wait = firstNonEmpty(base.Wait, next.Wait)
	base.DetachReason = firstNonEmpty(base.DetachReason, next.DetachReason)
	base.StopCause = firstNonEmpty(base.StopCause, next.StopCause)
	if base.Generation == 0 {
		base.Generation = next.Generation
	}
	if base.PID == 0 {
		base.PID = next.PID
	}
	if base.DetachedAt.IsZero() {
		base.DetachedAt = next.DetachedAt
	}
	if base.StopRequestedAt.IsZero() {
		base.StopRequestedAt = next.StopRequestedAt
	}
	if base.StartedAt.IsZero() {
		base.StartedAt = next.StartedAt
	}
	if base.EndedAt.IsZero() {
		base.EndedAt = next.EndedAt
	}
	if base.Exit == nil {
		base.Exit = next.Exit
	}
	base.Truncated = base.Truncated || next.Truncated
	base.Lost = base.Lost || next.Lost
	// Failure is recomputed from the merged facts rather than OR-ed: a
	// not-yet-settled notice merged with a settled record must classify on what
	// is now known, and a lost record must not inherit a failure flag.
	base.Failed = base.Lost == false && executionFailed(base.State, base.Outcome, base.Exit, base.Lost)
	return base
}

// patchRepairNotes returns the format mistakes file.write_patch healed in
// place, read from the note it appends to a successful result. Nothing else in
// the audit log records a repaired envelope, so this is the one observable
// source of "the model keeps getting the proposal shape wrong".
func patchRepairNotes(ev registry.AuditEvent) []string {
	if ev.ToolName != "file.write_patch" || ev.Error != "" {
		return nil
	}
	idx := strings.Index(ev.ResultContent, repairNoteMarker)
	if idx < 0 {
		return nil
	}

	var notes []string
	for _, raw := range strings.Split(ev.ResultContent[idx+len(repairNoteMarker):], "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			// The note block ends at the first blank line; anything after it
			// is the diff or diagnostics, not a repair note.
			if len(notes) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "Close every REPLACE block") {
			break
		}
		notes = append(notes, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
	}
	return notes
}

// isParseFailureError reports whether a tool error describes arguments that
// could not be parsed. The audit log has no structured parse-failure field, so
// this classifies the error text the way the model saw it. Deliberately
// narrow: a false negative only under-reports, while a false positive would
// mislabel an ordinary tool error as a prompt problem.
func isParseFailureError(msg string) bool {
	markers := []string{
		"could not parse arguments",
		"parse patch error",
		"cannot unmarshal",
		"invalid character",
		"unexpected end of JSON",
		"unexpected token",
		"failed to parse",
	}
	for _, m := range markers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// runEventKindName maps a RunEventKind to the stable report string. The
// session type has no String method on purpose, so the mapping lives here:
// renaming a kind must be a deliberate, versioned change rather than a silent
// fmt.Stringer drift.
func runEventKindName(kind session.RunEventKind) string {
	switch kind {
	case session.RunEventVerifyFailed:
		return "verify_failure"
	case session.RunEventGateSkipped:
		return "gate_skipped"
	case session.RunEventReview:
		return "review"
	case session.RunEventCommit:
		return "commit"
	case session.RunEventRetry:
		return "retry"
	case session.RunEventConcern:
		return "concern"
	case session.RunEventTaskDone:
		return "task_done"
	default:
		return "unknown_" + strconv.Itoa(int(kind))
	}
}

// isLengthFinish mirrors the agent runner's guard: the provider cut the
// response off at the output-token limit, so tool arguments in it may be
// silently truncated.
func isLengthFinish(reason string) bool {
	return reason == "length" || reason == "max_tokens"
}

// countUserTurns counts the user turns in the transcript. Steering and
// background report messages are stored under RoleUser too, so this is a
// literal count of that role rather than a render-time turn count.
func countUserTurns(messages []session.Message) int {
	turns := 0
	for _, m := range messages {
		if m.Role == session.RoleUser {
			turns++
		}
	}
	return turns
}

// firstLine returns the first line of s, which is the dedup key for errors:
// the first line is the message, the rest is usually a stack or a dump.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// copyIntPtr copies an exit-code pointer so the report never aliases a
// caller's memory.
func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
