package session

import "testing"

func TestLatestNarrationLine(t *testing.T) {
	s := newTestState()

	if got := s.LatestNarrationLine(); got != "" {
		t.Fatalf("empty session: got %q, want \"\"", got)
	}

	s.AddMessage(RoleAssistant, "first narration", ContentTypeNarration)
	s.AddMessage(RoleAssistant, "plain answer", ContentTypePlain)
	if got := s.LatestNarrationLine(); got != "first narration" {
		t.Fatalf("got %q, want %q", got, "first narration")
	}

	s.AddMessage(RoleAssistant, "second narration\nwith detail", ContentTypeNarration)
	if got := s.LatestNarrationLine(); got != "second narration" {
		t.Fatalf("multi-line: got %q, want first line only", got)
	}
}
