package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type mdBlock struct {
	kind string
	text string
}

func splitFencedBlocks(content string) []mdBlock {
	lines := strings.Split(content, "\n")
	var blocks []mdBlock
	inFence := false
	var current mdBlock

	if len(lines) > 0 {
		current.kind = "prose"
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inFence {
				blocks = append(blocks, current)
				current = mdBlock{kind: "prose"}
				inFence = false
			} else {
				if current.text != "" || current.kind == "prose" {
					blocks = append(blocks, current)
				}
				current = mdBlock{kind: "code"}
				inFence = true
			}
			continue
		}
		if current.text != "" {
			current.text += "\n"
		}
		if inFence && line == "" {
			current.text += " "
		} else {
			current.text += line
		}
	}
	if current.text != "" || (current.kind == "prose" && len(blocks) == 0) {
		blocks = append(blocks, current)
	}

	return blocks
}

func parseMarkdownLine(line string) (lipgloss.Style, string) {
	if strings.HasPrefix(line, "### ") {
		return lipgloss.NewStyle().Foreground(violetColor).Bold(true), strings.TrimPrefix(line, "### ")
	}
	if strings.HasPrefix(line, "## ") {
		return lipgloss.NewStyle().Foreground(violetColor).Bold(true), strings.TrimPrefix(line, "## ")
	}
	if strings.HasPrefix(line, "# ") {
		return lipgloss.NewStyle().Foreground(accentColor).Bold(true), strings.TrimPrefix(line, "# ")
	}

	trimmed := strings.TrimSpace(line)
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		return mutedStyle, "─────────────────────────────────────"
	}

	if strings.HasPrefix(line, "> ") {
		return mutedStyle, "│ " + strings.TrimPrefix(line, "> ")
	}

	if strings.HasPrefix(line, "- ") {
		return lipgloss.NewStyle(), "  • " + strings.TrimPrefix(line, "- ")
	}
	if strings.HasPrefix(line, "* ") {
		return lipgloss.NewStyle(), "  • " + strings.TrimPrefix(line, "* ")
	}

	return lipgloss.NewStyle(), line
}

func roleStyleFor(role string) lipgloss.Style {
	switch strings.ToLower(role) {
	case "user":
		return userRoleStyle
	case "agent", "assistant":
		return agentRoleStyle
	case "tool", "output":
		return toolRoleStyle
	default:
		return mutedStyle
	}
}

func renderCodeBlock(content string, width int) string {
	if width < 1 {
		width = 1
	}
	trimmed := strings.TrimSpace(content)
	style := lipgloss.NewStyle().
		Foreground(dimColor).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Width(width)
	return style.Render(trimmed)
}

