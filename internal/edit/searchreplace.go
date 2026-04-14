package edit

import (
	"strings"
)

// SearchReplaceEdit is a single SEARCH/REPLACE block targeting one file.
type SearchReplaceEdit struct {
	Path    string
	Search  string // text to find (empty = create new file)
	Replace string // replacement text
}

// ParseSearchReplace extracts SEARCH/REPLACE blocks from an executor response.
//
// Expected format (filename on the line before the opening marker):
//
//	path/to/file.go
//	<<<<<<< SEARCH
//	old content
//	=======
//	new content
//	>>>>>>> REPLACE
//
// Marker variants accepted:
//   - "<<<<<<< SEARCH" / "=======" / ">>>>>>> REPLACE"
//   - "<<<<<<<"         / "=======" / ">>>>>>>"  (markers without labels)
//
// Multiple blocks in one response are all extracted.
func ParseSearchReplace(response string) []SearchReplaceEdit {
	var results []SearchReplaceEdit
	lines := strings.Split(response, "\n")

	i := 0
	for i < len(lines) {
		// Find opening marker.
		if !isSearchMarker(lines[i]) {
			i++
			continue
		}

		// Walk backward to find file path.
		path := ""
		for j := i - 1; j >= 0; j-- {
			prev := strings.TrimSpace(lines[j])
			if prev == "" {
				continue
			}
			if looksLikeFilePath(prev) {
				path = prev
			}
			break
		}
		if path == "" {
			i++
			continue
		}

		// Collect SEARCH content (between <<< and ===).
		i++ // skip opening marker
		var searchLines []string
		for i < len(lines) {
			if isDividerMarker(lines[i]) {
				break
			}
			searchLines = append(searchLines, lines[i])
			i++
		}
		if i >= len(lines) {
			break
		}
		i++ // skip divider

		// Collect REPLACE content (between === and >>>).
		var replaceLines []string
		for i < len(lines) {
			if isReplaceMarker(lines[i]) {
				break
			}
			replaceLines = append(replaceLines, lines[i])
			i++
		}
		i++ // skip closing marker

		results = append(results, SearchReplaceEdit{
			Path:    path,
			Search:  strings.Join(searchLines, "\n"),
			Replace: strings.Join(replaceLines, "\n"),
		})
	}

	return results
}

// ApplyToContent applies a single SearchReplaceEdit to existing file content,
// returning the new content.  If the search string is not found (even after
// whitespace normalisation) it returns the original content unchanged and
// ok=false.
func (e *SearchReplaceEdit) ApplyToContent(current string) (updated string, ok bool) {
	// Empty SEARCH means create/overwrite entirely.
	if strings.TrimSpace(e.Search) == "" {
		return e.Replace, true
	}

	replace := e.Replace
	if !strings.HasSuffix(replace, "\n") {
		replace += "\n"
	}

	// 1. Exact match.
	if strings.Contains(current, e.Search) {
		return strings.Replace(current, e.Search, replace, 1), true
	}

	// 2. Normalised-whitespace match: compare trimmed lines.
	updated, ok = applyNormalised(current, e.Search, replace)
	if ok {
		return updated, true
	}

	// 3. Fuzzy match: try with leading/trailing whitespace trimmed
	trimmedSearch := strings.TrimSpace(e.Search)
	if trimmedSearch != e.Search && strings.Contains(current, trimmedSearch) {
		// Find the trimmed content and replace with proper indentation handling
		idx := strings.Index(current, trimmedSearch)
		if idx != -1 {
			return current[:idx] + replace + current[idx+len(trimmedSearch):], true
		}
	}

	return current, false
}

// MismatchInfo holds diagnostic info about why search/replace failed.
type MismatchInfo struct {
	SearchAttempted string  // what the model tried to find
	FilePreview     string  // what's actually in the file (first few lines)
	SearchLineCount int     // how many lines in search
	FileLineCount   int     // how many lines in file
	MatchPercentage float64 // approximate match quality
	SuggestedSearch string  // what the model probably meant
}

// GetMismatchInfo analyzes why the search failed and returns diagnostic info.
func (e *SearchReplaceEdit) GetMismatchInfo(current string) MismatchInfo {
	info := MismatchInfo{
		SearchAttempted: e.Search,
		FilePreview:     truncate(current, 500),
		SearchLineCount: strings.Count(e.Search, "\n") + 1,
		FileLineCount:   strings.Count(current, "\n") + 1,
	}

	// Calculate match percentage (simplified)
	if len(e.Search) > 0 {
		matched := 0
		searchLines := strings.Split(e.Search, "\n")
		currentLines := strings.Split(current, "\n")
		for _, sline := range searchLines {
			slineTrim := strings.TrimSpace(sline)
			if slineTrim == "" {
				continue
			}
			for _, cline := range currentLines {
				if strings.Contains(cline, slineTrim) {
					matched++
					break
				}
			}
		}
		info.MatchPercentage = float64(matched) / float64(len(searchLines)) * 100
	}

	// Try to suggest what the model probably meant
	// Look for the first non-empty search line in current file
	searchLines := strings.Split(e.Search, "\n")
	for _, sline := range searchLines {
		slineTrim := strings.TrimSpace(sline)
		if slineTrim != "" && len(slineTrim) > 10 {
			idx := strings.Index(current, slineTrim)
			if idx != -1 {
				// Found partial match, extract surrounding context
				start := idx - 100
				if start < 0 {
					start = 0
				}
				end := idx + len(slineTrim) + 200
				if end > len(current) {
					end = len(current)
				}
				info.SuggestedSearch = truncate(current[start:end], 300)
				break
			}
		}
	}

	return info
}

// applyNormalised tries to find search in current by comparing each line
// after trimming trailing whitespace.  This handles models that strip trailing
// spaces from their output.
func applyNormalised(current, search, replace string) (string, bool) {
	searchLines := strings.Split(search, "\n")
	currentLines := strings.Split(current, "\n")

	// Trim trailing whitespace from each search line for comparison.
	trimmed := make([]string, len(searchLines))
	for i, l := range searchLines {
		trimmed[i] = strings.TrimRight(l, " \t")
	}

	n := len(trimmed)
	for i := 0; i <= len(currentLines)-n; i++ {
		match := true
		for j, sl := range trimmed {
			if strings.TrimRight(currentLines[i+j], " \t") != sl {
				match = false
				break
			}
		}
		if match {
			result := append(
				append([]string{}, currentLines[:i]...),
				strings.Split(replace, "\n")...,
			)
			result = append(result, currentLines[i+n:]...)
			return strings.Join(result, "\n"), true
		}
	}
	return current, false
}

// --- marker detection --------------------------------------------------------

func isSearchMarker(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "<<<<<<<")
}

func isDividerMarker(line string) bool {
	t := strings.TrimSpace(line)
	return t == "=======" || strings.HasPrefix(t, "=======")
}

func isReplaceMarker(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, ">>>>>>>")
}

// truncate limits a string to n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
