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

// repairNoteMarker is the header file.write_patch appends to a successful
// result when its parser healed a malformed proposal in place. It is the only
// observable trace of a repaired tool envelope in the audit log (the agent's
// own envelope repairs are logged, not audited), so the report mines it to
// surface "the model keeps getting the format wrong" without synthesis.
const repairNoteMarker = "the proposal's format was repaired before applying:"

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
	// AgentObservations is nil until the optional agent pass appends semantic
	// observations. Extraction never fabricates it.
	AgentObservations *string `json:"agent_observations"`
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

// RunEventEntry is one deduplicated plan-run event. Run events are in-memory
// only, so this field is the only durable record of them.
type RunEventEntry struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Count  int    `json:"count"`
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

	for _, ev := range state.AuditLog() {
		if ev.Model != "" {
			models[ev.Model] = true
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
