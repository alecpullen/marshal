package patch

import (
	"fmt"
	"regexp"
	"strings"
)

// Marker matching is deliberately tolerant of cosmetic deviation. Requiring
// exact equality caused 13 of 43 observed file.write_patch failures: models
// emit "<<<<<<< SEARCH>", ">>>>>>> REPLACE>", or ">>>>>>> REPLACE</patch>",
// and a marker line that fails to match becomes ordinary content, so the block
// silently never opens or never closes.
//
// The tolerance is bounded by the keyword. Angle-bracket runs alone are not
// enough to transition, so a git conflict marker inside a patched file
// ("<<<<<<< HEAD", ">>>>>>> branch") stays content. The divider demands seven
// or more '=' and nothing else, so a markdown heading underline ("=====")
// inside a SEARCH block is not mistaken for it.
var (
	searchMarkerRe  = regexp.MustCompile(`^<{5,}\s*SEARCH\b`)
	replaceMarkerRe = regexp.MustCompile(`^>{5,}\s*REPLACE\b`)
	dividerRe       = regexp.MustCompile(`^={7,}$`)
)

type FilePatch struct {
	Path   string
	Chunks []PatchChunk
}

type PatchChunk struct {
	Search  string
	Replace string
}

func Parse(proposal string) ([]FilePatch, error) {
	var patches []FilePatch
	lines := strings.Split(strings.ReplaceAll(proposal, "\r\n", "\n"), "\n")

	var currentPath string
	var searchBuffer []string
	var replaceBuffer []string
	inSearch := false
	inReplace := false

	flushChunk := func() error {
		if !(inSearch || inReplace) {
			return nil
		}
		if inSearch {
			return fmt.Errorf("patch: unclosed SEARCH block for %q", currentPath)
		}
		return fmt.Errorf("patch: unclosed REPLACE block for %q", currentPath)
	}

	commitChunk := func() {
		chunk := PatchChunk{
			Search:  strings.Join(searchBuffer, "\n"),
			Replace: strings.Join(replaceBuffer, "\n"),
		}
		if currentPath == "" {
			// Drop chunks with no path; the surrounding File: line was
			// missing or came after the chunk header. Caller will still
			// see an empty result, which is the legacy behavior for
			// orphan chunks. Detected separately when the chunk is the
			// only one in the proposal (see TestParseRejectsEmptyPathChunk).
			return
		}
		found := false
		for i := range patches {
			if patches[i].Path == currentPath {
				patches[i].Chunks = append(patches[i].Chunks, chunk)
				found = true
				break
			}
		}
		if !found {
			patches = append(patches, FilePatch{
				Path:   currentPath,
				Chunks: []PatchChunk{chunk},
			})
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") {
			if err := flushChunk(); err != nil {
				return nil, err
			}
			currentPath = strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
			continue
		}

		if searchMarkerRe.MatchString(trimmed) {
			if err := flushChunk(); err != nil {
				return nil, err
			}
			inSearch = true
			searchBuffer = nil
			continue
		}
		if dividerRe.MatchString(trimmed) && inSearch {
			inSearch = false
			inReplace = true
			replaceBuffer = nil
			continue
		}
		if replaceMarkerRe.MatchString(trimmed) && inReplace {
			if currentPath == "" {
				return nil, fmt.Errorf("patch: chunk has no File: header before line %q", line)
			}
			inReplace = false
			commitChunk()
			continue
		}

		if inSearch {
			searchBuffer = append(searchBuffer, line)
		} else if inReplace {
			replaceBuffer = append(replaceBuffer, line)
		}
	}
	if err := flushChunk(); err != nil {
		return nil, err
	}
	return patches, nil
}
