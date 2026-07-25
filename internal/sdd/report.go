package sdd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// statusLineRe matches a "status: <VALUE>" line anywhere in the file,
// case-insensitive on the value. The first capture group is the raw value.
// "First line" matching is enforced by checking FindStringIndex's [0] == 0
// against this single regex (no need for a second regex).
var statusLineRe = regexp.MustCompile(`(?im)^status:\s*([A-Z_]+)\s*$`)

// Report is a worker's report file: the typed Status + the body text.
// The on-disk format is `status: <STATUS>\n\n<body>`. Body is whatever
// follows the status line (may itself contain `status:` lines).
type Report struct {
	TaskID string
	Status ReportStatus
	Body   string
}

// ReadReport reads <ws.Dir()>/reports/<taskID><kind>.md. `kind` is the
// suffix: "" for the implementer, "-audit" / "-review" / "-investigation"
// for the gate workers. Returns an error if the file is missing.
func ReadReport(ws *Workspace, taskID, kind string) (*Report, error) {
	path := filepath.Join(ws.Dir(), "reports", taskID+kind+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sdd report: read %s: %w", path, err)
	}
	content := string(data)
	// Pull the first status line. If absent, NormalizeReport would have
	// prepended one; here we accept whatever is on disk.
	var status ReportStatus
	if m := statusLineRe.FindStringIndex(content); m != nil && m[0] == 0 {
		status = ReportStatus(strings.ToUpper(statusLineRe.FindStringSubmatch(content)[1]))
	} else if m := statusLineRe.FindStringSubmatch(content); m != nil {
		// Status appears later in the file; surface it so the controller
		// can normalize via NormalizeReport.
		status = ReportStatus(strings.ToUpper(m[1]))
	}
	// Body: strip the first status line + the blank line after it.
	body := content
	if m := statusLineRe.FindStringIndex(content); m != nil && m[0] == 0 {
		body = content[m[1]:]
		body = strings.TrimLeft(body, "\r\n")
		body = strings.TrimLeft(body, "\n")
	}
	return &Report{TaskID: taskID, Status: status, Body: body}, nil
}

