package agent

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

func TestBuildHistoryMessagesKeepsUserAndFinalAssistantTurns(t *testing.T) {
	prior := []session.Message{
		{Role: session.RoleUser, Content: "first question", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "thinking aloud", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "first answer", ContentType: session.ContentTypeMarkdown, Final: true},
		{Role: session.RoleSystem, Content: "Agent stopped: ...", ContentType: session.ContentTypePlain},
	}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, session.GenerationInfo{}, nil)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (user turn + final answer): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != schema.RoleUser || msgs[0].Content != "first question" {
		t.Fatalf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != schema.RoleAssistant || msgs[1].Content != "first answer" {
		t.Fatalf("msgs[1] = %+v", msgs[1])
	}
}

func TestBuildHistoryMessagesAnnotatesToolBackedAnswers(t *testing.T) {
	prior := []session.Message{
		{Role: session.RoleUser, Content: "fix the bug", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "Done, fixed it.", ContentType: session.ContentTypeMarkdown, Final: true, ToolCallCount: 3},
	}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, session.GenerationInfo{}, nil)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (user turn + tool-activity note + final answer): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != schema.RoleUser {
		t.Fatalf("msgs[0] = %+v, want the user turn", msgs[0])
	}
	if msgs[1].Role != schema.RoleSystem || !strings.Contains(msgs[1].Content, "3 tool call") {
		t.Fatalf("msgs[1] = %+v, want a system note mentioning 3 tool calls", msgs[1])
	}
	if msgs[2].Role != schema.RoleAssistant || msgs[2].Content != "Done, fixed it." {
		t.Fatalf("msgs[2] = %+v, want the unmodified final answer", msgs[2])
	}
}

func TestBuildHistoryMessagesDropsOldestBeyondBudget(t *testing.T) {
	long := strings.Repeat("w ", 4000) // ~2000 tokens at 4 chars/token
	prior := []session.Message{
		{Role: session.RoleUser, Content: "old " + long, ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "old answer " + long, ContentType: session.ContentTypeMarkdown, Final: true},
		{Role: session.RoleUser, Content: "recent question", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "recent answer", ContentType: session.ContentTypeMarkdown, Final: true},
	}
	msgs := buildHistoryMessages(prior, 100, session.GenerationInfo{}, nil) // tiny budget: only the recent pair fits
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want the 2 most recent: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "recent question" || msgs[1].Content != "recent answer" {
		t.Fatalf("kept the wrong turns: %+v", msgs)
	}
}

func TestBuildHistoryMessagesFiltersByGenerationBoundary(t *testing.T) {
	prior := []session.Message{
		{ID: 1, Role: session.RoleUser, Content: "pre-boundary question", ContentType: session.ContentTypePlain},
		{ID: 2, Role: session.RoleAssistant, Content: "pre-boundary answer", ContentType: session.ContentTypeMarkdown, Final: true},
		{ID: 3, Role: session.RoleUser, Content: "post-boundary question", ContentType: session.ContentTypePlain},
		{ID: 4, Role: session.RoleAssistant, Content: "post-boundary answer", ContentType: session.ContentTypeMarkdown, Final: true},
	}
	genInfo := session.GenerationInfo{ID: "gen-2", Seq: 2, SeedDigest: "summary of gen-1", StartMsgID: 2}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, genInfo, nil)
	// Expect: seed digest system message + post-boundary user + post-boundary answer = 3
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (seed digest + post-boundary user + post-boundary answer): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != schema.RoleSystem || !strings.Contains(msgs[0].Content, "summary of gen-1") {
		t.Fatalf("msgs[0] should be seed digest system message, got: %+v", msgs[0])
	}
	if msgs[1].Role != schema.RoleUser || msgs[1].Content != "post-boundary question" {
		t.Fatalf("msgs[1] = %+v", msgs[1])
	}
	if msgs[2].Role != schema.RoleAssistant || msgs[2].Content != "post-boundary answer" {
		t.Fatalf("msgs[2] = %+v", msgs[2])
	}
}

func TestBuildHistoryMessagesBoundaryWithoutDigestSkipsPrepend(t *testing.T) {
	prior := []session.Message{
		{ID: 1, Role: session.RoleUser, Content: "pre", ContentType: session.ContentTypePlain},
		{ID: 2, Role: session.RoleUser, Content: "post", ContentType: session.ContentTypePlain},
	}
	genInfo := session.GenerationInfo{ID: "gen-2", Seq: 2, SeedDigest: "", StartMsgID: 1}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, genInfo, nil)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (post-boundary only, no digest prepended): %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "post" {
		t.Fatalf("msgs[0] = %+v, want post-boundary message", msgs[0])
	}
}

