package contextpack

// minPackTokens and maxPackTokens clamp the window-derived pack budget.
//
// The floor exists because window/8 on a small local model is too little
// to hold even a repo card plus a handful of memories; below this the
// pack stops being worth assembling at all. BudgetForWindow additionally
// holds the floor to a quarter of the window, so it cannot swallow a
// small model's whole context.
//
// The ceiling exists because repo orientation has diminishing returns: a
// 1M-window model does not need 125k tokens of directory listing, and the
// rest of its window is better spent on the current turn's work.
const (
	minPackTokens = 4000
	maxPackTokens = 64000
)

// BudgetForWindow derives the repo-context pack budget from a model's
// resolved context window.
//
// The pack is orientation material, not the whole prompt: it shares the
// window with the system prompt, cross-turn history (see
// agent.historyBudget, which uses the same window/8 shape), the current
// turn's tool results, and the model's own output. An eighth is the same
// share history takes.
//
// A window of 0 means the model is unknown — ResolveModelLimits could not
// find it in config, the limits table, or the catalog. In that case we
// keep the long-standing flat default rather than guessing.
func BudgetForWindow(window int) int {
	if window <= 0 {
		return DefaultMaxTokens
	}
	budget := window / 8
	// The floor is itself capped at a quarter of the window. A flat
	// minPackTokens on a small model would be a huge share of its whole
	// context — 4000 of an 8192 window is ~49%, and agent.historyBudget
	// applies its own 4000 floor to the same window, so between them the
	// prompt would be full before the turn's work is added.
	if floor := min(minPackTokens, window/4); budget < floor {
		budget = floor
	}
	if budget > maxPackTokens {
		budget = maxPackTokens
	}
	return budget
}
