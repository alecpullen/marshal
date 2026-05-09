// Package kb implements Phase 3.8 summariser functionality.
// This file contains the Summariser agent that generates LLM-backed summaries.
package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/agent"
	"github.com/alecpullen/marshal/internal/backend"
)

// Summariser generates LLM-backed summaries of code files and packages.
// Integrates with the backend to call cheaper models for summary generation.
type Summariser struct {
	store      *SummaryStore
	query      *Query
	budget     *BudgetTracker
	backend    backend.Backend
	manifest   *agent.Manifest
	
	// Configuration
	defaultModelBinding string
	maxRetries          int
}

// SummariserOption configures the summariser.
type SummariserOption func(*Summariser)

// WithDefaultModelBinding sets the default model binding for summaries.
func WithDefaultModelBinding(binding string) SummariserOption {
	return func(s *Summariser) {
		s.defaultModelBinding = binding
	}
}

// WithMaxRetries sets the maximum retry attempts.
func WithMaxRetries(retries int) SummariserOption {
	return func(s *Summariser) {
		s.maxRetries = retries
	}
}

// NewSummariser creates a new summariser instance.
func NewSummariser(
	store *SummaryStore,
	query *Query,
	budget *BudgetTracker,
	b backend.Backend,
	opts ...SummariserOption,
) (*Summariser, error) {
	s := &Summariser{
		store:               store,
		query:               query,
		budget:              budget,
		backend:             b,
		defaultModelBinding: "fast",
		maxRetries:          2,
	}
	
	for _, opt := range opts {
		opt(s)
	}
	
	// Load the summariser manifest
	manifest, err := agent.LoadManifest("configs/roles/summariser.yaml")
	if err != nil {
		// Fallback to embedded manifest
		manifest = s.defaultManifest()
	}
	s.manifest = manifest
	
	return s, nil
}

// defaultManifest returns an embedded fallback manifest.
func (s *Summariser) defaultManifest() *agent.Manifest {
	return &agent.Manifest{
		Role:           "summariser",
		ModelBinding:   s.defaultModelBinding,
		SystemPrompt:   s.defaultSystemPrompt(),
		Tools:          []string{"read_file", "kb_file_symbols"},
		MaxIterations:  1,
		Timeout:        30 * time.Second,
		OutputRequired: true,
	}
}

// defaultSystemPrompt returns the default system prompt.
func (s *Summariser) defaultSystemPrompt() string {
	return `You are a code summarization assistant. Generate a structured summary of the provided file.

Output a JSON object with these fields:
- path: file path
- content_hash: content hash (provided)
- symbols_hash: symbols hash (provided)
- purpose: 1-2 sentence description
- public_surface: array of exported symbol names (MUST match symbol index)
- depends_on: array of dependencies
- related_to: array of related files
- notes: implementation details
- generated_at: ISO 8601 timestamp

Rules:
- Output ONLY valid JSON
- PublicSurface must be verifiable against symbols
- Be concise (50-100 words total)`
}

// SummariseFile generates a FileSummary for a single file.
// Checks cache first, validates budget, calls LLM, validates output, stores result.
func (s *Summariser) SummariseFile(filePath string) (*FileSummary, error) {
	// 1. Get current symbols from index
	symbols, err := s.query.FileSymbols(filePath)
	if err != nil {
		return nil, fmt.Errorf("getting symbols: %w", err)
	}
	
	// 2. Compute hashes
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	contentHash := HashContent(content)
	symbolsHash := HashSymbols(symbols)
	
	// 3. Check existing summary
	existing, err := s.store.GetFileSummary(filePath)
	if err != nil {
		return nil, fmt.Errorf("checking existing: %w", err)
	}
	
	if existing != nil {
		// Check if still valid
		if existing.ContentHash == contentHash && existing.SymbolsHash == symbolsHash {
			if !existing.IsStale() {
				return existing, nil // Fresh and valid
			}
			// Stale but structurally unchanged - still usable
			return existing, nil
		}
		// Content or symbols changed - need regeneration
	}
	
	// 4. Check budget (estimate 1 cent)
	if !s.budget.Allowed(1) {
		return nil, ErrBudgetExceeded
	}
	
	// 5. Build LLM prompt
	prompt := s.buildFilePrompt(filePath, content, symbols)
	
	// 6. Call backend
	ctx, cancel := context.WithTimeout(context.Background(), s.manifest.Timeout)
	defer cancel()
	
	resp, cost, err := s.callBackend(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("backend call: %w", err)
	}
	
	// 7. Parse response
	summary, err := s.parseFileSummary(resp)
	if err != nil {
		return nil, fmt.Errorf("parsing summary: %w", err)
	}
	
	// 8. Fill in metadata
	summary.Path = filePath
	summary.ContentHash = contentHash
	summary.SymbolsHash = symbolsHash
	summary.GeneratedAt = time.Now()
	summary.GeneratedBy = resp.Model
	summary.ModelBinding = s.manifest.ModelBinding
	summary.CostCents = int(cost * 100) // Convert dollars to cents
	
	// 9. Validate against symbols
	if err := summary.ValidatePublicSurface(symbols); err != nil {
		// Validation failed - retry once with clearer prompt
		if s.maxRetries > 0 {
			s.maxRetries--
			retryPrompt := s.buildValidationFailedPrompt(filePath, content, symbols, err)
			resp, cost, err = s.callBackend(ctx, retryPrompt)
			if err != nil {
				return nil, fmt.Errorf("retry failed: %w", err)
			}
			summary, err = s.parseFileSummary(resp)
			if err != nil {
				return nil, fmt.Errorf("parsing retry: %w", err)
			}
			// Re-validate
			if err := summary.ValidatePublicSurface(symbols); err != nil {
				return nil, fmt.Errorf("validation failed after retry: %w", err)
			}
		} else {
			return nil, fmt.Errorf("validation failed: %w", err)
		}
	}
	
	// 10. Charge budget
	s.budget.ChargeFloat(cost)
	
	// 11. Store summary
	if err := s.store.StoreFileSummary(summary); err != nil {
		return nil, fmt.Errorf("storing summary: %w", err)
	}
	
	return summary, nil
}

