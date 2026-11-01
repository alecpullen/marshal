package tui

import (
	"marshal/internal/viewmodel"
)

// DisplayToolName returns a human-readable label for a tool. Unknown tools
// get a title-cased, dot-to-space transformation (only the first segment
// is capitalized).
func DisplayToolName(name string) string { return viewmodel.DisplayToolName(name) }
