package patch

import "strings"

// Failure classes reported by ValidatePatch for a SEARCH block that does not
// apply. They are exported so callers that need to classify a patch error
// (rather than merely print it) gate on the producer's own words instead of
// re-typing them: revise the message in ValidatePatch and every gate moves
// with it.
const (
	// FailureSearchNotFound marks a SEARCH block with no match in the file.
	FailureSearchNotFound = "search block not found"
	// FailureNearMatch marks a SEARCH block that matches only once whitespace
	// differences are collapsed away.
	FailureNearMatch = "near-match found"
)

// FailureGateMatches reports whether a patch error describes a SEARCH block
// that could not be located in the file (not found, or a whitespace-only
// near-match). It is deliberately false for ambiguous matches and for
// non-patch errors: those have their own fixes and must not trigger a
// content refresh.
func FailureGateMatches(errText string) bool {
	if errText == "" {
		return false
	}
	return strings.Contains(errText, FailureSearchNotFound) ||
		strings.Contains(errText, FailureNearMatch)
}

// FindFailingChunk returns the SEARCH block of the first chunk in fp that does
// not apply cleanly to content, together with the error ValidatePatch produced
// for it. failed is false when every chunk applies, which means the patch never
// failed against these bytes (the caller is holding a stale copy, or the
// failure was reported for a different file).
//
// Chunks are validated one at a time against the cumulative result of the
// preceding chunks, mirroring ValidatePatch's own sequential application, so
// the reported chunk is the one that genuinely fails rather than the first one.
// A caller that needs "the region that went stale" must use this instead of
// assuming chunk 0: in a multi-hunk patch against a file that changed on disk,
// the earlier chunks frequently still apply.
func FindFailingChunk(content string, fp FilePatch) (search string, err error, failed bool) {
	remaining := content
	for i := range fp.Chunks {
		one := FilePatch{Path: fp.Path, Chunks: []PatchChunk{fp.Chunks[i]}}
		ok, verr := ValidatePatch(remaining, one)
		if !ok || verr != nil {
			return fp.Chunks[i].Search, verr, true
		}
		remaining = ApplyPatch(remaining, one)
	}
	return "", nil, false
}
