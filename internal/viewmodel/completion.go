package viewmodel

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/progress"
	"marshal/internal/tools/registry"
)

// The two sources of a completion event.
const (
	CompletionJob      = "job"
	CompletionSubagent = "subagent"
)

// Outcome words that are not an exit code.
const (
	OutcomeDone    = "done"
	OutcomeStopped = "stopped"
	OutcomeLost    = "lost"
	OutcomeFailed  = "failed"
	OutcomeUnknown = "outcome unknown"
)

// ReportTailLines bounds the output an issue report shows.
const ReportTailLines = 20

// CompletionInfo is one finished piece of background work at the moment it
// ended: a background job's exit or a subagent's end.
type CompletionInfo struct {
	Source     string // CompletionJob or CompletionSubagent
	ID         string // "job-3", or "#3" for a subagent
	SubagentID int64  // the subagent's ID; 0 for a job
	Label      string // job: its progress title, else its command's first line; subagent: its label
	Outcome    string // OutcomeDone, "exit N", OutcomeStopped, OutcomeLost, OutcomeFailed or OutcomeUnknown
	Failed     bool   // true for every outcome except OutcomeDone
	ExitCode   *int   // the observed exit code; nil when none was observed
	StopCause  string
	StartedAt  time.Time
	EndedAt    time.Time     // zero for a lost job
	Duration   time.Duration // a job's recorded run time, or a subagent's EndedAt − StartedAt
	Measure    string        // the final measurement (MeasureSummary); "" when none
	Summary    string        // the one line under the head; "" when none

	// The issue report's fields.
	Command         string // job: the whole command
	Cwd             string // job: the working directory, when recorded
	Output          string // job: the retained output tail
	Truncated       bool   // job: the retained output hit its cap
	Report          string // subagent: the final report
	Error           string // subagent: the failure text
	WorkKey         string
	ExecutionID     string // job
	ReportMessageID int64  // subagent: the persisted report message; 0 when unavailable

	// Launch is the row that launched the work: a job's start row, or the
	// agent.run row that started the subagent. Zero when none is found.
	Launch NodeID
}

// MeasureSummary renders a final progress snapshot as the one measurement a
// completion event shows: the phase, then the primary metric. It is the single
// place the TUI and the web take these words from, so both print the same.
func MeasureSummary(p *progress.Snapshot) string {
	if p == nil {
		return ""
	}
	var parts []string
	if p.Phase != nil && p.Phase.Total > 0 {
		label := p.Phase.Label
		if label == "" {
			label = p.Phase.ID
		}
		parts = append(parts, fmt.Sprintf("%d/%d %s", p.Phase.Index, p.Phase.Total, progress.Clean(label, 80, false)))
	}
	if m := primaryMetric(p); m != nil {
		parts = append(parts, metricSummary(p, m))
	}
	return strings.Join(parts, " · ")
}

// primaryMetric is the metric a snapshot leads with: the one the producer
// named, else the first.
func primaryMetric(p *progress.Snapshot) *progress.Metric {
	if len(p.Metrics) == 0 {
		return nil
	}
	if p.PrimaryMetricID != "" {
		for i := range p.Metrics {
			if p.Metrics[i].ID == p.PrimaryMetricID {
				return &p.Metrics[i]
			}
		}
	}
	return &p.Metrics[0]
}

// metricSummary renders the metric part of a measurement summary.
func metricSummary(p *progress.Snapshot, m *progress.Metric) string {
	if m.Total != nil {
		if m.Unit == "bytes" {
			return bytesIEC(m.Current) + "/" + bytesIEC(*m.Total)
		}
		return num(m.Current) + "/" + num(*m.Total) + " " + progress.Clean(m.Unit, 80, false)
	}
	// A producer that reports counts without totals (the Go test adapter's
	// passed/failed/skipped) is best read as its non-zero counts.
	if len(p.Metrics) >= 2 && !anyMetricHasTotal(p.Metrics) {
		var counts []string
		for i := range p.Metrics {
			c := &p.Metrics[i]
			if c.Current <= 0 {
				continue
			}
			label := c.Label
			if label == "" {
				label = c.ID
			}
			counts = append(counts, num(c.Current)+" "+progress.Clean(strings.ToLower(label), 80, false))
		}
		if len(counts) > 0 {
			return strings.Join(counts, ", ")
		}
	}
	if m.Unit == "bytes" {
		return bytesIEC(m.Current)
	}
	return num(m.Current) + " " + progress.Clean(m.Unit, 80, false)
}

