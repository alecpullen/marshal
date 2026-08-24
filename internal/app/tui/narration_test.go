package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

func TestNarrationRendersWithAmbientGutter(t *testing.T) {
	out := renderNarration("Checking whether the guard already exists.", false, 60)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "Checking whether the guard already exists.") {
		t.Fatalf("narration text missing:\n%s", plain)
	}
	if !strings.HasPrefix(plain, " "+glyph.Ambient+" ") {
		t.Fatalf("narration must use the ambient gutter, got %q", plain)
	}
}

func TestNarrationEmptyRendersNothing(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t\n"} {
		if out := renderNarration(in, false, 60); out != "" {
			t.Errorf("input %q must render nothing, got %q", in, out)
		}
	}
}

// Short narration renders in full with no disclosure marker.
func TestNarrationShortRendersInFull(t *testing.T) {
	out := renderNarration("One short line.", false, 60)
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("want 1 row, got %d:\n%s", n, ansi.Strip(out))
	}
	if strings.Contains(ansi.Strip(out), glyph.DisclosureCollapsed) {
		t.Fatal("a short narration must not show a disclosure marker")
	}
}

// The cap counts DISPLAY rows after wrapping, not logical lines — the same
// ordering lesson as liveregion. One long logical line must still collapse.
func TestNarrationLongSingleLineCollapses(t *testing.T) {
	long := strings.Repeat("wordy ", 200)
	out := renderNarration(long, false, 40)
	if n := strings.Count(out, "\n"); n > narrationCollapsedRows {
		t.Fatalf("collapsed narration = %d rows, cap is %d", n, narrationCollapsedRows)
	}
	if !strings.Contains(ansi.Strip(out), glyph.DisclosureCollapsed) {
		t.Fatal("a collapsed narration must show a disclosure marker")
	}
}

func TestNarrationExpandsInFull(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("line of narration\n")
	}
	collapsed := strings.Count(renderNarration(sb.String(), false, 60), "\n")
	expanded := strings.Count(renderNarration(sb.String(), true, 60), "\n")
	if collapsed > narrationCollapsedRows {
		t.Fatalf("collapsed = %d rows, cap is %d", collapsed, narrationCollapsedRows)
	}
	if expanded <= collapsed {
		t.Fatalf("expanded (%d rows) must show more than collapsed (%d)", expanded, collapsed)
	}
	if !strings.Contains(ansi.Strip(renderNarration(sb.String(), true, 60)), glyph.DisclosureExpanded) {
		t.Fatal("expanded narration must show the expanded marker")
	}
}

// Narration is a record of something already said: flat, per the rule
// established in UX batch 3 (tinted = happening now).
func TestNarrationIsNotTinted(t *testing.T) {
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	t.Cleanup(func() { theme.Reload(theme.LoadFor(false, "xterm-256color")) })
	if strings.Contains(renderNarration("some narration", false, 60), "48;5;") {
		t.Fatal("narration is history and must not be tinted")
	}
}

// Narration is prose, not markdown: a stray '#' must not become a heading.
func TestNarrationIsNotMarkdownRendered(t *testing.T) {
	plain := ansi.Strip(renderNarration("# not a heading and *not* emphasis", false, 60))
	if !strings.Contains(plain, "# not a heading") {
		t.Fatalf("narration must render as plain prose, got %q", plain)
	}
}

// Routing: a narration message must reach renderNarration, and an ordinary
// assistant message must not.
func TestTranscriptItemRoutesNarration(t *testing.T) {
	item := session.TranscriptItem{
		Kind: session.KindMessage,
		Message: &session.Message{
			Role:        session.RoleAssistant,
			Content:     "Checking the guard.",
			ContentType: session.ContentTypeNarration,
		},
	}
	plain := ansi.Strip(renderTranscriptItem(item, false, "", 0, nil, 60))
	if !strings.HasPrefix(plain, " "+glyph.Ambient+" ") {
		t.Fatalf("narration item did not route to renderNarration: %q", plain)
	}
}

func TestOrdinaryAssistantMessageUnaffected(t *testing.T) {
	item := session.TranscriptItem{
		Kind: session.KindMessage,
		Message: &session.Message{
			Role:        session.RoleAssistant,
			Content:     "ordinary prose",
			ContentType: session.ContentTypeMarkdown,
		},
	}
	plain := ansi.Strip(renderTranscriptItem(item, false, "", 0, nil, 60))
	if strings.HasPrefix(plain, " "+glyph.Ambient+" ") {
		t.Fatalf("ordinary assistant prose must not render as narration: %q", plain)
	}
}

// Exactly at the cap: no marker, all rows shown.
func TestNarrationExactlyAtCapNoMarker(t *testing.T) {
	// Build content that wraps to exactly narrationCollapsedRows rows at
	// width 40. Each row holds ~38 cells of content (cw = contentWidth(40)).
	// We use short lines that each fit on one row.
	var sb strings.Builder
	for i := 0; i < narrationCollapsedRows; i++ {
		sb.WriteString("short line\n")
	}
	out := renderNarration(sb.String(), false, 40)
	if strings.Contains(ansi.Strip(out), glyph.DisclosureCollapsed) {
		t.Fatal("at-cap narration must not show a disclosure marker")
	}
	if n := strings.Count(out, "\n"); n != narrationCollapsedRows {
		t.Fatalf("at-cap narration = %d rows, want %d", n, narrationCollapsedRows)
	}
}

// The disclosure marker must not push the last row past the content width.
func TestNarrationCollapsedMarkerDoesNotOverflow(t *testing.T) {
	// A long unbroken token fills the wrap budget, so the last collapsed
	// row is at full width. The marker must fit within cw, not overflow.
	long := strings.Repeat("x", 200)
	out := renderNarration(long, false, 40)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("narration line width %d exceeds viewport 40:\n%q", w, line)
		}
	}
}

// Tabs in narration content are expanded before wrapping so width math
// matches what the terminal renders.
func TestNarrationExpandsTabs(t *testing.T) {
	out := renderNarration("a\tb\nc\td", false, 60)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "\t") {
		t.Fatalf("tabs must be expanded, got %q", plain)
	}
}
