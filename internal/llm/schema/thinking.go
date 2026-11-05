package schema

// ResolveThinkingOptions keeps capability policy out of the rendering layer.
// Explicit configuration wins; discovery wins over conservative fallbacks.
// nil means unknown, and an empty Levels list means known unsupported.
func ResolveThinkingOptions(providerType, model string, override *ThinkingOptions, models []ModelInfo) *ThinkingOptions {
	if override != nil {
		return CloneThinkingOptions(override)
	}
	for _, info := range models {
		if info.ID == model && info.Thinking != nil {
			return CloneThinkingOptions(info.Thinking)
		}
	}
	// The Codex transport always uses OpenAI's effort control. Without catalog
	// metadata, only offer the established three levels, never guess off support.
	if providerType == "openai_codex" {
		return &ThinkingOptions{Levels: []string{"low", "medium", "high"}, Mode: "effort"}
	}
	return nil
}

// CloneThinkingOptions copies options, dropping "", "default" and repeated
// levels. It returns nil for nil.
func CloneThinkingOptions(options *ThinkingOptions) *ThinkingOptions {
	if options == nil {
		return nil
	}
	copy := *options
	copy.Levels = nil
	seen := map[string]bool{"": true, "default": true}
	for _, level := range options.Levels {
		if !seen[level] {
			copy.Levels = append(copy.Levels, level)
			seen[level] = true
		}
	}
	return &copy
}

// PreserveThinkingOptions retains cached metadata when a model's enrichment
// fails. Explicit empty controls are authoritative and are never replaced.
func PreserveThinkingOptions(models, cached []ModelInfo) []ModelInfo {
	out := append([]ModelInfo(nil), models...)
	for i := range out {
		if out[i].Thinking != nil {
			continue
		}
		for _, previous := range cached {
			if previous.ID == out[i].ID && previous.Thinking != nil {
				out[i].Thinking = CloneThinkingOptions(previous.Thinking)
				break
			}
		}
	}
	return out
}
