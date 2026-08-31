package contextpack

// maxPackTokens caps the window-derived pack budget. The ceiling exists
// because repo orientation has diminishing returns: a 1M-window model does
// not need 125k tokens of directory listing, and the rest of its window is
// better spent on the current turn's work.
const maxPackTokens = 64000

// BudgetForWindow derives the repo-context pack budget from a model's
// resolved context window.
//
// The pack is orientation material, not the whole prompt: it shares the
// window with the system prompt, cross-turn history (see
// agent.historyBudget, which uses the same window/8 shape), the current
// turn's tool results, and the model's own output. An eighth is the same
// share history takes.
//
// There is deliberately no absolute floor: a flat floor (previously 4000
// tokens) is a huge share of a small model's whole context — 4000 of a
// 16384 window is ~24%, and agent.historyBudget applies its own floor to
// the same window, so between them the prompt would be full before the
// turn's work is added. Small windows take the window/8 derivation
// directly (16384 → 2048, 8192 → 1024).
//
// A window of 0 means the model is unknown — ResolveModelLimits could not
// find it in config, the limits table, or the catalog. In that case we
// keep the long-standing flat default rather than guessing.
func BudgetForWindow(window int) int {
	if window <= 0 {
		return DefaultMaxTokens
	}
	budget := window / 8
	if budget > maxPackTokens {
		budget = maxPackTokens
	}
	return budget
}
