package repo

import (
	"regexp"
	"strconv"
	"strings"
)

// LineRange is a half-open [Start, End) range of 1-based line numbers in a
// diff's post-image. internal/repo is uniformly 1-based; the conversion to
// LSP's 0-based positions happens once, outside this package.
type LineRange struct{ Start, End int }

// hunkRe captures the post-image start line and optional count from a
// unified-diff hunk header: "@@ -a,b +c,d @@". The count is omitted when it
// is exactly 1.
var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// DiffRanges parses a unified diff and returns, per file path, the
// post-image line ranges it touches.
//
// Only added lines are attributed. Context lines are excluded so a hunk
// that sits near a function boundary does not over-attribute to an
// adjacent symbol: the whole-hunk span would otherwise swallow the
// surrounding context and name the wrong declaration.
//
// Paths come from the "+++ b/path" header with the "b/" prefix stripped. A
// diff whose post-image is /dev/null is a deletion of the whole file and
// contributes nothing: there is no post-image source to attribute against.
func DiffRanges(diff string) map[string][]LineRange {
	out := map[string][]LineRange{}
	current := ""
	inHunk := false
	postLine := 0
	var added []LineRange
	// appendAdded records one added line, extending the previous range when
	// it is adjacent so a run of added lines stays a single range.
	appendAdded := func(line int) {
		if n := len(added); n > 0 && added[n-1].End == line {
			added[n-1].End = line + 1
			return
		}
		added = append(added, LineRange{Start: line, End: line + 1})
	}
	flushHunk := func() {
		if current != "" && len(added) > 0 {
			out[current] = append(out[current], added...)
		}
		added = nil
		inHunk = false
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			flushHunk()
			p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			// Some diffs append a tab and a timestamp to the path.
			if i := strings.IndexByte(p, '\t'); i >= 0 {
				p = p[:i]
			}
			if p == "/dev/null" {
				current = ""
				continue
			}
			current = strings.TrimPrefix(p, "b/")
		case strings.HasPrefix(line, "@@"):
			flushHunk()
			if current == "" {
				continue
			}
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			count := 1
			if m[2] != "" {
				c, err := strconv.Atoi(m[2])
				if err != nil {
					continue
				}
				count = c
			}
			if count == 0 {
				// A pure deletion. Attribute it to the line it was removed
				// from, so removing a whole block still names the symbol it
				// was removed from rather than nothing.
				if start < 1 {
					// A deletion at the very start of a file reports
					// post-image line 0, which is not a real line.
					start = 1
				}
				appendAdded(start)
				continue
			}
			postLine = start
			inHunk = true
		default:
			if !inHunk {
				continue
			}
			switch {
			case strings.HasPrefix(line, "+"):
				// An added line at the current post-image position.
				appendAdded(postLine)
				postLine++
			case strings.HasPrefix(line, "-"):
				// A removed line: it does not advance the post-image cursor.
			case strings.HasPrefix(line, " "):
				// A context line: it advances the cursor but is not
				// attributed, so a hunk near a boundary stays precise.
				postLine++
			}
		}
	}
	flushHunk()
	return out
}