func TestBuildHistoryMessagesZeroBoundaryReplaysAll(t *testing.T) {
	prior := []session.Message{
		{ID: 1, Role: session.RoleUser, Content: "first", ContentType: session.ContentTypePlain},
		{ID: 2, Role: session.RoleAssistant, Content: "answer", ContentType: session.ContentTypeMarkdown, Final: true},
	}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, session.GenerationInfo{}, nil)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (all messages replayed): %+v", len(msgs), msgs)
	}
}

func TestBuildHistoryMessagesOffBranchBoundaryFallsBackToFullReplay(t *testing.T) {
	prior := []session.Message{
		{ID: 1, Role: session.RoleUser, Content: "pre-boundary question", ContentType: session.ContentTypePlain},
		{ID: 2, Role: session.RoleAssistant, Content: "pre-boundary answer", ContentType: session.ContentTypeMarkdown, Final: true},
		{ID: 5, Role: session.RoleUser, Content: "post-boundary question", ContentType: session.ContentTypePlain},
		{ID: 6, Role: session.RoleAssistant, Content: "post-boundary answer", ContentType: session.ContentTypeMarkdown, Final: true},
	}
	// StartMsgID=3 is absent from the branch (IDs jump from 2 to 5).
	// The boundary must be ignored and all messages replayed.
	genInfo := session.GenerationInfo{ID: "gen-2", Seq: 2, SeedDigest: "summary of gen-1", StartMsgID: 3}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, genInfo, nil)
	// Expect all 4 messages replayed (no boundary filtering, no digest prepended).
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4 (full replay, off-branch boundary ignored): %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "pre-boundary question" {
		t.Fatalf("msgs[0] = %+v, want pre-boundary question (full replay)", msgs[0])
	}
	if msgs[2].Content != "post-boundary question" {
		t.Fatalf("msgs[2] = %+v, want post-boundary question", msgs[2])
	}
}

func TestBuildHistoryMessagesSkipsSalvagedAnswers(t *testing.T) {
	prior := []session.Message{
		{Role: session.RoleUser, Content: "q", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "half-baked salvage", ContentType: session.ContentTypeMarkdown, Final: true, Salvaged: true},
	}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, session.GenerationInfo{}, nil)
	for _, m := range msgs {
		if strings.Contains(m.Content, "half-baked salvage") {
			t.Fatal("salvaged answers must not be replayed as reliable history")
		}
	}
}

// TestBuildHistoryMessagesEmitsLedgerWhenAuditPresent: a final
// assistant message whose DBID matches an entry in the audits map gets
// the "tools: ..." ledger line in place of the legacy placeholder.
func TestBuildHistoryMessagesEmitsLedgerWhenAuditPresent(t *testing.T) {
	prior := []session.Message{
		{DBID: 100, Role: session.RoleUser, Content: "fix it", ContentType: session.ContentTypePlain},
		{DBID: 101, Role: session.RoleAssistant, Content: "done", ContentType: session.ContentTypeMarkdown, Final: true, ToolCallCount: 3},
	}
	audits := map[int64][]db.ToolAuditEntry{
		101: {
			{Tool: "file.read", Summary: "a.go:1-80", Ok: true},
			{Tool: "shell.run", Summary: "go test", Ok: true},
			{Tool: "shell.run", Summary: "go vet", Ok: false},
		},
	}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, session.GenerationInfo{}, audits)
	// Expect: user, ledger system msg, assistant final.
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != schema.RoleSystem || !strings.Contains(msgs[1].Content, "tools: file.read a.go:1-80, shell.run go test, shell.run go vet (err)") {
		t.Fatalf("expected ledger line in system message, got %+v", msgs[1])
	}
}

// TestBuildHistoryMessagesFallsBackToPlaceholderWhenAuditMissing:
// legacy sessions (no audits map entries) keep the placeholder.
func TestBuildHistoryMessagesFallsBackToPlaceholderWhenAuditMissing(t *testing.T) {
	prior := []session.Message{
		{DBID: 100, Role: session.RoleUser, Content: "fix it", ContentType: session.ContentTypePlain},
		{DBID: 101, Role: session.RoleAssistant, Content: "done", ContentType: session.ContentTypeMarkdown, Final: true, ToolCallCount: 2},
	}
	// audits map exists but has no entry for DBID 101.
	audits := map[int64][]db.ToolAuditEntry{999: nil}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens, session.GenerationInfo{}, audits)
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[1].Content, "2 tool call") {
		t.Fatalf("expected legacy placeholder, got %+v", msgs[1])
	}
}
