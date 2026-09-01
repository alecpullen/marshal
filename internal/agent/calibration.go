package agent

import (
	"unicode/utf8"

	"marshal/internal/llm/schema"
)

// Provider-reported prompt tokens are ground truth; estimateTokens is a
// heuristic. The runner keeps a per-instance correction ratio derived from
// the latest provider report and applies it to subsequent estimates. The
// clamp bounds the damage from providers whose accounting differs
// structurally (cached prefixes, tool-schema counting).

const (
	tokenRatioMin = 0.5
	tokenRatioMax = 2.0
)

// wireEstimate approximates the full request size: the message list plus,
// in native-tools mode, the tool-definition block. The schemas travel on
// the wire on every call but are not part of the message list — ignoring
// them underestimates a native-mode turn by the schema size (~3.5k tokens
// with the default registry), which on a small-window model is a third of
// the turn budget and routinely tripped compaction a turn later than it
// should have fired.
func (r *Runner) wireEstimate(messages []schema.ChatMessage) int {
	est := estimateTokens(messages)
	if !r.NativeTools || r.Registry == nil {
		return est
	}
	runes := 0
	for _, def := range r.buildToolDefinitions() {
		runes += utf8.RuneCountInString(def.Name)
		runes += utf8.RuneCountInString(def.Description)
		runes += len(def.Parameters)
	}
	return est + runes/4
}

// notePromptTokens updates the correction ratio from a provider report.
// Non-positive reports and empty message lists are ignored.
func (r *Runner) notePromptTokens(messages []schema.ChatMessage, reported int) {
	est := r.wireEstimate(messages)
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

// calibratedEstimate returns wireEstimate scaled by the provider-derived
// ratio, or the raw estimate when no ratio has been learned.
func (r *Runner) calibratedEstimate(messages []schema.ChatMessage) int {
	est := r.wireEstimate(messages)
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
