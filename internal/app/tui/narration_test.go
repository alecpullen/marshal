package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

func TestNarrationRendersWithAmbientGutter(t *testing.T) {
	out := renderNarration("Checking whether the guard already exists.", 60)
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
		if out := renderNarration(in, 60); out != "" {
			t.Errorf("input %q must render nothing, got %q", in, out)
		}
	}
}

// Short narration renders in full with no disclosure marker.
func TestNarrationShortRendersInFull(t *testing.T) {
	out := renderNarration("One short line.", 60)
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("want 1 row, got %d:\n%s", n, ansi.Strip(out))
	}
	if strings.Contains(ansi.Strip(out), glyph.DisclosureCollapsed) {
		t.Fatal("a short narration must not show a disclosure marker")
	}
}

// Narration is a record of something already said: flat, per the rule
// established in UX batch 3 (tinted = happening now).
func TestNarrationIsNotTinted(t *testing.T) {
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	t.Cleanup(func() { theme.Reload(theme.LoadFor(false, "xterm-256color")) })
	if strings.Contains(renderNarration("some narration", 60), "48;5;") {
		t.Fatal("narration is history and must not be tinted")
	}
}

// Narration must render through the same markdown pipeline as the final
// answer: markup is interpreted, never shown literally.
func TestNarrationRendersMarkdownLikeFinalAnswer(t *testing.T) {
	plain := ansi.Strip(renderNarration("# Heading\n\n**bold** move", 60))
	if strings.Contains(plain, "# Heading") {
		t.Fatalf("narration markup must be interpreted, got %q", plain)
	}
	if strings.Contains(plain, "**") {
		t.Fatalf("emphasis markers must not survive rendering, got %q", plain)
	}
	if !strings.Contains(plain, "Heading") || !strings.Contains(plain, "bold move") {
		t.Fatalf("narration content missing after markdown rendering, got %q", plain)
	}
}

// Narration is never truncated or shortened: every logical line renders,
// however long the block is, with no disclosure marker.
func TestNarrationNeverTruncates(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, "narration line %d\n", i)
	}
	plain := ansi.Strip(renderNarration(sb.String(), 60))
	for i := 0; i < 20; i++ {
		if !strings.Contains(plain, fmt.Sprintf("narration line %d", i)) {
			t.Fatalf("narration line %d missing — narration must never be truncated:\n%s", i, plain)
		}
	}
	if strings.Contains(plain, glyph.DisclosureCollapsed) || strings.Contains(plain, glyph.DisclosureExpanded) {
		t.Fatal("narration must never collapse behind a disclosure marker")
	}
}

// A single long logical line wraps but loses no words — the row cap used
// to cut these off behind a disclosure marker.
func TestNarrationLongLineFullyRendered(t *testing.T) {
	plain := ansi.Strip(renderNarration(strings.Repeat("wordy ", 200), 40))
	if got := strings.Count(plain, "wordy"); got != 200 {
		t.Fatalf("wordy count = %d, want 200 — nothing may be cut:\n%s", got, plain)
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
	plain := ansi.Strip(renderTranscriptItem(item, false, "", regionView{}, nil, 60))
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
	plain := ansi.Strip(renderTranscriptItem(item, false, "", regionView{}, nil, 60))
	if strings.HasPrefix(plain, " "+glyph.Ambient+" ") {
		t.Fatalf("ordinary assistant prose must not render as narration: %q", plain)
	}
}

// Narration obeys the transcript width contract even when wrapping a long
// unbroken token.
func TestNarrationLinesFitWidth(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := renderNarration(long, 40)
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
	out := renderNarration("a\tb\nc\td", 60)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "\t") {
		t.Fatalf("tabs must be expanded, got %q", plain)
	}
}
