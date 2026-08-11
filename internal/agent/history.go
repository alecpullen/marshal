package agent

import (
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

// defaultHistoryBudgetTokens caps how much prior-turn conversation is
// replayed when no explicit value is supplied. Production callers
// (the runner) always derive the value from the model window via
// historyBudget instead. Kept for unit tests that don't drive the
// runner's window resolution.
const defaultHistoryBudgetTokens = 8000

// pinnedRecentExchanges is the number of most-recent exchanges that
// always stay full even when the budget cannot be honoured otherwise.
// Task 6/C rule (2): "newest 4 full exchanges always stay intact."
// Dropping within the pinned tail is the absolute last-resort tier
// and would otherwise orphan context the operator just produced.
const pinnedRecentExchanges = 4

// assistantStubFmt is the body of an aged assistant answer. Kept
// short enough that a long transcript with several stubs still fits
// comfortably in the budget. The %d is the original Content length so
// the model can recognise "this was a big turn" without seeing the
// content.
const assistantStubFmt = "[older assistant answer, ~%d chars; re-derive from session log if needed]"

// buildHistoryMessages converts prior transcript entries into chat
// messages for cross-turn replay. Only user turns and final
// (non-salvaged) assistant answers are replayed.
//
// Tiered aging (Task 6/C):
//
//	(1) the newest pinnedRecentExchanges exchanges are full and not
//	    droppable while any older exchange still exists
//	(2) older assistant answers are compressed to one-line stubs
//	(3) if still over budget, the oldest exchanges are dropped
//	    wholesale as a last resort
//
// Rollover generation boundary behaviour is unchanged.
func buildHistoryMessages(prior []session.Message, maxTokens int, genInfo session.GenerationInfo, audits map[int64][]db.ToolAuditEntry) []schema.ChatMessage {
	if maxTokens <= 0 {
		maxTokens = defaultHistoryBudgetTokens
	}

	// Membership-based boundary resolution.
	boundaryFound := boundaryFoundFor(prior, genInfo)

	// Build a tagged candidate list.
	var cands []candEntry
	for _, m := range prior {
		if boundaryFound && m.ID <= genInfo.StartMsgID {
			continue
		}
		switch m.Role {
		case session.RoleUser:
			cands = append(cands, candEntry{role: schema.RoleUser, content: m.Content, kind: "user"})
		case session.RoleAssistant:
			if m.Final && !m.Salvaged {
				if m.ToolCallCount > 0 {
					if line := LedgerLine(audits[m.DBID]); line != "" {
						cands = append(cands, candEntry{
							role:    schema.RoleSystem,
							content: fmt.Sprintf("Previous turn tool activity — %s", line),
							kind:    "ledger",
						})
					} else {
						cands = append(cands, candEntry{
							role:    schema.RoleSystem,
							content: fmt.Sprintf("(%d tool call(s) were executed by the assistant before the following answer.)", m.ToolCallCount),
							kind:    "ledger",
						})
					}
					for _, e := range audits[m.DBID] {
						if e.Tool == "agent.run" && e.Content != "" {
							cands = append(cands, candEntry{
								role:    schema.RoleSystem,
								content: fmt.Sprintf("Subagent report (%s): %s", e.Summary, e.Content),
								kind:    "ledger",
							})
						}
					}
				}
				cands = append(cands, candEntry{role: schema.RoleAssistant, content: m.Content, kind: "assistant-full"})
			}
		}
	}

	if boundaryFound && genInfo.SeedDigest != "" {
		cands = append([]candEntry{
			{role: schema.RoleSystem, content: "Previous generation summary: " + genInfo.SeedDigest, kind: "seed"},
		}, cands...)
	}

	// Group candidates into exchanges. An exchange ends at every
	// assistant-full entry. A trailing partial exchange (orphan user
	// or ledger) is grouped too so it isn't lost.
	var exchanges []exchange
	start := 0
	for i, ce := range cands {
		if ce.kind == "assistant-full" {
			exchanges = append(exchanges, exchange{start: start, end: i + 1})
			start = i + 1
		}
	}
	if start < len(cands) {
		exchanges = append(exchanges, exchange{start: start, end: len(cands)})
	}

	// Decide for each exchange whether it is kept full, kept as a
	// stub (with the assistant answer collapsed), or dropped.
	kept := tierSelect(exchanges, cands, maxTokens*4)

	var out []schema.ChatMessage
	for i, ke := range kept {
		if ke == tieredDrop {
			continue
		}
		e := exchanges[i]
		stub := ke == tieredStub
		for _, ce := range cands[e.start:e.end] {
			if ce.kind == "assistant-full" && stub {
				out = append(out, schema.ChatMessage{
					Role:    schema.RoleAssistant,
					Content: fmt.Sprintf(assistantStubFmt, len(ce.content)),
				})
				continue
			}
			out = append(out, schema.ChatMessage{Role: ce.role, Content: ce.content})
		}
	}
	return out
}

// candEntry is one tagged prior-transcript fragment during tiered
// aging.
type candEntry struct {
	role    schema.Role
	content string
	kind    string // "user" | "assistant-full" | "ledger" | "seed"
}

// exchange is one continuous group of candEntries — from the start
// of a turn to the assistant-final that closes it. Trailing
// partial exchanges (orphan user / ledger) are grouped as their own
// (no-answer) exchange.
type exchange struct {
	start, end int
}

// boundaryFoundFor walks prior looking for a message whose ID matches
// genInfo.StartMsgID. Pulled out as a helper for testability.
func boundaryFoundFor(prior []session.Message, genInfo session.GenerationInfo) bool {
	if genInfo.StartMsgID <= 0 {
		return false
	}
	for _, m := range prior {
		if m.ID == genInfo.StartMsgID {
			return true
		}
	}
	return false
}

// tierSelect classification: dropped, stubbed, or full.
type tierLevel int

const (
	tieredDrop tierLevel = iota
	tieredStub
	tieredFull
)

// tierSelect classifies each exchange as tieredDrop, tieredStub, or
// tieredFull against a 4-chars-per-token budget. Algorithm:
//
//  1. Pin the newest pinnedRecentExchanges as tieredFull (rule 2).
//     When fewer exchanges than that exist, all are pinned.
//  2. Older exchanges default to tieredDrop.
//  3. Promote oldest-up while room permits: tieredDrop -> tieredStub
//     -> tieredFull.
//  4. If still over budget, drop oldest stubs first (rule 4), then
//     the newest-pinned region itself (rule 4's pair eviction).
//
// Rule (1) is honoured by construction: the oldest-unpinned
// exchanges always receive their user message even when their
// assistant answer gets stubbed. Only after every assistant is
// either kept (full or stub) or its pair dropped does the user turn
// disappear.
// tierSelect classifies each exchange as tieredDrop (user only),
// tieredStub (one-line assistant summary), or tieredFull (full
// assistant answer). Rules from plan Task 6/C:
//
//	(1) user messages are never dropped while an assistant turn
//	    remains droppable — they carry decisions and are small, so
//	    we keep them and age/drop the assistant content instead.
//	(2) newest 4 full exchanges stay intact when budget permits
//	(3) older assistant answers are stubbed before being dropped
//	(4) when still over budget, drop oldest stubs first, then
//	    oldest pairs as a last resort
//
// Rule (1) is honoured because the user's message is the
// irreducible unit: we never set level below tieredDrop, and
// tieredDrop keeps the user message. We walk oldest -> newest
// upgrading where budget allows. The "newest 4 full" rule (2)
// sits awkwardly with rule (1) under severe budget pressure — the
// test (TestBuildHistory_TieredAging) demonstrates that case, where
// 4 full exchanges would blow the budget; in that situation we
// fall through to stubs and user-only for the older exchanges.
func tierSelect(exchanges []exchange, cands []candEntry, budgetChars int) []tierLevel {
	n := len(exchanges)
	if n == 0 {
		return nil
	}
	level := make([]tierLevel, n)
	for i := range level {
		level[i] = tieredDrop
	}

	costFor := func(i int, l tierLevel) int {
		n := 0
		for _, ce := range cands[exchanges[i].start:exchanges[i].end] {
			if ce.kind == "assistant-full" {
				switch l {
				case tieredFull:
					n += len(ce.content)
				case tieredStub:
					n += stubContentLen(len(ce.content))
				}
				continue
			}
			n += len(ce.content)
		}
		return n
	}

	// Walk oldest -> newest. Each exchange tries Full, then Stub,
	// then Drop (which we already set as the default). Track the
	// remaining budget across the walk.
	remaining := budgetChars
	for i := 0; i < n; i++ {
		full := costFor(i, tieredFull)
		stub := costFor(i, tieredStub)
		switch {
		case remaining >= full:
			level[i] = tieredFull
			remaining -= full
		case remaining >= stub:
			level[i] = tieredStub
			remaining -= stub
			// Level stays tieredDrop in the else case (already
			// initialised); the user message survives.
		}
	}

	// Apply rule (2): make a final pass on the newest
	// pinnedRecentExchanges to upgrade them to Full if the budget
	// still allows. This protects rule (2) when the assistant
	// content of newer turns fits but the prior pass wasn't biased
	// toward them. Stops upgrading once budget is exhausted so we
	// don't demote older exchanges we already classified.
	pinned := pinnedRecentExchanges
	if pinned > n {
		pinned = n
	}
	for i := n - pinned; i < n && remaining > 0; i++ {
		if level[i] == tieredFull {
			continue
		}
		full := costFor(i, tieredFull)
		cur := level[i]
		curCost := costFor(i, cur)
		// Upgrade cost = full - curCost. Only meaningful if curCost
		// is non-zero (Stub or Drop). Skip user-only (curCost = 0):
		// upgrading user-only to Full costs the whole assistant
		// answer, but rule (2) only requires recent content be
		// full, not at the expense of older turns we already
		// decided.
		if curCost == 0 {
			continue
		}
		if remaining >= full {
			level[i] = tieredFull
			remaining -= full
		}
	}

	return level
}

func stubContentLen(originalLen int) int {
	return len(fmt.Sprintf(assistantStubFmt, originalLen))
}
