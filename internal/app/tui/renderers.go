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
	roleStyle := mutedStyle
	switch label {
	case "user":
		roleStyle = userRoleStyle
	case "agent", "assistant":
		roleStyle = agentRoleStyle
	case "tool":
		roleStyle = toolRoleStyle
	case "output":
		roleStyle = outputRoleStyle
	}
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

func renderMarkdown(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	label := strings.ToLower(role)
	roleStyle := mutedStyle
	switch label {
	case "user":
		roleStyle = userRoleStyle
	case "agent", "assistant":
		roleStyle = agentRoleStyle
	case "tool":
		roleStyle = toolRoleStyle
	case "output":
		roleStyle = outputRoleStyle
	}
	prefixWidth := 10
	contentWidth := max(width-prefixWidth-2, 1)

	blocks := splitFencedBlocks(content)
	var b strings.Builder

	for _, block := range blocks {
		switch block.kind {
		case "code":
			rendered := renderCodeBlock(block.text, contentWidth)
			codeLines := strings.Split(rendered, "\n")
			for i, line := range codeLines {
				if i == 0 {
					b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
					b.WriteString("  ")
					b.WriteString(line)
					b.WriteString("\n")
				} else if line != "" {
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
				for j, wl := range wrappedLines {
					if j == 0 {
						b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
						b.WriteString("  ")
						b.WriteString(style.Render(wl))
						b.WriteString("\n")
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
