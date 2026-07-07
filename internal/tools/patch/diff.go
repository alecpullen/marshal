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
			region := NearestRegion(normContent, normSearch, 5)
			msg := fmt.Sprintf("search block not found in %s", fp.Path)
			if region != "" {
				msg += fmt.Sprintf("\n\nnearest region:\n%s", region)
			}
			return false, fmt.Errorf("%s", msg)
		}
		if count > 1 {
			return false, fmt.Errorf("ambiguous match: search block matched %d locations in %s", count, fp.Path)
		}
		normContent = strings.Replace(normContent, normSearch, strings.ReplaceAll(chunk.Replace, "\r\n", "\n"), 1)
	}
	return true, nil
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

func NearestRegion(content, search string, windowLines int) string {
	if content == "" || search == "" {
		return ""
	}
	if windowLines <= 0 {
		windowLines = 5
	}
	contentLines := strings.Split(content, "\n")
	searchLines := strings.Split(search, "\n")
	if len(contentLines) == 0 || len(searchLines) == 0 {
		return ""
	}

	searchTokens := make([][]string, len(searchLines))
	for i, sl := range searchLines {
		trimmed := strings.TrimSpace(sl)
		if trimmed == "" {
			continue
		}
		searchTokens[i] = tokenize(trimmed)
	}

	bestScore := -1
	bestStart := 0

	for start := 0; start <= len(contentLines)-windowLines; start++ {
		window := contentLines[start : start+windowLines]
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
	return strings.Join(contentLines[bestStart:end], "\n")
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
