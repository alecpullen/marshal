package help

import (
	"regexp"
	"strings"
	"testing"
)

// stripANSI removes all ANSI escape sequences from text so test assertions
// can match on visible runes without lipgloss styling interfering.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestFooterIdle(t *testing.T) {
	out := stripANSI(Footer(FooterHints{}))
	for _, want := range []string{"Tab", "Alt+M", "?", "/", "@"} {
		if !strings.Contains(out, want) {
			t.Fatalf("idle footer missing %q: %q", want, out)
		}
	}
}

func TestFooterBusyShowsCancelAndQueue(t *testing.T) {
	out := stripANSI(Footer(FooterHints{Busy: true}))
	if !strings.Contains(out, "Esc cancel") || !strings.Contains(out, "Ctrl+X clear queue") {
		t.Fatalf("busy footer missing cancel/queue hints: %q", out)
	}
}

func TestFooterQuestionShowsAnswer(t *testing.T) {
	out := stripANSI(Footer(FooterHints{QuestionPending: true}))
	if !strings.Contains(out, "Enter answer") {
		t.Fatalf("question footer missing answer hint: %q", out)
	}
}

func TestFooterIdleShowsThinkingToggle(t *testing.T) {
	out := stripANSI(Footer(FooterHints{}))
	if !strings.Contains(out, "Ctrl+G") {
		t.Fatalf("idle footer missing Ctrl+G: %q", out)
	}
}

func TestFooterIdleShowsRollbackWhenEligible(t *testing.T) {
	out := stripANSI(Footer(FooterHints{IdleRollbackEligible: true}))
	if !strings.Contains(out, "Ctrl+R") {
		t.Fatalf("idle footer missing Ctrl+R: %q", out)
	}
}

func TestFooterApprovalWording(t *testing.T) {
	out := stripANSI(Footer(FooterHints{ApprovalPending: true}))
	if strings.Contains(out, "Enter×2") {
		t.Fatalf("stale 'Enter×2' label still present: %q", out)
	}
	if !strings.Contains(out, "arm") || !strings.Contains(out, "submit") {
		t.Fatalf("expected arm/submit labels, got %q", out)
	}
}

func TestOverlayEnumeratesAllBindings(t *testing.T) {
	out := stripANSI(Overlay(80, 24, OverlayHints{}))
	for _, want := range []string{"Enter", "Shift+Enter", "/", "@", "Esc", "?", "Ctrl+O", "Ctrl+K", "Ctrl+G", "Ctrl+R", "Ctrl+X", "PgUp", "PgDn", "End"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help overlay missing %q:\n%s", want, out)
		}
	}
}

// firstLineContaining returns the first line in s that contains substr.
func firstLineContaining(s, substr string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

func TestOverlayUsesFixedKeyColumn(t *testing.T) {
	out := stripANSI(Overlay(120, 80, OverlayHints{}))
	tabLine := firstLineContaining(out, "Tab")
	altLine := firstLineContaining(out, "Alt+Shift+M")
	if tabLine == "" || altLine == "" {
		t.Fatal("expected both Tab and Alt+Shift+M in overlay")
	}
	// Both descriptions should start at the same column because the
	// key column is fixed-width.
	tabDesc := strings.Index(tabLine, "cycle mode")
	altDesc := strings.Index(altLine, "cycle model backward")
	if tabDesc < 0 || altDesc < 0 {
		t.Fatal("expected descriptions in both lines")
	}
	if tabDesc != altDesc {
		t.Fatalf("descriptions misaligned: Tab at col %d, Alt+Shift+M at col %d", tabDesc, altDesc)
	}
}

func TestOverlayListsApprovalShortcuts(t *testing.T) {
	out := stripANSI(Overlay(120, 60, OverlayHints{}))
	for _, want := range []string{"always allow", "deny", "edit command/args", "PgUp", "Ctrl+U"} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q: %s", want, out)
		}
	}
}

func TestOverlayWrapsOnNarrowWidth(t *testing.T) {
	out := stripANSI(Overlay(40, 30, OverlayHints{}))
	// With width=40, desc column is max(40-20-4, 20) = 20 chars.
	// "cancel turn · dismiss popup · deny approval" (42 chars) wraps.
	// Verify that "cancel turn" and "approval" end up on different lines.
	lines := strings.Split(out, "\n")
	var cancelLine, approvalLine int
	for i, line := range lines {
		if strings.Contains(line, "cancel turn") {
			cancelLine = i
		}
		if strings.Contains(line, "approval") {
			approvalLine = i
		}
	}
	if cancelLine == 0 || approvalLine == 0 {
		t.Fatal("expected 'cancel turn' and 'approval' in output")
	}
	if cancelLine == approvalLine {
		t.Fatal("expected description to wrap, but 'cancel turn' and 'approval' are on the same line")
	}
}

func TestOverlayRendersMode(t *testing.T) {
	out := stripANSI(Overlay(120, 60, OverlayHints{Mode: "ask"}))
	if !strings.Contains(out, "ask") {
		t.Fatalf("mode not rendered: %s", out)
	}
}
