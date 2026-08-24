package sidepanel

import (
	"encoding/json"
	"sort"
	"time"

	"marshal/internal/tools/registry"
)

// FileStat is one file's activity across the session.
type FileStat struct {
	Path  string
	Edits int
	Reads int
	Last  time.Time
}

// readTools are the tools whose Args carry the path of a file they read.
// Writes are identified by FilesChanged instead, which the write tools
// populate directly.
var readTools = map[string]bool{"file.read": true}

// FileStats aggregates an audit log by file path, most recently touched
// first, with alphabetical tie-breaking so the ordering is stable across
// renders (same rule as ToolStats).
//
// It is derived from the audit log rather than from internal/filetrack:
// filetrack upserts one row per path and keeps no counts, and exists for
// the read-before-write staleness check rather than for display.
func FileStats(events []registry.AuditEvent) []FileStat {
	if len(events) == 0 {
		return nil
	}
	idx := map[string]*FileStat{}
	touch := func(path string, ts time.Time) *FileStat {
		s, ok := idx[path]
		if !ok {
			s = &FileStat{Path: path}
			idx[path] = s
		}
		if ts.After(s.Last) {
			s.Last = ts
		}
		return s
	}
	for _, e := range events {
		// A failed call did not touch the file.
		if e.Error != "" {
			continue
		}
		if len(e.FilesChanged) > 0 {
			for _, p := range e.FilesChanged {
				touch(p, e.Timestamp).Edits++
			}
			continue
		}
		if !readTools[e.ToolName] || len(e.Args) == 0 {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal(e.Args, &args); err != nil {
			continue
		}
		if p, ok := args["path"].(string); ok && p != "" {
			touch(p, e.Timestamp).Reads++
		}
	}
	out := make([]FileStat, 0, len(idx))
	for _, s := range idx {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].Path < out[j].Path
	})
	if len(out) == 0 {
		return nil
	}
	return out
}
