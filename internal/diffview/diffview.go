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
	"sort"
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

// Result is a rendered diff plus the facts about that rendering which a string
// cannot carry.
//
// It exists because the renderer CAPS its output at maxRenderLines. A caller
// that only receives the string cannot tell a complete patch from its first 500
// lines, and the Changes inspector has to state which one the reader is looking
// at: labelling a truncated diff as the whole thing is a claim the bytes do not
// support.
type Result struct {
	// Text is the styled output, identical to what Render returns.
	Text string
	// Lines is the number of diff content lines the renderer consumed.
	Lines int
	// Truncated reports that content was dropped, so Text is a prefix of the
	// real diff and the reader is not seeing all of it.
	Truncated bool
}

// Render produces a styled string for the TUI. It never returns an error —
// on parse failure it falls back to rendering the raw input as plain text
// (F17 R1: "falls back to plain text below a width floor or when
// highlighting fails").
//
// It is a thin wrapper over RenderResult, kept so existing callers are
// unaffected by the richer return type.
func Render(diff string, opts Options) string {
	return RenderResult(diff, opts).Text
}

// RenderResult renders a diff and reports whether content was dropped.
func RenderResult(diff string, opts Options) Result {
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		// The fallback caps on its own line count and says so inline, so both
		// the flag AND the line count come from the enumeration that actually
		// did the dropping. Deriving either from strings.Count here counted
		// newlines where the fallback counts strings.Split elements, and the
		// two differ by one whenever the last line has no trailing newline —
		// which is exactly the case where the fallback drops a line, so the
		// capped render was labelled complete.
		text, lines, truncated := plainTextFallback(diff, opts.Width)
		return Result{Text: text, Lines: lines, Truncated: truncated}
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
		// The budget is GLOBAL, so it is handed to the renderer as the number
		// of lines that are still available rather than letting each hunk
		// measure against the full cap. A per-hunk cap is not a cap on the
		// output: two hunks that each fit would both render in full, the total
		// would reach roughly twice maxRenderLines, and — because no individual
		// renderer had dropped anything — the snapshot would be reported
		// complete. Passing `remaining` makes crossing the budget impossible
		// and makes `capped` a decision made against the true budget left.
		//
		// lineCount < maxRenderLines whenever a hunk is entered: the loop breaks
		// the moment the budget is spent and more hunks remain, so remaining is
		// at least 1 here (and the final hunk may legitimately spend it all).
		remaining := maxRenderLines - lineCount
		var written int
		var capped bool
		if mode == ModeSideBySide {
			written, capped = renderSideBySide(&b, h, opts, remaining)
		} else {
			written, capped = renderUnified(&b, h, opts, remaining)
		}
		lineCount += written
		// A hunk reports whether IT dropped content. The previous code inferred
		// truncation from lineCount exceeding the cap, but the renderers stop AT
		// the cap rather than crossing it — so a single hunk larger than the cap
		// lost its tail with no notice and no flag. Asking the hunk is the only
		// way to tell "the budget is exactly spent" from "content was dropped".
		if capped {
			truncated = true
			break
		}
		if lineCount >= maxRenderLines && i < len(hunks)-1 {
			// The budget is spent and more hunks remained.
			truncated = true
			break
		}
	}
	if truncated {
		fmt.Fprintf(&b, "\n%s\n",
			mutedStyle.Render("... (truncated; rerun /diff for full output)"))
	}
	return Result{Text: b.String(), Lines: lineCount, Truncated: truncated}
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
		case strings.HasPrefix(line, "\\ No newline at end of file"):
			// Git "no newline at end of file" marker — metadata, not content.
			// Only the exact marker is skipped; any other backslash-prefixed
			// line is treated as unrecognized so content is never silently
			// dropped.
			continue
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

