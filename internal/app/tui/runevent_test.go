package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
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
	got := stripANSI(renderRunEvent(wireRunEvent(ev), nil, false, 80))

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

	collapsed := stripANSI(renderRunEvent(wireRunEvent(ev), nil, false, 80))
	if n := strings.Count(collapsed, "\n"); n > runEventCollapsedBodyLines+3 {
		t.Errorf("collapsed body must cap at %d lines, got %d newlines:\n%s",
			runEventCollapsedBodyLines, n, collapsed)
	}
	if !strings.Contains(collapsed, "more") {
		t.Error("a capped body must say how much is hidden")
	}

	expanded := stripANSI(renderRunEvent(wireRunEvent(ev), nil, true, 80))
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
	got := stripANSI(renderRunEvent(wireRunEvent(ev), nil, false, 80))
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
	got := stripANSI(renderRunEvent(wireRunEvent(ev), nil, false, 100))
	for _, want := range []string{"Important", "backoff overflows past attempt 30"} {
		if !strings.Contains(got, want) {
			t.Errorf("review finding missing %q:\n%s", want, got)
		}
	}
}

func TestRenderCleanReviewIsOneLine(t *testing.T) {
	ev := session.RunEvent{Kind: session.RunEventReview, TaskN: 4, Detail: "clean"}
	got := stripANSI(renderRunEvent(wireRunEvent(ev), nil, false, 80))
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
	got := stripANSI(renderRunEvent(wireRunEvent(ev), nil, false, 90))
	for _, want := range []string{"a3f9e21", "Scaffold config"} {
		if !strings.Contains(got, want) {
			t.Errorf("commit event missing %q:\n%s", want, got)
		}
	}
}

func TestRenderRetryAndConcern(t *testing.T) {
	retry := stripANSI(renderRunEvent(wireRunEvent(session.RunEvent{
		Kind: session.RunEventRetry, TaskN: 2, Title: "retry 2/3", Detail: "connection reset",
	}), nil, false, 80))
	if !strings.Contains(retry, "retry 2/3") || !strings.Contains(retry, "connection reset") {
		t.Errorf("retry event incomplete:\n%s", retry)
	}

	concern := stripANSI(renderRunEvent(wireRunEvent(session.RunEvent{
		Kind: session.RunEventConcern, TaskN: 2, Title: "no coverage for the overflow path",
	}), nil, false, 80))
	if !strings.Contains(concern, "concern") || !strings.Contains(concern, "overflow path") {
		t.Errorf("concern event incomplete:\n%s", concern)
	}
}

func TestRenderRunEventTruncatesToWidth(t *testing.T) {
	ev := session.RunEvent{
		Kind: session.RunEventReview, TaskN: 1, Severity: "Critical",
		Title: strings.Repeat("very long finding text ", 20),
	}
	for _, line := range strings.Split(stripANSI(renderRunEvent(wireRunEvent(ev), nil, false, 40)), "\n") {
		if len([]rune(line)) > 40 {
			t.Errorf("line exceeds width 40 (%d runes): %q", len([]rune(line)), line)
		}
	}
}

// thousandLineRunEvent adds one verify-failed run event whose body runs from
// "line 0001" to "line 1000" — well over acpwire.WireTextCap — to m's
// transcript, and refreshes the viewport. It returns the event's wire node ID.
func thousandLineRunEvent(t *testing.T, m *Model) string {
	t.Helper()
	var body strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&body, "line %04d\n", i)
	}
	stateOf(m).AddRunEvent(session.RunEvent{
		Kind: session.RunEventVerifyFailed, TaskN: 1, Title: "go test ./...",
		Body: body.String(),
	})
	m.invalidateTranscript()
	m.refreshViewport()

	for id, w := range m.wire {
		if w.RunEvent != nil {
			return id
		}
	}
	t.Fatal("the wire holds no run event")
	return ""
}

// contentOf is what the transcript holds, as text.
func contentOf(m Model) string { return stripANSI(strings.Join(m.contentLines, "\n")) }

// An expanded run event draws the whole body, which the wire cut: until the
// detail lands the row still shows the cut, and once it lands the tail is
// there.
func TestExpandedRunEventReadsItsWholeBody(t *testing.T) {
	m := newTestModel(t)
	thousandLineRunEvent(t, &m)

	if got := contentOf(m); !strings.Contains(got, "… 988 more lines — ctrl+g to expand") {
		t.Fatalf("the collapsed body must count the whole body:\n%s", got)
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = asModel(t, updated)
	if !m.expandAll {
		t.Fatal("ctrl+g did not turn expand-all on")
	}
	if strings.Contains(contentOf(m), "line 1000") {
		t.Fatalf("the undrained expansion must draw the wire's cut:\n%s", contentOf(m))
	}

	m = drainCmds(m, cmd)
	if got := contentOf(m); !strings.Contains(got, "line 1000") {
		t.Fatalf("the drained expansion must read the whole body:\n%s", got)
	}
}

// Copying a cut run event copies the whole body, not the wire's cut of it.
func TestCopyingACutRunEventCopiesItsWholeBody(t *testing.T) {
	m := newTestModel(t)
	key := thousandLineRunEvent(t, &m)

	got := m.nodeCopyText(nodeID{Kind: acpwire.KindRunEvent, Key: key})
	if !strings.Contains(got, "line 1000") {
		t.Fatalf("the copy must hold the whole body, got %d bytes:\n%s", len(got), got)
	}
}
