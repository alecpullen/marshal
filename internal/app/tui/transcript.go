package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/tools/registry"
)

func isBrowserTool(name string) bool {
	return strings.HasPrefix(name, "browser.")
}

// marshalStyleConfig adapts glamour's dark style to the Warm Sunset
// palette: coral H1 instead of the banner-style default, violet section
// headings. Document text (252) and the 2-space margin already match the
// transcript's prose treatment.
func marshalStyleConfig() gansi.StyleConfig {
	cfg := styles.DarkStyleConfig
	cfg.Heading.StylePrimitive.Color = ptr("175") // violetColor
	cfg.H1.StylePrimitive = gansi.StylePrimitive{
		Color: ptr("209"), // coralColor
		Bold:  ptr(true),
	}
	return cfg
}

func ptr[T any](v T) *T { return &v }

// mdRenderers caches glamour renderers by wrap width; building one parses
// the full style config, which is too slow to repeat per message. The TUI
// renders on Bubble Tea's single Update/View goroutine, so no locking.
var mdRenderers = map[int]*glamour.TermRenderer{}

// renderMarkdown renders content as ANSI-styled markdown wrapped to
// width. ok is false when glamour is unavailable (renderer construction
// or rendering failed); callers fall back to plain text.
func renderMarkdown(content string, width int) (out string, ok bool) {
	r, cached := mdRenderers[width]
	if !cached {
		var err error
		r, err = glamour.NewTermRenderer(
			glamour.WithStyles(marshalStyleConfig()),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return "", false
		}
		mdRenderers[width] = r
	}
	rendered, err := r.Render(content)
	if err != nil {
		return "", false
	}
	return rendered, true
}

