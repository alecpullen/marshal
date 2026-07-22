// Package diffview renders unified diffs as styled, syntax-highlighted
// side-by-side or unified text for the TUI (F17). The diff algorithm for
// intraline emphasis uses github.com/sergi/go-diff (MIT); syntax highlighting
// uses github.com/alecthomas/chroma/v2 (MIT, already in the dep tree via
// glamour). This is a clean-room implementation of the public behavior
// described in docs/12 F17; it does not derive from crush's FSL-licensed
// diffview source.
package diffview

import (
	"bufio"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// maxRenderLines caps the rendered output to keep the TUI responsive on
// very large diffs. After this many content lines the renderer appends a
// truncation note. Full virtualization is a follow-up (see plan F17 R3).
const maxRenderLines = 500

// Mode selects the render layout.
type Mode int

const (
	ModeAuto       Mode = iota // side-by-side when width >= 120, else unified
	ModeSideBySide             // force side-by-side
	ModeUnified                // force unified
)

// Options configures a render.
type Options struct {
	Width     int    // available terminal width
	Mode      Mode   // layout selection
	Highlight bool   // apply chroma syntax highlighting
	Language  string // override lexer language (default: "go")
}

// LineKind classifies a diff line.
type LineKind int

const (
	LineContext LineKind = iota
	LineAdded
	LineRemoved
	LineHunkHeader
)

// Line is a single parsed diff line.
type Line struct {
	Kind      LineKind
	Content   string
	OldNumber int // 0 if not an old-side line
	NewNumber int // 0 if not a new-side line
}

// Hunk is a parsed @@ hunk.
type Hunk struct {
	OldStart int
	NewStart int
	Lines    []Line
	// FilePath is the best-effort path extracted from the `+++ b/...` header.
	// Empty if no header was seen. Used for language detection when the
	// caller does not override Options.Language.
	FilePath string
}

// Render produces a styled string for the TUI. It never returns an error —
// on parse failure it falls back to rendering the raw input as plain text
// (F17 R1: "falls back to plain text below a width floor or when
// highlighting fails").
func Render(diff string, opts Options) string {
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		return plainTextFallback(diff, opts.Width)
	}
	mode := opts.Mode
	if mode == ModeAuto {
		if opts.Width >= 120 {
			mode = ModeSideBySide
		} else {
			mode = ModeUnified
		}
	}
	if opts.Width <= 0 {
		opts.Width = 80
	}
	if opts.Language != "" {
		for i := range hunks {
			hunks[i].FilePath = opts.Language
		}
	}
	var b strings.Builder
	lineCount := 0
	truncated := false
	for i, h := range hunks {
		if i > 0 {
			b.WriteString("\n")
		}
		var written int
		if mode == ModeSideBySide {
			written = renderSideBySide(&b, h, opts)
		} else {
			written = renderUnified(&b, h, opts)
		}
		lineCount += written
		if lineCount > maxRenderLines {
			truncated = true
			break
		}
	}
	if truncated {
		fmt.Fprintf(&b, "\n%s\n",
			mutedStyle.Render("... (truncated; rerun /diff for full output)"))
	}
	return b.String()
}

func parseUnifiedDiff(diff string) ([]Hunk, error) {
	var hunks []Hunk
	var cur *Hunk
	var newPath string
	oldLine, newLine := 0, 0
	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "--- "):
			if cur != nil {
				hunks = append(hunks, *cur)
				cur = nil
			}
			continue
		case strings.HasPrefix(line, "+++ "):
			if cur != nil {
				hunks = append(hunks, *cur)
				cur = nil
			}
			newPath = strings.TrimPrefix(line, "+++ ")
			newPath = strings.TrimPrefix(newPath, "b/")
			if idx := strings.IndexByte(newPath, '\t'); idx >= 0 {
				newPath = newPath[:idx]
			}
			continue
		case strings.HasPrefix(line, "@@ "):
			if cur != nil {
				hunks = append(hunks, *cur)
			}
			os, ns, ok := parseHunkHeader(line)
			if !ok {
				return nil, fmt.Errorf("bad hunk header: %q", line)
			}
			cur = &Hunk{OldStart: os, NewStart: ns, FilePath: newPath}
			oldLine, newLine = os, ns
			cur.Lines = append(cur.Lines, Line{Kind: LineHunkHeader, Content: line})
			continue
		case strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index "):
			if cur != nil {
				hunks = append(hunks, *cur)
				cur = nil
			}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("diff line outside hunk: %q", line)
		}
		switch {
		case strings.HasPrefix(line, "+"):
			cur.Lines = append(cur.Lines, Line{Kind: LineAdded, Content: line[1:], NewNumber: newLine})
			newLine++
		case strings.HasPrefix(line, "-"):
			cur.Lines = append(cur.Lines, Line{Kind: LineRemoved, Content: line[1:], OldNumber: oldLine})
			oldLine++
		case strings.HasPrefix(line, " "):
			cur.Lines = append(cur.Lines, Line{Kind: LineContext, Content: line[1:], OldNumber: oldLine, NewNumber: newLine})
			oldLine++
			newLine++
		default:
			return nil, fmt.Errorf("unrecognized diff line: %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("no hunks parsed")
	}
	return hunks, nil
}

