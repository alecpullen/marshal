// Package presetflow implements the shared "discovered model → confirmed
// preset" flow used by both the /connect wizard and Settings' per-role
// model assignment: resolving context/output limits and tool-calling
// support, confirming or editing them interactively, and materializing the
// result into a named ModelPreset.
package presetflow

import (
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Source labels where a limit or capability figure came from, so the user
// can tell a provider-reported number from a default.
type Source string

const (
	SourceReported Source = "provider-reported"
	SourceDefault  Source = "model default"
	SourcePreset   Source = "saved preset"
	SourceEdited   Source = "edited"
	SourceProbed   Source = "detected via /api/show"
	SourceUnknown  Source = "unknown"
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

// Resolve finds modelID's limits from what discovery already resolved. Most
// /models endpoints report no context metadata, so when a field is absent the
// locality-appropriate default applies: 1M context / 128K output for remote
// models, the conservative 8K / 4K pair for local endpoints (Ollama's
// /api/show probe can then supply the pulled model's real window). The
// confirm screen lets the user edit either figure before the preset is saved.
//
// ToolCalling comes only from discovered and is left nil/SourceUnknown if
// unreported — a later per-model capability probe (see probe.go) may fill it
// in via WithProbed.
func Resolve(discovered []schema.ModelInfo, modelID string, local bool) Limits {
	var ctx, out int
	var toolCalling *bool
	for _, mi := range discovered {
		if mi.ID == modelID {
			ctx, out, toolCalling = mi.ContextWindow, mi.MaxOutputTokens, mi.ToolCalling
			break
		}
	}

	defCtx, defOut := routing.DefaultModelLimits(local)
	lim := Limits{
		ContextWindow:   ctx,
		MaxOutputTokens: out,
		ContextSource:   SourceReported,
		OutputSource:    SourceReported,
	}
	// The documented defaults are always nonzero, so a limit can no longer
	// resolve to SourceUnknown: every field has a figure and a provenance.
	if lim.ContextWindow == 0 {
		lim.ContextWindow, lim.ContextSource = defCtx, SourceDefault
	}
	if lim.MaxOutputTokens == 0 {
		lim.MaxOutputTokens, lim.OutputSource = defOut, SourceDefault
	}

	lim.ToolCalling = toolCalling
	if toolCalling != nil {
		lim.ToolSource = SourceReported
	} else {
		lim.ToolSource = SourceUnknown
	}
	return lim
}

// WithPreset prefers context/output figures already saved for the same
// provider+model over a fresh default, so re-picking a model does not show
// a guessed number when a prior confirmation already set the user's choice.
// A figure the provider actually reported, or one the user already edited or
// probed, is newer and more authoritative, so it is not overwritten. Fields
// resolve independently.
func (l Limits) WithPreset(presetContext, presetMaxOutput int) Limits {
	if presetContext != 0 && (l.ContextWindow == 0 || l.ContextSource == SourceDefault || l.ContextSource == SourceUnknown) {
		l.ContextWindow, l.ContextSource = presetContext, SourcePreset
	}
	if presetMaxOutput != 0 && (l.MaxOutputTokens == 0 || l.OutputSource == SourceDefault || l.OutputSource == SourceUnknown) {
		l.MaxOutputTokens, l.OutputSource = presetMaxOutput, SourcePreset
	}
	return l
}

// WithProbed merges a per-model capability probe's result over the current
// limits. ToolCalling replaces whatever discovery reported, because the
// probe reflects the actual pulled model file. The context window is
// different: a probed value is the model file's training ceiling, not the
// budget the user chose, so it must never overwrite a saved-preset or
// user-edited figure (SourcePreset/SourceEdited) — those are deliberate
// choices that survive re-probing /connect. A positive probed value does
// still overwrite default/provider-reported figures so a wrong default can
// be corrected by re-probing. A zero probed window is ignored to avoid
// erasing an already-known cap.
func (l Limits) WithProbed(toolCalling *bool, contextWindow int) Limits {
	if toolCalling != nil {
		l.ToolCalling, l.ToolSource = toolCalling, SourceProbed
	}
	if contextWindow > 0 && l.ContextSource != SourcePreset && l.ContextSource != SourceEdited {
		l.ContextWindow, l.ContextSource = contextWindow, SourceProbed
	}
	return l
}
