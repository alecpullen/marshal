package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
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

func renderFinalAnswer(content string, width int) string {
	if width < 10 {
		width = 10
	}
	prefixWidth := 10
	contentWidth := max(width-prefixWidth-4, 1)
	label := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("Response")

	blocks := splitFencedBlocks(strings.TrimRight(content, "\n"))
	var b strings.Builder
	b.WriteString(label)
	b.WriteString("  ")
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

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(accentColor).
		PaddingLeft(1)
	return borderStyle.Render(strings.TrimRight(b.String(), "\n")) + "\n"
}

func formatThinkDuration(d time.Duration) string {
	return fmt.Sprintf("%.0fs", d.Seconds())
}

const thinkingBoxTailLines = 6

// renderThinkingBox renders live reasoning as a compact inline line. It
// intentionally returns nothing until reasoning text has arrived; providers
// that do not stream reasoning should not get an empty thinking panel.
func renderThinkingBox(reasoning, spinnerFrame string, width int) string {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return ""
	}
	contentWidth := max(width-4, 1)
	tail := strings.ReplaceAll(tailRunes(reasoning, contentWidth*2), "\n", " ")
	line := fmt.Sprintf("%s thinking  %s", spinnerFrame, tail)
	return thinkingLineStyle.Render(truncateRunes(line, max(width-2, 1))) + "\n"
}

