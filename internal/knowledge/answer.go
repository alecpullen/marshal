// Package knowledge implements the three-layer knowledge tier for swarm.
package knowledge

import (
	"github.com/alecpullen/marshal/pkg/protocol"
)

// Confidence represents the confidence level of a knowledge answer.
type Confidence string

const (
	ConfidenceHigh    Confidence = "high"
	ConfidenceMedium  Confidence = "medium"
	ConfidenceLow     Confidence = "low"
	ConfidenceUnknown Confidence = "unknown"
)

// IsValid checks if the confidence level is valid.
func (c Confidence) IsValid() bool {
	switch c {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow, ConfidenceUnknown:
		return true
	}
	return false
}

// KnowledgeAnswer is the structured output from the knowledge agent.
// Non-empty citations are schema-enforced.
type KnowledgeAnswer struct {
	Answer     string                `json:"answer"`
	Citations  []protocol.ContextRef `json:"citations"`  // Required, non-empty
	Confidence Confidence            `json:"confidence"`
	Followups  []string              `json:"followups,omitempty"`
	Scope      string                `json:"scope,omitempty"` // Echo back for tracking
}

// Validate checks if the answer meets requirements.
func (a *KnowledgeAnswer) Validate() error {
	if len(a.Citations) == 0 {
		return &EnforcementError{
			Code:    "missing_citations",
			Message: "KnowledgeAnswer must include at least one citation",
		}
	}
	if !a.Confidence.IsValid() {
		return &EnforcementError{
			Code:    "invalid_confidence",
			Message: "Confidence must be high, medium, low, or unknown",
		}
	}
	return nil
}

// ShortCitations returns shortened citation references for display.
func (a *KnowledgeAnswer) ShortCitations() []string {
	var shorts []string
	for _, ref := range a.Citations {
		shorts = append(shorts, formatShortRef(ref))
	}
	return shorts
}

// FullCitations returns full citation references.
func (a *KnowledgeAnswer) FullCitations() []string {
	var fulls []string
	for _, ref := range a.Citations {
		fulls = append(fulls, string(ref))
	}
	return fulls
}

func formatShortRef(ref protocol.ContextRef) string {
	return string(ref.Kind()) + "/" + ref.Path()
}

// EnforcementError indicates a validation failure.
type EnforcementError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *EnforcementError) Error() string {
	return e.Message
}

// CacheKey uniquely identifies a cached query result.
type CacheKey struct {
	QuestionHash       string // Hash of normalized question
	CitedEntriesHash   string // Hash of sorted cited entry refs
	SearchSignature    string // Hash of top 10 search results at query time
}

// String returns a string representation for map keys.
func (k CacheKey) String() string {
	return k.QuestionHash + ":" + k.CitedEntriesHash + ":" + k.SearchSignature
}

// CacheEntry stores a cached knowledge query result.
type CacheEntry struct {
	Key              CacheKey
	Answer           KnowledgeAnswer
	InspectedRefs    []protocol.ContextRef // All entries looked at (for invalidation)
	CitedRefs        []protocol.ContextRef // Only cited entries
	TopSearchResults []protocol.ContextRef // Top 10 search results (for signature)
	Timestamp        int64                 // Unix timestamp
	Scope            string
	Query            string // Original query (for re-search on invalidation)
}

// SearchMode controls the search behavior.
type SearchMode string

const (
	SearchModeExact    SearchMode = "exact"    // BM25 only
	SearchModeFuzzy    SearchMode = "fuzzy"    // BM25 + edit distance
	SearchModeSemantic SearchMode = "semantic" // Reserved for Phase 3.75
)

// IsValid checks if the search mode is valid.
func (m SearchMode) IsValid() bool {
	switch m {
	case SearchModeExact, SearchModeFuzzy, SearchModeSemantic:
		return true
	}
	return false
}

// Scope represents the project scope for queries.
type Scope string

const (
	ScopeBackend  Scope = "backend"
	ScopeFrontend Scope = "frontend"
	ScopeDocs     Scope = "docs"
	ScopeTests    Scope = "tests"
	ScopeAll      Scope = "all"
	ScopeAuto     Scope = "auto"
)

// IsValid checks if the scope is valid.
func (s Scope) IsValid() bool {
	switch s {
	case ScopeBackend, ScopeFrontend, ScopeDocs, ScopeTests, ScopeAll, ScopeAuto:
		return true
	}
	return false
}

// ToTags converts scope to context store tags.
func (s Scope) ToTags() []string {
	switch s {
	case ScopeBackend:
		return []string{"backend", "go", "api", "server"}
	case ScopeFrontend:
		return []string{"frontend", "typescript", "react", "ui"}
	case ScopeDocs:
		return []string{"docs", "documentation", "md", "readme"}
	case ScopeTests:
		return []string{"tests", "test", "spec", "integration"}
	case ScopeAll, ScopeAuto:
		return nil // No filtering
	default:
		return []string{string(s)}
	}
}

// EnforcementConfig controls knowledge agent validation.
type EnforcementConfig struct {
	RequireCitations  bool
	MinCitations      int
	RequireConfidence bool
	MaxRetries        int
	AutoFixCitations  bool
}

// DefaultEnforcementConfig returns the default configuration.
func DefaultEnforcementConfig() EnforcementConfig {
	return EnforcementConfig{
		RequireCitations:  true,
		MinCitations:      1,
		RequireConfidence: true,
		MaxRetries:        2,
		AutoFixCitations:  true,
	}
}