// NormalizeReport ensures the file starts with `status: <STATUS>` on the
// first non-empty line. If the first line is already a status line, content
// is returned unchanged. Otherwise, the first status line found later in
// the content is moved to the top. If no status is found, content is
// returned unchanged (Validate will catch it).
func NormalizeReport(content string) string {
	// Fast path: first non-empty line is already a status line.
	firstNonEmpty := firstNonEmptyLine(content)
	if firstNonEmpty != "" && statusLineRe.MatchString(firstNonEmpty) {
		return content
	}
	// Find a status line later in the content and move it to the top.
	m := statusLineRe.FindStringSubmatch(content)
	if m == nil {
		return content
	}
	statusLine := "status: " + m[1]
	// Strip the matched status line from wherever it appears.
	content = statusLineRe.ReplaceAllString(content, "")
	// Trim leading newlines, prepend the status line + a blank line.
	content = strings.TrimLeft(content, "\r\n")
	content = strings.TrimLeft(content, "\n")
	return statusLine + "\n\n" + content
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// stripLeadingStatusLine removes a leading `status: <VALUE>` line and the
// blank line that follows it from body, if present. Returns the stripped body.
func stripLeadingStatusLine(body string) string {
	if !strings.HasPrefix(body, "status:") {
		return body
	}
	// Find the end of the first line.
	idx := strings.IndexByte(body, '\n')
	if idx == -1 {
		return ""
	}
	rest := body[idx+1:]
	// Strip blank lines that follow.
	rest = strings.TrimLeft(rest, "\r\n")
	return rest
}

// Write serializes the report to <ws.Dir()>/reports/<TaskID><kind>.md with
// `status: <STATUS>` on the first line, a blank line, then Body.
// The on-disk format is always `status: <STATUS>\n\n<body>` (or just
// `status: <STATUS>\n` when Body is empty). Any leading status line in
// Body is stripped before writing, making Write → Read → Write a fixed point.
func (r *Report) Write(ws *Workspace, kind string) error {
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	dir := filepath.Join(ws.Dir(), "reports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("sdd report: mkdir: %w", err)
	}
	path := filepath.Join(dir, r.TaskID+kind+".md")
	body := stripLeadingStatusLine(r.Body)
	if body == "" {
		body = "status: " + string(r.Status) + "\n"
	} else {
		body = "status: " + string(r.Status) + "\n\n" + body
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		return fmt.Errorf("sdd report: write: %w", err)
	}
	return nil
}

// ReportValidationError indicates a report failed Validate. Same shape as
// ValidationError; distinct type so callers can distinguish contract vs
// report issues.
type ReportValidationError struct {
	Field  string
	Reason string
}

func (e *ReportValidationError) Error() string {
	return fmt.Sprintf("sdd report: invalid %s: %s", e.Field, e.Reason)
}

// Validate checks that Status is a known ReportStatus constant AND that
// Body is non-empty. Returns *ReportValidationError on failure.
func (r *Report) Validate() error {
	if !knownReportStatus(r.Status) {
		return &ReportValidationError{Field: "Status", Reason: fmt.Sprintf("unknown status %q", r.Status)}
	}
	if strings.TrimSpace(r.Body) == "" {
		return &ReportValidationError{Field: "Body", Reason: "empty"}
	}
	return nil
}

// parseReportFromString parses a normalized report string into a Report.
// It does NOT read from disk; it extracts the status from the first status
// line and treats the rest as Body.
func parseReportFromString(taskID, content string) *Report {
	var status ReportStatus
	if m := statusLineRe.FindStringSubmatch(content); m != nil {
		status = ReportStatus(strings.ToUpper(m[1]))
	}
	body := content
	if idx := statusLineRe.FindStringIndex(content); idx != nil && idx[0] == 0 {
		body = content[idx[1]:]
		body = strings.TrimLeft(body, "\r\n")
	}
	return &Report{TaskID: taskID, Status: status, Body: body}
}

// OrchestratorReport is the structured report the orchestrator returns in
// task.Summary after one drain iteration. The controller parses this to
// decide the next state-machine transition.
type OrchestratorReport struct {
	Status       ReportStatus
	BatchID      int
	Dispatched   []string
	Merged       []string
	BlockedTasks []BlockedTask
	HealthAlerts []string
	EditGuard    string
	NextAction   string
}

// BlockedTask is one entry in the orchestrator's blocked_tasks list.
type BlockedTask struct {
	Task            string
	Reason          string
	Detail          string
	FilesInConflict []string
}

// ParseOrchestratorReport parses the line-oriented structured report the
// orchestrator returns. The format is YAML-ish (not strict YAML): top-level
// `key: value` lines, with `blocked_tasks:` introducing a YAML-style list of
// `- task: T5` / `    reason: ...` blocks.
func ParseOrchestratorReport(text string) (*OrchestratorReport, error) {
	r := &OrchestratorReport{}
	lines := strings.Split(text, "\n")
	var statusFound bool
	var inBlockedTasks bool
	var currentBlocked *BlockedTask
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Top-level key: value.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inBlockedTasks = false
			key, val, ok := splitKV(trimmed)
			if !ok {
				continue
			}
			switch key {
			case "status":
				r.Status = ReportStatus(strings.TrimSpace(val))
				statusFound = true
			case "batch_id":
				if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					r.BatchID = n
				}
			case "dispatched":
				r.Dispatched = parseBracketList(val)
			case "merged":
				r.Merged = parseBracketList(val)
			case "blocked_tasks":
				if strings.TrimSpace(val) == "[]" {
					r.BlockedTasks = []BlockedTask{}
				} else {
					inBlockedTasks = true
					r.BlockedTasks = []BlockedTask{}
				}
			case "health_alerts":
				r.HealthAlerts = parseBracketList(val)
			case "edit_guard":
				r.EditGuard = strings.TrimSpace(val)
			case "next_action":
				r.NextAction = strings.TrimSpace(val)
			}
			continue
		}
		// Indented line under blocked_tasks.
		if inBlockedTasks {
			if strings.HasPrefix(strings.TrimSpace(line), "- task:") {
				if currentBlocked != nil {
					r.BlockedTasks = append(r.BlockedTasks, *currentBlocked)
				}
				currentBlocked = &BlockedTask{}
				_, v, _ := splitKV(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- ")))
				currentBlocked.Task = strings.TrimSpace(v)
			} else if currentBlocked != nil {
				key, val, ok := splitKV(strings.TrimSpace(line))
				if !ok {
					continue
				}
				switch key {
				case "reason":
					currentBlocked.Reason = strings.TrimSpace(val)
				case "detail":
					currentBlocked.Detail = strings.TrimSpace(val)
				case "files_in_conflict":
					currentBlocked.FilesInConflict = parseBracketList(val)
				}
			}
		}
	}
	if currentBlocked != nil {
		r.BlockedTasks = append(r.BlockedTasks, *currentBlocked)
	}
	if !statusFound {
		return nil, fmt.Errorf("sdd report: missing status line")
	}
	return r, nil
}

// splitKV splits "key: value" into (key, value, true). Returns ("", "", false)
// if the line has no colon.
func splitKV(line string) (string, string, bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), line[idx+1:], true
}

// parseBracketList parses "[T1, T2]" into ["T1", "T2"]. Returns nil for "[]".
func parseBracketList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func knownReportStatus(s ReportStatus) bool {
	switch s {
	case ReportDone, ReportBlocked, ReportNeedsContext,
		ReportPass, ReportFail, ReportSkip,
		ReportNeedsHuman, ReportHealthAlert:
		return true
	}
	return false
}