// renderThinkingSummary renders a finished message's captured reasoning,
// either collapsed to one line or, when expanded, as a full boxed panel
// matching renderThinkingBox's style.
func renderThinkingSummary(reasoning string, duration time.Duration, expanded bool, width int) string {
	if !expanded {
		return thinkingLineStyle.Render(fmt.Sprintf("  ⚙ thought for %s", formatThinkDuration(duration))) + "\n"
	}
	contentWidth := max(width-4, 1)
	var b strings.Builder
	b.WriteString(thinkingLineStyle.Render(fmt.Sprintf("  ⚙ thought for %s", formatThinkDuration(duration))))
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimSpace(reasoning), "\n") {
		wrapped := ansi.Wrap(line, contentWidth, "")
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(thinkingLineStyle.Render("    " + wl))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderMessage formats one transcript entry in the symbol-bullet style:
// user prompts get a ❯ prefix, agent prose renders as plain markdown with
// no role label, tool results render as ⏺/⎿ bullets, system notices are
// dim. Final answers keep the rich-content Response treatment.
func renderMessage(msg session.Message, width int) string {
	if msg.Final {
		return renderFinalAnswer(msg.Content, width)
	}
	if msg.Role == session.RoleUser {
		return renderUserMessage(msg.Content, width)
	}
	if msg.Role == session.RoleSystem {
		return renderSystemNotice(msg.Content, width)
	}
	switch msg.ContentType {
	case session.ContentTypePlan:
		return renderPlanBlock(msg.Content, width)
	case session.ContentTypeDiff:
		return renderDiffBlock(msg.Content, width)
	case session.ContentTypeToolResult:
		return renderToolResultLine(msg.Content, width)
	case session.ContentTypeCode:
		return indentBlock(renderCodeBlock(msg.Content, max(width-4, 1)), "  ") + "\n"
	default: // plain and markdown prose render identically
		return renderAgentMarkdown(msg.Content, width)
	}
}

var promptPrefixStyle = lipgloss.NewStyle().Foreground(accentColor).Bold(true)

func renderUserMessage(content string, width int) string {
	contentWidth := max(width-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(promptPrefixStyle.Render("❯ "))
		} else {
			b.WriteString("  ")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderAgentMarkdown(content string, width int) string {
	contentWidth := max(width-2, 1)
	blocks := splitFencedBlocks(strings.TrimRight(content, "\n"))
	var b strings.Builder
	for _, block := range blocks {
		switch block.kind {
		case "code":
			b.WriteString(indentBlock(renderCodeBlock(block.text, max(contentWidth-2, 1)), "  "))
			b.WriteString("\n")
		case "prose":
			for _, pLine := range strings.Split(block.text, "\n") {
				if strings.TrimSpace(pLine) == "" {
					continue
				}
				style, transformed := parseMarkdownLine(pLine)
				wrapped := ansi.Wrap(transformed, contentWidth, "")
				for _, wl := range strings.Split(wrapped, "\n") {
					b.WriteString(style.Render(wl))
					b.WriteString("\n")
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderTranscriptItem(item session.TranscriptItem, thinkingExpanded bool, width int) string {
	switch item.Kind {
	case session.KindThinking:
		if item.Thinking == nil {
			return ""
		}
		return renderThinkingSummary(item.Thinking.Text, item.Thinking.Duration, thinkingExpanded, width)
	case session.KindAudit:
		if item.Audit == nil {
			return ""
		}
		return renderCompletedToolCall(*item.Audit, width)
	case session.KindMessage:
		if item.Message == nil {
			return ""
		}
		var b strings.Builder
		if item.Message.Reasoning != "" {
			b.WriteString(renderThinkingSummary(item.Message.Reasoning, item.Message.ThinkDuration, thinkingExpanded, width))
		}
		b.WriteString(renderMessage(*item.Message, width))
		return b.String()
	}
	return ""
}

func renderSystemNotice(content string, width int) string {
	contentWidth := max(width-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(mutedStyle.Render("· " + line))
		} else {
			b.WriteString(mutedStyle.Render("  " + line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

var toolBulletStyle = lipgloss.NewStyle().Foreground(warningColor)

func renderToolResultLine(content string, width int) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(toolBulletStyle.Render("⏺ "))
	b.WriteString(truncateRunes(strings.TrimSpace(lines[0]), max(width-2, 1)))
	b.WriteString("\n")
	continuation := lines[1:]
	for i, line := range continuation {
		wrapped := ansi.Wrap(line, max(width-4, 1), "")
		for j, wl := range strings.Split(wrapped, "\n") {
			if i == 0 && j == 0 {
				b.WriteString(mutedStyle.Render("  ⎿ " + wl))
			} else {
				b.WriteString(mutedStyle.Render("    " + wl))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderPlanBlock(content string, width int) string {
	var b strings.Builder
	b.WriteString(promptPrefixStyle.Render("⏺ Plan"))
	b.WriteString("\n")
	contentWidth := max(width-4, 1)
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		wrapped := ansi.Wrap(line, contentWidth, "")
		for j, wl := range strings.Split(wrapped, "\n") {
			if j == 0 {
				b.WriteString("  " + wl)
			} else {
				b.WriteString("    " + wl)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderDiffBlock(content string, width int) string {
	addStyle := lipgloss.NewStyle().Foreground(successColor)
	delStyle := lipgloss.NewStyle().Foreground(errorColor)
	contentWidth := max(width-2, 1)
	var b strings.Builder
	for _, line := range strings.Split(content, "\n") {
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
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString("  ")
			b.WriteString(lineStyle.Render(wl))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

var providerErrorStyle = lipgloss.NewStyle().Foreground(errorColor).Bold(true)

func renderProviderError(err error, width int) string {
	contentWidth := max(width-2, 1)
	wrapped := ansi.Wrap("✗ provider: "+err.Error(), contentWidth, "")
	var b strings.Builder
	for _, line := range strings.Split(wrapped, "\n") {
		b.WriteString(providerErrorStyle.Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderActiveToolCall shows the in-flight tool as a spinner line — no
// border, matching the ⏺ bullet style. Command tools get a second $ line.
func renderActiveToolCall(atc session.ActiveToolCall, spinnerFrame string, now time.Time, width int) string {
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	head := fmt.Sprintf("%s %s · %s", spinnerFrame, atc.Name, formatElapsed(elapsed))
	var b strings.Builder
	b.WriteString(toolBulletStyle.Render(truncateRunes(head, max(width-2, 1))))
	b.WriteString("\n")
	if atc.Name == "shell.run" || atc.Name == "test.run" {
		b.WriteString(mutedStyle.Render(truncateRunes("  $ "+atc.Args, max(width-2, 1))))
		b.WriteString("\n")
	} else if atc.Args != "" {
		b.WriteString(mutedStyle.Render(truncateRunes("  "+atc.Args, max(width-2, 1))))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderCompletedToolCall(event registry.AuditEvent, width int) string {
	state := "done"
	style := statusOkStyle
	if event.Error != "" {
		state = "failed"
		style = statusErrStyle
	}
	head := fmt.Sprintf("✓ %s %s", event.ToolName, state)
	var b strings.Builder
	b.WriteString(style.Render(truncateRunes(head, max(width-2, 1))))
	b.WriteString("\n")
	summary := event.ResultSummary
	if event.Error != "" {
		summary = event.Error
	}
	if summary != "" {
		b.WriteString(mutedStyle.Render(truncateRunes("  "+summary, max(width-2, 1))))
		b.WriteString("\n")
	}
	return b.String()
}

// indentBlock prefixes every non-empty line of a rendered block.
func indentBlock(block, prefix string) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}

func riskText(tc *session.PendingToolCall) string {
	if tc.Risk != "" {
		return tc.Risk
	}
	return tc.Reason
}

// renderApprovalInline renders the approval prompt inside the chat viewport.
func renderApprovalInline(tc *session.PendingToolCall, width int) string {
	if width < 10 {
		width = 10
	}
	helpLine := "Enter approve · d deny · e edit · a always"
	innerWidth := max(width-2, 1)

	var b strings.Builder
	b.WriteString(panelTitleStyle.Foreground(warningColor).Render("⚠ Approval needed"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("Agent wants to run:"))
	b.WriteString("\n")
	b.WriteString(truncateRunes(tc.Command, innerWidth))
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render("Risk: "))
	b.WriteString(truncateRunes(riskText(tc), innerWidth))
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render(helpLine))

	style := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(warningColor).
		Padding(0, 1)
	return style.Render(b.String()) + "\n\n"
}
