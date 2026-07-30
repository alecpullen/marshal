package connect

import (
	"marshal/internal/llm/catalog"
	"marshal/internal/llm/schema"
)

// LimitSource labels where a limit figure came from, so the user can tell
// a fetched number from a guess.
type LimitSource string

const (
	SourceFetched LimitSource = "fetched limits table"
	SourceCatalog LimitSource = "local catalog"
	SourcePreset  LimitSource = "saved preset"
	SourceEdited  LimitSource = "edited"
	SourceUnknown LimitSource = "unknown"
)

// ModelLimits is a model's context and output caps with per-field
// provenance. Zero with SourceUnknown means nothing knew — it must be
// rendered as "unknown", never as the number 0.
type ModelLimits struct {
	ContextWindow   int
	MaxOutputTokens int
	ContextSource   LimitSource
	OutputSource    LimitSource
}

// resolveLimits finds modelID's limits, preferring what discovery already
// resolved from the fetched limits table, then the local catalog. The two
// fields resolve independently: a model can have a fetched context window
// and a catalog output cap.
func resolveLimits(discovered []schema.ModelInfo, modelID string) ModelLimits {
	var ctx, out int
	for _, mi := range discovered {
		if mi.ID == modelID {
			ctx, out = mi.ContextWindow, mi.MaxOutputTokens
			break
		}
	}

	lim := ModelLimits{ContextWindow: ctx, MaxOutputTokens: out}
	lim.ContextSource, lim.OutputSource = SourceFetched, SourceFetched

	if ctx == 0 || out == 0 {
		catCtx, catOut := catalog.Lookup(modelID)
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
	return lim
}
