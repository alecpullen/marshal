package provider

import (
	"context"

	"marshal/internal/llm/provider/limits"
)

// ResolveReasoningSupport reports whether the model behind prov is known to
// accept a reasoning/effort control, consulting every signal marshal has:
//
//  1. provider-wide Capabilities().Reasoning == false → (false, true). The
//     chat path (internal/agent/chat.go) gates the wire field on exactly
//     this bit, so a provider-level "no" is authoritative even when a
//     per-model probe disagrees.
//  2. limits table entry with Reasoning != nil → that value.
//  3. CapabilityProber result with Reasoning != nil → that value.
//  4. provider-wide default → (caps.Reasoning, false): a baseline
//     assumption, not knowledge about this model.
//
// known=false means no source reported either way; callers should keep the
// control visible rather than hide it on a guess.
//
// Note the honesty limit of the remote feeds: they report WHETHER reasoning
// is supported, never WHICH effort values. The dynamic part of the UI is
// show/hide; the value set stays a hardcoded fallback.
//
// Step 3 is currently speculative for the built-in providers: OllamaNative
// short-circuits at step 1 (factory sets caps.Reasoning=false because Ollama's
// think toggle is not reasoning_effort), and OpenAICompatible does not
// implement CapabilityProber. The step exists so a future provider (or an
// OpenAICompatible prober) can supply per-model knowledge without changing
// the resolver contract. The Ollama ProbeCapabilities.Reasoning plumbing is
// tested but has no live consumer today.
func ResolveReasoningSupport(ctx context.Context, prov Provider, providerName, modelID string, table *limits.Table) (supported, known bool) {
	caps := prov.Capabilities(ctx)
	if !caps.Reasoning {
		return false, true
	}
	if table != nil {
		if lim, kind := table.Lookup(providerName, modelID); kind != limits.MatchNone && lim.Reasoning != nil {
			return *lim.Reasoning, true
		}
	}
	if prober, ok := prov.(CapabilityProber); ok {
		if mc := prober.ProbeCapabilities(ctx, modelID); mc.Reasoning != nil {
			return *mc.Reasoning, true
		}
	}
	return caps.Reasoning, false
}
