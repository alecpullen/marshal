// Package catalog holds Marshal's curated, local-friendly table of well-known
// model context windows and max output tokens. It is a small Go map, never a
// network fetch (docs/12 F12 R1). Unknown models resolve to (0, 0) so the
// runner keeps its configured budget rather than guessing.
package catalog

import "strings"

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

// Lookup resolves the context window and max output tokens for a model id.
// Returns (0, 0) when the model is unknown — callers must treat 0 as
// "unknown, keep configured budget" and never extrapolate.
func Lookup(modelID string) (contextWindow, maxOutput int) {
	if modelID == "" {
		return 0, 0
	}
	e, ok := builtin[strings.ToLower(modelID)]
	if !ok {
		return 0, 0
	}
	return e.contextWindow, e.maxOutput
}
