// Package kb implements Phase 3.8 convention extraction functionality.
// This file contains the ConventionExtractor for detecting codebase patterns.
package kb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/agent"
	"github.com/alecpullen/marshal/internal/backend"
)

// ConventionExtractor analyzes codebases to extract conventions and patterns.
// Generates ExtractedConvention with evidence and requires user approval.
type ConventionExtractor struct {
	store    *SummaryStore
	query    *Query
	budget   *BudgetTracker
	backend  backend.Backend
	manifest *agent.Manifest
}

// ConventionExtractorOption configures the extractor.
type ConventionExtractorOption func(*ConventionExtractor)

// NewConventionExtractor creates a new convention extractor.
func NewConventionExtractor(
	store *SummaryStore,
	query *Query,
	budget *BudgetTracker,
	b backend.Backend,
	opts ...ConventionExtractorOption,
) (*ConventionExtractor, error) {
	ce := &ConventionExtractor{
		store:   store,
		query:   query,
		budget:  budget,
		backend: b,
	}
	
	for _, opt := range opts {
		opt(ce)
	}
	
	// Load manifest
	manifest, err := agent.LoadManifest("configs/roles/convention_extractor.yaml")
	if err != nil {
		ce.manifest = ce.defaultManifest()
	} else {
		ce.manifest = manifest
	}
	
	return ce, nil
}

// defaultManifest returns the embedded fallback manifest.
func (ce *ConventionExtractor) defaultManifest() *agent.Manifest {
	return &agent.Manifest{
		Role:         "convention_extractor",
		ModelBinding: "default",
		SystemPrompt: ce.defaultSystemPrompt(),
		Tools:        []string{"read_file", "kb_file_symbols", "kb_symbol_lookup"},
		MaxIterations: 2,
		Timeout:      60 * time.Second,
		OutputRequired: true,
	}
}

// defaultSystemPrompt returns the default convention extraction prompt.
func (ce *ConventionExtractor) defaultSystemPrompt() string {
	return `You are a convention extraction assistant. Analyze code samples and identify consistent patterns.

You will receive:
- A topic to investigate
- Multiple code samples showing the pattern in practice

Your task:
1. Identify common patterns across samples
2. Detect deviations or inconsistencies
3. Formulate a clear convention description
4. Select the best evidence samples (minimum 3)
5. Assign confidence score (0.0-1.0)

Output JSON with:
- topic: The convention topic
- description: Clear explanation (2-3 sentences)
- evidence: Array of code references (file, line, snippet)
- confidence: 0.0-1.0 based on consistency
- min_evidence: 3

Confidence guidelines:
- 0.9-1.0: Universal pattern, zero deviations
- 0.7-0.9: Strong pattern, minor deviations OK
- 0.5-0.7: Emerging pattern, some inconsistency
- <0.5: Do not report (pattern not established)

Rules:
- Only report conventions with confidence >= 0.5
- Include at least 3 evidence samples
- Snippets should be 3-5 lines showing the pattern
- Be objective: describe what IS, not what SHOULD BE
- Output ONLY valid JSON`
}

// Extract analyzes the codebase for conventions on a given topic.
// Samples code locations and uses LLM to identify patterns.
func (ce *ConventionExtractor) Extract(topic string, sampleSize int) (*ExtractedConvention, error) {
	if sampleSize < 3 {
		sampleSize = 3
	}
	
	// 1. Sample code locations related to topic
	samples, err := ce.sampleTopic(topic, sampleSize)
	if err != nil {
		return nil, fmt.Errorf("sampling topic: %w", err)
	}
	
	if len(samples) < 3 {
		return nil, fmt.Errorf("insufficient samples: got %d, need at least 3", len(samples))
	}
	
	// 2. Check budget
	if !ce.budget.Allowed(2) { // Convention extraction costs ~2 cents
		return nil, ErrBudgetExceeded
	}
	
	// 3. Build prompt
	prompt := ce.buildExtractionPrompt(topic, samples)
	
	// 4. Call backend
	ctx, cancel := context.WithTimeout(context.Background(), ce.manifest.Timeout)
	defer cancel()
	
	resp, cost, err := ce.callBackend(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("backend call: %w", err)
	}
	
	// 5. Parse response
	conv, err := ce.parseConvention(resp)
	if err != nil {
		return nil, fmt.Errorf("parsing convention: %w", err)
	}
	
	// 6. Fill in metadata
	conv.Topic = topic
	conv.MinEvidence = 3
	conv.Evidence = samples
	conv.GeneratedAt = time.Now()
	conv.ApprovedByUser = false // NEVER auto-approve
	
	// 7. Validate minimum evidence
	if !conv.HasMinimumEvidence() {
		return nil, fmt.Errorf("insufficient evidence: got %d, need %d", 
			len(conv.Evidence), conv.MinEvidence)
	}
	
	// 8. Validate confidence threshold
	if conv.Confidence < 0.5 {
		return nil, fmt.Errorf("confidence too low: %.2f < 0.5", conv.Confidence)
	}
	
	// 9. Generate unique ID
	conv.ID = ce.generateID(topic, conv.Description)
	
	// 10. Charge budget
	ce.budget.ChargeFloat(cost)
	
	// 11. Store (unapproved)
	if err := ce.store.StoreConvention(conv); err != nil {
		return nil, fmt.Errorf("storing convention: %w", err)
	}
	
	return conv, nil
}

