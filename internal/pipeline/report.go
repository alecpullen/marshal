package pipeline

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrReportParse indicates that an implementer or reviewer produced output
// that does not match the required report format. The controller retries
// these at the dispatch level because a fresh subagent can recover from a
// malformed response.
var ErrReportParse = errors.New("pipeline report: unparseable output")

// Status is the implementer's verdict, the first line of its report.
type Status string

const (
	StatusDone             Status = "DONE"
	StatusDoneWithConcerns Status = "DONE_WITH_CONCERNS"
	StatusNeedsContext     Status = "NEEDS_CONTEXT"
	StatusBlocked          Status = "BLOCKED"
)

// ImplementerReport is a parsed implementer (or fixer) report. Raw is the
// full report text, handed to the reviewer and to any fix dispatch.
type ImplementerReport struct {
	Status   Status
	Tests    string
	Concerns string
	Question string
	Raw      string
}

// NeedsHuman reports whether the status requires context the implementer
// does not have.
func (r ImplementerReport) NeedsHuman() bool {
	return r.Status == StatusNeedsContext || r.Status == StatusBlocked
}

// ParseImplementerReport reads the report format the implementer prompt
// mandates. Unrecognised or missing status is an error: a report the
// controller cannot read is a failed dispatch, never a guess.
func ParseImplementerReport(text string) (ImplementerReport, error) {
	r := ImplementerReport{Raw: text}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := splitKey(line)
		if !ok {
			continue
		}
		switch key {
		case "STATUS":
			r.Status = Status(strings.ToUpper(value))
		case "TESTS":
			r.Tests = value
		case "CONCERNS":
			r.Concerns = value
		case "QUESTION":
			r.Question = value
		}
	}
	switch r.Status {
	case StatusDone, StatusDoneWithConcerns, StatusNeedsContext, StatusBlocked:
	default:
		return r, fmt.Errorf("%w: missing or unknown STATUS (got %q)", ErrReportParse, r.Status)
	}
	if r.NeedsHuman() && r.Question == "" {
		return r, fmt.Errorf("%w: status %s requires a QUESTION line", ErrReportParse, r.Status)
	}
	return r, nil
}

// Severity is a review finding's severity. Critical and Important block the
// task; Minor is recorded for the branch review to triage.
type Severity string

const (
	SeverityCritical  Severity = "Critical"
	SeverityImportant Severity = "Important"
	SeverityMinor     Severity = "Minor"
)

// Finding is one review finding.
type Finding struct {
	Severity Severity
	Text     string
}

// ReviewReport is a parsed reviewer verdict pair plus its findings.
type ReviewReport struct {
	SpecPass        bool
	QualityApproved bool
	Findings        []Finding
	Raw             string
}

// Clean reports whether the task may advance: both verdicts positive and
// no blocking findings.
func (r ReviewReport) Clean() bool {
	return r.SpecPass && r.QualityApproved && len(r.Blocking()) == 0
}

// Blocking returns the Critical and Important findings.
func (r ReviewReport) Blocking() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == SeverityCritical || f.Severity == SeverityImportant {
			out = append(out, f)
		}
	}
	return out
}

// Minors returns the Minor findings.
func (r ReviewReport) Minors() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == SeverityMinor {
			out = append(out, f)
		}
	}
	return out
}

var findingRe = regexp.MustCompile(`^-\s*\[(Critical|Important|Minor)\]\s*(.+)$`)

// ParseReviewReport reads the reviewer report format. Both verdicts are
// required: a report carrying only one is a failed review, not a pass.
func ParseReviewReport(text string) (ReviewReport, error) {
	r := ReviewReport{Raw: text}
	var sawSpec, sawQuality bool
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := findingRe.FindStringSubmatch(trimmed); m != nil {
			r.Findings = append(r.Findings, Finding{Severity: Severity(m[1]), Text: strings.TrimSpace(m[2])})
			continue
		}
		key, value, ok := splitKey(line)
		if !ok {
			continue
		}
		switch key {
		case "SPEC":
			sawSpec = true
			r.SpecPass = strings.EqualFold(value, "PASS")
		case "QUALITY":
			sawQuality = true
			r.QualityApproved = strings.EqualFold(value, "APPROVED")
		}
	}
	if !sawSpec || !sawQuality {
		return r, fmt.Errorf("%w: report must carry both SPEC and QUALITY verdicts", ErrReportParse)
	}
	return r, nil
}

// splitKey splits an "KEY: value" line, returning the upper-cased key and
// the trimmed value. ok is false for any line that is not a key line.
func splitKey(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	if key == "" || key != strings.ToUpper(key) {
		return "", "", false
	}
	for _, r := range key {
		if (r < 'A' || r > 'Z') && r != '_' {
			return "", "", false
		}
	}
	return key, strings.TrimSpace(line[idx+1:]), true
}
