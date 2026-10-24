// Package presetsetup resolves model metadata for bulk preset creation and
// records which fields models.dev manages, so reconnects refresh catalog
// values without overwriting user edits.
package presetsetup

import (
	"math"

	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Source labels where a resolved field came from.
type Source string

const (
	SourceProvider  Source = "provider"
	SourceModelsDev Source = "models.dev"
	SourceDefault   Source = "default"
	SourceUnknown   Source = "unknown"
)

// Metadata is one model's resolved metadata with per-field provenance.
type Metadata struct {
	ContextWindow   int
	ContextSource   Source
	MaxOutputTokens int
	OutputSource    Source
	ToolCalling     *bool
	ToolSource      Source
	Pricing         *pricing.ModelPricing
	PricingSource   Source
}

// Resolve computes metadata for modelID. Provider-reported values win, then
// the models.dev catalog, then Marshal's documented defaults. Fields resolve
// independently.
func Resolve(providerName, baseURL, modelID string, reported schema.ModelInfo, cat *modelcatalog.Catalog) Metadata {
	local := routing.IsLocalProvider(baseURL)
	defCtx, defOut := routing.DefaultModelLimits(local)

	m := Metadata{
		ContextWindow:   reported.ContextWindow,
		ContextSource:   SourceProvider,
		MaxOutputTokens: reported.MaxOutputTokens,
		OutputSource:    SourceProvider,
		ToolCalling:     reported.ToolCalling,
		ToolSource:      SourceUnknown,
	}
	if m.ContextWindow == 0 {
		m.ContextWindow, m.ContextSource = defCtx, SourceDefault
	}
	if m.MaxOutputTokens == 0 {
		m.MaxOutputTokens, m.OutputSource = defOut, SourceDefault
	}
	if m.ToolCalling != nil {
		m.ToolSource = SourceProvider
	}

	catalogProvider, ok := modelcatalog.ResolveBinding(providerName, baseURL)
	if !ok {
		return m
	}
	cm, ok := cat.Lookup(catalogProvider, modelID)
	if !ok {
		return m
	}
	if m.ContextSource != SourceProvider && cm.Limit.Context > 0 {
		m.ContextWindow, m.ContextSource = cm.Limit.Context, SourceModelsDev
	}
	if m.OutputSource != SourceProvider && cm.Limit.Output > 0 {
		m.MaxOutputTokens, m.OutputSource = cm.Limit.Output, SourceModelsDev
	}
	if m.ToolCalling == nil && cm.ToolCall != nil {
		v := *cm.ToolCall
		m.ToolCalling, m.ToolSource = &v, SourceModelsDev
	}
	if p, ok := pricingFromCost(cm.Cost); ok {
		m.Pricing, m.PricingSource = p, SourceModelsDev
	}
	return m
}

// CentsFromUSDPerMTok converts a USD-per-million-token rate to Marshal's
// integer cents per million, rounding to the nearest cent. Negative, NaN, and
// infinite values are rejected.
func CentsFromUSDPerMTok(usd float64) (int64, bool) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, false
	}
	return int64(math.Round(usd * 100)), true
}

func pricingFromCost(c modelcatalog.Cost) (*pricing.ModelPricing, bool) {
	if c.Input == 0 && c.Output == 0 && c.Reasoning == 0 && c.CacheRead == 0 && c.CacheWrite == 0 {
		return nil, false
	}
	in, okIn := CentsFromUSDPerMTok(c.Input)
	out, okOut := CentsFromUSDPerMTok(c.Output)
	reason, okReason := CentsFromUSDPerMTok(c.Reasoning)
	read, okRead := CentsFromUSDPerMTok(c.CacheRead)
	write, okWrite := CentsFromUSDPerMTok(c.CacheWrite)
	if !okIn && !okOut && !okReason && !okRead && !okWrite {
		return nil, false
	}
	p := &pricing.ModelPricing{}
	if okIn {
		p.InputPerMTokCents = in
	}
	if okOut {
		p.OutputPerMTokCents = out
	}
	if okReason {
		p.ReasoningPerMTokCents = reason
	}
	if okRead {
		p.CacheReadPerMTokCents = read
	}
	if okWrite {
		p.CacheWritePerMTokCents = write
	}
	return p, true
}