// renderPlainProse is the fallback prose treatment when markdown
// rendering fails: wrapped text with the transcript's 2-space indent.
func renderPlainProse(content string, width int) string {
	contentWidth := max(width-4, 1)
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		wrapped := ansi.Wrap(line, contentWidth, "")
		for i, wl := range strings.Split(wrapped, "\n") {
			if i == 0 {
				b.WriteString("  ")
			} else {
				b.WriteString("    ")
			}
			b.WriteString(wl)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func renderCodeBlock(content string, width int) string {
	if width < 1 {
		width = 1
	}
	trimmed := strings.TrimSpace(content)
	return codeBorderStyle.Width(width).Render(trimmed)
}

func renderFinalAnswer(msg session.Message, width int) string {
	if width < 10 {
		width = 10
	}
	labelText := "Response"
	if msg.Salvaged {
		labelText = "Response · salvaged"
	}
	label := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(labelText)

	var b strings.Builder
	b.WriteString(label)
	if msg.Salvaged && msg.SalvageReason != "" {
		b.WriteString("  ")
		b.WriteString(mutedStyle.Render(msg.SalvageReason))
	}
	b.WriteString("\n")

	// Border (1) + padding (1) sit left of the body; glamour's own
	// 2-space margin indents the prose inside that.
	contentWidth := max(width-6, 1)
	body, ok := renderMarkdown(msg.Content, contentWidth)
	if !ok {
		body = renderPlainProse(msg.Content, contentWidth)
	}
	b.WriteString(strings.Trim(body, "\n"))

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
	lines := strings.Split(reasoning, "\n")
	tailLines := lines
	if len(lines) > 3 {
		tailLines = lines[len(lines)-3:]
	}
	var b strings.Builder
	header := spinnerLabel(spinnerFrame, "thinking")
	b.WriteString(thinkingLineStyle.Render(header))
	b.WriteString("\n")
	for _, line := range tailLines {
		wrapped := ansi.Wrap(line, contentWidth, "")
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(thinkingLineStyle.Render("  " + wl))
			b.WriteString("\n")
		}
	}
	return b.String()
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
		return renderFinalAnswer(msg, width)
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
	userPrefix := lipgloss.NewStyle().Foreground(userColor).Bold(true).Render("› ")
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(userPrefix)
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
	// Glamour's document margin provides the transcript's 2-space prose
	// indent; wrap to width-2 so rendered lines stay inside the viewport.
	contentWidth := max(width-2, 1)
	out, ok := renderMarkdown(content, contentWidth)
	if !ok {
		out = renderPlainProse(content, width)
	}
	return strings.Trim(out, "\n") + "\n"
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

var toolBulletStyle = lipgloss.NewStyle().Foreground(goldColor)

var queuedStyle = lipgloss.NewStyle().Foreground(warningColor).Bold(true)

// renderQueuedMessages renders the F16 steering queue as a footer
// beneath the live transcript so the user can see what they typed
// while the agent was working. width is informational (no wrapping
// here yet — each queued line stays short in practice).
func renderQueuedMessages(q []string, width int) string {
	if len(q) == 0 {
		return ""
	}
	_ = width
	var b strings.Builder
	b.WriteString(queuedStyle.Render(" Queued (Ctrl+X to clear):"))
	b.WriteString("\n")
	for _, msg := range q {
		b.WriteString("  ")
		b.WriteString(mutedStyle.Render("›"))
		b.WriteString(" ")
		b.WriteString(msg)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

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

var providerErrorStyle = lipgloss.NewStyle().Foreground(errorColor).Bold(true)

func renderProviderError(err error, width int) string {
	contentWidth := max(width-2, 1)
	wrapped := ansi.Wrap("✘ provider: "+err.Error(), contentWidth, "")
	var b strings.Builder
	for _, line := range strings.Split(wrapped, "\n") {
		b.WriteString(providerErrorStyle.Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderActiveToolCall shows the in-flight tool as a spinner line — no
// border, matching the ⏺ bullet style. Command tools get a second $ line
// and, when a sandbox backend is active, an isolation-status line.
func renderActiveToolCall(atc session.ActiveToolCall, sb session.SandboxInfo, allowNetwork bool, spinnerFrame string, now time.Time, width int) string {
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	head := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", atc.Name, formatElapsed(elapsed)))
	var b strings.Builder
	if isBrowserTool(atc.Name) {
		b.WriteString(browserGlyphStyle.Render("🌐"))
		b.WriteString(" ")
		prefixed := browserPrefixStyle.Render("browser") + "." + strings.TrimPrefix(atc.Name, "browser.")
		full := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", prefixed, formatElapsed(elapsed)))
		b.WriteString(truncateRunes(full, max(width-4, 1)))
	} else {
		b.WriteString(toolBulletStyle.Render(truncateRunes(head, max(width-2, 1))))
	}
	b.WriteString("\n")
	if atc.Name == "shell.run" || atc.Name == "test.run" {
		b.WriteString(mutedStyle.Render(truncateRunes("  $ "+atc.Args, max(width-2, 1))))
		b.WriteString("\n")
		if iso := sandboxIsolationText(sb, allowNetwork); iso != "" {
			b.WriteString(mutedStyle.Render(truncateRunes("  "+iso, max(width-2, 1))))
			b.WriteString("\n")
		}
	} else if atc.Args != "" {
		b.WriteString(mutedStyle.Render(truncateRunes("  "+atc.Args, max(width-2, 1))))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderCompletedToolCall(event registry.AuditEvent, width int) string {
	glyph := "✔"
	style := statusOkStyle
	state := "done"
	if event.Error != "" {
		glyph = "✘"
		style = statusErrStyle
		state = "failed"
	}
	var head string
	if isBrowserTool(event.ToolName) {
		head = fmt.Sprintf("%s %s %s", glyph, browserPrefixStyle.Render("browser")+"."+strings.TrimPrefix(event.ToolName, "browser."), state)
	} else {
		head = fmt.Sprintf("%s %s %s", glyph, event.ToolName, state)
	}
	if hookHint := hookIndicatorText(event.Hooks); hookHint != "" {
		head += " · " + hookHint
	}
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

// hookIndicatorText picks the single highest-signal hook event from the
// metadata slice for the tool summary line. Priority (most specific first):
// block > rewrote > failed-open > generic count. Returns "" when there are
// no hooks to summarize.
func hookIndicatorText(hooks []registry.HookMetadata) string {
	if len(hooks) == 0 {
		return ""
	}
	for _, h := range hooks {
		if h.Decision == "block" || h.Decision == "halt" {
			return "hook blocked"
		}
	}
	for _, h := range hooks {
		if h.Rewrote {
			return "hook rewrote"
		}
	}
	for _, h := range hooks {
		if h.FailedOpen {
			return "hook failed-open"
		}
	}
	return fmt.Sprintf("hooks %d", len(hooks))
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

// sandboxIsolationText renders an honest one-line description of the active
// sandbox backend for shell/test commands. It combines the backend's
// capability snapshot with the configured AllowNetwork flag so the user
// never sees a false "isolated" claim (restricted mode cannot block network
// cross-platform). Returns "" for backends with no isolation story (e.g.
// passthrough).
func sandboxIsolationText(sb session.SandboxInfo, allowNetwork bool) string {
	switch sb.Backend {
	case "", "passthrough":
		return ""
	case "container":
		if sb.NetworkIsolation && !allowNetwork {
			return "sandbox: container · network blocked"
		}
		return "sandbox: container · network allowed"
	case "restricted":
		if sb.NetworkIsolation {
			return "sandbox: restricted"
		}
		return "sandbox: restricted · network not isolated"
	default:
		return "sandbox: " + sb.Backend
	}
}

func renderApprovalPanel(tc *session.PendingToolCall, sb session.SandboxInfo, allowNetwork bool, width int) string {
	innerWidth := max(width-2, 1)

	titleStyle := panelTitleStyle.Foreground(warningColor)
	muted := mutedStyle
	text := lipgloss.NewStyle()
	key := keyHintStyle

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚠ Approval needed"))
	b.WriteString("\n")

	if tc.Name == "shell.run" {
		b.WriteString(muted.Render("Agent wants to run:"))
		b.WriteString("\n")
		b.WriteString(text.Render(truncateRunes(tc.Command, innerWidth)))
	} else {
		b.WriteString(muted.Render("Agent wants to call tool: ") + toolNameStyle.Render(tc.Name))
		b.WriteString("\n")
		if tc.Schema != "" {
			b.WriteString(muted.Render("Description: ") + text.Render(truncateRunes(tc.Schema, innerWidth)))
			b.WriteString("\n")
		}
		b.WriteString(muted.Render("Arguments: "))
		b.WriteString(text.Render(truncateRunes(tc.Args, innerWidth)))
	}
	b.WriteString("\n\n")
	b.WriteString(riskLabelStyle.Render("Risk: "))
	b.WriteString(text.Render(truncateRunes(riskText(tc), innerWidth)))
	if iso := sandboxIsolationText(sb, allowNetwork); iso != "" && (tc.Name == "shell.run" || tc.Name == "test.run") {
		b.WriteString("\n")
		b.WriteString(muted.Render(truncateRunes(iso, innerWidth)))
	}
	b.WriteString("\n\n")
	helpLine := key.Render("Enter") + muted.Render(" approve ") + key.Render("d") + muted.Render(" deny ") + key.Render("e") + muted.Render(" edit ") + key.Render("a") + muted.Render(" always")
	b.WriteString(helpLine)
	return b.String()
}

func renderQuestionPanel(q *session.PendingQuestion, width int) string {
	title := lipgloss.NewStyle().Bold(true).Render("Marshal asks:")
	var b strings.Builder
	for _, qs := range q.Questions {
		b.WriteString("• ")
		b.WriteString(qs.Question)
		if len(qs.Options) > 0 {
			b.WriteString("  (")
			b.WriteString(strings.Join(qs.Options, " / "))
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	hint := lipgloss.NewStyle().Faint(true).Render("answer each question and press Enter · Esc to skip the rest")
	return lipgloss.JoinVertical(lipgloss.Left, title, lipgloss.NewStyle().Width(max(width-2, 1)).Render(b.String()), hint)
}

func renderWelcomeBanner(width int) string {
	dot := lipgloss.NewStyle().Foreground(coralColor).Render("●")
	brand := lipgloss.NewStyle().Foreground(coralColor).Bold(true).Render("marshal")
	tagline := mutedStyle.Render("local-first coding agent")
	cta := mutedStyle.Render("Type a question, or " + lipgloss.NewStyle().Bold(true).Render("/") + " for commands.")

	headline := dot + " " + brand + dimSeparator + tagline
	body := lipgloss.JoinVertical(lipgloss.Left, headline, "", cta)

	cardW := width
	if cardW > 48 {
		cardW = 48
	}
	cardH := 5
	return chrome.Panel("", body, cardW, cardH, true, activeTheme) + "\n"
}
