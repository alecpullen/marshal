package patch

import (
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

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") {
			currentPath = strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
			continue
		}

		if trimmed == "<<<<<<< SEARCH" {
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
			inReplace = false
			chunk := PatchChunk{
				Search:  strings.Join(searchBuffer, "\n"),
				Replace: strings.Join(replaceBuffer, "\n"),
			}
			found := false
			for i := range patches {
				if patches[i].Path == currentPath {
					patches[i].Chunks = append(patches[i].Chunks, chunk)
					found = true
					break
				}
			}
			if !found && currentPath != "" {
				patches = append(patches, FilePatch{
					Path:   currentPath,
					Chunks: []PatchChunk{chunk},
				})
			}
			continue
		}

		if inSearch {
			searchBuffer = append(searchBuffer, line)
		} else if inReplace {
			replaceBuffer = append(replaceBuffer, line)
		}
	}

	return patches, nil
}
