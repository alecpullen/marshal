package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zeebo/blake3"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// MaxFileSizeForHash is the size threshold for using BLAKE3 vs mtime heuristic.
const MaxFileSizeForHash = 1 << 20 // 1MB

// ReadSet tracks files read by the agent for staleness protection.
type ReadSet struct {
	mu      sync.RWMutex
	entries map[string]*ReadRecord // path -> record
}

// ReadRecord tracks when and how a file was read.
type ReadRecord struct {
	Path     string
	Hash     string    // BLAKE3 hash of content at read time
	Mtime    time.Time // Modification time (for large files)
	Size     int64     // File size (for large files)
	ReadAt   time.Time
	ReadVia  string    // Tool name (e.g., "read_file", "ctx_fetch")
	LineHint int       // Line number hint if partial read
}

// NewReadSet creates an empty read set.
func NewReadSet() *ReadSet {
	return &ReadSet{
		entries: make(map[string]*ReadRecord),
	}
}

// RecordRead records that a file was read.
func (rs *ReadSet) RecordRead(path string, hash string, readVia string) {
	// For new files or files we don't have, just record the hash
	rs.mu.Lock()
	defer rs.mu.Unlock()

	info, err := os.Stat(path)
	var mtime time.Time
	var size int64
	if err == nil {
		mtime = info.ModTime()
		size = info.Size()
	}

	rs.entries[path] = &ReadRecord{
		Path:    path,
		Hash:    hash,
		Mtime:   mtime,
		Size:    size,
		ReadAt:  time.Now(),
		ReadVia: readVia,
	}
}

// RecordReadFromContent records a read with content bytes.
func (rs *ReadSet) RecordReadFromContent(path string, content []byte, readVia string) {
	hash := hashBytes(content)
	rs.RecordRead(path, hash, readVia)
}

// RecordReadWithRef records a read from a context reference.
func (rs *ReadSet) RecordReadWithRef(path string, ref protocol.ContextRef, readVia string) {
	// Extract hash from ContextRef
	hash := extractHashFromRef(ref)
	rs.RecordRead(path, hash, readVia)
}

// HasRead returns true if the file has been read.
func (rs *ReadSet) HasRead(path string) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	_, ok := rs.entries[path]
	return ok
}

// GetHash returns the expected hash for a file.
func (rs *ReadSet) GetHash(path string) (string, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	record, ok := rs.entries[path]
	if !ok {
		return "", false
	}
	return record.Hash, true
}

// VerifyHash checks if content matches the expected hash.
func (rs *ReadSet) VerifyHash(path string, content []byte) error {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	record, ok := rs.entries[path]
	if !ok {
		return fmt.Errorf("file %s was never read", path)
	}

	actualHash := hashBytes(content)

	if record.Hash != actualHash {
		return &StalenessError{
			Path:         path,
			ExpectedHash: record.Hash,
			ActualHash:   actualHash,
			ReadAt:       record.ReadAt,
		}
	}
	return nil
}

// VerifyCurrent checks the current file state against the read-set.
func (rs *ReadSet) VerifyCurrent(path string) error {
	rs.mu.RLock()
	record, ok := rs.entries[path]
	rs.mu.RUnlock()

	if !ok {
		return fmt.Errorf("file %s was never read", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", path, err)
	}

	// For large files, use mtime+size heuristic
	if info.Size() > MaxFileSizeForHash {
		if info.ModTime() != record.Mtime || info.Size() != record.Size {
			return &StalenessError{
				Path:         path,
				ExpectedHash: record.Hash,
				ActualHash:   "<mtime/size changed>",
				ReadAt:       record.ReadAt,
			}
		}
		return nil
	}

	// For small files, verify hash
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}

	return rs.VerifyHash(path, content)
}

// AllPaths returns all paths that have been read.
func (rs *ReadSet) AllPaths() []string {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	paths := make([]string, 0, len(rs.entries))
	for path := range rs.entries {
		paths = append(paths, path)
	}
	return paths
}

// Size returns the number of tracked files.
func (rs *ReadSet) Size() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.entries)
}

// Remove removes a path from the read-set (e.g., after deletion).
func (rs *ReadSet) Remove(path string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.entries, path)
}

// Clear removes all entries.
func (rs *ReadSet) Clear() {
	rs.mu.Lock()
	defer rs.mu.RUnlock()
	rs.entries = make(map[string]*ReadRecord)
}

// Snapshot creates a copy of the current read-set state.
func (rs *ReadSet) Snapshot() map[string]ReadRecord {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	snapshot := make(map[string]ReadRecord, len(rs.entries))
	for path, record := range rs.entries {
		snapshot[path] = *record
	}
	return snapshot
}

// StalenessError indicates a file changed since it was read.
type StalenessError struct {
	Path         string
	ExpectedHash string
	ActualHash   string
	ReadAt       time.Time
}

func (e *StalenessError) Error() string {
	return fmt.Sprintf(
		"file %s changed since read at %s (expected hash: %s..., actual: %s...)",
		e.Path,
		e.ReadAt.Format(time.RFC3339),
		truncateHash(e.ExpectedHash),
		truncateHash(e.ActualHash),
	)
}

// CanRebase returns true if the agent can safely rebase (re-read) the file.
func (e *StalenessError) CanRebase() bool {
	// In Phase 3, all staleness can be resolved by re-reading
	return true
}

// IsStalenessError checks if an error is a staleness error.
func IsStalenessError(err error) bool {
	_, ok := err.(*StalenessError)
	return ok
}

// Helper functions
func hashBytes(content []byte) string {
	hash := blake3.Sum256(content)
	return fmt.Sprintf("%x", hash)
}

func extractHashFromRef(ref protocol.ContextRef) string {
	// ContextRef format: <kind>/<path>@sha256:<hash> or <kind>/<path>@blake3:<hash>
	parts := strings.Split(string(ref), "@")
	if len(parts) >= 2 {
		hashPart := parts[len(parts)-1]
		// Remove prefix if present
		if idx := strings.Index(hashPart, ":"); idx != -1 {
			return hashPart[idx+1:]
		}
		return hashPart
	}
	return ""
}

func truncateHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// IsSafePath checks if a path is safe (doesn't escape repo root).
func IsSafePath(repoRoot, path string) bool {
	absPath, err := filepath.Abs(filepath.Join(repoRoot, filepath.Clean(path)))
	if err != nil {
		return false
	}
	return strings.HasPrefix(absPath, repoRoot)
}
