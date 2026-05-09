package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// EntryKind categorizes the type of a context entry.
type EntryKind string

const (
	EntryKindFile         EntryKind = "files"
	EntryKindDirectory    EntryKind = "directories"
	EntryKindSymbol       EntryKind = "symbols"
	EntryKindOutput       EntryKind = "outputs"
	EntryKindPlan         EntryKind = "plans"
	EntryKindSearchResult EntryKind = "search_results"
	EntryKindUserInput    EntryKind = "user_inputs"
	EntryKindSummary      EntryKind = "summaries"
	EntryKindTestResult   EntryKind = "test_results"
)

// IsValid reports whether the EntryKind is a known value.
func (k EntryKind) IsValid() bool {
	switch k {
	case EntryKindFile, EntryKindDirectory, EntryKindSymbol, EntryKindOutput,
		EntryKindPlan, EntryKindSearchResult, EntryKindUserInput, EntryKindSummary, EntryKindTestResult:
		return true
	}
	return false
}

// String returns the string representation of the EntryKind.
func (k EntryKind) String() string {
	return string(k)
}

// ContextRef is a content-addressed reference to a context entry.
// Format: <kind>/<path-or-id>@sha256:<hex>
type ContextRef string

// ParseContextRef parses a context reference string.
func ParseContextRef(s string) (ContextRef, error) {
	if !strings.Contains(s, "@sha256:") {
		return "", fmt.Errorf("invalid context ref format: missing sha256 prefix")
	}
	parts := strings.Split(s, "@sha256:")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid context ref format: multiple sha256 prefixes")
	}
	prefix := parts[0] // <kind>/<path-or-id>
	hash := parts[1]   // <hex>

	if !strings.Contains(prefix, "/") {
		return "", fmt.Errorf("invalid context ref format: missing kind prefix")
	}

	kind := EntryKind(strings.Split(prefix, "/")[0])
	if !kind.IsValid() {
		return "", fmt.Errorf("invalid context ref format: unknown kind %q", kind)
	}

	if len(hash) != 64 {
		return "", fmt.Errorf("invalid context ref format: sha256 hash must be 64 hex chars")
	}

	_, err := hex.DecodeString(hash)
	if err != nil {
		return "", fmt.Errorf("invalid context ref format: sha256 hash not valid hex: %w", err)
	}

	return ContextRef(s), nil
}

// NewContextRef creates a new ContextRef from kind, path, and content.
func NewContextRef(kind EntryKind, path string, content []byte) ContextRef {
	hash := sha256.Sum256(content)
	hashStr := hex.EncodeToString(hash[:])
	return ContextRef(fmt.Sprintf("%s/%s@sha256:%s", kind, path, hashStr))
}

// Kind extracts the EntryKind from the reference.
func (r ContextRef) Kind() EntryKind {
	s := string(r)
	idx := strings.Index(s, "/")
	if idx == -1 {
		return ""
	}
	return EntryKind(s[:idx])
}

// Path extracts the path-or-id portion from the reference.
func (r ContextRef) Path() string {
	s := string(r)
	start := strings.Index(s, "/")
	if start == -1 {
		return ""
	}
	end := strings.Index(s, "@sha256:")
	if end == -1 {
		return s[start+1:]
	}
	return s[start+1 : end]
}

// Hash extracts the sha256 hash portion from the reference.
func (r ContextRef) Hash() string {
	s := string(r)
	idx := strings.Index(s, "@sha256:")
	if idx == -1 {
		return ""
	}
	return s[idx+len("@sha256:"):]
}

// String returns the string representation.
func (r ContextRef) String() string {
	return string(r)
}

// IsValid checks if the reference is well-formed.
func (r ContextRef) IsValid() bool {
	_, err := ParseContextRef(string(r))
	return err == nil
}

// MatchesContent verifies that the given content matches the reference hash.
func (r ContextRef) MatchesContent(content []byte) bool {
	expectedHash := sha256.Sum256(content)
	expectedStr := hex.EncodeToString(expectedHash[:])
	return r.Hash() == expectedStr
}

// EntryKey is a human-readable key for context entries.
// Format: <kind>/<path-or-id>
type EntryKey string

// NewEntryKey creates a new EntryKey.
func NewEntryKey(kind EntryKind, path string) EntryKey {
	return EntryKey(fmt.Sprintf("%s/%s", kind, path))
}

// Kind extracts the EntryKind from the key.
func (k EntryKey) Kind() EntryKind {
	s := string(k)
	idx := strings.Index(s, "/")
	if idx == -1 {
		return ""
	}
	return EntryKind(s[:idx])
}

// Path extracts the path portion from the key.
func (k EntryKey) Path() string {
	s := string(k)
	idx := strings.Index(s, "/")
	if idx == -1 {
		return ""
	}
	return s[idx+1:]
}

// String returns the string representation.
func (k EntryKey) String() string {
	return string(k)
}

