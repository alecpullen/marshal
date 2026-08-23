package agent

import "marshal/internal/llm/schema"

// Provider-reported prompt tokens are ground truth; estimateTokens is a
// heuristic. The runner keeps a per-instance correction ratio derived from
// the latest provider report and applies it to subsequent estimates. The
// clamp bounds the damage from providers whose accounting differs
// structurally (cached prefixes, tool-schema counting).

const (
	tokenRatioMin = 0.5
	tokenRatioMax = 2.0
)

// notePromptTokens updates the correction ratio from a provider report.
// Non-positive reports and empty message lists are ignored.
func (r *Runner) notePromptTokens(messages []schema.ChatMessage, reported int) {
	est := estimateTokens(messages)
	if reported <= 0 || est <= 0 {
		return
	}
	ratio := float64(reported) / float64(est)
	if ratio < tokenRatioMin {
		ratio = tokenRatioMin
	} else if ratio > tokenRatioMax {
		ratio = tokenRatioMax
	}
	r.tokenRatio = ratio
}

// calibratedEstimate returns estimateTokens scaled by the provider-derived
// ratio, or the raw estimate when no ratio has been learned.
func (r *Runner) calibratedEstimate(messages []schema.ChatMessage) int {
	est := estimateTokens(messages)
	if r.tokenRatio <= 0 {
		return est
	}
	return int(float64(est) * r.tokenRatio)
}

// resetTokenRatio clears the learned ratio. Called wherever the wire history
// is rebuilt rather than appended (compaction/rollover) — after a rebuild
// the old ratio describes a transcript that no longer exists.
func (r *Runner) resetTokenRatio() {
	r.tokenRatio = 0
}
