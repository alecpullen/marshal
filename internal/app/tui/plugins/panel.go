// Package plugins provides the docked panel for browsing and managing plugins.
//
// STUB — will be replaced by Task 8 (and modified by Tasks 13/14/15).
package plugins

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/dock"
)

// Panel is a docked plugins browser.
type Panel struct{}

var _ dock.Panel = (*Panel)(nil)

// NewPanel creates a plugins browser panel.
// Signature matches the dispatch call in commands_dispatch.go.
func NewPanel(homeDir, workDir string, projectTrusted bool) *Panel {
	return &Panel{}
}

// Update handles messages for the plugins panel.
func (p *Panel) Update(msg tea.Msg) tea.Cmd { return nil }

// View renders the plugins panel.
func (p *Panel) View(width, maxHeight int) string { return "" }

// Sizing reports the panel's height-budget hint.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
