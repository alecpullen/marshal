package chrome

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// dividerMark is the private-use rune that opens a content line built by
// Divider. Frame.Render recognises it and draws that line as a horizontal
// rule through the frame instead of as content.
const dividerMark = '\ue0d0'

// Frame is a panel's outline: the title and hints in the top border, the
// content inside "│ … │", and, when Closed, a bottom border carrying Footer.
type Frame struct {
	Title   string
	Hints   string      // right-aligned in the top border
	Footer  string      // the bottom border's left label; drawn only when Closed
	Closed  bool        // draw the bottom border; an open frame continues into what follows
	Color   color.Color // the border colour; nil uses FocusColor(th) when Focused, th.BorderMuted otherwise
	Focused bool
}

// FocusColor is the border colour a focused panel draws in.
func FocusColor(th theme.Theme) color.Color { return th.AccentSecondary }

// Render draws the frame around content: exactly outer cells wide and at most
// h rows tall — the top border, up to h-1 content rows (h-2 when Closed), then
// the bottom border.
//
// A content line built by Divider is drawn as a rule across the frame. When
// the first content row is a titled divider it is not drawn at all: its title
// joins the top border's left label as "<Title> · <divider title>".
func (f Frame) Render(content string, outer, h int, th theme.Theme) string {
	if h < 1 {
		h = 1
	}
	c := f.Color
	if c == nil {
		c = th.BorderMuted
		if f.Focused {
			c = FocusColor(th)
		}
	}
	border := lipgloss.NewStyle().Foreground(c)

	lines := strings.Split(content, "\n")
	title := f.Title
	if len(lines) > 0 {
		if dt, ok := dividerTitle(lines[0]); ok && dt != "" {
			lines = lines[1:]
			if title == "" {
				title = dt
			} else {
				title = title + " · " + dt
			}
		}
	}

	// A closed frame needs two rows before it can draw both its borders. Below
	// that it draws only the top border, so "at most h rows tall" holds even
	// for the heights a collapsing terminal can produce.
	closed := f.Closed && h >= 2
	budget := max(h-1, 0)
	if closed {
		budget = max(h-2, 0)
	}
	if len(lines) > budget {
		lines = lines[:budget]
	}

	out := make([]string, 0, h)
	out = append(out, BorderLine("╭─ ", f.titleLabel(title, th), f.hintsLabel(th), "╮", outer, c))

	inner := max(outer-4, 1)
	for _, l := range lines {
		if dt, ok := dividerTitle(l); ok {
			if dt == "" {
				out = append(out, border.Render("├"+strings.Repeat("─", max(outer-2, 1))+"┤"))
				continue
			}
			out = append(out, BorderLine("├─ ", border.Render(dt), "", "┤", outer, c))
			continue
		}
		row := ansi.Truncate(l, inner, "…")
		row += strings.Repeat(" ", max(inner-ansi.StringWidth(row), 0))
		out = append(out, border.Render("│ ")+row+border.Render(" │"))
	}

	if closed {
		if f.Footer == "" {
			out = append(out, border.Render("╰"+strings.Repeat("─", max(outer-2, 1))+"╯"))
		} else {
			footer := lipgloss.NewStyle().Foreground(th.FGMuted).Render(f.Footer)
			out = append(out, BorderLine("╰─ ", footer, "", "╯", outer, c))
		}
	}
	return strings.Join(out, "\n")
}

// titleLabel styles the top border's left label: accent-secondary and bold
// when focused (bold is dropped in monochrome), muted otherwise.
func (f Frame) titleLabel(title string, th theme.Theme) string {
	if !f.Focused {
		return lipgloss.NewStyle().Foreground(th.FGMuted).Render(title)
	}
	if isMonochrome(th) {
		return lipgloss.NewStyle().Foreground(th.AccentSecondary).Render(title)
	}
	return lipgloss.NewStyle().Bold(true).Foreground(th.AccentSecondary).Render(title)
}

// hintsLabel styles the top border's right label.
func (f Frame) hintsLabel(th theme.Theme) string {
	if f.Hints == "" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(th.FGMuted).Render(f.Hints)
}

