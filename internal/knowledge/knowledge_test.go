package knowledge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecpullen/marshal/pkg/protocol"
)

// TestConfidenceValidation tests confidence level validation.
func TestConfidenceValidation(t *testing.T) {
	tests := []struct {
		confidence Confidence
		wantValid  bool
	}{
		{ConfidenceHigh, true},
		{ConfidenceMedium, true},
		{ConfidenceLow, true},
		{ConfidenceUnknown, true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.confidence), func(t *testing.T) {
			if got := tt.confidence.IsValid(); got != tt.wantValid {
				t.Errorf("IsValid() = %v, want %v", got, tt.wantValid)
			}
		})
	}
}

// TestKnowledgeAnswerValidation tests answer validation.
func TestKnowledgeAnswerValidation(t *testing.T) {
	tests := []struct {
		name    string
		answer  KnowledgeAnswer
		wantErr bool
		errCode string
	}{
		{
			name: "valid answer",
			answer: KnowledgeAnswer{
				Answer:     "Test answer",
				Citations:  []protocol.ContextRef{"files/test.go@sha256:abc123"},
				Confidence: ConfidenceHigh,
			},
			wantErr: false,
		},
		{
			name: "missing citations",
			answer: KnowledgeAnswer{
				Answer:     "Test answer",
				Citations:  []protocol.ContextRef{},
				Confidence: ConfidenceHigh,
			},
			wantErr: true,
			errCode: "missing_citations",
		},
		{
			name: "invalid confidence",
			answer: KnowledgeAnswer{
				Answer:     "Test answer",
				Citations:  []protocol.ContextRef{"files/test.go@sha256:abc123"},
				Confidence: "invalid",
			},
			wantErr: true,
			errCode: "invalid_confidence",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.answer.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errCode != "" {
				if enforcementErr, ok := err.(*EnforcementError); ok {
					if enforcementErr.Code != tt.errCode {
						t.Errorf("Error code = %v, want %v", enforcementErr.Code, tt.errCode)
					}
				} else {
					t.Errorf("Expected EnforcementError, got %T", err)
				}
			}
		})
	}
}

// TestCacheKey tests cache key generation.
func TestCacheKey(t *testing.T) {
	qc := &QueryCache{}

	key1 := qc.computeKey("What is auth?", nil, "sig1")
	key2 := qc.computeKey("What is auth?", nil, "sig1")
	key3 := qc.computeKey("what is auth?", nil, "sig1")  // Different normalization
	key4 := qc.computeKey("What is auth?", nil, "sig2") // Different signature

	if key1.String() != key2.String() {
		t.Error("Same question should produce same key")
	}

	if key1.String() != key3.String() {
		t.Error("Different case should be normalized to same key")
	}

	if key1.String() == key4.String() {
		t.Error("Different signatures should produce different keys")
	}
}

// TestSearchSignatureChange tests cache invalidation on search signature change.
func TestSearchSignatureChange(t *testing.T) {
	qc := &QueryCache{}

	cached := []protocol.ContextRef{
		"files/a.go@hash1",
		"files/b.go@hash2",
		"files/c.go@hash3",
	}

	// Less than 30% change (0 changes out of 3 = 0%)
	current1 := []protocol.ContextRef{
		"files/a.go@hash1",
		"files/b.go@hash2",
		"files/c.go@hash3",
	}
	if qc.hasSearchResultsChangedSignificantly(cached, current1) {
		t.Error("No changes should not trigger invalidation")
	}

	// 33% change (1 change out of 3)
	current2 := []protocol.ContextRef{
		"files/a.go@hash1",
		"files/b.go@hash2",
		"files/d.go@hash4", // Changed
	}
	if !qc.hasSearchResultsChangedSignificantly(cached, current2) {
		t.Error("33% change should trigger invalidation (threshold is 30%)")
	}

	// 30% change exactly (on boundary)
	cached10 := make([]protocol.ContextRef, 10)
	current10 := make([]protocol.ContextRef, 10)
	for i := 0; i < 10; i++ {
		cached10[i] = protocol.ContextRef("files/a.go@hash1")
		current10[i] = protocol.ContextRef("files/a.go@hash1")
	}
	current10[3] = "files/b.go@hash2" // 1 change out of 10 = 10%
	if qc.hasSearchResultsChangedSignificantly(cached10, current10) {
		t.Error("10% change should not trigger invalidation")
	}

	// Change 4 out of 10 = 40%
	current10[4] = "files/c.go@hash3"
	current10[5] = "files/d.go@hash4"
	current10[6] = "files/e.go@hash5"
	if !qc.hasSearchResultsChangedSignificantly(cached10, current10) {
		t.Error("40% change should trigger invalidation")
	}
}

