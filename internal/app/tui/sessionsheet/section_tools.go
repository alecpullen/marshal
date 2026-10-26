package sessionsheet

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
)

// ToolsSection is the session's tool-call histogram. Nothing surfaces
// this today — the audit log is written but never shown.
type ToolsSection struct{}

func (ToolsSection) ID() string      { return "tools" }
func (ToolsSection) Title() string   { return "TOOLS" }
func (ToolsSection) Priority() int   { return 3 }
func (ToolsSection) Clippable() bool { return true }

func (ToolsSection) Relevant(d Data) bool { return len(d.Tools) > 0 }

// shortToolName drops the namespace so "file.read" reads as "read" in a
// narrow sheet. MCP tools keep their server segment.
func shortToolName(name string) string {
	if strings.HasPrefix(name, "mcp.") {
		return name
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

func (ToolsSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.Tools)+1)

	var slowest acpwire.SheetTool
	for _, s := range d.Tools {
		if s.SlowestMs > slowest.SlowestMs {
			slowest = s
		}
		// The error badge carries ANSI; railRow measures the right column
		// with ansi.StringWidth so the escapes cost no cells.
		right := fmt.Sprintf("×%d", s.Calls)
		if s.Errors > 0 {
			right += styleError(fmt.Sprintf(" (%d✘)", s.Errors))
		}
		rows = append(rows, railRow("", shortToolName(s.Name), right, width))
	}
	if slowest.SlowestMs > 0 {
		rows = append(rows, railRow("", "slowest "+shortToolName(slowest.Name),
			fmt.Sprintf("%.1fs", float64(slowest.SlowestMs)/1000), width))
	}

	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], width, "…")
	}
	return rows
}

func (ToolsSection) OneLine(d Data, width int) string {
	calls, errs, slowest := 0, 0, int64(0)
	for _, s := range d.Tools {
		calls += s.Calls
		errs += s.Errors
		if s.SlowestMs > slowest {
			slowest = s.SlowestMs
		}
	}
	line := fmt.Sprintf("⚒ %d calls", calls)
	if errs > 0 {
		line += fmt.Sprintf(" · %d failed", errs)
	}
	if slowest > 0 {
		line += fmt.Sprintf(" · %.1fs max", float64(slowest)/1000)
	}
	return ansi.Truncate(line, width, "…")
}
