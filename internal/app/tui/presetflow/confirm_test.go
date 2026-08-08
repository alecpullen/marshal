package presetflow

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHandleKeyEnterWithNoEditReturnsDone(t *testing.T) {
	s := NewConfirmState(Limits{ContextWindow: 32768, MaxOutputTokens: 8192})
	result, _ := s.HandleKey(tea.KeyPressMsg{Code: 13})
	if result != ConfirmDone {
		t.Fatalf("result = %v, want ConfirmDone", result)
	}
}

func TestHandleKeyEscWithNoEditReturnsCancelled(t *testing.T) {
	s := NewConfirmState(Limits{})
	result, _ := s.HandleKey(tea.KeyPressMsg{Code: 27})
	if result != ConfirmCancelled {
		t.Fatalf("result = %v, want ConfirmCancelled", result)
	}
}

func TestHandleKeyCEntersContextEditWithoutReturningDone(t *testing.T) {
	s := NewConfirmState(Limits{ContextWindow: 32768})
	result, _ := s.HandleKey(tea.KeyPressMsg{Text: "c", Code: 'c'})
	if result != ConfirmPending {
		t.Fatalf("result = %v, want ConfirmPending (entering edit mode)", result)
	}
	if !s.Editing() {
		t.Fatal("expected Editing()=true after 'c'")
	}
}

func TestHandleKeyEditContextThenEnterCommitsAndStaysPending(t *testing.T) {
	// Start unknown so the pre-filled edit field is empty — typing appends
	// to the pre-filled value, so a known starting figure would concatenate.
	s := NewConfirmState(Limits{ContextWindow: 0, ContextSource: SourceUnknown})
	s.HandleKey(tea.KeyPressMsg{Text: "c", Code: 'c'})
	for _, r := range "65536" {
		s.UpdateInput(tea.KeyPressMsg{Text: string(r), Code: r})
	}
	result, _ := s.HandleKey(tea.KeyPressMsg{Code: 13})
	if result != ConfirmPending {
		t.Fatalf("result = %v, want ConfirmPending (committing an edit, not the whole screen)", result)
	}
	if s.Editing() {
		t.Fatal("expected Editing()=false after committing the edit")
	}
	if s.Limits.ContextWindow != 65536 || s.Limits.ContextSource != SourceEdited {
		t.Errorf("Limits = %+v, want ContextWindow=65536 Source=edited", s.Limits)
	}
}

func TestHandleKeyEditThenEscCancelsOnlyTheEditNotTheScreen(t *testing.T) {
	s := NewConfirmState(Limits{ContextWindow: 32768, ContextSource: SourceCatalog})
	s.HandleKey(tea.KeyPressMsg{Text: "c", Code: 'c'})
	result, _ := s.HandleKey(tea.KeyPressMsg{Code: 27})
	if result != ConfirmPending {
		t.Fatalf("result = %v, want ConfirmPending (Esc while editing cancels the edit, not the screen)", result)
	}
	if s.Editing() {
		t.Fatal("expected Editing()=false after Esc")
	}
	if s.Limits.ContextWindow != 32768 {
		t.Errorf("ContextWindow = %d, want unchanged 32768", s.Limits.ContextWindow)
	}
}

func TestHandleKeyEditZeroClearsToUnknown(t *testing.T) {
	// Pre-filled edit of 32768; clearing via typing is covered by the
	// unknown-start case above, so here drive the text field directly.
	s := NewConfirmState(Limits{ContextWindow: 32768, ContextSource: SourceCatalog})
	s.HandleKey(tea.KeyPressMsg{Text: "c", Code: 'c'})
	s.input.SetValue("0")
	s.HandleKey(tea.KeyPressMsg{Code: 13})
	if s.Limits.ContextWindow != 0 || s.Limits.ContextSource != SourceUnknown {
		t.Errorf("Limits = %+v, want ContextWindow=0 Source=unknown", s.Limits)
	}
}

func TestRenderShowsToolCallingRowWhenKnown(t *testing.T) {
	toolCalling := true
	s := NewConfirmState(Limits{ContextWindow: 32768, ContextSource: SourceCatalog,
		MaxOutputTokens: 8192, OutputSource: SourceCatalog,
		ToolCalling: &toolCalling, ToolSource: SourceProbed})
	out := s.Render(60)
	if !strings.Contains(out, "native") || !strings.Contains(out, string(SourceProbed)) {
		t.Errorf("Render output missing tool-calling row: %q", out)
	}
}

func TestRenderOmitsToolCallingRowWhenUnknown(t *testing.T) {
	s := NewConfirmState(Limits{ContextWindow: 32768, ContextSource: SourceCatalog})
	out := s.Render(60)
	if strings.Contains(out, "tool calling") {
		t.Errorf("Render output should omit the tool-calling row when nothing was detected: %q", out)
	}
}
