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

	lines := strings.Split(normContent, "\n")
	for _, chunk := range fp.Chunks {
		normSearch := strings.ReplaceAll(chunk.Search, "\r\n", "\n")
		idx := strings.Index(normContent, normSearch)
		if idx == -1 {
			return "", fmt.Errorf("search block not found during diffing: %s", path)
		}

		// Find start line number
		before := normContent[:idx]
		startLine := strings.Count(before, "\n") + 1

		searchLines := strings.Split(normSearch, "\n")
		replaceLines := strings.Split(strings.ReplaceAll(chunk.Replace, "\r\n", "\n"), "\n")

		// Grab context lines
		ctxBeforeCount := 3
		if startLine-1 < ctxBeforeCount {
			ctxBeforeCount = startLine - 1
		}
		ctxBefore := lines[startLine-1-ctxBeforeCount : startLine-1]

		ctxAfterStart := startLine - 1 + len(searchLines)
		ctxAfterCount := 3
		if len(lines)-ctxAfterStart < ctxAfterCount {
			ctxAfterCount = len(lines) - ctxAfterStart
		}
		ctxAfter := lines[ctxAfterStart : ctxAfterStart+ctxAfterCount]

		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", startLine-ctxBeforeCount, len(searchLines)+ctxBeforeCount+ctxAfterCount, startLine-ctxBeforeCount, len(replaceLines)+ctxBeforeCount+ctxAfterCount)
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