func parseHunkHeader(line string) (oldStart, newStart int, ok bool) {
	rest := strings.TrimPrefix(line, "@@ ")
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 {
		return 0, 0, false
	}
	oldStart, ok = parseRangeStart(parts[0])
	if !ok {
		return 0, 0, false
	}
	newStart, ok = parseRangeStart(parts[1])
	return oldStart, newStart, ok
}

func parseRangeStart(s string) (int, bool) {
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "+")
	if idx := strings.IndexByte(s, ','); idx >= 0 {
		s = s[:idx]
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func plainTextFallback(diff string, width int) string {
	if width <= 0 {
		return diff
	}
	var b strings.Builder
	count := 0
	for _, line := range strings.Split(diff, "\n") {
		if count >= maxRenderLines {
			fmt.Fprintf(&b, "%s\n", mutedStyle.Render("... (truncated; rerun /diff for full output)"))
			break
		}
		if lipgloss.Width(line) > width {
			line = truncateVisible(line, width)
		}
		b.WriteString(line)
		b.WriteString("\n")
		count++
	}
	return b.String()
}

// --- styling -----------------------------------------------------------

var (
	addedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	removedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	contextStyle   = lipgloss.NewStyle()
	hunkStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	mutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	addEmphStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	remEmphStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	separatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// --- unified -----------------------------------------------------------

func renderUnified(b *strings.Builder, h Hunk, opts Options) int {
	lang := detectLanguage(h)
	count := 0
	for _, ln := range h.Lines {
		if count >= maxRenderLines {
			return count
		}
		switch ln.Kind {
		case LineHunkHeader:
			b.WriteString(hunkStyle.Render(ln.Content))
		case LineAdded:
			content := ln.Content
			if opts.Highlight {
				content = highlightCode(content, lang)
			}
			b.WriteString(addedStyle.Render("+ " + content))
		case LineRemoved:
			content := ln.Content
			if opts.Highlight {
				content = highlightCode(content, lang)
			}
			b.WriteString(removedStyle.Render("- " + content))
		case LineContext:
			content := ln.Content
			if opts.Highlight {
				content = highlightCode(content, lang)
			}
			b.WriteString(contextStyle.Render("  " + content))
		}
		b.WriteString("\n")
		count++
	}
	return count
}

// --- side-by-side ------------------------------------------------------

type sidePair struct {
	left, right *Line
	// emph flags: when non-nil, only the differing substrings should be
	// rendered with the emph style. Nil means "no intraline emphasis" —
	// render the whole line in the base style.
	leftEmph, rightEmph *lineEmphasis
}

type lineEmphasis struct {
	// runs of (start, end) byte offsets into the line content that should
	// be rendered with the emph style. Other substrings stay base-styled.
	runs []offset
}

type offset struct{ start, end int }

var dmp = diffmatchpatch.New()

func pairLines(lines []Line) []sidePair {
	var pairs []sidePair
	i := 0
	for i < len(lines) {
		ln := lines[i]
		if ln.Kind == LineHunkHeader {
			i++
			continue
		}
		if ln.Kind == LineRemoved {
			if i+1 < len(lines) && lines[i+1].Kind == LineAdded {
				rem := lines[i]
				add := lines[i+1]
				le, re := computeEmphasis(rem.Content, add.Content)
				pairs = append(pairs, sidePair{left: &rem, right: &add, leftEmph: le, rightEmph: re})
				i += 2
				continue
			}
			l := lines[i]
			pairs = append(pairs, sidePair{left: &l})
			i++
			continue
		}
		if ln.Kind == LineAdded {
			r := ln
			pairs = append(pairs, sidePair{right: &r})
			i++
			continue
		}
		c := ln
		pairs = append(pairs, sidePair{left: &c, right: &c})
		i++
	}
	return pairs
}

// computeEmphasis runs diffmatchpatch on the two sides and returns the
// emphasis ranges (the substrings that differ from the counterpart).
// nil is returned when the line is empty on either side, or when nothing
// differs (so the whole line stays base-styled).
func computeEmphasis(left, right string) (*lineEmphasis, *lineEmphasis) {
	if left == "" || right == "" {
		return nil, nil
	}
	diffs := dmp.DiffMain(left, right, true)
	var le, re lineEmphasis
	lp, rp := 0, 0
	for _, d := range diffs {
		if d.Type == diffmatchpatch.DiffEqual {
			lp += len(d.Text)
			rp += len(d.Text)
			continue
		}
		if d.Type == diffmatchpatch.DiffDelete {
			le.runs = append(le.runs, offset{start: lp, end: lp + len(d.Text)})
			lp += len(d.Text)
		}
		if d.Type == diffmatchpatch.DiffInsert {
			re.runs = append(re.runs, offset{start: rp, end: rp + len(d.Text)})
			rp += len(d.Text)
		}
	}
	if len(le.runs) == 0 && len(re.runs) == 0 {
		return nil, nil
	}
	return &le, &re
}

func renderSideBySide(b *strings.Builder, h Hunk, opts Options) int {
	half := (opts.Width - 3) / 2 // 3 for the " | " separator
	if half < 20 {
		return renderUnified(b, h, opts)
	}
	lang := detectLanguage(h)
	pairs := pairLines(h.Lines)
	count := 0
	for _, p := range pairs {
		if count >= maxRenderLines {
			return count
		}
		lstr := renderSideColumn(p.left, half, removedStyle, remEmphStyle, p.leftEmph, opts.Highlight, lang)
		rstr := renderSideColumn(p.right, half, addedStyle, addEmphStyle, p.rightEmph, opts.Highlight, lang)
		b.WriteString(lstr)
		b.WriteString(separatorStyle.Render(" │ "))
		b.WriteString(rstr)
		b.WriteString("\n")
		count++
	}
	return count
}

func renderSideColumn(ln *Line, width int, baseStyle, emphStyle lipgloss.Style, emph *lineEmphasis, highlight bool, lang string) string {
	if ln == nil {
		return strings.Repeat(" ", width)
	}
	content := ln.Content
	if highlight {
		content = highlightCode(content, lang)
	}
	if emph != nil {
		content = applyEmphasis(content, emph, emphStyle)
	}
	content = truncateVisible(content, width)
	return baseStyle.Render(padRight(content, width))
}

// applyEmphasis rewrites content so that each emph run is pre-styled with
// emphStyle. The outer baseStyle.Render will compose with the existing
// ANSI sequences (lipgloss keeps them).
func applyEmphasis(content string, emph *lineEmphasis, emphStyle lipgloss.Style) string {
	if len(emph.runs) == 0 {
		return content
	}
	type span struct{ start, end int }
	runs := make([]span, len(emph.runs))
	for i, r := range emph.runs {
		runs[i] = span{r.start, r.end}
	}
	// Sort by start to make this robust to out-of-order diffs.
	for i := 0; i < len(runs); i++ {
		for j := i + 1; j < len(runs); j++ {
			if runs[j].start < runs[i].start {
				runs[i], runs[j] = runs[j], runs[i]
			}
		}
	}
	runes := []rune(content)
	// Convert byte offsets to rune offsets.
	type rsp struct{ rs, re int }
	var rrs []rsp
	for _, r := range runs {
		if r.start < 0 || r.end > len(content) || r.start >= r.end {
			continue
		}
		rs, re := -1, -1
		pos := 0
		for ri, ru := range runes {
			end := pos + len(string(ru))
			if rs < 0 && r.start >= pos && r.start < end {
				rs = ri
			}
			if r.end > pos && r.end <= end {
				re = ri + 1
				break
			}
			pos = end
		}
		if rs >= 0 && re > rs {
			rrs = append(rrs, rsp{rs, re})
		}
	}
	if len(rrs) == 0 {
		return content
	}
	merged := make([]rsp, 0, len(rrs))
	for _, r := range rrs {
		if len(merged) > 0 && r.rs <= merged[len(merged)-1].re {
			last := &merged[len(merged)-1]
			if r.re > last.re {
				last.re = r.re
			}
		} else {
			merged = append(merged, r)
		}
	}
	var sb strings.Builder
	cur := 0
	for _, m := range merged {
		if m.rs > cur {
			sb.WriteString(string(runes[cur:m.rs]))
		}
		sb.WriteString(emphStyle.Render(string(runes[m.rs:m.re])))
		cur = m.re
	}
	if cur < len(runes) {
		sb.WriteString(string(runes[cur:]))
	}
	return sb.String()
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// truncateVisible cuts s to at most width visible cells, preserving ANSI
// escape sequences (chroma highlighting / emphasis are applied before this
// runs in side-by-side mode).
func truncateVisible(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
}
