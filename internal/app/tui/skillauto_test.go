package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
)

// The collapsed form must name skills, not just count them: the rail is
// absent on a narrow terminal, when hidden, and while drilled into a
// subagent, so this line has to stand alone.
func TestAutoSkillsCollapsedNamesSkills(t *testing.T) {
	out := ansi.Strip(renderAutoSkills("brainstorming\ntui-design\nwriting-plans", false, 80))
	if !strings.Contains(out, "brainstorming") {
		t.Fatalf("collapsed form must name skills:\n%s", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("collapsed form must be one row, got %d:\n%s", strings.Count(out, "\n"), out)
	}
	if !strings.Contains(out, "+1") {
		t.Fatalf("overflow beyond the shown names must be counted:\n%s", out)
	}
}

func TestAutoSkillsCollapsedShowsAllWhenFew(t *testing.T) {
	out := ansi.Strip(renderAutoSkills("brainstorming\ntui-writer", false, 80))
	for _, want := range []string{"brainstorming", "tui-writer"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "+") {
		t.Errorf("no overflow marker expected: %q", out)
	}
}

func TestAutoSkillsExpandedListsAll(t *testing.T) {
	out := ansi.Strip(renderAutoSkills("a\nb\nc\nd", true, 80))
	for _, want := range []string{"a", "b", "c", "d"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded form missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, glyph.DisclosureExpanded) {
		t.Error("expanded form must show the expanded marker")
	}
}

func TestAutoSkillsEmptyRendersNothing(t *testing.T) {
	for _, in := range []string{"", "  ", "\n\n"} {
		if out := renderAutoSkills(in, false, 80); out != "" {
			t.Errorf("input %q must render nothing, got %q", in, out)
		}
	}
}

func TestAutoSkillsIsNotTinted(t *testing.T) {
	if strings.Contains(renderAutoSkills("a\nb", false, 80), "48;5;") {
		t.Fatal("a skill-load record is history and must not be tinted")
	}
}

func TestAutoSkillsRoutesFromTranscriptItem(t *testing.T) {
	item := session.TranscriptItem{
		Kind: session.KindMessage,
		Message: &session.Message{
			Role:        session.RoleSystem,
			Content:     "brainstorming\ntui-writer",
			ContentType: session.ContentTypeSkillAuto,
		},
	}
	out := ansi.Strip(renderTranscriptItem(item, false, "", regionView{}, nil, 80))
	if !strings.Contains(out, "brainstorming") {
		t.Fatalf("skill-auto item did not route to renderAutoSkills:\n%s", out)
	}
}
