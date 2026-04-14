// Package models provides per-model capability detection and settings.
package models

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/alec/marshal/internal/config"
	"github.com/alec/marshal/internal/edit"
)

//go:embed settings.toml
var defaultSettings []byte

// ModelSettings holds capability flags and format preferences for a model.
type ModelSettings struct {
	Name           string `toml:"name"`
	Pattern        string `toml:"pattern"`
	SupportsTools  bool   `toml:"supports_tools"`
	SupportsVision bool   `toml:"supports_vision"`
	SupportsJSON   bool   `toml:"supports_json"`
	EditFormat     string `toml:"edit_format"`
	MaxTokens      int    `toml:"max_tokens"`
	// ContextWindow is the maximum total tokens (input + output) the model
	// accepts. Used to derive the file-injection budget so small local models
	// don't have their windows blown by 100K-char dumps.
	ContextWindow  int    `toml:"context_window"`
}

// Registry holds model settings keyed by model name or pattern.
type Registry struct {
	exact   map[string]ModelSettings
	patterns []patternEntry
}

type patternEntry struct {
	pattern  string
	settings ModelSettings
}

// LoadDefault loads the embedded default settings.
func LoadDefault() (*Registry, error) {
	return Load(defaultSettings)
}

// Load parses model settings from TOML bytes.
func Load(data []byte) (*Registry, error) {
	var raw struct {
		Models map[string]ModelSettings `toml:"models"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse model settings: %w", err)
	}

	r := &Registry{
		exact:    make(map[string]ModelSettings),
		patterns: nil,
	}

	for key, ms := range raw.Models {
		if ms.Pattern != "" {
			// This is a pattern-based entry (e.g., "ollama/*")
			r.patterns = append(r.patterns, patternEntry{
				pattern:  ms.Pattern,
				settings: ms,
			})
		} else {
			// Exact match entry
			r.exact[key] = ms
		}
	}

	return r, nil
}

// Lookup returns settings for a model name, or defaults if not found.
func (r *Registry) Lookup(modelName string) ModelSettings {
	if r == nil {
		return DefaultSettings()
	}

	// Try exact match first.
	if s, ok := r.exact[modelName]; ok {
		return s
	}

	// Try pattern matches.
	for _, pe := range r.patterns {
		if matchPattern(pe.pattern, modelName) {
			return pe.settings
		}
	}

	return DefaultSettings()
}

// matchPattern checks if a model name matches a pattern (e.g., "ollama/*").
func matchPattern(pattern, name string) bool {
	// Simple wildcard matching: "prefix/*" matches "prefix/anything"
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		return strings.HasPrefix(name, prefix+"/")
	}
	return pattern == name
}

// DefaultSettings returns conservative defaults for unknown models.
func DefaultSettings() ModelSettings {
	return ModelSettings{
		Name:           "unknown",
		SupportsTools:  false,
		SupportsVision: false,
		SupportsJSON:   true,
		EditFormat:     "wholefile",
		MaxTokens:      4096,
		ContextWindow:  8192,
	}
}

// defaultReg caches the embedded registry for package-level helpers.
var (
	defaultReg     *Registry
	defaultRegOnce sync.Once
)

func defaultRegistry() *Registry {
	defaultRegOnce.Do(func() {
		defaultReg, _ = LoadDefault()
	})
	return defaultReg
}

// ContextWindowFor returns the context window (in tokens) for the given
// model name. Falls back to the default when the model is unknown or has no
// configured window.
func ContextWindowFor(modelName string) int {
	reg := defaultRegistry()
	ms := reg.Lookup(modelName)
	if ms.ContextWindow > 0 {
		return ms.ContextWindow
	}
	return DefaultSettings().ContextWindow
}

// GetEditFormat returns the appropriate edit.Format for the model.
func (ms ModelSettings) GetEditFormat() edit.Format {
	return edit.FormatFor(ms.EditFormat)
}

// GetConfigEditFormat converts model edit_format string to config.EditFormat.
func (ms ModelSettings) GetConfigEditFormat() config.EditFormat {
	// Use the field EditFormat (string), not the method
	formatStr := ms.EditFormat
	switch formatStr {
	case "search-replace":
		return config.EditFormatSearchReplace
	case "udiff":
		return config.EditFormatUdiff
	default:
		return config.EditFormatWholeFile
	}
}