// SummarisePackage generates a PackageSummary for a package/directory.
func (s *Summariser) SummarisePackage(packagePath string) (*PackageSummary, error) {
	// 1. Get all files in package
	files, err := s.findPackageFiles(packagePath)
	if err != nil {
		return nil, fmt.Errorf("finding package files: %w", err)
	}
	
	// 2. Ensure all files have summaries
	var fileSummaries []*FileSummary
	for _, file := range files {
		fs, err := s.SummariseFile(file)
		if err != nil {
			// Log but continue
			continue
		}
		fileSummaries = append(fileSummaries, fs)
	}
	
	// 3. Compute package symbols hash
	allSymbols, err := s.query.PackageExports(packagePath)
	if err != nil {
		return nil, fmt.Errorf("getting package exports: %w", err)
	}
	symbolsHash := HashSymbols(allSymbols)
	
	// 4. Check existing package summary
	existing, err := s.store.GetPackageSummary(packagePath)
	if err != nil {
		return nil, fmt.Errorf("checking existing: %w", err)
	}
	
	if existing != nil && existing.SymbolsHash == symbolsHash && !existing.IsStale() {
		return existing, nil
	}
	
	// 5. Check budget
	if !s.budget.Allowed(2) { // Package summaries cost more
		return nil, ErrBudgetExceeded
	}
	
	// 6. Build package prompt
	prompt := s.buildPackagePrompt(packagePath, fileSummaries, allSymbols)
	
	// 7. Call backend
	ctx, cancel := context.WithTimeout(context.Background(), s.manifest.Timeout)
	defer cancel()
	
	resp, cost, err := s.callBackend(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("backend call: %w", err)
	}
	
	// 8. Parse response
	ps, err := s.parsePackageSummary(resp)
	if err != nil {
		return nil, fmt.Errorf("parsing summary: %w", err)
	}
	
	// 9. Fill in metadata
	ps.Path = packagePath
	ps.Files = files
	ps.SymbolsHash = symbolsHash
	ps.GeneratedAt = time.Now()
	ps.CostCents = int(cost * 100)
	
	// 10. Charge budget
	s.budget.ChargeFloat(cost)
	
	// 11. Store
	if err := s.store.StorePackageSummary(ps); err != nil {
		return nil, fmt.Errorf("storing summary: %w", err)
	}
	
	return ps, nil
}

// GenerateProjectMap creates a high-level ProjectMap.
func (s *Summariser) GenerateProjectMap() (*ProjectMap, error) {
	// 1. Get all package summaries
	// 2. Build project-level understanding
	// This is a more expensive operation, only done on explicit request
	
	if !s.budget.Allowed(5) { // Project maps cost more
		return nil, ErrBudgetExceeded
	}
	
	// For now, return a basic implementation
	// Full implementation would aggregate all package summaries
	pm := &ProjectMap{
		ID:           "1",
		Name:         "project",
		Description:  "Auto-generated project map",
		Languages:    make(map[string]int),
		MajorModules: []ModuleSummary{},
		Conventions:  []string{},
		GeneratedAt:  time.Now(),
	}
	
	return pm, nil
}

// GetSummary retrieves an existing file summary (no generation).
func (s *Summariser) GetSummary(filePath string) (*FileSummary, error) {
	return s.store.GetFileSummary(filePath)
}

