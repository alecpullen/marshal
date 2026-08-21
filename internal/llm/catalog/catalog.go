// Package catalog holds Marshal's curated, local-friendly table of well-known
// model context windows and max output tokens. It is a small Go map, never a
// network fetch (docs/12 F12 R1). Unknown models resolve to conservative
// default specs with a warning so callers still get a usable budget rather
// than a guessed zero.
package catalog

import (
	"log/slog"
	"strings"
)

type entry struct {
	contextWindow int
	maxOutput     int
}

// builtin is keyed by lowercased model id. Curated from public datasheets;
// values are conservative defaults, overridable by [models.presets.<id>]
// context_window / max_output_tokens in config.
var builtin = map[string]entry{
	"qwen2.5-coder:7b":      {contextWindow: 32768, maxOutput: 8192},
	"qwen2.5-coder:14b":     {contextWindow: 32768, maxOutput: 8192},
	"qwen2.5-coder:32b":     {contextWindow: 32768, maxOutput: 8192},
	"qwen2.5:7b":            {contextWindow: 32768, maxOutput: 8192},
	"qwen2.5:14b":           {contextWindow: 32768, maxOutput: 8192},
	"llama3.1:8b":           {contextWindow: 128000, maxOutput: 4096},
	"llama3.1:70b":          {contextWindow: 128000, maxOutput: 4096},
	"deepseek-coder-v2:16b": {contextWindow: 128000, maxOutput: 8192},
	"codestral:22b":         {contextWindow: 32000, maxOutput: 8192},
	"mistral:7b":            {contextWindow: 32000, maxOutput: 8192},
	"phi3:14b":              {contextWindow: 128000, maxOutput: 4096},
}

// Default specs for models not in the catalog. These are conservative
// values that work with most local models. Users should configure
// explicit specs in their model preset for accuracy.
const (
	defaultContextWindow = 8192
	defaultMaxOutput     = 4096
)

// Lookup resolves the context window and max output tokens for a model id.
// Unknown models resolve to conservative default specs and log a warning so
// callers still get a usable budget rather than a zero. An empty model id
// returns (0, 0) — there is nothing to resolve.
func Lookup(modelID string) (contextWindow, maxOutput int) {
	if modelID == "" {
		return 0, 0
	}
	e, ok := builtin[strings.ToLower(modelID)]
	if !ok {
		slog.Warn("catalog: model not in catalog, using default specs (configure manually)",
			"model", modelID,
			"default_context_window", defaultContextWindow,
			"default_max_output", defaultMaxOutput)
		return defaultContextWindow, defaultMaxOutput
	}
	return e.contextWindow, e.maxOutput
}
