package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestRenderVerifyFailureShowsCommandAndOutput(t *testing.T) {
	ev := session.RunEvent{
		Kind:   session.RunEventVerifyFailed,
		TaskN:  3,
		Title:  "go test ./...",
		Detail: "fix round 1/3",
		Body:   "--- FAIL: TestRetryBackoff (0.02s)\n    retry_test.go:44: got 3 attempts, want 5",
	}
	got := stripANSI(renderRunEvent(ev, false, 80))

	for _, want := range []string{"go test ./...", "fix round 1/3", "retry_test.go:44"} {
		if !strings.Contains(got, want) {
			t.Errorf("collapsed verify failure missing %q:\n%s", want, got)
		}
	}
}

func TestRenderVerifyFailureCapsCollapsedOutput(t *testing.T) {
	var b strings.Builder
	for i := range 60 {
		b.WriteString("output line ")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteByte('\n')
	}
	ev := session.RunEvent{Kind: session.RunEventVerifyFailed, Title: "go build ./...", Body: b.String()}

	collapsed := stripANSI(renderRunEvent(ev, false, 80))
	if n := strings.Count(collapsed, "\n"); n > runEventCollapsedBodyLines+3 {
		t.Errorf("collapsed body must cap at %d lines, got %d newlines:\n%s",
			runEventCollapsedBodyLines, n, collapsed)
	}
	if !strings.Contains(collapsed, "more") {
		t.Error("a capped body must say how much is hidden")
	}

	expanded := stripANSI(renderRunEvent(ev, true, 80))
	if strings.Count(expanded, "\n") <= strings.Count(collapsed, "\n") {
		t.Error("expanded must show more than collapsed")
	}
}

func TestRenderGateSkippedIsAWarningNotASuccess(t *testing.T) {
	ev := session.RunEvent{
		Kind:  session.RunEventGateSkipped,
		TaskN: 2,
		Title: "no build or test command configured",
	}
	got := stripANSI(renderRunEvent(ev, false, 80))
	if !strings.Contains(got, "⚠") {
		t.Errorf("a skipped gate must render as a warning, not a checkmark:\n%s", got)
	}
	if strings.Contains(got, "✓") {
		t.Errorf("a skipped gate must never render a success glyph:\n%s", got)
	}
	if !strings.Contains(got, "skipped") {
		t.Errorf("the word 'skipped' must appear:\n%s", got)
	}
}

func TestRenderReviewFindingShowsSeverity(t *testing.T) {
	ev := session.RunEvent{
		Kind:     session.RunEventReview,
		TaskN:    4,
		Title:    "backoff overflows past attempt 30",
		Severity: "Important",
	}
	got := stripANSI(renderRunEvent(ev, false, 100))
	for _, want := range []string{"Important", "backoff overflows past attempt 30"} {
		if !strings.Contains(got, want) {
			t.Errorf("review finding missing %q:\n%s", want, got)
		}
	}
}

func TestRenderCleanReviewIsOneLine(t *testing.T) {
	ev := session.RunEvent{Kind: session.RunEventReview, TaskN: 4, Detail: "clean"}
	got := stripANSI(renderRunEvent(ev, false, 80))
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Errorf("a clean review must be one line:\n%s", got)
	}
	if !strings.Contains(got, "review clean") {
		t.Errorf("clean review text missing:\n%s", got)
	}
}

func TestRenderCommitShowsShaAndSubject(t *testing.T) {
	ev := session.RunEvent{
		Kind: session.RunEventCommit, TaskN: 1,
		Title: "a3f9e21", Detail: "myplan: task 1 — Scaffold config",
	}
	got := stripANSI(renderRunEvent(ev, false, 90))
	for _, want := range []string{"a3f9e21", "Scaffold config"} {
		if !strings.Contains(got, want) {
			t.Errorf("commit event missing %q:\n%s", want, got)
		}
	}
}

func TestRenderRetryAndConcern(t *testing.T) {
	retry := stripANSI(renderRunEvent(session.RunEvent{
		Kind: session.RunEventRetry, TaskN: 2, Title: "retry 2/3", Detail: "connection reset",
	}, false, 80))
	if !strings.Contains(retry, "retry 2/3") || !strings.Contains(retry, "connection reset") {
		t.Errorf("retry event incomplete:\n%s", retry)
	}

	concern := stripANSI(renderRunEvent(session.RunEvent{
		Kind: session.RunEventConcern, TaskN: 2, Title: "no coverage for the overflow path",
	}, false, 80))
	if !strings.Contains(concern, "concern") || !strings.Contains(concern, "overflow path") {
		t.Errorf("concern event incomplete:\n%s", concern)
	}
}

func TestRenderRunEventTruncatesToWidth(t *testing.T) {
	ev := session.RunEvent{
		Kind: session.RunEventReview, TaskN: 1, Severity: "Critical",
		Title: strings.Repeat("very long finding text ", 20),
	}
	for _, line := range strings.Split(stripANSI(renderRunEvent(ev, false, 40)), "\n") {
		if len([]rune(line)) > 40 {
			t.Errorf("line exceeds width 40 (%d runes): %q", len([]rune(line)), line)
		}
	}
}