func renderPlain(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	label := strings.ToLower(role)
	roleStyle := roleStyleFor(role)
	prefixWidth := 10
	contentWidth := max(width-prefixWidth-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", prefixWidth+2))
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderPlan(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	label := strings.ToLower(role)
	roleStyle := roleStyleFor(role)
	prefixWidth := 10
	panelInner := max(width-2, 1)
	contentWidth := max(panelInner-prefixWidth-4, 1)
	bullet := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("•")

	lines := strings.Split(content, "\n")
	var b strings.Builder
	for _, line := range lines {
		wrapped := ansi.Wrap(line, contentWidth, "")
		wrappedLines := strings.Split(wrapped, "\n")
		for i, wl := range wrappedLines {
			if i == 0 {
				b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
				b.WriteString("  ")
				b.WriteString(bullet)
				b.WriteString(" ")
				b.WriteString(wl)
				b.WriteString("\n")
			} else {
				b.WriteString(strings.Repeat(" ", prefixWidth+2))
				b.WriteString(wl)
				b.WriteString("\n")
			}
		}
	}
	body := strings.TrimRight(b.String(), "\n")
	bodyLines := 0
	if len(body) > 0 {
		bodyLines = strings.Count(body, "\n") + 1
	}
	height := max(bodyLines+1, 2)
	return renderPanel("Plan", "", body, panelInner, height)
}

func renderDiff(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	label := strings.ToLower(role)
	roleStyle := roleStyleFor(role)
	prefixWidth := 10
	panelInner := max(width-2, 1)
	contentWidth := max(panelInner-prefixWidth-2, 1)

	addStyle := lipgloss.NewStyle().Foreground(successColor)
	delStyle := lipgloss.NewStyle().Foreground(errorColor)

	lines := strings.Split(content, "\n")
	var b strings.Builder
	for _, line := range lines {
		var lineStyle lipgloss.Style
		switch {
		case strings.HasPrefix(line, "@@"):
			lineStyle = mutedStyle
		case strings.HasPrefix(line, "+"):
			lineStyle = addStyle
		case strings.HasPrefix(line, "-"):
			lineStyle = delStyle
		default:
			lineStyle = lipgloss.NewStyle()
		}
		wrapped := ansi.Wrap(line, contentWidth, "")
		wrappedLines := strings.Split(wrapped, "\n")
		for i, wl := range wrappedLines {
			if i == 0 {
				b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
				b.WriteString("  ")
				b.WriteString(lineStyle.Render(wl))
				b.WriteString("\n")
			} else {
				b.WriteString(strings.Repeat(" ", prefixWidth+2))
				b.WriteString(lineStyle.Render(wl))
				b.WriteString("\n")
			}
		}
	}
	body := strings.TrimRight(b.String(), "\n")
	bodyLines := 0
	if len(body) > 0 {
		bodyLines = strings.Count(body, "\n") + 1
	}
	height := max(bodyLines+1, 2)
	return renderPanel("Diff", "", body, panelInner, height)
}

func renderToolResult(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return ""
	}
	label := "tool"
	prefixWidth := 10
	roleStyle := roleStyleFor("tool")
	var b strings.Builder
	firstLine := strings.TrimSpace(lines[0])
	b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
	b.WriteString("  ")
	b.WriteString(toolRoleStyle.Render(firstLine))
	b.WriteString("\n")
	for _, line := range lines[1:] {
		wrapped := ansi.Wrap(line, width, "")
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(strings.Repeat(" ", prefixWidth+2))
			b.WriteString(mutedStyle.Render(wl))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func renderMarkdown(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	label := strings.ToLower(role)
	roleStyle := roleStyleFor(role)
	prefixWidth := 10
	contentWidth := max(width-prefixWidth-2, 1)

	blocks := splitFencedBlocks(content)
	var b strings.Builder
	firstBlock := true

	for _, block := range blocks {
		switch block.kind {
		case "code":
			rendered := renderCodeBlock(block.text, contentWidth)
			codeLines := strings.Split(rendered, "\n")
			for _, line := range codeLines {
				if line == "" {
					continue
				}
				if firstBlock {
					b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
					b.WriteString("  ")
					b.WriteString(line)
					b.WriteString("\n")
					firstBlock = false
				} else {
					b.WriteString(strings.Repeat(" ", prefixWidth+2))
					b.WriteString(line)
					b.WriteString("\n")
				}
			}
		case "prose":
			proseLines := strings.Split(block.text, "\n")
			if len(proseLines) == 1 && proseLines[0] == "" {
				continue
			}
			for _, pLine := range proseLines {
				style, transformed := parseMarkdownLine(pLine)
				wrapped := ansi.Wrap(transformed, contentWidth, "")
				wrappedLines := strings.Split(wrapped, "\n")
				for _, wl := range wrappedLines {
					if firstBlock {
						b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
						b.WriteString("  ")
						b.WriteString(style.Render(wl))
						b.WriteString("\n")
						firstBlock = false
					} else {
						b.WriteString(strings.Repeat(" ", prefixWidth+2))
						b.WriteString(style.Render(wl))
						b.WriteString("\n")
					}
				}
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}
