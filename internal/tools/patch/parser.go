package patch

import (
	"fmt"
	"strings"
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

		if trimmed == "<<<<<<< SEARCH" {
			if err := flushChunk(); err != nil {
				return nil, err
			}
			inSearch = true
			searchBuffer = nil
			continue
		}
		if trimmed == "=======" && inSearch {
			inSearch = false
			inReplace = true
			replaceBuffer = nil
			continue
		}
		if trimmed == ">>>>>>> REPLACE" && inReplace {
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