// ContextEntry represents a stored context item.
type ContextEntry struct {
	// Ref is the content-addressed reference (primary key).
	Ref ContextRef `json:"ref"`

	// Key is the human-readable lookup key.
	Key EntryKey `json:"key"`

	// Kind categorizes the entry type.
	Kind EntryKind `json:"kind"`

	// Size in bytes.
	Size int `json:"size"`

	// SizeTokens is an estimate of token count.
	SizeTokens int `json:"size_tokens"`

	// Content is the actual content (may be omitted in listings).
	Content []byte `json:"content,omitempty"`

	// ContentHash is the sha256 hash (redundant with Ref, but convenient).
	ContentHash string `json:"content_hash"`

	// ProducedBy identifies the agent/tool that created this entry.
	ProducedBy string `json:"produced_by"`

	// ProducedAt is the creation timestamp.
	ProducedAt time.Time `json:"produced_at"`

	// Metadata contains additional structured metadata.
	Metadata EntryMetadata `json:"metadata"`
}

// EntryMetadata contains optional metadata for context entries.
type EntryMetadata struct {
	// Tags for filtering and categorization.
	Tags []string `json:"tags,omitempty"`

	// TTL after which the entry may be garbage collected.
	TTL time.Duration `json:"ttl,omitempty"`

	// SupersededBy references a newer version of this entry.
	SupersededBy ContextRef `json:"superseded_by,omitempty"`

	// Source indicates where this entry came from (e.g., "read_file", "tool_output").
	Source string `json:"source,omitempty"`

	// Language for code entries (e.g., "go", "typescript").
	Language string `json:"language,omitempty"`

	// LineCount for file entries.
	LineCount int `json:"line_count,omitempty"`

	// Scope indicates the project scope (backend, frontend, docs, tests).
	Scope string `json:"scope,omitempty"`
}

// IsSuperseded returns true if this entry has been superseded.
func (e ContextEntry) IsSuperseded() bool {
	return e.Metadata.SupersededBy != ""
}

// Supersede creates a new ContextRef that supersedes this entry.
func (e ContextEntry) Supersede(newContent []byte) ContextEntry {
	newRef := NewContextRef(e.Kind, e.Key.Path(), newContent)
	return ContextEntry{
		Ref:         newRef,
		Key:         e.Key,
		Kind:        e.Kind,
		Size:        len(newContent),
		SizeTokens:  EstimateTokens(newContent),
		Content:     newContent,
		ContentHash: newRef.Hash(),
		ProducedBy:  e.ProducedBy,
		ProducedAt:  time.Now().UTC(),
		Metadata: EntryMetadata{
			Tags:         e.Metadata.Tags,
			Source:       e.Metadata.Source,
			Language:     e.Metadata.Language,
			LineCount:    strings.Count(string(newContent), "\n"),
			SupersededBy: "", // This is the new version
		},
	}
}

// EstimateTokens provides a rough token count estimate.
// This is a simple heuristic (~4 chars/token). For accurate counts,
// use a proper tokenizer.
func EstimateTokens(content []byte) int {
	return len(content) / 4
}

// --- Query types for context store ---

// ListQuery filters entries for listing.
type ListQuery struct {
	Kinds      []EntryKind `json:"kinds,omitempty"`
	Tags       []string    `json:"tags,omitempty"`
	PathPrefix string      `json:"path_prefix,omitempty"`
	ProducedBy string      `json:"produced_by,omitempty"`
	Limit      int         `json:"limit,omitempty"`
	Offset     int         `json:"offset,omitempty"`
	// LatestOnly excludes superseded entries.
	LatestOnly bool `json:"latest_only,omitempty"`
}

// SearchQuery performs full-text search over context entries.
type SearchQuery struct {
	// Query is the search text (BM25-ranked).
	Query string `json:"query"`

	// Kinds restricts search to specific entry types.
	Kinds []EntryKind `json:"kinds,omitempty"`

	// Tags filters by tags.
	Tags []string `json:"tags,omitempty"`

	// PathGlob filters by path pattern (e.g., "*.go", "internal/*").
	PathGlob string `json:"path_glob,omitempty"`

	// ProducedBy filters by producer agent.
	ProducedBy string `json:"produced_by,omitempty"`

	// LatestOnly excludes superseded entries.
	LatestOnly bool `json:"latest_only,omitempty"`

	// Scope filters by project scope (backend, frontend, docs, tests).
	Scope string `json:"scope,omitempty"`

	// Limit on results (default: 10, max: 100).
	Limit int `json:"limit,omitempty"`
}

// SearchResult represents a single search result.
type SearchResult struct {
	Entry   ContextEntry `json:"entry"`
	Score   float64      `json:"score"`   // BM25 relevance score
	Snippet string       `json:"snippet"` // Highlighted excerpt
}

// --- Helper functions ---

// ComputeHash computes the sha256 hash of content.
func ComputeHash(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

// ContentMatchesHash verifies content against a hash string.
func ContentMatchesHash(content []byte, hash string) bool {
	return ComputeHash(content) == hash
}
