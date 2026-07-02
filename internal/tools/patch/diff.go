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
			return false, fmt.Errorf("search block not found in %s", fp.Path)
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
