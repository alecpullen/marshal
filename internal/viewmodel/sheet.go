package viewmodel

import (
	"encoding/json"
	"sort"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/tools/registry"
)

// sheetToolAgg is one tool's aggregate while it is being built. Slowest is a
// duration here and milliseconds on the wire.
type sheetToolAgg struct {
	name    string
	calls   int
	errors  int
	slowest time.Duration
}

// SheetToolsOf aggregates a tool-call log by tool name, most-called first,
// with ties broken by name. It returns nil for an empty log.
func SheetToolsOf(events []registry.AuditEvent) []acpwire.SheetTool {
	if len(events) == 0 {
		return nil
	}
	idx := map[string]*sheetToolAgg{}
	for _, e := range events {
		s, ok := idx[e.ToolName]
		if !ok {
			s = &sheetToolAgg{name: e.ToolName}
			idx[e.ToolName] = s
		}
		s.calls++
		if e.Error != "" {
			s.errors++
		}
		if e.Duration > s.slowest {
			s.slowest = e.Duration
		}
	}
	out := make([]acpwire.SheetTool, 0, len(idx))
	for _, s := range idx {
		out = append(out, acpwire.SheetTool{
			Name:      s.name,
			Calls:     s.calls,
			Errors:    s.errors,
			SlowestMs: s.slowest.Milliseconds(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// sheetReadTools are the tools whose Args carry the path of a file they read.
// Only file.read is tracked here: other tools that happen to take a path
// argument (git.diff, git.blame, …) are not part of the working set. Writes
// are identified by FilesChanged instead, which the write tools populate
// directly.
var sheetReadTools = map[string]bool{"file.read": true}

// sheetFileAgg is one file's activity while it is being built. Last is a time
// here and Unix milliseconds on the wire.
type sheetFileAgg struct {
	path  string
	edits int
	reads int
	last  time.Time
}

// SheetFilesOf aggregates a tool-call log by file path, most recently touched
// first, with ties broken by path. A failed call touches nothing. An edit is a
// path in FilesChanged, and a read is the path argument of a file.read call.
// It returns nil when no file was touched.
func SheetFilesOf(events []registry.AuditEvent) []acpwire.SheetFile {
	if len(events) == 0 {
		return nil
	}
	idx := map[string]*sheetFileAgg{}
	touch := func(path string, ts time.Time) *sheetFileAgg {
		s, ok := idx[path]
		if !ok {
			s = &sheetFileAgg{path: path}
			idx[path] = s
		}
		if ts.After(s.last) {
			s.last = ts
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
				if p == "" {
					continue
				}
				touch(p, e.Timestamp).edits++
			}
			continue
		}
		if !sheetReadTools[e.ToolName] || len(e.Args) == 0 {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal(e.Args, &args); err != nil {
			continue
		}
		if p, ok := args["path"].(string); ok && p != "" {
			touch(p, e.Timestamp).reads++
		}
	}
	out := make([]acpwire.SheetFile, 0, len(idx))
	for _, s := range idx {
		out = append(out, acpwire.SheetFile{
			Path:   s.path,
			Edits:  s.edits,
			Reads:  s.reads,
			LastAt: acpwire.Millis(s.last),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastAt != out[j].LastAt {
			return out[i].LastAt > out[j].LastAt
		}
		return out[i].Path < out[j].Path
	})
	if len(out) == 0 {
		return nil
	}
	return out
}