// TestScopeAutoDetection tests scope auto-detection from questions.
func TestScopeAutoDetection(t *testing.T) {
	tests := []struct {
		question string
		want     Scope
	}{
		{"How do we handle API authentication?", ScopeBackend},
		{"What's the database schema?", ScopeBackend},
		{"How do I create a React component?", ScopeFrontend},
		{"What CSS styles should I use?", ScopeFrontend},
		{"Where is the documentation?", ScopeDocs},
		{"How do I run tests?", ScopeTests},
		{"What is the meaning of life?", ScopeAll}, // Unknown -> all
	}

	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			got := AutoDetect(tt.question)
			if got != tt.want {
				t.Errorf("AutoDetect(%q) = %v, want %v", tt.question, got, tt.want)
			}
		})
	}
}

// TestScopeToTags tests scope to tags conversion.
func TestScopeToTags(t *testing.T) {
	tests := []struct {
		scope Scope
		want  int // Expected number of tags
	}{
		{ScopeBackend, 4},
		{ScopeFrontend, 4},
		{ScopeDocs, 4},
		{ScopeTests, 4},
		{ScopeAll, 0},
		{ScopeAuto, 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.scope), func(t *testing.T) {
			tags := tt.scope.ToTags()
			if len(tags) != tt.want {
				t.Errorf("ToTags() returned %d tags, want %d: %v", len(tags), tt.want, tags)
			}
		})
	}
}

// TestPersistentCache tests the L2 persistent cache.
func TestPersistentCache(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "cache.db")

	cache, err := NewPersistentCache(dbPath)
	if err != nil {
		t.Fatalf("NewPersistentCache failed: %v", err)
	}
	defer cache.Close()

	// Create a cache entry
	key := CacheKey{
		QuestionHash:     "abc123",
		CitedEntriesHash: "def456",
		SearchSignature:  "ghi789",
	}

	entry := &CacheEntry{
		Key:           key,
		Answer:        KnowledgeAnswer{Answer: "Test", Citations: []protocol.ContextRef{"ref1"}, Confidence: ConfidenceHigh},
		InspectedRefs: []protocol.ContextRef{"ref1", "ref2"},
		CitedRefs:     []protocol.ContextRef{"ref1"},
		Timestamp:     time.Now().Unix(),
		Scope:         "backend",
		Query:         "test query",
	}

	// Store
	if err := cache.Put(key, entry); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Retrieve
	retrieved, err := cache.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved.Answer.Answer != entry.Answer.Answer {
		t.Errorf("Retrieved answer = %v, want %v", retrieved.Answer.Answer, entry.Answer.Answer)
	}

	// Remove
	if err := cache.Remove(key); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// Verify removal
	_, err = cache.Get(key)
	if err == nil {
		t.Error("Expected error after removal, got nil")
	}
}

// TestFormatResults tests result formatting.
func TestFormatResults(t *testing.T) {
	results := []protocol.SearchResult{
		{
			Entry: protocol.ContextEntry{
				Ref:        "files/test.go@sha256:abc",
				Key:        protocol.NewEntryKey(protocol.EntryKindFile, "test.go"),
				SizeTokens: 100,
				Content:    []byte("package main\n\nfunc main() {}"),
			},
			Score:   5.5,
			Snippet: "func main()",
		},
		{
			Entry: protocol.ContextEntry{
				Ref:        "files/other.go@sha256:def",
				Key:        protocol.NewEntryKey(protocol.EntryKindFile, "other.go"),
				SizeTokens: 50,
				Content:    []byte("package other"),
			},
			Score: 3.2,
		},
	}

	formatted := FormatResults(results, 10)

	if formatted == "" {
		t.Error("FormatResults returned empty string")
	}

	if !contains(formatted, "Found 2 results") {
		t.Error("Expected 'Found 2 results' in output")
	}

	if !contains(formatted, "test.go") {
		t.Error("Expected 'test.go' in output")
	}

	if !contains(formatted, "5.50") && !contains(formatted, "5.5") {
		t.Error("Expected score in output")
	}
}

