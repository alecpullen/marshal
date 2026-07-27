package sidepanel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/tools/registry"
)

// ToolStat is one tool's aggregate across the session.
type ToolStat struct {
	Name    string
	Calls   int
	Errors  int
	Slowest time.Duration
}

// ToolStats aggregates an audit log by tool name, most-called first.
// Ties break alphabetically so the ordering is stable across renders.
func ToolStats(events []registry.AuditEvent) []ToolStat {
	if len(events) == 0 {
		return nil
	}
	idx := map[string]*ToolStat{}
	for _, e := range events {
		s, ok := idx[e.ToolName]
		if !ok {
			s = &ToolStat{Name: e.ToolName}
			idx[e.ToolName] = s
		}
		s.Calls++
		if e.Error != "" {
			s.Errors++
		}
		if e.Duration > s.Slowest {
			s.Slowest = e.Duration
		}
	}
	out := make([]ToolStat, 0, len(idx))
	for _, s := range idx {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ToolsSection is the session's tool-call histogram. Nothing surfaces
// this today — the audit log is written but never shown.
type ToolsSection struct{}

func (ToolsSection) ID() string      { return "tools" }
func (ToolsSection) Title() string   { return "TOOLS" }
func (ToolsSection) Priority() int   { return 3 }
func (ToolsSection) Clippable() bool { return true }

func (ToolsSection) Relevant(d Data) bool { return len(d.Audit) > 0 }

// shortToolName drops the namespace so "file.read" reads as "read" in a
// 30-column rail. MCP tools keep their server segment.
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
	stats := ToolStats(d.Audit)
	rows := make([]string, 0, len(stats)+1)

	var slowest ToolStat
	for _, s := range stats {
		if s.Slowest > slowest.Slowest {
			slowest = s
		}
		line := fmt.Sprintf(" %-14s ×%d", shortToolName(s.Name), s.Calls)
		if s.Errors > 0 {
			line += fmt.Sprintf(" (%d✘)", s.Errors)
		}
		rows = append(rows, line)
	}
	if slowest.Slowest > 0 {
		rows = append(rows, fmt.Sprintf(" slowest  %s %.1fs",
			shortToolName(slowest.Name), slowest.Slowest.Seconds()))
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
	errs, slowest := 0, time.Duration(0)
	for _, e := range d.Audit {
		if e.Error != "" {
			errs++
		}
		if e.Duration > slowest {
			slowest = e.Duration
		}
	}
	line := fmt.Sprintf("⚒ %d calls", len(d.Audit))
	if errs > 0 {
		line += fmt.Sprintf(" · %d failed", errs)
	}
	if slowest > 0 {
		line += fmt.Sprintf(" · %.1fs max", slowest.Seconds())
	}
	return ansi.Truncate(line, width, "…")
}
