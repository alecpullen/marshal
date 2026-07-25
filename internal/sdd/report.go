package sdd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// Write serializes the report to <ws.Dir()>/reports/<TaskID><kind>.md with
// `status: <STATUS>` on the first line, a blank line, then Body.
func (r *Report) Write(ws *Workspace, kind string) error {
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	dir := filepath.Join(ws.Dir(), "reports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("sdd report: mkdir: %w", err)
	}
	path := filepath.Join(dir, r.TaskID+kind+".md")
	var body string
	if r.Body == "" {
		body = ""
	} else if strings.HasPrefix(r.Body, "status:") {
		// Body already has a status line; write as-is.
		body = r.Body
	} else {
		body = "status: " + string(r.Status) + "\n\n" + r.Body
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

func knownReportStatus(s ReportStatus) bool {
	switch s {
	case ReportDone, ReportBlocked, ReportNeedsContext,
		ReportPass, ReportFail, ReportSkip,
		ReportNeedsHuman, ReportHealthAlert:
		return true
	}
	return false
}