// ApproveConvention marks a convention as approved by the user.
// Only approved conventions are visible to agents.
func (ce *ConventionExtractor) ApproveConvention(id string) error {
	// Get convention
	conv, err := ce.store.GetConvention(id)
	if err != nil {
		return fmt.Errorf("getting convention: %w", err)
	}
	if conv == nil {
		return fmt.Errorf("convention not found: %s", id)
	}
	
	// Update approval
	conv.ApprovedByUser = true
	now := time.Now()
	conv.ApprovedAt = &now
	
	// Save
	if err := ce.store.StoreConvention(conv); err != nil {
		return fmt.Errorf("updating convention: %w", err)
	}
	
	return nil
}

// RejectConvention removes a convention (user rejected it).
func (ce *ConventionExtractor) RejectConvention(id string) error {
	return ce.store.DeleteConvention(id)
}

// GetConvention retrieves a convention by ID.
func (ce *ConventionExtractor) GetConvention(id string) (*ExtractedConvention, error) {
	return ce.store.GetConvention(id)
}

// ListConventions returns all conventions.
// If approvedOnly is true, only returns user-approved conventions.
func (ce *ConventionExtractor) ListConventions(approvedOnly bool) ([]*ExtractedConvention, error) {
	return ce.store.ListConventions(approvedOnly)
}

// ListPendingConventions returns conventions awaiting approval.
func (ce *ConventionExtractor) ListPendingConventions() ([]*ExtractedConvention, error) {
	all, err := ce.store.ListConventions(false)
	if err != nil {
		return nil, err
	}
	
	var pending []*ExtractedConvention
	for _, conv := range all {
		if !conv.ApprovedByUser {
			pending = append(pending, conv)
		}
	}
	
	return pending, nil
}

// ListApprovedConventions returns only approved conventions.
// These are the ones agents can actually use.
func (ce *ConventionExtractor) ListApprovedConventions() ([]*ExtractedConvention, error) {
	return ce.store.ListConventions(true)
}

// sampleTopic finds code samples related to a topic.
// Uses heuristics based on topic keywords and symbol names.
func (ce *ConventionExtractor) sampleTopic(topic string, sampleSize int) ([]CodeRef, error) {
	// Build search keywords from topic
	keywords := extractKeywords(topic)
	
	// Search for symbols matching keywords
	var samples []CodeRef
	
	// Get all indexed files
	files, err := ce.store.List()
	if err != nil {
		return nil, err
	}
	
	// For each file, look for matching symbols
	for _, file := range files {
		if len(samples) >= sampleSize {
			break
		}
		
		symbols, err := ce.query.FileSymbols(file)
		if err != nil {
			continue
		}
		
		for _, sym := range symbols {
			if len(samples) >= sampleSize {
				break
			}
			
			// Check if symbol matches any keyword
			symNameLower := strings.ToLower(sym.Name)
			for _, kw := range keywords {
				if strings.Contains(symNameLower, kw) {
					// Found a match - add to samples
					ref := CodeRef{
						FilePath: file,
						Line:     sym.Range.StartLine,
						Snippet:  ce.extractSnippet(file, sym.Range.StartLine),
					}
					samples = append(samples, ref)
					break
				}
			}
		}
	}
	
	// If we don't have enough keyword matches, sample randomly
	if len(samples) < sampleSize {
		samples = ce.sampleRandomly(files, sampleSize-len(samples), samples)
	}
	
	return samples, nil
}

// extractKeywords extracts searchable keywords from a topic.
func extractKeywords(topic string) []string {
	topic = strings.ToLower(topic)
	
	// Split on common delimiters
	words := strings.FieldsFunc(topic, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-' || r == '/'
	})
	
	// Filter to meaningful words
	var keywords []string
	for _, w := range words {
		if len(w) > 2 {
			keywords = append(keywords, w)
		}
	}
	
	return keywords
}

