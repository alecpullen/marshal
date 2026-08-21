package patch

import (
	"fmt"
	"log/slog"
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

// Parse reads a SEARCH/REPLACE proposal. Repairs holds a note for every
// deviation it healed rather than rejected, so the caller can tell the model
// what it got wrong without forcing a whole new proposal.
type Result struct {
	Patches []FilePatch
	Repairs []string
}

// Parse is the strict-signature wrapper kept for existing callers.
func Parse(proposal string) ([]FilePatch, error) {
	res, err := ParseRepairing(proposal)
	return res.Patches, err
}

// ParseRepairing parses a proposal, recovering from terminator mistakes that
// have an unambiguous reading. A REPLACE block left open at a "File:" line or
// at end of input is committed rather than rejected: both are unambiguous
// terminators, and the alternative was already a hard error, so nothing that
// used to parse changes meaning. A trailing "=======" on such a block is
// dropped — models reach for the divider as the closing marker, and leaving
// it in silently corrupts the replacement text.
func ParseRepairing(proposal string) (Result, error) {
	var patches []FilePatch
	var repairs []string
	lines := strings.Split(strings.ReplaceAll(proposal, "\r\n", "\n"), "\n")

	var currentPath string
	var searchBuffer []string
	var replaceBuffer []string
	inSearch := false
	inReplace := false

	// declared here so flushChunk can call it; defined below.
	var commitChunk func()

	flushChunk := func(terminator string) error {
		if !(inSearch || inReplace) {
			return nil
		}
		if inSearch {
			return fmt.Errorf("patch: unclosed SEARCH block for %q: add a %q divider line, then the replacement, then %q",
				currentPath, "=======", ">>>>>>> REPLACE")
		}
		// An open REPLACE block: commit it if it carries a replacement and a
		// path. An empty buffer stays an error — "delete these lines" and
		// "the output got cut off" look identical, and guessing deletes code.
		// Trailing blank lines are an artifact of the missing terminator —
		// the normal path never captures them because the marker line ends
		// the block. Only this repair path trims.
		trimTrailingBlanks := func() {
			for len(replaceBuffer) > 0 && strings.TrimSpace(replaceBuffer[len(replaceBuffer)-1]) == "" {
				replaceBuffer = replaceBuffer[:len(replaceBuffer)-1]
			}
		}
		droppedDivider := false
		trimTrailingBlanks()
		for len(replaceBuffer) > 0 && dividerRe.MatchString(strings.TrimSpace(replaceBuffer[len(replaceBuffer)-1])) {
			replaceBuffer = replaceBuffer[:len(replaceBuffer)-1]
			droppedDivider = true
			trimTrailingBlanks()
		}
		if currentPath == "" || len(replaceBuffer) == 0 {
			return fmt.Errorf("patch: unclosed REPLACE block for %q: add a %q line to close it",
				currentPath, ">>>>>>> REPLACE")
		}
		marker := ">>>>>>> REPLACE"
		if droppedDivider {
			marker = "======="
		}
		repairs = append(repairs, fmt.Sprintf(
			"%s: REPLACE block closed with %q instead of %q; treated %s as the terminator",
			currentPath, marker, ">>>>>>> REPLACE", terminator))
		inReplace = false
		commitChunk()
		return nil
	}

	commitChunk = func() {
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
			if err := flushChunk("the next File: line"); err != nil {
				return Result{}, err
			}
			currentPath = strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
			continue
		}

		if searchMarkerRe.MatchString(trimmed) {
			if err := flushChunk("the next SEARCH marker"); err != nil {
				return Result{}, err
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
				// Orphan chunk — no File: header preceded this block. This is
				// the reachable drop point for empty-path chunks (the guard
				// inside commitChunk is unreachable via ParseRepairing).
				preview := strings.Join(searchBuffer, "\n")
				if len(preview) > 80 {
					preview = preview[:80]
				}
				slog.Warn("dropped patch chunk with no File: header", "preview", preview)
				return Result{}, fmt.Errorf("patch: chunk has no File: header before line %q", line)
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
	if err := flushChunk("end of the proposal"); err != nil {
		return Result{}, err
	}
	if len(patches) == 0 {
		// Returning an empty slice with no error left the caller to report
		// "no valid patches found in proposal", which tells a model nothing
		// about what it got wrong. The error is read by the model that wrote
		// the proposal, so it states the format instead.
		return Result{}, fmt.Errorf("patch: no SEARCH/REPLACE blocks found; each edit must be written as " +
			"a \"File: <path>\" line, then \"<<<<<<< SEARCH\", the exact existing lines, " +
			"\"=======\", the replacement lines, and \">>>>>>> REPLACE\"")
	}
	return Result{Patches: patches, Repairs: repairs}, nil
}
