// Package presetflow implements the shared "discovered model → confirmed
// preset" flow used by both the /connect wizard and Settings' per-role
// model assignment: resolving context/output limits and tool-calling
// support, confirming or editing them interactively, and materializing the
// result into a named ModelPreset.
package presetflow

import (
	"marshal/internal/llm/catalog"
	"marshal/internal/llm/schema"
)

// Source labels where a limit or capability figure came from, so the user
// can tell a fetched number from a guess.
type Source string

const (
	SourceFetched Source = "fetched limits table"
	SourceCatalog Source = "local catalog"
	SourcePreset  Source = "saved preset"
	SourceEdited  Source = "edited"
	SourceProbed  Source = "detected via /api/show"
	SourceUnknown Source = "unknown"
)

// Limits is a model's context/output caps and tool-calling support, each
// with independent provenance. A zero ContextWindow/MaxOutputTokens with
// SourceUnknown means nothing knew — render as "unknown," never as 0. A nil
// ToolCalling means no source reported it.
type Limits struct {
	ContextWindow   int
	MaxOutputTokens int
	ContextSource   Source
	OutputSource    Source
	ToolCalling     *bool
	ToolSource      Source
}

// Resolve finds modelID's limits from what discovery already resolved
// (bulk fetched-limits-table enrichment already folded into ModelInfo by
// the provider layer) and, failing that, the local catalog for the numeric
// fields. ToolCalling comes only from discovered (the catalog has no
// tool-calling column) and is left nil/SourceUnknown if unreported — a
// later per-model capability probe (see probe.go) may fill it in via
// WithProbed.
func Resolve(discovered []schema.ModelInfo, modelID string) Limits {
	var ctx, out int
	var toolCalling *bool
	for _, mi := range discovered {
		if mi.ID == modelID {
			ctx, out, toolCalling = mi.ContextWindow, mi.MaxOutputTokens, mi.ToolCalling
			break
		}
	}

	lim := Limits{ContextWindow: ctx, MaxOutputTokens: out}
	lim.ContextSource, lim.OutputSource = SourceFetched, SourceFetched

	if ctx == 0 || out == 0 {
		catCtx, catOut := catalog.Lookup(modelID, nil)
		if ctx == 0 {
			lim.ContextWindow, lim.ContextSource = catCtx, SourceCatalog
		}
		if out == 0 {
			lim.MaxOutputTokens, lim.OutputSource = catOut, SourceCatalog
		}
	}
	if lim.ContextWindow == 0 {
		lim.ContextSource = SourceUnknown
	}
	if lim.MaxOutputTokens == 0 {
		lim.OutputSource = SourceUnknown
	}

	lim.ToolCalling = toolCalling
	if toolCalling != nil {
		lim.ToolSource = SourceFetched
	} else {
		lim.ToolSource = SourceUnknown
	}
	return lim
}

// WithPreset prefers context/output figures already saved for the same
// provider+model over a catalog guess or unknown, so re-picking a model
// whose limits came back unknown does not show a guessed default (or
// "unknown") when a prior confirmation already set them. A non-zero
// fetched/probed/edited figure is authoritative and is not overwritten.
// Fields resolve independently.
func (l Limits) WithPreset(presetContext, presetMaxOutput int) Limits {
	if presetContext != 0 && (l.ContextWindow == 0 || l.ContextSource == SourceCatalog) {
		l.ContextWindow, l.ContextSource = presetContext, SourcePreset
	}
	if presetMaxOutput != 0 && (l.MaxOutputTokens == 0 || l.OutputSource == SourceCatalog) {
		l.MaxOutputTokens, l.OutputSource = presetMaxOutput, SourcePreset
	}
	return l
}

// WithProbed merges a per-model capability probe's result over the current
// limits. ToolCalling replaces whatever the catalog or discovery stage
// reported, because the probe reflects the actual pulled model file. The
// context window is treated as authoritative as well: a positive probed
// value overwrites catalog/saved/fetched figures (unlike Materialize,
// which never overwrites a saved non-zero field) so a wrong catalog figure
// can be corrected by re-probing. A zero probed window is ignored to
// avoid erasing an already-known cap.
func (l Limits) WithProbed(toolCalling *bool, contextWindow int) Limits {
	if toolCalling != nil {
		l.ToolCalling, l.ToolSource = toolCalling, SourceProbed
	}
	if contextWindow > 0 {
		l.ContextWindow, l.ContextSource = contextWindow, SourceProbed
	}
	return l
}
