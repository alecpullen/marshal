package tui

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/theme"
	"marshal/internal/diffview"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// marshalStyleConfig adapts glamour's dark style to the Warm Sunset
// palette: coral H1 instead of the banner-style default, violet section
// headings. Document text (252) and the 2-space margin already match the
// transcript's prose treatment.
func marshalStyleConfig() gansi.StyleConfig {
	cfg := styles.DarkStyleConfig
	cfg.Heading.StylePrimitive.Color = strutil.Ptr("175") // violetColor
	cfg.H1.StylePrimitive = gansi.StylePrimitive{
		Color: strutil.Ptr("209"), // coralColor
		Bold:  strutil.Ptr(true),
	}
	return cfg
}

const maxRenderers = 4

// mdRenderers caches glamour renderers by wrap width; building one parses
// the full style config, which is too slow to repeat per message. The cache
// is bounded to prevent unbounded growth on repeated resizes.
var (
	mdMu        sync.Mutex
	mdRenderers = map[int]*glamour.TermRenderer{}
)

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// getRenderer returns a cached glamour renderer for the requested width,
// evicting the entry farthest from width when the cache is full.
func getRenderer(width int) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()
	if r, ok := mdRenderers[width]; ok {
		return r
	}
	if len(mdRenderers) >= maxRenderers {
		var evictKey int
		var evictDist int
		first := true
		for k := range mdRenderers {
			d := abs(k - width)
			if first || d > evictDist {
				evictKey, evictDist, first = k, d, false
			}
		}
		delete(mdRenderers, evictKey)
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(marshalStyleConfig()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	mdRenderers[width] = r
	return r
}

// renderMarkdown renders content as ANSI-styled markdown wrapped to
// width. ok is false when glamour is unavailable (renderer construction
// or rendering failed); callers fall back to plain text.
func renderMarkdown(content string, width int) (out string, ok bool) {
	r := getRenderer(width)
	if r == nil {
		return "", false
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

// gutterPrefix renders the hairline gutter marker: a single glyph preceded
// and followed by one space. In NO_COLOR mode the glyph survives because
// lipgloss emits no SGR when the color is NoColor{}.
func gutterPrefix(glyph string, c color.Color) string {
	return lipgloss.NewStyle().Foreground(c).Render(" " + glyph + " ")
}

func renderCodeBlock(content string, width int) string {
	if width < 1 {
		width = 1
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	// Reserve 2 cells for left/right surface padding.
	inner := max(width-2, 1)
	style := codeSurfaceStyle().Width(inner)
	var b strings.Builder
	for i, line := range strings.Split(trimmed, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(style.Render(ansi.Truncate(line, inner, "…")))
	}
	return b.String()
}

func renderFinalAnswer(msg session.Message, width int) string {
	if width < 10 {
		width = 10
	}
	gutter := gutterPrefix("▍", accentColor)
	contentWidth := max(width-3, 1)

	var b strings.Builder
	if msg.Salvaged {
		note := "salvaged"
		if msg.SalvageReason != "" {
			note += dimSeparator + msg.SalvageReason
		}
		b.WriteString(gutter)
		b.WriteString(mutedStyle().Render(note))
		b.WriteString("\n")
	}

	body, ok := renderMarkdown(msg.Content, contentWidth)
	if !ok {
		body = renderPlainProse(msg.Content, contentWidth)
	}
	for _, line := range strings.Split(strings.Trim(body, "\n"), "\n") {
		b.WriteString(gutter)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func formatThinkDuration(d time.Duration) string {
	return fmt.Sprintf("%.0fs", d.Seconds())
}

// renderThinkingBox renders live reasoning as a compact inline line. It
// intentionally returns nothing until reasoning text has arrived; providers
// that do not stream reasoning should not get an empty thinking panel.
func renderThinkingBox(reasoning, spinnerFrame string, width int) string {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return ""
	}
	contentWidth := max(width-3, 1)
	lines := strings.Split(reasoning, "\n")
	tailLines := lines
	if len(lines) > 3 {
		tailLines = lines[len(lines)-3:]
	}
	var b strings.Builder
	header := spinnerLabel(spinnerFrame, "thinking")
	b.WriteString(gutterPrefix("·", dimColor))
	b.WriteString(thinkingLineStyle().Render(header))
	b.WriteString("\n")
	for _, line := range tailLines {
		wrapped := ansi.Wrap(line, contentWidth, "")
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(gutterPrefix("▍", dimColor))
			b.WriteString(thinkingLineStyle().Render(wl))
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
		return thinkingLineStyle().Render(fmt.Sprintf("  ⚙ thought for %s", formatThinkDuration(duration))) + "\n"
	}
	contentWidth := max(width-3, 1)
	var b strings.Builder
	b.WriteString(gutterPrefix("▍", dimColor))
	b.WriteString(thinkingLineStyle().Render(fmt.Sprintf("⚙ thought for %s", formatThinkDuration(duration))))
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimSpace(reasoning), "\n") {
		wrapped := ansi.Wrap(line, contentWidth, "")
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(gutterPrefix("▍", dimColor))
			b.WriteString(thinkingLineStyle().Render(wl))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderMessage formats one transcript entry with gutter-prefixed lines:
// user prompts get a ❯ prefix, agent prose renders as plain markdown with
// no role label, tool results render with a · gutter, system notices are
// dim. Final answers use a ▍ gutter and rich-content rendering.
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
		return gutterPrefix("▍", accentColor) + renderCodeBlock(msg.Content, max(width-3, 1)) + "\n"
	default: // plain and markdown prose render identically
		return renderAgentMarkdown(msg.Content, width)
	}
}

func renderUserMessage(content string, width int) string {
	gutter := gutterPrefix("❯", coralColor)
	contentWidth := max(width-3, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(gutter)
		} else {
			b.WriteString(strings.Repeat(" ", 3))
		}
		b.WriteString(lipgloss.NewStyle().Foreground(userColor).Render(line))
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
			b.WriteString(mutedStyle().Render("· " + line))
		} else {
			b.WriteString(mutedStyle().Render("  " + line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderQueuedMessages renders the F16 steering queue as a footer
// beneath the live transcript so the user can see what they typed
// while the agent was working. width is informational (no wrapping
// here yet — each queued line stays short in practice).
func renderQueuedMessages(q []string, width int) string {
	if len(q) == 0 {
		return ""
	}
	gutter := gutterPrefix("·", dimColor)
	var b strings.Builder
	for _, msg := range q {
		b.WriteString(gutter)
		b.WriteString(mutedStyle().Render("queued: " + strutil.Truncate(msg, max(width-12, 1), false)))
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
	gutter := gutterPrefix("·", dimColor)
	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(strutil.Truncate(strings.TrimSpace(lines[0]), max(width-3, 1), false))
	b.WriteString("\n")
	continuation := lines[1:]
	for _, line := range continuation {
		wrapped := ansi.Wrap(line, max(width-3, 1), "")
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(strings.Repeat(" ", 3))
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderPlanBlock(content string, width int) string {
	gutter := gutterPrefix("·", dimColor)
	contentWidth := max(width-3, 1)
	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(mutedStyle().Render("plan"))
	b.WriteString("\n")
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		wrapped := ansi.Wrap(line, contentWidth, "")
		for j, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(strings.Repeat(" ", 3))
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

func renderProviderError(err error, width int) string {
	contentWidth := max(width-3, 1)
	wrapped := ansi.Wrap("provider: "+err.Error(), contentWidth, "")
	lines := strings.Split(wrapped, "\n")
	if len(lines) == 0 {
		return ""
	}
	gutter := gutterPrefix("✗", errorColor)
	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(errorStyle().Render(lines[0]))
	b.WriteString("\n")
	for _, line := range lines[1:] {
		b.WriteString(strings.Repeat(" ", 3))
		b.WriteString(mutedStyle().Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderActiveToolCall shows the in-flight tool as a spinner line with a
// hairline gutter — no border. Command tools get a $ line and, when a
// sandbox backend is active, an isolation-status line, both dim-indented
// under the gutter. When the tool has streamed output, the last 6 lines
// are rendered dim-indented beneath the command line.
func renderActiveToolCall(atc session.ActiveToolCall, sb session.SandboxInfo, allowNetwork bool, spinnerFrame string, now time.Time, width int) string {
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	head := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", atc.Name, formatElapsed(elapsed)))
	gutter := gutterPrefix("·", dimColor)
	headerLine := gutter + toolBulletStyle().Render(strutil.Truncate(head, max(width-3, 1), false))
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Background(theme.Current().BGSurface).Render(headerLine))
	b.WriteString("\n")
	if atc.Name == "shell.run" || atc.Name == "test.run" {
		cmdLine := "$ " + atc.Args
		b.WriteString(strings.Repeat(" ", 3))
		b.WriteString(mutedStyle().Render(strutil.Truncate(cmdLine, max(width-3, 1), false)))
		b.WriteString("\n")
		if iso := sandboxIsolationText(sb, allowNetwork); iso != "" {
			b.WriteString(strings.Repeat(" ", 3))
			b.WriteString(mutedStyle().Render(strutil.Truncate(iso, max(width-3, 1), false)))
			b.WriteString("\n")
		}
	} else if atc.Args != "" {
		b.WriteString(strings.Repeat(" ", 3))
		b.WriteString(mutedStyle().Render(strutil.Truncate(atc.Args, max(width-3, 1), false)))
		b.WriteString("\n")
	}
	if atc.Output != "" {
		lines := strings.Split(strings.TrimRight(atc.Output, "\n"), "\n")
		const tail = 6
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		for _, line := range lines {
			b.WriteString(strings.Repeat(" ", 3))
			b.WriteString(mutedStyle().Render(strutil.Truncate(line, max(width-3, 1), false)))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderCompletedToolCall(event registry.AuditEvent, width int) string {
	glyph := "·"
	style := statusOkStyle()
	if event.Error != "" {
		glyph = "✗"
		style = errorStyle()
	}
	var gutterColor color.Color
	if event.Error != "" {
		gutterColor = theme.Current().StatusError
	} else {
		gutterColor = theme.Current().FGMuted
	}
	gutter := gutterPrefix(glyph, gutterColor)
	head := DisplayToolName(event.ToolName)
	if hookHint := hookIndicatorText(event.Hooks); hookHint != "" {
		head += dimSeparator + hookHint
	}
	if event.Error != "" {
		head += dimSeparator + event.Error
	} else if event.ResultSummary != "" {
		head += dimSeparator + event.ResultSummary
	}

	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(style.Render(strutil.Truncate(head, max(width-3, 1), false)))
	b.WriteString("\n")
	if isDiffTool(event.ToolName) && event.ResultContent != "" {
		rendered := diffview.Render(event.ResultContent, diffview.Options{
			Width:     max(width-2, 1),
			Mode:      diffview.ModeAuto,
			Highlight: true,
		})
		lines := strings.Split(rendered, "\n")
		const maxDiffLines = 20
		if len(lines) > maxDiffLines {
			lines = lines[:maxDiffLines]
		}
		b.WriteString(strings.Repeat(" ", 3))
		b.WriteString(dimStyle().Render("Diff:"))
		b.WriteString("\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			b.WriteString(strings.Repeat(" ", 3))
			b.WriteString(line)
			b.WriteString("\n")
		}
		if len(strings.Split(rendered, "\n")) > maxDiffLines {
			b.WriteString(strings.Repeat(" ", 3))
			b.WriteString(dimStyle().Render(fmt.Sprintf("... (%d more lines)", len(strings.Split(rendered, "\n"))-maxDiffLines)))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func isDiffTool(name string) bool {
	return name == "file.write_patch" || name == "patch.apply"
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
	var b strings.Builder
	b.WriteString(gutterPrefix("⚠", warningColor))
	headParts := []string{}
	if tc.Name == "shell.run" {
		headParts = append(headParts, tc.Command)
	} else {
		headParts = append(headParts, tc.Name)
	}
	headParts = append(headParts, riskText(tc))
	if iso := sandboxIsolationText(sb, allowNetwork); iso != "" {
		headParts = append(headParts, iso)
	}
	b.WriteString(warningStyle().Render(strings.Join(headParts, dimSeparator)))
	b.WriteString("\n")

	b.WriteString(gutterPrefix(" ", warningColor))
	b.WriteString(mutedStyle().Render("▸ allow   always   session   edit   deny"))
	b.WriteString("\n")
	return b.String()
}

func renderQuestionPanel(q *session.PendingQuestion, width int) string {
	gutter := gutterPrefix("?", violetColor)
	var b strings.Builder
	for _, qs := range q.Questions {
		b.WriteString(gutter)
		b.WriteString(lipgloss.NewStyle().Foreground(violetColor).Bold(true).Render(qs.Question))
		b.WriteString("\n")
		if len(qs.Options) > 0 {
			b.WriteString(strings.Repeat(" ", 3))
			b.WriteString(mutedStyle().Render("(" + strings.Join(qs.Options, " / ") + ")"))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderWelcomeBanner prints the one-time startup identity as plain
// transcript lines — brand chrome pays rent once, at startup, not as a
// persistent title bar.
func renderWelcomeBanner(width int) string {
	_ = width
	dot := lipgloss.NewStyle().Foreground(coralColor).Render("●")
	brand := lipgloss.NewStyle().Foreground(coralColor).Bold(true).Render("marshal")
	tagline := mutedStyle().Render("local-first coding agent")
	cta := mutedStyle().Render("Type a question, or " + lipgloss.NewStyle().Bold(true).Render("/") + " for commands.")
	return "  " + dot + " " + brand + dimSeparator + tagline + "\n\n  " + cta + "\n\n"
}