func anyMetricHasTotal(metrics []progress.Metric) bool {
	for i := range metrics {
		if metrics[i].Total != nil {
			return true
		}
	}
	return false
}

// num prints a whole number without a decimal and anything else with one.
func num(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// bytesIEC renders a byte count with IEC binary prefixes (KiB, MiB, …).
func bytesIEC(v float64) string {
	const base = 1024
	if v < base {
		return fmt.Sprintf("%.0f B", v)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value, i := v, -1
	for value >= base && i < len(units)-1 {
		value /= base
		i++
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}

// ReportHeadline is a summary's first sentence as a row: emphasis and a
// leading markdown heading mark come off, because the row is plain text.
func ReportHeadline(summary string) string {
	head, _ := FirstSentence(strings.TrimSpace(summary))
	return stripHeading(StripEmphasis(head))
}

// stripHeading drops a leading ATX heading marker — one to six '#' followed by
// a space — and its padding. Anything else starting with '#' is text: "#3 done"
// keeps its number.
func stripHeading(s string) string {
	t := strings.TrimLeft(s, " ")
	n := len(t) - len(strings.TrimLeft(t, "#"))
	if n == 0 || n > 6 || (n < len(t) && t[n] != ' ') {
		return s
	}
	return strings.TrimLeft(t[n:], " ")
}

// ReportTail is the bounded output an issue report shows: the failure lines
// first, then the end of the output, so a reader sees what broke and where the
// run stopped.
func ReportTail(output string) []string {
	trimmed := strings.TrimRight(output, "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	var failures []string
	for _, l := range lines {
		if strings.Contains(l, "--- FAIL") {
			failures = append(failures, l)
			if len(failures) == 10 {
				break
			}
		}
	}
	if len(failures) == 0 {
		return tailLines(lines, ReportTailLines)
	}
	out := append([]string{}, failures...)
	rest := ReportTailLines - len(failures)
	if rest <= 0 {
		return out
	}
	var others []string
	for _, l := range lines {
		if strings.Contains(l, "--- FAIL") {
			continue
		}
		others = append(others, l)
	}
	return append(out, tailLines(others, rest)...)
}

// tailLines is the last n elements, or the whole slice when it is shorter.
func tailLines(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// jobCompletion is a background job's exit as a completion event. start is the
// paired start audit, or nil when the transcript no longer holds it.
func jobCompletion(e *session.JobExit, start *registry.AuditEvent) CompletionInfo {
	c := CompletionInfo{Source: CompletionJob, ID: e.ID}
	command := e.Command
	if start != nil {
		command = ToolTarget(*start)
	}
	c.Command = command
	if e.ProgressConfig != nil && e.ProgressConfig.Title != "" {
		c.Label = e.ProgressConfig.Title
	} else {
		c.Label = firstLineOf(command)
	}

	// A missing exit never reads as success: ExitCode's -1 is the legacy
	// spelling of "unknown", and a lost job has no exit at all.
	var code *int
	switch {
	case e.Exit != nil:
		code = e.Exit
	case !e.Lost && e.ExitCode >= 0:
		v := e.ExitCode
		code = &v
	}

	switch {
	case e.Lost:
		c.Outcome = OutcomeLost
	case code != nil && *code == 0 && !terminalFailureState(e.State):
		c.Outcome = OutcomeDone
	case code != nil:
		c.Outcome = fmt.Sprintf("exit %d", *code)
	case e.StopCause != "":
		c.Outcome = OutcomeStopped
	case terminalFailureState(e.State):
		c.Outcome = OutcomeFailed
	default:
		c.Outcome = OutcomeUnknown
	}
	c.Failed = c.Outcome != OutcomeDone
	c.ExitCode = code
	c.StopCause = e.StopCause

	c.StartedAt = e.StartedAt
	if c.StartedAt.IsZero() && start != nil {
		c.StartedAt = start.Timestamp
	}
	c.EndedAt = e.EndedAt
	if c.EndedAt.IsZero() {
		c.EndedAt = e.At
	}
	if e.Lost {
		c.EndedAt = time.Time{}
	}
	c.Duration = e.Duration
	c.Measure = MeasureSummary(e.Progress)
	c.Summary = jobSummary(e)

	c.Cwd = e.Cwd
	c.Output = e.Output
	c.Truncated = e.Truncated
	c.ExecutionID = e.ExecutionID
	if start != nil {
		c.Launch = ToolID(*start)
	}
	if e.ExecutionID != "" {
		epoch := e.SourceEpoch
		if epoch == "" {
			epoch = legacyWorkEpoch
		}
		c.WorkKey = WorkKey(e.ConversationID, epoch, workKindCommand, e.ExecutionID)
	}
	return c
}

// terminalFailureState reports whether a lifecycle status is a failure that
// must not render as a clean exit.
func terminalFailureState(state string) bool {
	switch state {
	case "failed", "killed", "timed_out":
		return true
	}
	return false
}

// jobSummary is the one line under a job's head, first match wins: the failure
// line, the progress detail, then the last useful output line.
func jobSummary(e *session.JobExit) string {
	lines := strings.Split(e.Output, "\n")
	for _, l := range lines {
		if strings.Contains(l, "--- FAIL") {
			return strings.TrimSpace(l)
		}
	}
	if e.Progress != nil {
		if d := strings.TrimSpace(e.Progress.Detail); d != "" && d != "go test" {
			return d
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || l == "[output truncated]" {
			continue
		}
		return l
	}
	return ""
}

// subagentCompletion is a finished subagent as a completion event. launch is
// the agent.run row that dispatched it, zero when none is found.
func subagentCompletion(v *session.SubagentView, launch NodeID) CompletionInfo {
	c := CompletionInfo{
		Source:     CompletionSubagent,
		ID:         fmt.Sprintf("#%d", v.ID),
		SubagentID: v.ID,
		Label:      v.Label,
		Launch:     launch,
	}
	switch {
	case v.Status == session.SubagentFailed:
		c.Outcome = OutcomeFailed
	case v.Status == session.SubagentDone:
		if v.Outcome == "stopped" || v.StopRequested {
			c.Outcome = OutcomeStopped
		} else {
			c.Outcome = OutcomeDone
		}
	default:
		c.Outcome = OutcomeUnknown
	}
	c.Failed = c.Outcome != OutcomeDone
	c.StartedAt, c.EndedAt = v.StartedAt, v.EndedAt
	if !v.StartedAt.IsZero() && !v.EndedAt.IsZero() {
		c.Duration = v.EndedAt.Sub(v.StartedAt)
	}
	c.Summary = ReportHeadline(v.Summary)
	if c.Summary == "" && v.Status == session.SubagentFailed {
		c.Summary = firstLineOf(v.Error)
	}
	c.Report = v.Summary
	c.Error = v.Error
	c.WorkKey = v.WorkKey
	c.ReportMessageID = v.Report.MessageID
	return c
}

// completionNode builds a completion event's node. Its text is model-authored —
// a subagent's label and report, a job's command and output — so it is scrubbed
// here, at the one place every event is constructed, rather than at each
// renderer.
func completionNode(c CompletionInfo, refKey string) *Node {
	c.Label = ScrubText(c.Label)
	c.Summary = ScrubText(c.Summary)
	c.StopCause = ScrubText(c.StopCause)
	c.Cwd = ScrubText(c.Cwd)
	c.Command = ScrubBlock(c.Command)
	c.Output = ScrubBlock(c.Output)
	c.Report = ScrubBlock(c.Report)
	c.Error = ScrubBlock(c.Error)
	n := &Node{ID: NodeID{KindCompletion, "completion:" + refKey}, Kind: KindCompletion, Completion: &c}
	n.Version = versionOfCompletion(&c)
	return n
}

// versionOfCompletion hashes every field a renderer reads, so a change to any
// of them invalidates a cached render.
func versionOfCompletion(c *CompletionInfo) uint64 {
	b := newHash()
	b.f("cp|%s|%s|%d|%s|%s|%v|%s|%d|%d|%d|%s|%s|%s|",
		c.Source, c.ID, c.SubagentID, c.Label, c.Outcome, c.Failed, c.StopCause,
		c.StartedAt.UnixNano(), c.EndedAt.UnixNano(), c.Duration, c.Measure, c.Summary, c.WorkKey)
	b.f("%s|%s|%s|%s|%v|%s|%s|%d|%s|",
		c.Command, c.Cwd, c.Output, c.Report, c.Truncated, c.Error, c.ExecutionID, c.ReportMessageID, c.Launch.Key)
	if c.ExitCode != nil {
		b.f("x%d|", *c.ExitCode)
	}
	return b.sum()
}

// firstLineOf is s up to its first newline, trimmed.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
