// Package context provides content-addressed storage for context entries
// with FTS5 full-text search and hybrid blob storage.
package context

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// StorageType indicates how content is stored.
type StorageType string

const (
	StorageInline     StorageType = "inline"
	StorageFileBacked StorageType = "file"
)

// Entry represents a single context entry with content-addressed storage.
type Entry struct {
	// Ref is the content-addressed reference: kind@hash
	Ref string `json:"ref"`

	// Key is the semantic key: kind:path
	Key string `json:"key"`

	// Kind is the entry kind (file, output, snippet, web, plan, etc.)
	Kind string `json:"kind"`

	// Content is populated on Get, empty on List/Search
	Content []byte `json:"-"` // Omit from JSON to avoid serialization issues

	// ContentHash is the SHA256 hex hash of the content
	ContentHash string `json:"content_hash"`

	// Size in bytes
	Size int `json:"size"`

	// SizeTokens is the estimated token count
	SizeTokens int `json:"size_tokens"`

	// ProducedBy identifies what produced this entry (tool name, command, etc.)
	ProducedBy string `json:"produced_by"`

	// ProducedAt is when the entry was created
	ProducedAt time.Time `json:"produced_at"`

	// Metadata contains additional structured data
	Metadata Metadata `json:"metadata"`

	// StorageType indicates inline vs file-backed storage
	StorageType StorageType `json:"storage_type"`

	// SessionID is the owning session
	SessionID string `json:"session_id"`

	// SupersededBy is set when this entry has been superseded
	SupersededBy string `json:"superseded_by,omitempty"`
}

// Metadata contains structured metadata for an entry.
type Metadata struct {
	// Tags for categorization
	Tags []string `json:"tags,omitempty"`

	// TTL is how long this entry should be retained
	TTL time.Duration `json:"ttl,omitempty"`

	// Source indicates the origin (file path, URL, command, etc.)
	Source string `json:"source,omitempty"`

	// Language for code entries (go, ts, etc.)
	Language string `json:"language,omitempty"`

	// LineCount for text entries
	LineCount int `json:"line_count,omitempty"`
}

// MarshalJSON serializes Metadata to JSON.
func (m Metadata) MarshalJSON() ([]byte, error) {
	type Alias Metadata
	return json.Marshal(&struct {
		TTLSeconds int64 `json:"ttl_seconds,omitempty"`
		*Alias
	}{
		TTLSeconds: int64(m.TTL.Seconds()),
		Alias:      (*Alias)(&m),
	})
}

// UnmarshalJSON deserializes Metadata from JSON.
func (m *Metadata) UnmarshalJSON(data []byte) error {
	type Alias Metadata
	aux := &struct {
		TTLSeconds int64 `json:"ttl_seconds,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	m.TTL = time.Duration(aux.TTLSeconds) * time.Second
	return nil
}

// ListQuery filters for listing entries.
type ListQuery struct {
	// Kinds filter by entry kinds
	Kinds []string

	// Tags filter by tags (all must match)
	Tags []string

	// PathPrefix filters by path prefix (for file entries)
	PathPrefix string

	// ProducedBy filters by producer
	ProducedBy string

	// LatestOnly excludes superseded entries
	LatestOnly bool

	// Limit for pagination
	Limit int

	// Offset for pagination
	Offset int

	// SessionID restricts to a specific session
	SessionID string
}

// SearchQuery filters for full-text search.
type SearchQuery struct {
	// Query is the FTS5 search query
	Query string

	// Kinds filter by entry kinds
	Kinds []string

	// Tags filter by tags
	Tags []string

	// PathGlob filters by path pattern
	PathGlob string

	// ProducedBy filters by producer
	ProducedBy string

	// LatestOnly excludes superseded entries
	LatestOnly bool

	// Limit for pagination
	Limit int
}

// SearchResult represents a single search hit with ranking info.
type SearchResult struct {
	Entry   Entry   `json:"entry"`
	Score   float64 `json:"score"`   // BM25 ranking score (lower is better)
	Snippet string  `json:"snippet"` // Highlighted excerpt
}

// EntryKey creates a semantic key from kind and path.
func EntryKey(kind, path string) string {
	return fmt.Sprintf("%s:%s", kind, path)
}

// ParseEntryKey splits a key into kind and path.
func ParseEntryKey(key string) (kind, path string, err error) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid entry key: %s", key)
	}
	return parts[0], parts[1], nil
}

// ComputeHash computes the SHA256 hash of content.
func ComputeHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// ShortHash returns the first 16 characters of a hash.
func ShortHash(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// FormatContextRef creates a context reference from kind, path, and hash.
func FormatContextRef(kind, path, hash string) string {
	key := EntryKey(kind, path)
	return fmt.Sprintf("%s@%s", key, hash)
}

// ParseContextRef splits a context reference into components.
func ParseContextRef(ref string) (kind, path, hash string, err error) {
	atIdx := strings.LastIndex(ref, "@")
	if atIdx == -1 {
		return "", "", "", fmt.Errorf("invalid context ref (missing @): %s", ref)
	}

	hash = ref[atIdx+1:]
	key := ref[:atIdx]

	kind, path, err = ParseEntryKey(key)
	if err != nil {
		return "", "", "", err
	}

	return kind, path, hash, nil
}

// MakeBlobPath returns the file path for a blob.
func MakeBlobPath(blobDir, hash string) string {
	if len(hash) < 2 {
		return filepath.Join(blobDir, hash[:1], hash+".blob")
	}
	return filepath.Join(blobDir, hash[:2], hash+".blob")
}

// ParseBlobPath extracts the hash from a blob file path.
func ParseBlobPath(blobPath string) (hash string, err error) {
	base := filepath.Base(blobPath)
	if !strings.HasSuffix(base, ".blob") {
		return "", fmt.Errorf("invalid blob path (missing .blob extension): %s", blobPath)
	}
	return strings.TrimSuffix(base, ".blob"), nil
}

// ContentMatchesHash verifies content matches a hash.
func ContentMatchesHash(content []byte, hash string) bool {
	return ComputeHash(content) == hash
}

// ShouldStoreInline determines if content should be stored inline.
func ShouldStoreInline(content []byte, threshold int) bool {
	return len(content) <= threshold
}

// ShouldIndex determines if content should be indexed in FTS5.
func ShouldIndex(content []byte, limit int) bool {
	return len(content) <= limit
}

// EstimateTokens provides a rough token count estimate.
// Uses a simple heuristic: average 4 bytes per token for English/code.
func EstimateTokens(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	// Conservative estimate: 4 bytes per token
	return (len(content) + 3) / 4
}

// String returns a human-readable string representation of an entry.
func (e Entry) String() string {
	ref := e.Ref
	if len(ref) > 40 {
		ref = ref[:40] + "..."
	}
	return fmt.Sprintf("Entry{ref=%s, kind=%s, size=%d, tokens=%d, storage=%s}",
		ref, e.Kind, e.Size, e.SizeTokens, e.StorageType)
}

// IsSuperseded returns true if this entry has been superseded.
func (e Entry) IsSuperseded() bool {
	return e.SupersededBy != ""
}

// ExpiredAt returns the expiration time for an entry, or zero if no TTL.
func (e Entry) ExpiredAt() time.Time {
	if e.Metadata.TTL == 0 {
		return time.Time{}
	}
	return e.ProducedAt.Add(e.Metadata.TTL)
}

// IsExpired returns true if the entry has expired.
func (e Entry) IsExpired() bool {
	if e.Metadata.TTL == 0 {
		return false
	}
	return time.Now().After(e.ExpiredAt())
}
