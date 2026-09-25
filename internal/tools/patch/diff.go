package patch

import (
	"fmt"
	"strings"
)

func ValidatePatch(content string, fp FilePatch) (bool, error) {
	normContent := strings.ReplaceAll(content, "\r\n", "\n")
	for _, chunk := range fp.Chunks {
		normSearch := strings.ReplaceAll(chunk.Search, "\r\n", "\n")
		count := strings.Count(normContent, normSearch)
		if count == 0 {
			region := NearestRegion(normContent, normSearch, NearestRegionWindowSize(normSearch))
			msg := fmt.Sprintf("%s in %s", FailureSearchNotFound, fp.Path)
			// When the block matches once whitespace is collapsed away, the
			// failure is tabs/spaces/indentation, so say so rather than
			// reporting a plain not-found and sending the model looking for
			// content differences that do not exist.
			if whitespaceNearMatch(normContent, normSearch) {
				msg = fmt.Sprintf("%s; whitespace differs (tabs/spaces/indentation) in %s. Exact file content shown in the nearest region above.", FailureNearMatch, fp.Path)
			}
			if region != "" {
				msg += fmt.Sprintf("\n\nnearest region:\n%s", region)
				// Only point at a region when one was actually shown: the
				// guidance is otherwise a dangling reference to nothing.
				msg += "\n\nre-read the file around that region and retry with the exact bytes from disk."
			}
			return false, fmt.Errorf("%s", msg)
		}
		if count > 1 {
			return false, fmt.Errorf("ambiguous match: search block matched %d locations in %s; add more context lines to the search block to make it unique, then re-apply.", count, fp.Path)
		}
		normContent = strings.Replace(normContent, normSearch, strings.ReplaceAll(chunk.Replace, "\r\n", "\n"), 1)
	}
	return true, nil
}

// whitespaceNearMatch reports whether search matches content once whitespace
// differences are collapsed away. It distinguishes a failure caused purely by
// tabs/spaces/indentation from one caused by genuinely different content, and
// only rewrites the diagnostic message -- it never changes match semantics.
func whitespaceNearMatch(content, search string) bool {
	if search == "" {
		return false
	}
	return strings.Contains(collapseWhitespace(content), collapseWhitespace(search))
}

// collapseWhitespace normalizes every line by trimming trailing whitespace and
// collapsing runs of spaces/tabs into a single space.
func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = collapseWhitespaceLine(line)
	}
	return strings.Join(lines, "\n")
}

func collapseWhitespaceLine(line string) string {
	line = strings.TrimRight(line, " \t")
	var sb strings.Builder
	lastWasSpace := false
	for _, r := range line {
		if r == ' ' || r == '\t' {
			if !lastWasSpace {
				sb.WriteByte(' ')
				lastWasSpace = true
			}
			continue
		}
		sb.WriteRune(r)
		lastWasSpace = false
	}
	return sb.String()
}

// NearestRegionWindowSize sizes NearestRegion's fuzzy-match window to the
// search block itself (plus a little context on top), so the hint can show
// the model the ENTIRE region it needs to reconstruct rather than a
// truncated slice. A fixed small window can never contain a multi-line
// search block that's longer than it, so every retry against the same
// content sees the identical incomplete hint — live testing showed exactly
// this: 4 consecutive failed patch attempts against the same file, each
// shown the same truncated 5-line window that never covered the full block.
func NearestRegionWindowSize(search string) int {
	const margin = 2
	const minWindow = 5
	size := strings.Count(search, "\n") + 1 + margin
	if size < minWindow {
		return minWindow
	}
	return size
}

func GenerateDiff(path string, content string, fp FilePatch) (string, error) {
	normContent := strings.ReplaceAll(content, "\r\n", "\n")
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", path, path)

	currentContent := normContent
	lineDelta := 0

	for _, chunk := range fp.Chunks {
		normSearch := strings.ReplaceAll(chunk.Search, "\r\n", "\n")
		normReplace := strings.ReplaceAll(chunk.Replace, "\r\n", "\n")

		idx := strings.Index(currentContent, normSearch)
		if idx == -1 {
			return "", fmt.Errorf("search block not found during diffing: %s", path)
		}

		before := currentContent[:idx]
		intermediateStartLine := strings.Count(before, "\n") + 1

		oldStartLine := intermediateStartLine - lineDelta
		newStartLine := intermediateStartLine

		searchLines := strings.Split(normSearch, "\n")
		replaceLines := strings.Split(normReplace, "\n")
		currentLines := strings.Split(currentContent, "\n")

		ctxBeforeCount := 3
		if intermediateStartLine-1 < ctxBeforeCount {
			ctxBeforeCount = intermediateStartLine - 1
		}
		ctxBefore := currentLines[intermediateStartLine-1-ctxBeforeCount : intermediateStartLine-1]

		ctxAfterStart := intermediateStartLine - 1 + len(searchLines)
		ctxAfterCount := 3
		if len(currentLines)-ctxAfterStart < ctxAfterCount {
			ctxAfterCount = len(currentLines) - ctxAfterStart
		}
		ctxAfter := currentLines[ctxAfterStart : ctxAfterStart+ctxAfterCount]

		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n",
			oldStartLine-ctxBeforeCount, len(searchLines)+ctxBeforeCount+ctxAfterCount,
			newStartLine-ctxBeforeCount, len(replaceLines)+ctxBeforeCount+ctxAfterCount,
		)
		for _, l := range ctxBefore {
			fmt.Fprintf(&sb, " %s\n", l)
		}
		for _, l := range searchLines {
			fmt.Fprintf(&sb, "-%s\n", l)
		}
		for _, l := range replaceLines {
			fmt.Fprintf(&sb, "+%s\n", l)
		}
		for _, l := range ctxAfter {
			fmt.Fprintf(&sb, " %s\n", l)
		}

		currentContent = strings.Replace(currentContent, normSearch, normReplace, 1)
		lineDelta += len(replaceLines) - len(searchLines)
	}
	return sb.String(), nil
}