// BorderLine draws one row of at most w cells — exactly w whenever there is
// room for the fixed glyphs:
// <open><left> ─…─ <right> ─<close>. With right empty it is
// <open><left> ─…─<close>, as livebox's border line is today. The glyphs are
// drawn in c; left and right are drawn as passed (already styled).
//
// When the row is too narrow for both labels, left is truncated with "…" down
// to 12 cells; below that right is dropped and left truncated to fit. At
// least one fill cell always stays.
func BorderLine(open, left, right, close string, w int, c color.Color) string {
	b := lipgloss.NewStyle().Foreground(c)
	openW, closeW := ansi.StringWidth(open), ansi.StringWidth(close)
	// room is the cells left for the left label once the glyphs, the
	// single-cell gaps around the labels and one fill cell are paid for.
	room := func(right string) int {
		n := w - openW - closeW - 2
		if right != "" {
			n -= ansi.StringWidth(right) + 2
		}
		return n
	}
	// A cramped row keeps room for the right label only while the left one
	// still says something; below that the right label is what goes.
	const minLeftWithRight = 12
	if right != "" && room(right) < minLeftWithRight {
		right = ""
	}
	left = ansi.Truncate(left, max(room(right), 1), "…")
	var out string
	if right != "" {
		fill := max(w-openW-closeW-ansi.StringWidth(left)-ansi.StringWidth(right)-3, 1)
		out = b.Render(open) + left + b.Render(" "+strings.Repeat("─", fill)+" ") + right + b.Render("─"+close)
	} else {
		fill := max(w-openW-closeW-ansi.StringWidth(left)-1, 1)
		out = b.Render(open) + left + b.Render(" "+strings.Repeat("─", fill)+close)
	}
	// A row can be narrower than its own fixed glyphs (open + gap + one fill
	// cell + close). Nothing inside the TUI asks for that — panels floor at
	// layout.PanelWidth and the renderer at minTerminalWidth — but BorderLine
	// is exported, so the row is clamped rather than allowed to overflow the
	// width a caller asked for.
	return ansi.Truncate(out, max(w, 1), "")
}

// Divider returns a content line Frame.Render draws as a horizontal rule
// across the frame instead of as content. With an empty title it is a plain
// "├───┤".
func Divider(title string) string {
	return string(dividerMark) + title
}

// dividerTitle reports whether line is a divider built by Divider and, if so,
// the title it carries.
func dividerTitle(line string) (string, bool) {
	r, size := decodeFirstRune(line)
	if r != dividerMark {
		return "", false
	}
	return line[size:], true
}

// Close appends a bottom border to an open frame: ╰ + ─×(w−2) + ╯ in c. A view
// whose last row is already a bottom border, or that has no rows at all, is
// returned unchanged.
func Close(view string, w int, c color.Color) string {
	if view == "" || isFramedBottom(view) {
		return view
	}
	border := lipgloss.NewStyle().Foreground(c).Render("╰" + strings.Repeat("─", max(w-2, 1)) + "╯")
	return view + "\n" + border
}

// JoinTop replaces the first ╭ and the last ╮ of the view's first row with ├
// and ┤, so a panel drawn directly under another one shares its opening rule.
func JoinTop(view string) string {
	if view == "" {
		return view
	}
	head := view
	tail := ""
	if i := strings.IndexByte(view, '\n'); i >= 0 {
		head, tail = view[:i], view[i:]
	}
	if i := strings.Index(head, "╭"); i >= 0 {
		head = head[:i] + "├" + head[i+len("╭"):]
	}
	if i := strings.LastIndex(head, "╮"); i >= 0 {
		head = head[:i] + "┤" + head[i+len("╮"):]
	}
	return head + tail
}

// IsFramed reports whether the first visible rune of the view's first row is
// ╭ (an open frame) or ├ (a frame joined to what precedes it).
func IsFramed(view string) bool {
	r, _ := decodeFirstRune(firstVisibleRow(view))
	return r == '╭' || r == '├'
}

// isFramedBottom reports whether the view's last row already starts with a
// bottom border.
func isFramedBottom(view string) bool {
	lines := strings.Split(view, "\n")
	r, _ := decodeFirstRune(ansi.Strip(lines[len(lines)-1]))
	return r == '╰'
}

// firstVisibleRow returns the view's first row with escape sequences removed.
func firstVisibleRow(view string) string {
	if i := strings.IndexByte(view, '\n'); i >= 0 {
		view = view[:i]
	}
	return ansi.Strip(view)
}

// decodeFirstRune returns the first rune of s and its encoded size. s must
// already be free of escape sequences; an empty string yields a zero rune.
func decodeFirstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}