// TestFormatResultsEmpty tests empty results formatting.
func TestFormatResultsEmpty(t *testing.T) {
	formatted := FormatResults([]protocol.SearchResult{}, 10)
	if formatted != "No results found." {
		t.Errorf("Expected 'No results found.', got %q", formatted)
	}
}

// TestHashRefs tests reference hashing.
func TestHashRefs(t *testing.T) {
	refs := []protocol.ContextRef{
		"files/b.go@hash2",
		"files/a.go@hash1",
		"files/c.go@hash3",
	}

	hash1 := hashRefs(refs)

	// Same refs in different order should produce same hash
	refs2 := []protocol.ContextRef{
		"files/a.go@hash1",
		"files/b.go@hash2",
		"files/c.go@hash3",
	}
	hash2 := hashRefs(refs2)

	if hash1 != hash2 {
		t.Error("Hash should be order-independent")
	}

	// Different refs should produce different hash
	refs3 := []protocol.ContextRef{
		"files/a.go@hash1",
		"files/b.go@hash2",
	}
	hash3 := hashRefs(refs3)

	if hash1 == hash3 {
		t.Error("Different refs should produce different hashes")
	}
}

// TestNormalizeQuestion tests question normalization.
func TestNormalizeQuestion(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"  Hello World  ", "hello world"},
		{"UPPER CASE", "upper case"},
		{"Mixed Case", "mixed case"},
		{"  ", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeQuestion(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeQuestion(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestKnowledgeAnswerCitations tests citation formatting.
func TestKnowledgeAnswerCitations(t *testing.T) {
	answer := KnowledgeAnswer{
		Citations: []protocol.ContextRef{
			"files/test.go@sha256:abc123def456",
			"docs/readme.md@sha256:xyz789",
		},
	}

	shorts := answer.ShortCitations()
	if len(shorts) != 2 {
		t.Errorf("ShortCitations() returned %d items, want 2", len(shorts))
	}

	if shorts[0] != "files/test.go" {
		t.Errorf("First short citation = %q, want 'files/test.go'", shorts[0])
	}

	fulls := answer.FullCitations()
	if len(fulls) != 2 {
		t.Errorf("FullCitations() returned %d items, want 2", len(fulls))
	}
}

// TestCacheStats tests cache statistics.
func TestCacheStats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "cache.db")

	cache, err := NewQueryCache(100, dbPath)
	if err != nil {
		t.Fatalf("NewQueryCache failed: %v", err)
	}
	defer cache.Close()

	// Initially empty
	l1Size, l2Total, l2Avg, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if l1Size != 0 {
		t.Errorf("Initial L1 size = %d, want 0", l1Size)
	}

	if l2Total != 0 {
		t.Errorf("Initial L2 total = %d, want 0", l2Total)
	}

	if l2Avg != 0 {
		t.Errorf("Initial L2 avg = %f, want 0", l2Avg)
	}
}

// TestClearOld tests clearing old entries.
func TestClearOld(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "cache.db")

	cache, err := NewPersistentCache(dbPath)
	if err != nil {
		t.Fatalf("NewPersistentCache failed: %v", err)
	}
	defer cache.Close()

	// Add old entry
	oldKey := CacheKey{QuestionHash: "old1", CitedEntriesHash: "old1", SearchSignature: "old1"}
	oldEntry := &CacheEntry{
		Key:       oldKey,
		Timestamp: time.Now().Add(-48 * time.Hour).Unix(),
		Query:     "old query",
	}
	cache.Put(oldKey, oldEntry)

	// Add new entry
	newKey := CacheKey{QuestionHash: "new1", CitedEntriesHash: "new1", SearchSignature: "new1"}
	newEntry := &CacheEntry{
		Key:       newKey,
		Timestamp: time.Now().Unix(),
		Query:     "new query",
	}
	cache.Put(newKey, newEntry)

	// Clear entries older than 24 hours
	cleared, err := cache.ClearOld(24 * time.Hour)
	if err != nil {
		t.Fatalf("ClearOld failed: %v", err)
	}

	if cleared != 1 {
		t.Errorf("Cleared %d entries, want 1", cleared)
	}

	// Verify old entry is gone
	_, err = cache.Get(oldKey)
	if err == nil {
		t.Error("Old entry should be removed")
	}

	// Verify new entry still exists
	_, err = cache.Get(newKey)
	if err != nil {
		t.Error("New entry should still exist")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestMain ensures tests have proper setup
func TestMain(m *testing.M) {
	// Run tests
	os.Exit(m.Run())
}
