package sidepanel

import (
	"strings"
	"testing"
)

func TestSkillsSectionIrrelevantWhenEmpty(t *testing.T) {
	if (SkillsSection{}).Relevant(Data{}) {
		t.Fatal("no active skills means the section must not render")
	}
}

func TestSkillsSectionListsNames(t *testing.T) {
	d := Data{Skills: []string{"brainstorming", "tui-design", "writing-plans"}}
	rows := SkillsSection{}.Render(d, 40, 0)
	if len(rows) != 3 {
		t.Fatalf("want a row per skill, got %d: %v", len(rows), rows)
	}
	if !strings.Contains(rows[0], "brainstorming") {
		t.Fatalf("first row = %q", rows[0])
	}
}

func TestSkillsSectionClipsToMaxRows(t *testing.T) {
	d := Data{Skills: []string{"a", "b", "c", "d", "e"}}
	if rows := (SkillsSection{}).Render(d, 40, 2); len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
}

func TestSkillsSectionOneLineSummarises(t *testing.T) {
	d := Data{Skills: []string{"a", "b", "c"}}
	if got := (SkillsSection{}).OneLine(d, 40); !strings.Contains(got, "3") {
		t.Fatalf("one-line form should carry the count, got %q", got)
	}
}

// Long names must not overflow the rail's width.
func TestSkillsSectionTruncatesToWidth(t *testing.T) {
	d := Data{Skills: []string{strings.Repeat("x", 200)}}
	for _, row := range (SkillsSection{}).Render(d, 20, 0) {
		if len([]rune(row)) > 20 {
			t.Fatalf("row exceeds width: %d runes", len([]rune(row)))
		}
	}
}

func TestSkillsSectionIDIsStable(t *testing.T) {
	// The ID is the config key for tui.side_panel.hidden; changing it
	// silently un-hides the section for anyone who had hidden it.
	if got := (SkillsSection{}).ID(); got != "skills" {
		t.Fatalf("ID = %q, want \"skills\"", got)
	}
}