// sampleRandomly adds random samples to fill quota.
func (ce *ConventionExtractor) sampleRandomly(files []string, needed int, existing []CodeRef) []CodeRef {
	existingSet := make(map[string]bool)
	for _, ref := range existing {
		existingSet[fmt.Sprintf("%s:%d", ref.FilePath, ref.Line)] = true
	}
	
	maxAttempts := needed * 10
	attempts := 0
	
	for len(existing) < needed && attempts < maxAttempts && len(files) > 0 {
		attempts++
		
		// Pick random file
		file := files[rand.Intn(len(files))]
		
		// Get symbols
		symbols, err := ce.query.FileSymbols(file)
		if err != nil {
			continue
		}
		
		if len(symbols) == 0 {
			continue
		}
		
		// Pick random symbol
		sym := symbols[rand.Intn(len(symbols))]
		
		key := fmt.Sprintf("%s:%d", file, sym.Range.StartLine)
		if existingSet[key] {
			continue
		}
		
		ref := CodeRef{
			FilePath: file,
			Line:     sym.Range.StartLine,
			Snippet:  ce.extractSnippet(file, sym.Range.StartLine),
		}
		
		existing = append(existing, ref)
		existingSet[key] = true
	}
	
	return existing
}

// extractSnippet extracts a code snippet from a file.
func (ce *ConventionExtractor) extractSnippet(filePath string, line int) string {
	// This is a simplified version
	// Full implementation would read file and extract context
	return fmt.Sprintf("// Code at %s:%d", filePath, line)
}

// buildExtractionPrompt constructs the LLM prompt.
func (ce *ConventionExtractor) buildExtractionPrompt(topic string, samples []CodeRef) string {
	var sampleTexts []string
	for i, ref := range samples {
		sampleTexts = append(sampleTexts, fmt.Sprintf(
			"Sample %d:\nFile: %s\nLine: %d\n%s\n",
			i+1, ref.FilePath, ref.Line, ref.Snippet))
	}
	
	return fmt.Sprintf(`%s

Topic: %s

Code Samples:
%s

Analyze these samples and generate the convention JSON.`,
		ce.manifest.SystemPrompt,
		topic,
		strings.Join(sampleTexts, "\n---\n"),
	)
}

// callBackend invokes the LLM.
func (ce *ConventionExtractor) callBackend(ctx context.Context, prompt string) (*LLMResponse, float64, error) {
	req := backend.Request{
		Messages: []backend.Message{
			{Role: backend.MessageRoleUser, Content: prompt},
		},
		MaxTokens:   1500,
		Temperature: 0.2,
	}
	
	resp, err := ce.backend.Complete(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	
	cost := ce.estimateCost(resp.Content)
	
	return &LLMResponse{
		Content: resp.Content,
		Model:   "extractor-model",
	}, cost, nil
}

// parseConvention extracts ExtractedConvention from LLM response.
func (ce *ConventionExtractor) parseConvention(resp *LLMResponse) (*ExtractedConvention, error) {
	content := strings.TrimSpace(resp.Content)
	
	// Remove markdown
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	
	var conv ExtractedConvention
	if err := json.Unmarshal([]byte(content), &conv); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	
	return &conv, nil
}

// estimateCost estimates the cost.
func (ce *ConventionExtractor) estimateCost(content string) float64 {
	tokens := len(content) / 4
	return float64(tokens) * 0.000002
}

// generateID creates a unique ID for a convention.
func (ce *ConventionExtractor) generateID(topic, description string) string {
	h := sha256.New()
	h.Write([]byte(topic))
	h.Write([]byte(description))
	h.Write([]byte(time.Now().String()))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ConventionProposal represents a convention awaiting approval.
// Used for TUI display.
type ConventionProposal struct {
	ID            string
	Topic         string
	Description   string
	EvidenceCount int
	Confidence    float64
	GeneratedAt   time.Time
	Preview       string // Short preview for display
}

// ToProposal converts an ExtractedConvention to a Proposal for UI.
func (ce *ConventionExtractor) ToProposal(conv *ExtractedConvention) *ConventionProposal {
	return &ConventionProposal{
		ID:            conv.ID,
		Topic:         conv.Topic,
		Description:   conv.Description,
		EvidenceCount: len(conv.Evidence),
		Confidence:    conv.Confidence,
		GeneratedAt:   conv.GeneratedAt,
		Preview:       truncateString(conv.Description, 100),
	}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}