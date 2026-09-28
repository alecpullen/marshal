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
const assistantStubFmt = "[older assistant answer, ~%d chars; fetch it with the transcript_read tool if needed]"

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

// tierSelect classifies each exchange against a budget of maxTokens*4
// characters (the caller passes exactly that). An exchange is an
// indivisible unit: every candidate from exchange.start to exchange.end
// is emitted together, so the levels are whole-exchange outcomes.
// tieredFull emits the exchange with its assistant answer intact,
// tieredStub collapses that answer to assistantStubFmt, and tieredDrop
// drops the exchange entirely — including its user message, because the
// emit loop skips the whole exchange (buildHistoryMessages,
// `if ke == tieredDrop { continue }`).
//
// Rules from plan Task 6/C:
//
//	(1) the exchange is the unit of aging. A kept exchange (tieredFull
//	    or tieredStub) always replays its user message, ledger lines and
//	    subagent reports; only the assistant answer is collapsed, at
//	    tieredStub. A drop is whole-exchange, so it takes the user
//	    message with it.
//	(2) the newest pinnedRecentExchanges are upgraded to Full in a
//	    final pass when the remaining budget covers the delta from
//	    their current tier. Exchanges already at Drop (curCost 0) are
//	    skipped: the delta would be the whole answer, and rule (2)
//	    protects recent content rather than spending budget the first
//	    pass already committed.
//	(3) older assistant answers are stubbed before being dropped: the
//	    first pass runs oldest -> newest and each exchange takes the
//	    best tier the remaining budget affords (Full, else Stub).
//	(4) exchanges the budget cannot reach at all are tieredDrop.
//	(5) the newest assistant answer — the exchange the current user
//	    turn responds to — is forced to tieredFull last and
//	    unconditionally, even when the budget cannot afford it. Losing
//	    the proposal under active discussion is worse than a modest
//	    over-budget prompt, so this override is deliberately not
//	    budget-gated. It only ever adds content; it never demotes an
//	    exchange an earlier pass classified.
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
			// initialised).
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
		// Upgrade cost is the delta from the current tier to full, not
		// the full cost: the exchange already consumed curCost of budget
		// in the prior pass, so only the incremental cost is new.
		delta := full - curCost
		if remaining >= delta {
			level[i] = tieredFull
			remaining -= delta
		}
	}

	// Rule (5): the newest assistant answer — the one the current user turn is
	// responding to — is never stubbed or dropped. Losing the proposal under
	// active discussion is worse than a modest over-budget prompt, so this
	// override is not budget-gated.
	if last := newestAssistantExchange(exchanges, cands); last >= 0 {
		level[last] = tieredFull
	}

	return level
}

// newestAssistantExchange returns the index of the newest exchange that
// contains an assistant-full candidate, or -1 when none exists (e.g. a
// trailing orphan user turn with no answer yet).
func newestAssistantExchange(exchanges []exchange, cands []candEntry) int {
	for i := len(exchanges) - 1; i >= 0; i-- {
		for _, ce := range cands[exchanges[i].start:exchanges[i].end] {
			if ce.kind == "assistant-full" {
				return i
			}
		}
	}
	return -1
}

func stubContentLen(originalLen int) int {
	return len(fmt.Sprintf(assistantStubFmt, originalLen))
}
