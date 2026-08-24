package session

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestActivityTailPrefersNarration(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AppendThinking("some verbose mid-thought reasoning\n")
	s.AddMessage(RoleAssistant, "Checking the guard before I touch anything.", ContentTypeNarration)

	got := s.SubagentActivityTail(5)
	if len(got) == 0 || !strings.Contains(got[0], "Checking the guard") {
		t.Fatalf("narration must win over streamed reasoning, got %v", got)
	}
}

func TestActivityTailUsesLatestNarration(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AddMessage(RoleAssistant, "first thing", ContentTypeNarration)
	s.AddMessage(RoleAssistant, "second thing", ContentTypeNarration)
	got := s.SubagentActivityTail(5)
	if len(got) == 0 || !strings.Contains(strings.Join(got, " "), "second thing") {
		t.Fatalf("want the most recent narration, got %v", got)
	}
}

// Ordinary assistant prose is not narration and must not be picked up.
func TestActivityTailIgnoresNonNarrationMessages(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AddMessage(RoleAssistant, "an ordinary answer", ContentTypeMarkdown)
	s.AppendThinking("reasoning line\n")
	got := s.SubagentActivityTail(5)
	if strings.Contains(strings.Join(got, " "), "an ordinary answer") {
		t.Fatalf("only ContentTypeNarration counts, got %v", got)
	}
}

// The existing fallbacks are unchanged when there is no narration.
func TestActivityTailFallsBackToReasoningThenAudit(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AppendThinking("reasoning line\n")
	if got := s.SubagentActivityTail(5); len(got) == 0 || !strings.Contains(got[0], "reasoning line") {
		t.Fatalf("want reasoning fallback, got %v", got)
	}
}

func TestActivityTailRespectsN(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AddMessage(RoleAssistant, "one\ntwo\nthree\nfour\nfive\nsix", ContentTypeNarration)
	if got := s.SubagentActivityTail(2); len(got) > 2 {
		t.Fatalf("want at most 2 lines, got %d: %v", len(got), got)
	}
}
