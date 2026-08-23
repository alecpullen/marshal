package patch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Unified-diff acceptance. Models trained on diff editing emit
// ---/+++/@@ hunks no matter what the prompt says; instead of rejecting
// those proposals (and burning a retry), parseUnifiedDiff converts each
// hunk into the equivalent PatchChunk: context + removed lines become the
// Search text, context + added lines become the Replace text. Application
// stays content-anchored through ValidatePatch/ApplyPatch, so wrong @@
// line numbers — the most common model diff error — cost nothing.
// Detection is whole-proposal (looksLikeUnifiedDiff); mixing diff hunks
// and SEARCH/REPLACE blocks in one proposal is a teaching error, not a
// silent drop.

var (
	hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(,\d+)? \+\d+(,\d+)? @@`)
	hunkCountsRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	renameRe     = regexp.MustCompile(`^(rename from|rename to) `)
)

// elisionLines are the exact trimmed line contents models use to elide
// code inside hunks. A hunk containing one can never apply cleanly, so it
// is a teaching error rather than a silent mismatch.
var elisionLines = map[string]bool{
	"...":             true,
	"…":               true,
	"[unchanged]":     true,
	"// ...":          true,
	"// rest of file": true,
	"# unchanged":     true,
}

// looksLikeUnifiedDiff reports whether the proposal should be parsed as a
// unified diff: it contains a full @@ hunk header, a "diff --git" section
// opener (which carries rename-only diffs that have no hunks), or a ---/+++
// header pair. Only diff markers that appear BEFORE any SEARCH/REPLACE
// marker (File:, <<<<<<< SEARCH) count — a search/replace proposal starts
// with one of those, so its searched content can never trigger routing even
// if it contains diff-looking lines (e.g. patching a file that documents a
// diff). Routing on the header pair keeps a header-only proposal (no hunks)
// reaching the diff parser, which reports the "no hunks" teaching error
// rather than a misleading "no SEARCH/REPLACE blocks" one.
func looksLikeUnifiedDiff(proposal string) bool {
	lines := strings.Split(strings.ReplaceAll(proposal, "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") || searchMarkerRe.MatchString(trimmed) {
			// A SEARCH/REPLACE block has begun; anything after this is
			// content, not a diff header.
			return false
		}
		if hunkHeaderRe.MatchString(trimmed) {
			return true
		}
		if strings.HasPrefix(line, "diff --git ") {
			return true
		}
		if strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
			return true
		}
	}
	return false
}

// parseHunkCounts extracts the old/new line counts from a @@ hunk header.
// A missing count means 1 (git's default). The header shape is already
// validated by hunkHeaderRe before this is called.
func parseHunkCounts(header string) (oldCount, newCount int) {
	m := hunkCountsRe.FindStringSubmatch(header)
	if m == nil {
		return 0, 0
	}
	oldCount = 1
	if m[2] != "" {
		oldCount, _ = strconv.Atoi(m[2])
	}
	newCount = 1
	if m[4] != "" {
		newCount, _ = strconv.Atoi(m[4])
	}
	return oldCount, newCount
}

func parseUnifiedDiff(proposal string) (Result, error) {
	// TrimSuffix drops the phantom empty element a trailing newline would
	// otherwise produce, so it cannot be absorbed into the last hunk as a
	// spurious empty context line by the blank-line healing below.
	norm := strings.TrimSuffix(strings.ReplaceAll(proposal, "\r\n", "\n"), "\n")
	lines := strings.Split(norm, "\n")

	var patches []FilePatch
	var repairs []string

	var currentPath string
	var lastOldPath string
	var chunks []PatchChunk
	var searchBuf, replaceBuf []string
	inHunk := false
	sawHunk := false
	// Remaining old/new lines in the open hunk, from its @@ header. A
	// ---/+++ line is only a file header once the hunk is complete (both
	// counts zero); until then it is hunk body content, however much it
	// looks like a header. This is the reliable disambiguator — path-prefix
	// shape alone cannot tell "--- a/foo" the header from "-- a/foo" the
	// removed line.
	var oldRemaining, newRemaining int

	commitHunk := func() {
		if !inHunk {
			return
		}
		inHunk = false
		search := strings.Join(searchBuf, "\n")
		replace := strings.Join(replaceBuf, "\n")
		searchBuf, replaceBuf = nil, nil
		if search == replace {
			// Context-only hunk: a no-op the model emitted for
			// orientation. Committing it could only trigger a spurious
			// ambiguous-match error downstream.
			return
		}
		chunks = append(chunks, PatchChunk{Search: search, Replace: replace})
	}

	commitFile := func() {
		commitHunk()
		if currentPath != "" && len(chunks) > 0 {
			patches = append(patches, FilePatch{Path: currentPath, Chunks: chunks})
			repairs = append(repairs, fmt.Sprintf(
				"%s: converted unified diff hunks to search/replace blocks; prefer the SEARCH/REPLACE format",
				currentPath))
		}
		chunks = nil
		currentPath = ""
	}

	// hunkLine consumes one body line of the open hunk.
	hunkLine := func(line, trimmed string) error {
		prefix := line[0]
		body := line[1:]
		switch prefix {
		case ' ':
			if elisionLines[strings.TrimSpace(body)] {
				return elisionErr(currentPath)
			}
			oldRemaining--
			newRemaining--
			searchBuf = append(searchBuf, body)
			replaceBuf = append(replaceBuf, body)
		case '-':
			if elisionLines[strings.TrimSpace(body)] {
				return elisionErr(currentPath)
			}
			oldRemaining--
			searchBuf = append(searchBuf, body)
		case '+':
			if elisionLines[strings.TrimSpace(body)] {
				return elisionErr(currentPath)
			}
			newRemaining--
			replaceBuf = append(replaceBuf, body)
		default:
			// A SEARCH/REPLACE marker here means the model mixed formats.
			if searchMarkerRe.MatchString(trimmed) || replaceMarkerRe.MatchString(trimmed) ||
				strings.HasPrefix(trimmed, "File:") {
				return mixedErr()
			}
			if elisionLines[trimmed] {
				return elisionErr(currentPath)
			}
			return fmt.Errorf("patch: malformed line inside diff hunk for %q (must start with ' ', '-', or '+'): %q", currentPath, line)
		}
		return nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		next := ""
		if i+1 < len(lines) {
			next = lines[i+1]
		}

		// Hunk body is checked first while a hunk is open. A ---/+++ line is
		// only a file header once the hunk is complete (counts zero); inside
		// an open hunk it is content, however much it looks like a header.
		if inHunk {
			switch {
			case strings.HasPrefix(line, `\ No newline at end of file`):
				continue
			case line == "":
				// git emits a single space for an empty context line;
				// models often emit a truly empty line instead. Heal it.
				// A healed blank is a context line, so it consumes one old
				// and one new line from the hunk's declared counts.
				oldRemaining--
				newRemaining--
				searchBuf = append(searchBuf, "")
				replaceBuf = append(replaceBuf, "")
				continue
			case hunkHeaderRe.MatchString(trimmed):
				commitHunk()
				inHunk = true
				searchBuf, replaceBuf = nil, nil
				oldRemaining, newRemaining = parseHunkCounts(trimmed)
				continue
			case oldRemaining <= 0 && newRemaining <= 0 && strings.HasPrefix(line, "diff --git "):
				// Next file section — fall out of the switch into header
				// handling below, which commits the finished file.
				commitHunk()
			case oldRemaining <= 0 && newRemaining <= 0 &&
				strings.HasPrefix(line, "--- ") && strings.HasPrefix(next, "+++ "):
				// Header pair for the next file — fall out of the switch
				// into header handling below.
				commitHunk()
			case oldRemaining <= 0 && newRemaining <= 0 &&
				strings.HasPrefix(line, "+++ ") && hunkHeaderRe.MatchString(strings.TrimSpace(next)):
				// New file section with the --- line omitted. Only fires
				// once the current hunk's declared counts are exhausted, so
				// an added line like "++ b/foo" (rendered "+++ b/foo")
				// inside a hunk is never mistaken for a header. <= 0 (not
				// == 0) tolerates models that over-emit body lines beyond
				// the @@ header's counts.
				commitHunk()
			default:
				if err := hunkLine(line, trimmed); err != nil {
					return Result{}, err
				}
				continue
			}
		}

		// Mixed-format detection outside hunks.
		if searchMarkerRe.MatchString(trimmed) || replaceMarkerRe.MatchString(trimmed) ||
			dividerRe.MatchString(trimmed) || strings.HasPrefix(trimmed, "File:") {
			return Result{}, mixedErr()
		}

		switch {
		case strings.HasPrefix(line, "diff --git "):
			commitFile()
		case renameRe.MatchString(trimmed):
			return Result{}, fmt.Errorf("patch: renames via unified diff are not supported (%s); patch the new path and delete the old one separately", trimmed)
		case strings.HasPrefix(trimmed, "index ") || strings.HasPrefix(trimmed, "new file mode") ||
			strings.HasPrefix(trimmed, "deleted file mode") || strings.HasPrefix(trimmed, "old mode") ||
			strings.HasPrefix(trimmed, "new mode") || strings.HasPrefix(trimmed, "similarity index"):
			// Metadata lines carry nothing the content-anchored apply needs.
		case strings.HasPrefix(line, "--- "):
			commitFile()
			lastOldPath = stripDiffPrefix(strings.TrimSpace(strings.TrimPrefix(line, "---")))
		case strings.HasPrefix(line, "+++ "):
			commitFile()
			raw := strings.TrimSpace(strings.TrimPrefix(line, "+++"))
			if raw == "/dev/null" {
				return Result{}, fmt.Errorf("patch: file deletion via unified diff is not supported (%s); delete files with shell.run instead", lastOldPath)
			}
			currentPath = stripDiffPrefix(raw)
		case hunkHeaderRe.MatchString(trimmed):
			if currentPath == "" {
				return Result{}, fmt.Errorf("patch: hunk found before any +++ file header; each diff section needs \"--- a/<path>\" and \"+++ b/<path>\" lines")
			}
			inHunk = true
			sawHunk = true
			searchBuf, replaceBuf = nil, nil
			oldRemaining, newRemaining = parseHunkCounts(trimmed)
		default:
			// Stray prose between sections: tolerate.
		}
	}
	commitFile()

	if !sawHunk {
		return Result{}, fmt.Errorf("patch: no unified diff hunks found; each edit needs a \"@@ -start,count +start,count @@\" hunk with context, \"-\" removed, and \"+\" added lines")
	}
	if len(patches) == 0 {
		return Result{}, fmt.Errorf("patch: unified diff contained no effective changes; every hunk was context-only")
	}
	return Result{Patches: patches, Repairs: repairs}, nil
}

func elisionErr(path string) error {
	return fmt.Errorf("patch: elided lines are not supported inside diff hunks (%s); include every context line in full", path)
}

// stripDiffPrefix turns a diff header path ("b/internal/foo.go", possibly
// quoted) into a workspace-relative path.
func stripDiffPrefix(p string) string {
	p = strings.Trim(p, `"`)
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		p = p[2:]
	}
	return p
}
