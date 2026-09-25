package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/pathutil"
	"marshal/internal/tools/patch"
)

// failedPatchTargetHint builds the tier-3+ retry hint for a failed
// file.write_patch: which SEARCH block actually failed, and the exact file.read
// call that shows the current bytes for it.
//
// It deliberately does NOT inline the region. The tool's own error already
// embeds the nearest region for the failing chunk (both "search block not
// found" and "changed on disk" render one), so pasting a second copy added
// nothing in the single-chunk case and pointed at the WRONG bytes whenever the
// failure was not in the first chunk. What is genuinely missing is that the
// model keeps rebuilding its SEARCH block from its own stale memory of the
// file, so the intervention is a bounded, explicit re-read of the real target.
//
// Any failure to parse, resolve, read, or match returns "" — a retry aid must
// never become a new error path.
func failedPatchTargetHint(r *Runner, args json.RawMessage, execErr error) string {
	search, path, ok := failingPatchTarget(r, args, execErr)
	if !ok {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	region, start, end := patch.NearestRegionLines(string(data), search, patch.NearestRegionWindowSize(search))
	if region == "" || start <= 0 {
		return ""
	}
	// Advertise only lines the region actually spans: NearestRegionLines caps
	// the window at the end of the file, so the raw end is not always a line
	// that exists.
	end = start + strings.Count(region, newline)
	// The located region is anchored on the best-matching LINE, so it can end
	// before a later line of the failing block: a block whose tail is the stale
	// part lands with its anchor at the region's edge, and a hint that stops
	// one line short sends the model straight back to the bytes it already
	// guessed wrong. Extend by the block's own line count so the window covers
	// the whole footprint it has to retype, whichever line the anchor matched.
	if blockLines := strings.Count(search, newline) + 1; blockLines > 1 {
		end += blockLines - 1
	}
	return fmt.Sprintf("\n\nre-read the exact bytes before retrying: file.read %s start_line=%d end_line=%d (%d lines)",
		pathutil.WorkspaceRelative(r.State.WorkingDir, path), start, end, end-start+1)
}

// newline keeps the line-count arithmetic above free of escape noise.
const newline = "\n"

// failingPatchTarget returns the SEARCH block that failed and the absolute path
// of the file it failed in. Only a SEARCH block that could not be located at
// all qualifies (patch.FailureGateMatches): an ambiguous match has a different
// remedy and must not trigger a re-read.
func failingPatchTarget(r *Runner, args json.RawMessage, execErr error) (search, path string, ok bool) {
	if execErr == nil || !patch.FailureGateMatches(execErr.Error()) || len(args) == 0 {
		return "", "", false
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return "", "", false
	}
	patchText, _ := m["patch"].(string)
	if patchText == "" {
		return "", "", false
	}
	patches, err := patch.Parse(patchText)
	if err != nil || len(patches) == 0 {
		return "", "", false
	}
	// The failing file is located, never assumed to be the first one: walk the
	// patch in order and take the first file whose chunks genuinely do not
	// apply. A multi-file or multi-hunk patch therefore names the target that
	// actually broke, and a file the patch applies to cleanly is never reported
	// as the failing target.
	for _, fp := range patches {
		resolved, resolves := resolvePatchPath(r, fp.Path)
		if !resolves {
			continue
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			continue
		}
		search, _, failed := patch.FindFailingChunk(string(data), fp)
		if !failed {
			continue
		}
		return search, resolved, true
	}
	return "", "", false
}

// resolvePatchPath resolves a patch's File: header the way the file tools do:
// system access accepts an absolute path, otherwise the path resolves against
// the workspace root with symlink containment. Named-root aliases ("@name/...")
// are a tool-layer concept this reader does not model — such a path simply
// fails to resolve and the hint degrades to nothing rather than describing the
// wrong file.
func resolvePatchPath(r *Runner, rel string) (string, bool) {
	if rel == "" {
		return "", false
	}
	if r.State.SystemAccess() && filepath.IsAbs(rel) {
		abs, err := pathutil.ResolveAbsolute(filepath.Clean(rel))
		if err != nil {
			return "", false
		}
		return abs, true
	}
	resolved, err := pathutil.ResolveWithinRoots(r.State.WorkingDir, nil, rel)
	if err != nil {
		return "", false
	}
	return resolved, true
}
