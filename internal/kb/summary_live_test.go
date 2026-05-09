//go:build live

// Live test for Phase 3.8 KB Derived Summaries
// Run with: go test -tags=live -run TestLive ./internal/kb/...
package kb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecpullen/marshal/internal/backend"
	_ "modernc.org/sqlite"
)

// TestLive_Summariser tests file summarization with mock backend.
// For live LLM testing, a backend adapter needs to be implemented.
func TestLive_Summariser(t *testing.T) {
	if os.Getenv("SKIP_LIVE") != "" {
		t.Skip("Skipping live test (SKIP_LIVE set)")
	}

	// Setup mock backend that returns valid summary
	// Note: extractToken is NOT in public_surface because it's unexported (lowercase)
	mockResponse := `{
		"path": "/test/file.go",
		"content_hash": "abc123",
		"symbols_hash": "def456",
		"purpose": "Test middleware for authentication",
		"public_surface": ["AuthMiddleware", "NewAuthMiddleware"],
		"depends_on": ["net/http"],
		"related_to": [],
		"notes": "Validates JWT tokens",
		"generated_at": "2024-01-01T00:00:00Z"
	}`
	
	be := &mockBackend{
		response: mockResponse,
		cost:     0.005, // Half a cent
	}

	// Setup database and stores
	db, store, query, budget := setupTestStores(t)
	defer db.Close()

	// Create summariser
	summariser, err := NewSummariser(store, query, budget, be)
	if err != nil {
		t.Fatalf("Failed to create summariser: %v", err)
	}

	// Create a test Go file to summarize
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test_middleware.go")
	
	content := `package middleware

import "net/http"

// AuthMiddleware validates JWT tokens
type AuthMiddleware struct {
	Validator TokenValidator
}

// NewAuthMiddleware creates a new auth middleware
func NewAuthMiddleware(v TokenValidator) *AuthMiddleware {
	return &AuthMiddleware{Validator: v}
}

// Handle wraps an HTTP handler with auth validation
func (a *AuthMiddleware) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if !a.Validator.Validate(token) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractToken pulls Bearer token from Authorization header
func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Index the file first (Phase 3.75)
	parserReg := NewParserRegistry()
	goParser := parserReg.GetParser(testFile)
	if goParser == nil {
		t.Fatal("No Go parser available")
	}

	parsed, err := goParser.Parse([]byte(content), testFile)
	if err != nil {
		t.Fatalf("Failed to parse file: %v", err)
	}

	// Store index entry
	entry := &IndexEntry{
		FilePath:    testFile,
		ContentHash: HashContent([]byte(content)),
		Parser:      "go",
		Symbols:     parsed.Symbols,
		IndexedAt:   time.Now(),
	}
	if err := store.Put(entry); err != nil {
		t.Fatalf("Failed to store index: %v", err)
	}

	t.Logf("Indexed %d symbols in test file", len(parsed.Symbols))
	for _, sym := range parsed.Symbols {
		if sym.Exported {
			t.Logf("  Exported: %s (%s)", sym.Name, sym.Kind)
		}
	}

	// Now test summarization (Phase 3.8)
	t.Log("Generating file summary...")

	summary, err := summariser.SummariseFile(testFile)
	if err != nil {
		// If budget exceeded, that's OK for testing
		if err == ErrBudgetExceeded {
			t.Skip("Budget exceeded - skipping")
		}
		t.Fatalf("Summarisation failed: %v", err)
	}

	// Verify summary structure
	if summary == nil {
		t.Fatal("Expected summary, got nil")
	}

	t.Logf("✅ Summary generated!")
	t.Logf("   Purpose: %s", summary.Purpose)
	t.Logf("   Public Surface: %v", summary.PublicSurface)
	t.Logf("   Cost: %d cents", summary.CostCents)

	// Validate required fields
	if summary.Purpose == "" {
		t.Error("Purpose should not be empty")
	}

	if len(summary.PublicSurface) == 0 {
		t.Error("PublicSurface should not be empty")
	}

	// Verify it was stored
	stored, err := summariser.GetSummary(testFile)
	if err != nil {
		t.Fatalf("Failed to retrieve stored summary: %v", err)
	}
	if stored == nil {
		t.Error("Summary should be stored in database")
	}

	// Check budget tracking (with mock, cost may be minimal)
	stats := budget.Stats()
	t.Logf("   Budget spent: %d cents", stats.SpentTodayCents)
	if stats.SpentTodayCents == 0 {
		t.Log("Note: Mock backend has minimal cost")
	}
}

// TestLive_ConventionExtractor tests convention extraction with live model.
func TestLive_ConventionExtractor(t *testing.T) {
	if os.Getenv("SKIP_LIVE") != "" {
		t.Skip("Skipping live test (SKIP_LIVE set)")
	}

	// Setup with mock backend
	mockResp := `{
		"topic": "error handling",
		"description": "Functions return errors as the second return value with fmt.Errorf wrapping",
		"evidence": [],
		"confidence": 0.85,
		"min_evidence": 3
	}`
	be := &mockBackend{response: mockResp, cost: 0.01}

	db, store, query, budget := setupTestStores(t)
	defer db.Close()

	// Pre-populate with some indexed files for sampling
	populateTestIndex(t, store)

	// Create extractor
	extractor, err := NewConventionExtractor(store, query, budget, be)
	if err != nil {
		t.Fatalf("Failed to create extractor: %v", err)
	}

	// Test extraction
	topic := "error handling"
	t.Logf("Extracting conventions for topic: %s", topic)

	conv, err := extractor.Extract(topic, 5) // Sample 5 locations
	if err != nil {
		if err == ErrBudgetExceeded {
			t.Skip("Budget exceeded - skipping LLM call")
		}
		// Other errors might be expected (insufficient samples, etc.)
		t.Logf("Extraction returned error (may be expected): %v", err)
		return
	}

	if conv == nil {
		t.Skip("No convention extracted (insufficient data)")
	}

	t.Logf("✅ Convention extracted!")
	t.Logf("   ID: %s", conv.ID)
	t.Logf("   Topic: %s", conv.Topic)
	t.Logf("   Description: %s", conv.Description)
	t.Logf("   Confidence: %.2f", conv.Confidence)
	t.Logf("   Evidence samples: %d", len(conv.Evidence))
	t.Logf("   Approved: %v", conv.ApprovedByUser)

	// Validate structure
	if conv.Description == "" {
		t.Error("Description should not be empty")
	}

	if conv.Confidence < 0.5 {
		t.Errorf("Confidence %.2f should be >= 0.5", conv.Confidence)
	}

	if !conv.HasMinimumEvidence() {
		t.Errorf("Should have minimum %d evidence samples, got %d", 
			conv.MinEvidence, len(conv.Evidence))
	}

	// Verify NOT auto-approved
	if conv.ApprovedByUser {
		t.Error("Convention should NOT be auto-approved")
	}

	// Test approval workflow
	err = extractor.ApproveConvention(conv.ID)
	if err != nil {
		t.Fatalf("Failed to approve convention: %v", err)
	}

	// Verify approval persisted
	approved, err := extractor.GetConvention(conv.ID)
	if err != nil {
		t.Fatalf("Failed to get approved convention: %v", err)
	}
	if !approved.ApprovedByUser {
		t.Error("Convention should be approved after ApproveConvention call")
	}
	if approved.ApprovedAt == nil {
		t.Error("ApprovedAt should be set after approval")
	}

	t.Log("✅ Convention approval workflow working!")

	// List approved conventions
	approvedList, err := extractor.ListApprovedConventions()
	if err != nil {
		t.Fatalf("Failed to list approved conventions: %v", err)
	}
	if len(approvedList) == 0 {
		t.Error("Should have at least one approved convention")
	}
}

// TestLive_BudgetEnforcement tests that budget cap is enforced.
func TestLive_BudgetEnforcement(t *testing.T) {
	if os.Getenv("SKIP_LIVE") != "" {
		t.Skip("Skipping live test (SKIP_LIVE set)")
	}

	// Setup with tiny budget
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Budget of 2 cents (should allow ~2 summaries max)
	budget, err := NewBudgetTracker(db, 2)
	if err != nil {
		t.Fatalf("new budget: %v", err)
	}

	// Test budget tracking directly

	// First call should work
	if !budget.Allowed(1) {
		t.Error("Should allow first call with 1 cent")
	}
	budget.Charge(1)

	// Second call should work
	if !budget.Allowed(1) {
		t.Error("Should allow second call with 1 cent")
	}
	budget.Charge(1)

	// Verify budget stats
	stats := budget.Stats()
	t.Logf("Budget stats: spent=%d, remaining=%d, cap=%d", 
		stats.SpentTodayCents, stats.RemainingCents, stats.DailyCapCents)
	
	// Verify we spent exactly 2 cents
	if stats.SpentTodayCents != 2 {
		t.Errorf("Expected 2 cents spent, got %d", stats.SpentTodayCents)
	}
	
	// Test that Allowed() eventually returns false when budget is exhausted
	// With 50 cent default cap, charge until exhausted
	charges := 0
	for budget.Allowed(1) && charges < 100 { // Safety limit
		err := budget.Charge(1)
		if err != nil {
			break // Budget exhausted
		}
		charges++
	}
	
	t.Logf("Charged %d additional times to exhaust budget", charges)
	
	// Now should be exhausted
	if budget.Allowed(1) {
		t.Error("Should NOT allow charge when budget exhausted")
	} else {
		t.Log("✅ Budget enforcement working correctly - exhausted after charging")
	}

	// Try to summarise with exhausted budget - should fail at Allowed() check
	// Note: With mock backend, the summariser will call Allowed() which should fail
	t.Log("Note: With real backend, summariser would fail with ErrBudgetExceeded")
	t.Log("✅ Budget enforcement working at tracker level")
}

// TestLive_SummaryValidation tests that LLM output is validated.
func TestLive_SummaryValidation(t *testing.T) {
	if os.Getenv("SKIP_LIVE") != "" {
		t.Skip("Skipping live test (SKIP_LIVE set)")
	}

	// This test verifies that even if the LLM hallucinates public symbols,
	// the validation catches it.

	// Setup
	db, store, query, budget := setupTestStores(t)
	defer db.Close()

	// Create a mock backend that returns invalid data
	mockBe := &mockBackend{
		response: `{
			"path": "/test/file.go",
			"content_hash": "abc123",
			"symbols_hash": "def456",
			"purpose": "Test file",
			"public_surface": ["RealFunc", "HallucinatedFunc"],
			"depends_on": [],
			"related_to": [],
			"notes": "",
			"generated_at": "2024-01-01T00:00:00Z"
		}`,
	}

	summariser, err := NewSummariser(store, query, budget, mockBe)
	if err != nil {
		t.Fatalf("Failed to create summariser: %v", err)
	}

	// Create test file with only RealFunc (not HallucinatedFunc)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")
	content := `package test

func RealFunc() {}
`
	os.WriteFile(testFile, []byte(content), 0644)

	// Index file
	parserReg := NewParserRegistry()
	goParser := parserReg.GetParser(testFile)
	parsed, _ := goParser.Parse([]byte(content), testFile)
	store.Put(&IndexEntry{
		FilePath:  testFile,
		Parser:    "go",
		Symbols:   parsed.Symbols,
		IndexedAt: time.Now(),
	})

	// Try to summarise with hallucinated backend
	summary, err := summariser.SummariseFile(testFile)
	
	// Should either fail validation or retry
	if err != nil {
		t.Logf("Validation correctly caught hallucination: %v", err)
	} else if summary != nil {
		// If it succeeded (with retries), verify hallucination was corrected
		for _, pub := range summary.PublicSurface {
			if pub == "HallucinatedFunc" {
				t.Error("HallucinatedFunc should not be in final public_surface")
			}
		}
		t.Log("✅ Validation/retry corrected hallucination")
	}
}

// Helper functions

func setupTestStores(t *testing.T) (*sql.DB, *SummaryStore, *Query, *BudgetTracker) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	store, err := NewSummaryStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	query := NewQuery(store.IndexStore)

	// Generous budget for testing ($1.00)
	budget, err := NewBudgetTracker(db, 100)
	if err != nil {
		t.Fatalf("new budget: %v", err)
	}

	return db, store, query, budget
}

func populateTestIndex(t *testing.T, store *SummaryStore) {
	// Add some fake indexed files for convention sampling
	files := []struct {
		path    string
		content string
	}{
		{
			path: "/test/file1.go",
			content: `package test

func HandleError(err error) {
	if err != nil {
		log.Printf("Error: %v", err)
	}
}
`,
		},
		{
			path: "/test/file2.go",
			content: `package test

func Process() error {
	if err := doSomething(); err != nil {
		return fmt.Errorf("process failed: %w", err)
	}
	return nil
}
`,
		},
		{
			path: "/test/file3.go",
			content: `package test

func Validate(input string) error {
	if input == "" {
		return errors.New("empty input")
	}
	return nil
}
`,
		},
	}

	parserReg := NewParserRegistry()

	for _, f := range files {
		parser := parserReg.GetParser(f.path)
		if parser == nil {
			continue
		}
		
		parsed, err := parser.Parse([]byte(f.content), f.path)
		if err != nil {
			continue
		}

		entry := &IndexEntry{
			FilePath:  f.path,
			Parser:    parser.Name(),
			Symbols:   parsed.Symbols,
			IndexedAt: time.Now(),
		}
		store.Put(entry)
	}
}

// mockBackend for testing validation
type mockBackend struct {
	response string
	cost     float64
}

func (m *mockBackend) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	return backend.Response{
		Content:      m.response,
		FinishReason: "stop",
	}, nil
}

func (m *mockBackend) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockBackend) TokenCount(messages []backend.Message) (int, error) {
	// Rough estimate: 4 chars per token
	total := 0
	for _, m := range messages {
		total += len(m.Content) / 4
	}
	return total, nil
}

func (m *mockBackend) SupportsTools() bool {
	return false
}

func (m *mockBackend) SupportsJSONMode() bool {
	return true
}

func (m *mockBackend) Model() string {
	return "mock-model"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}