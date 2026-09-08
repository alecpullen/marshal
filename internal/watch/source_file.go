package watch

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// sampleFile polls the watch's path (a file path or glob) and returns a
// Sample describing the current match set. Exists is true when at least one
// path matches. Hash is an FNV-1a hash over the concatenated
// (name, size, modtime) of every match.
//
// The modtime+size cheap check is a deliberate v1 decision: detecting change
// by content hash would require reading every file each tick, which is
// expensive for large trees. Modtime+size catches the overwhelming majority
// of real changes (edits, creation, deletion, truncation) without reading
// file contents. A file whose content changes without its size or modtime
// changing is not detected — an accepted v1 limitation.
func (s sourceSampler) sampleFile(ctx context.Context, w *watch) (Sample, error) {
	if w.path == "" {
		return Sample{}, fmt.Errorf("file watch %q: path is required", w.name)
	}
	matches, err := filepath.Glob(w.path)
	if err != nil {
		return Sample{}, fmt.Errorf("file watch %q: bad glob %q: %v", w.name, w.path, err)
	}
	if len(matches) == 0 {
		return Sample{Exists: false, Hash: ""}, nil
	}
	// Sort for a stable hash regardless of glob order.
	sort.Strings(matches)
	h := fnv.New64a()
	for _, name := range matches {
		info, err := os.Stat(name)
		if err != nil {
			// A match that vanished between glob and stat is treated as
			// absent; skip it rather than erroring the whole sample.
			continue
		}
		h.Write([]byte(name))
		h.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		h.Write([]byte(info.ModTime().UTC().Format("20060102150405.000000000")))
	}
	return Sample{Exists: true, Hash: fmt.Sprintf("%x", h.Sum64())}, nil
}
