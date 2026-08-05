package agent

import (
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

// defaultHistoryBudgetTokens caps how much prior-turn conversation is
// replayed into a new turn (4-chars-per-token heuristic, same as
// estimateTokens in compact.go).
const defaultHistoryBudgetTokens = 8000

// buildHistoryMessages converts prior session transcript entries into chat
// messages so the model remembers earlier turns. Only user turns and final
// (non-salvaged) assistant answers are replayed: intermediate reasoning,
// plans, system notices, and salvaged fallbacks are noise or unreliable.
// When the total exceeds maxTokens, the oldest turns are dropped first.
//
// When genInfo.StartMsgID > 0, only messages after the generation boundary
// are included, and the generation's seed digest is prepended as a system
// message. A zero-value genInfo replays everything (backward compatible).
//
// The boundary is resolved by membership: the function walks prior looking
// for a message whose ID equals genInfo.StartMsgID. If found, only messages
// after that point are replayed. If NOT found (off-branch / rollback case),
// the boundary is ignored and all messages are replayed — this prevents
// silent history blanking when the boundary ID is absent from the active
// branch.
//
// audits is the cross-turn ledger payload keyed by message DBID
// (result of db.DB.LoadAllTurnToolAudit). When non-nil, every assistant
// turn that has matching ledger entries gets a compact "tools: ..." line
// prepended in place of the legacy "(N tool calls were executed)"
// placeholder. When nil (older sessions without the turn_tool_audit
// table, or legacy message replay), the legacy placeholder is used.
func buildHistoryMessages(prior []session.Message, maxTokens int, genInfo session.GenerationInfo, audits map[int64][]db.ToolAuditEntry) []schema.ChatMessage {
	if maxTokens <= 0 {
		maxTokens = defaultHistoryBudgetTokens
	}

	// Membership-based boundary resolution: walk prior for m.ID == StartMsgID.
	// If found, skip all messages up to and including that ID. If not found
	// (off-branch / rollback case), fall back to full replay.
	boundaryFound := false
	if genInfo.StartMsgID > 0 {
		for _, m := range prior {
			if m.ID == genInfo.StartMsgID {
				boundaryFound = true
				break
			}
		}
	}

	var candidates []schema.ChatMessage
	for _, m := range prior {
		// When a generation boundary is found on the active branch, skip
		// messages up to and including the boundary ID.
		if boundaryFound && m.ID <= genInfo.StartMsgID {
			continue
		}
		switch m.Role {
		case session.RoleUser:
			candidates = append(candidates, schema.ChatMessage{Role: schema.RoleUser, Content: m.Content})
		case session.RoleAssistant:
			if m.Final && !m.Salvaged {
				if m.ToolCallCount > 0 {
					// Prefer the compact ledger line over the legacy
					// placeholder when the turn persisted audit data;
					// fall back to the placeholder for old sessions.
					if line := LedgerLine(audits[m.DBID]); line != "" {
						candidates = append(candidates, schema.ChatMessage{
							Role:    schema.RoleSystem,
							Content: fmt.Sprintf("Previous turn tool activity — %s", line),
						})
					} else {
						candidates = append(candidates, schema.ChatMessage{
							Role:    schema.RoleSystem,
							Content: fmt.Sprintf("(%d tool call(s) were executed by the assistant before the following answer.)", m.ToolCallCount),
						})
					}
				}
				candidates = append(candidates, schema.ChatMessage{Role: schema.RoleAssistant, Content: m.Content})
			}
		}
	}

	// Prepend the seed digest as a system message when a generation boundary
	// is found on the active branch and a digest exists. This gives the model
	// context about what happened before the boundary without replaying the
	// full transcript. When the boundary is not found (off-branch), the
	// digest is also omitted since the full transcript is replayed.
	if boundaryFound && genInfo.SeedDigest != "" {
		digestMsg := schema.ChatMessage{
			Role:    schema.RoleSystem,
			Content: "Previous generation summary: " + genInfo.SeedDigest,
		}
		candidates = append([]schema.ChatMessage{digestMsg}, candidates...)
	}

	// Walk backwards accumulating until the budget is spent, then restore order.
	budget := maxTokens * 4 // chars
	total := 0
	start := len(candidates)
	for i := len(candidates) - 1; i >= 0; i-- {
		total += len(candidates[i].Content)
		if total > budget {
			break
		}
		start = i
	}
	return candidates[start:]
}