func ApplyPatch(content string, fp FilePatch) string {
	normContent := strings.ReplaceAll(content, "\r\n", "\n")
	for _, chunk := range fp.Chunks {
		normSearch := strings.ReplaceAll(chunk.Search, "\r\n", "\n")
		normContent = strings.Replace(normContent, normSearch, strings.ReplaceAll(chunk.Replace, "\r\n", "\n"), 1)
	}
	return normContent
}

// locateWindowCap bounds the sliding window used to LOCATE the best-matching
// position, independent of how many lines are ultimately displayed. A larger
// scoring window dilutes precision: brace-only lines like "}" or "})" tokenize
// to nothing, so widening the scan window to fit a long search block can tie
// the true match against an earlier, wrong position that happens to cover the
// same number of scoreable (non-empty-token) lines. Locating with a small,
// fixed window and only widening the DISPLAYED slice afterward avoids this.
const locateWindowCap = 5

// NearestRegion returns the slice of content that best matches search,
// widened to windowLines. It is the display-only convenience form of
// NearestRegionLines.
func NearestRegion(content, search string, windowLines int) string {
	region, _, _ := NearestRegionLines(content, search, windowLines)
	return region
}

// NearestRegionLines returns the region NearestRegion would display together
// with that region's 1-based, inclusive line range within content. A caller
// that points the model at the bytes on disk needs the range; a caller that
// merely prints them does not. The range is zero when no region was located.
func NearestRegionLines(content, search string, windowLines int) (string, int, int) {
	if content == "" || search == "" {
		return "", 0, 0
	}
	if windowLines <= 0 {
		windowLines = 5
	}
	contentLines := strings.Split(content, "\n")
	searchLines := strings.Split(search, "\n")
	if len(contentLines) == 0 || len(searchLines) == 0 {
		return "", 0, 0
	}

	searchTokens := make([][]string, len(searchLines))
	for i, sl := range searchLines {
		trimmed := strings.TrimSpace(sl)
		if trimmed == "" {
			continue
		}
		searchTokens[i] = tokenize(trimmed)
	}

	locateWindow := windowLines
	if locateWindow > locateWindowCap {
		locateWindow = locateWindowCap
	}
	if locateWindow > len(contentLines) {
		locateWindow = len(contentLines)
	}

	bestScore := -1
	bestStart := 0

	for start := 0; start <= len(contentLines)-locateWindow; start++ {
		window := contentLines[start : start+locateWindow]
		windowTokenSets := make([][]string, len(window))
		for i, wl := range window {
			trimmed := strings.TrimSpace(wl)
			if trimmed != "" {
				windowTokenSets[i] = tokenize(trimmed)
			}
		}

		score := 0
		for _, sTokens := range searchTokens {
			if len(sTokens) == 0 {
				continue
			}
			bestLineOverlap := 0
			for _, wTokens := range windowTokenSets {
				if len(wTokens) == 0 {
					continue
				}
				overlap := countOverlap(sTokens, wTokens)
				if overlap > bestLineOverlap {
					bestLineOverlap = overlap
				}
			}
			score += bestLineOverlap
		}
		if score > bestScore {
			bestScore = score
			bestStart = start
		}
	}

	end := bestStart + windowLines
	if end > len(contentLines) {
		end = len(contentLines)
	}
	return strings.Join(contentLines[bestStart:end], "\n"), bestStart + 1, end
}

func tokenize(s string) []string {
	var tokens []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '(' || r == ')' || r == '{' || r == '}' || r == ',' || r == ';'
	}) {
		t = strings.Trim(t, "\"'")
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

func countOverlap(a, b []string) int {
	count := 0
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	for _, s := range a {
		if set[s] {
			count++
		}
	}
	return count
}
