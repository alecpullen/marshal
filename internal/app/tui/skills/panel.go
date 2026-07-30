// Package skills provides the docked panel for browsing and managing skills.
//
// STUB — will be replaced by Task 7 (and modified by Tasks 9/10/11).
package skills

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/dock"
)

// Panel is a docked skills browser.
type Panel struct{}

var _ dock.Panel = (*Panel)(nil)

// NewPanel creates a skills browser panel.
// Signature matches the dispatch call in commands_dispatch.go.
// Task 10 will add a *session.State parameter.
func NewPanel(homeDir, workDir string, projectTrusted bool) *Panel {
	return &Panel{}
}

// Update handles messages for the skills panel.
func (p *Panel) Update(msg tea.Msg) tea.Cmd { return nil }

// View renders the skills panel.
func (p *Panel) View(width, maxHeight int) string { return "" }

// Sizing reports the panel's height-budget hint.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