// buildFilePrompt constructs the LLM prompt for file summarization.
func (s *Summariser) buildFilePrompt(filePath string, content []byte, symbols []Symbol) string {
	// Format symbols for LLM
	var exportedSymbols []string
	for _, sym := range symbols {
		if sym.Exported {
			exportedSymbols = append(exportedSymbols, 
				fmt.Sprintf("- %s (%s, line %d)", sym.Name, sym.Kind, sym.Range.StartLine))
		}
	}
	
	return fmt.Sprintf(`%s

File: %s
Content Hash: %s
Symbols Hash: %s

Source Code:
%s

Exported Symbols (MUST match these exactly):
%s

Generate the FileSummary JSON now.`,
		s.manifest.SystemPrompt,
		filePath,
		HashContent(content),
		HashSymbols(symbols),
		string(content),
		strings.Join(exportedSymbols, "\n"),
	)
}

// buildValidationFailedPrompt creates a retry prompt when validation fails.
func (s *Summariser) buildValidationFailedPrompt(filePath string, content []byte, symbols []Symbol, valErr error) string {
	base := s.buildFilePrompt(filePath, content, symbols)
	return base + fmt.Sprintf(`

IMPORTANT: Your previous response failed validation:
%s

Please correct the public_surface field to ONLY include symbols from the list above.
Do not hallucinate symbols that don't exist in the exported symbols list.`,
		valErr.Error())
}

// buildPackagePrompt constructs the LLM prompt for package summarization.
func (s *Summariser) buildPackagePrompt(packagePath string, fileSummaries []*FileSummary, exports []Symbol) string {
	var fileList []string
	for _, fs := range fileSummaries {
		fileList = append(fileList, fmt.Sprintf("- %s: %s", 
			filepath.Base(fs.Path), fs.Purpose))
	}
	
	var exportList []string
	for _, sym := range exports {
		exportList = append(exportList, fmt.Sprintf("- %s (%s)", sym.Name, sym.Kind))
	}
	
	return fmt.Sprintf(`Generate a PackageSummary for the package at %s.

Files in package:
%s

Package exports:
%s

Output JSON with: path, files, symbols_hash, purpose, public_api, entry_points, architecture, subpackages, dependencies, generated_at, cost_cents.

Purpose: 2-3 sentence description of package responsibility.
PublicAPI: Array of main exported symbols.
EntryPoints: Most important exports users interact with.
Architecture: Brief structural description.

Output ONLY valid JSON.`,
		packagePath,
		strings.Join(fileList, "\n"),
		strings.Join(exportList, "\n"),
	)
}

// callBackend invokes the LLM backend.
func (s *Summariser) callBackend(ctx context.Context, prompt string) (*LLMResponse, float64, error) {
	// Build backend request
	req := backend.Request{
		Messages: []backend.Message{
			{Role: backend.MessageRoleUser, Content: prompt},
		},
		MaxTokens:   1000,
		Temperature: 0.1, // Low creativity for consistent output
	}
	
	// Call backend
	resp, err := s.backend.Complete(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	
	// Estimate cost (simplified - real implementation would use actual token counts)
	cost := s.estimateCost(resp.Content)
	
	return &LLMResponse{
		Content: resp.Content,
		Model:   "summariser-model", // Would come from actual response
	}, cost, nil
}

// LLMResponse represents a response from the LLM backend.
type LLMResponse struct {
	Content string
	Model   string
}

// parseFileSummary extracts FileSummary from LLM response.
func (s *Summariser) parseFileSummary(resp *LLMResponse) (*FileSummary, error) {
	content := strings.TrimSpace(resp.Content)
	
	// Remove markdown code block if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	
	var summary FileSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	
	return &summary, nil
}

// parsePackageSummary extracts PackageSummary from LLM response.
func (s *Summariser) parsePackageSummary(resp *LLMResponse) (*PackageSummary, error) {
	content := strings.TrimSpace(resp.Content)
	
	// Remove markdown code block if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	
	var summary PackageSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	
	return &summary, nil
}

// estimateCost estimates the cost in dollars based on response size.
func (s *Summariser) estimateCost(content string) float64 {
	// Simplified estimation: ~$0.002 per 1K tokens for cheaper models
	// Assuming 4 chars per token on average
	tokens := len(content) / 4
	return float64(tokens) * 0.000002
}

// findPackageFiles finds all source files in a package directory.
func (s *Summariser) findPackageFiles(packagePath string) ([]string, error) {
	var files []string
	
	// Get all indexed files and filter by path prefix
	indexedFiles, err := s.store.List()
	if err != nil {
		return nil, err
	}
	
	for _, file := range indexedFiles {
		dir := filepath.Dir(file)
		if strings.HasPrefix(dir, packagePath) || dir == packagePath {
			files = append(files, file)
		}
	}
	
	return files, nil
}

// ErrBudgetExceeded is returned when the daily budget is exhausted.
var ErrBudgetExceeded = fmt.Errorf("daily summarization budget exceeded")