// plainTextFallback renders the raw input as plain text — wrapped to width when
// one is given — and reports how many lines it kept and whether it dropped any.
//
// The report is RETURNED rather than inferred by the caller, because the
// caller's old rule was not the same condition. Counting newlines (what
// strings.Count sees) yields one fewer line than enumerating the input's lines
// whenever the last line has no trailing newline, and that off-by-one lands
// exactly on the drop decision: a 501-line input whose last line is unterminated
// lost that line while the flag said complete — a capped render presented as the
// whole diff, which is the false statement this API exists to prevent — while a
// naive Count+1 correction lies the other way, crying truncation for 500
// terminated lines that all rendered. Asking the code that actually drops is the
// only reading that matches what the reader sees.
//
// lines is what the render actually consumed, capped like the render: a caller
// comparing it against maxRenderLines must not be told a number the text does
// not contain. When width <= 0 the fallback is verbatim by definition, so lines
// is the input's true content-line count even past the cap — nothing was
// consumed by a cap that did not run.
func plainTextFallback(diff string, width int) (text string, lines int, truncated bool) {
	all := strings.Split(diff, "\n")
	// strings.Split appends an empty element when the input ends in a newline.
	// That element is not a line of content — it is the newline the previous
	// line already emitted — and charging it against the budget would make the
	// cap depend on whether the input happened to end in "\n": 500 terminated
	// lines would read as 501 and be reported truncated with all of their
	// content on screen. Drop it here and re-emit the newline at the end, so the
	// rendering stays byte-for-byte what it was.
	//
	// The rule cannot tell a newline artifact from real content, so input that
	// deliberately ends with a BLANK line ("a\n\n") loses that blank line from
	// the count as well: its trailing empty element is indistinguishable from a
	// terminator. Lines is therefore one lower than a reader counting the
	// visible lines would say, for that input only. Nothing is dropped from the
	// text (the newline is re-emitted below), and the alternative — counting the
	// artifact — is the mis-reporting described above, so this is the safer of
	// the two off-by-ones rather than a bug to be fixed.
	trailingNewline := len(all) > 0 && all[len(all)-1] == ""
	if trailingNewline {
		all = all[:len(all)-1]
	}
	if width <= 0 {
		// No wrapping was requested: the input is returned whole, nothing is
		// dropped, and both halves of the report say so.
		return diff, len(all), false
	}
	var b strings.Builder
	count := 0
	for _, line := range all {
		if count >= maxRenderLines {
			// Lines remain in the input, so this is a real drop. Reported, not
			// merely printed: the notice is for the reader, the flag is for the
			// caller that has to label the render, and the two must agree.
			fmt.Fprintf(&b, "%s\n", mutedStyle.Render("... (truncated; rerun /diff for full output)"))
			return b.String(), count, true
		}
		count++
		if lipgloss.Width(line) > width {
			line = truncateVisible(line, width)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if trailingNewline {
		// Reproduce the newline carried by the element dropped above.
		b.WriteString("\n")
	}
	return b.String(), count, false
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

// renderUnified writes one hunk in unified layout and returns how many content
// lines it wrote and whether it dropped any.
//
// budget is the number of lines this hunk may still spend, i.e. the GLOBAL
// remaining budget — not maxRenderLines. See RenderResult's loop: a per-hunk
// budget is what let several hunks each render in full and push the total past
// the cap while every renderer reported that it had dropped nothing.
func renderUnified(b *strings.Builder, h Hunk, opts Options, budget int) (int, bool) {
	lang := detectLanguage(h)
	count := 0
	// Unified lines are prefixed with two visible cells ("+ ", "- ", "  ").
	// Truncate the content so the full rendered line fits within opts.Width.
	maxContentWidth := opts.Width - 2
	if maxContentWidth < 1 {
		maxContentWidth = 1
	}
	for _, ln := range h.Lines {
		if count >= budget {
			// Lines remain in this hunk, so content is being dropped.
			return count, true
		}
		switch ln.Kind {
		case LineHunkHeader:
			b.WriteString(hunkStyle.Render(truncateVisible(ln.Content, opts.Width)))
		case LineAdded:
			content := ln.Content
			if opts.Highlight {
				content = highlightCode(content, lang)
			}
			content = truncateVisible(content, maxContentWidth)
			b.WriteString(addedStyle.Render("+ " + content))
		case LineRemoved:
			content := ln.Content
			if opts.Highlight {
				content = highlightCode(content, lang)
			}
			content = truncateVisible(content, maxContentWidth)
			b.WriteString(removedStyle.Render("- " + content))
		case LineContext:
			content := ln.Content
			if opts.Highlight {
				content = highlightCode(content, lang)
			}
			content = truncateVisible(content, maxContentWidth)
			b.WriteString(contextStyle.Render("  " + content))
		}
		b.WriteString("\n")
		count++
	}
	return count, false
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

// renderSideBySide writes one hunk in side-by-side layout and returns how many
// output lines it wrote and whether it dropped any.
//
// budget is the lines still available GLOBALLY, as for renderUnified.
func renderSideBySide(b *strings.Builder, h Hunk, opts Options, budget int) (int, bool) {
	// Reserve 3 for the " │ " separator. Give the left side
	// floor(remaining/2) and the right side ceil(remaining/2) so the full
	// width is used without wasting a character on even-width terminals.
	remaining := opts.Width - 3
	leftHalf := remaining / 2
	rightHalf := remaining - leftHalf
	if leftHalf < 20 {
		// Too narrow for two columns; the unified layout is the same content in
		// a different shape, so it inherits the same budget.
		return renderUnified(b, h, opts, budget)
	}
	lang := detectLanguage(h)
	pairs := pairLines(h.Lines)
	count := 0
	for _, p := range pairs {
		if count >= budget {
			// The guard runs before the pair is rendered, so reaching it means
			// at least this pair — and possibly more — is being dropped: the
			// return is capped unconditionally. (The old `i < len(pairs)` term
			// read i, the index of the pair just abandoned, so it was always
			// true; it said "capped if pairs remain" while answering a question
			// that could not come out false.)
			//
			// A diff that ends exactly at the budget never reaches here: the
			// loop simply runs out of pairs, falls out, and reports complete.
			return count, true
		}
		lstr := renderSideColumn(p.left, leftHalf, removedStyle, remEmphStyle, p.leftEmph, opts.Highlight, lang)
		rstr := renderSideColumn(p.right, rightHalf, addedStyle, addEmphStyle, p.rightEmph, opts.Highlight, lang)
		b.WriteString(lstr)
		b.WriteString(separatorStyle.Render(" │ "))
		b.WriteString(rstr)
		b.WriteString("\n")
		count++
	}
	return count, false
}

func renderSideColumn(ln *Line, width int, baseStyle, emphStyle lipgloss.Style, emph *lineEmphasis, highlight bool, lang string) string {
	if ln == nil {
		return strings.Repeat(" ", width)
	}
	content := ln.Content
	if highlight {
		content = highlightCode(content, lang)
	}
	// Emphasis offsets are computed from un-highlighted content and are
	// incompatible with the highlighted string (which has ANSI escapes
	// inserted). Skip emphasis when highlighting is on — syntax colors
	// already provide visual differentiation.
	if emph != nil && !highlight {
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
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].start < runs[j].start
	})
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
