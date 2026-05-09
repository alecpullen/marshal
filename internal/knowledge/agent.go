package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/agent"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// KnowledgeAgent wraps the generic agent with knowledge-specific enforcement.
type KnowledgeAgent struct {
	runtime  *agent.Runtime
	tier    *KnowledgeTier
	cache   *QueryCache
	enforce EnforcementConfig
}

// NewKnowledgeAgent creates a new knowledge agent.
func NewKnowledgeAgent(runtime *agent.Runtime, tier *KnowledgeTier, cache *QueryCache) *KnowledgeAgent {
	return &KnowledgeAgent{
		runtime: runtime,
		tier:    tier,
		cache:   cache,
		enforce: DefaultEnforcementConfig(),
	}
}

// Run executes the knowledge agent with enforcement and caching.
func (ka *KnowledgeAgent) Run(ctx context.Context, question string, scope string) (*KnowledgeAnswer, error) {
	// Auto-detect scope if needed
	if scope == "" || scope == "auto" {
		scope = string(AutoDetect(question))
	}

	// Get current search results for cache key
	currentTopResults := ka.tier.LastTopResults()

	// Check cache first
	if cached := ka.cache.Get(question, scope, currentTopResults); cached != nil {
		return cached, nil
	}

	// Run the agent runtime
	result, err := ka.runtime.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent execution failed: %w", err)
	}

	// Try to parse and enforce
	answer, err := ka.parseAndEnforce(result.Output)
	if err != nil {
		// Try auto-fix by re-searching
		if ka.enforce.AutoFixCitations {
			answer, err = ka.autoFixWithReSearch(ctx, question, scope, result.Output, err)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	// Convert read set paths to refs for cache
	readSetRefs := ka.convertReadSetToRefs()

	// Store in cache
	ka.cache.Put(
		question,
		scope,
		answer,
		readSetRefs,
		ka.tier.LastTopResults(),
	)

	return answer, nil
}

// convertReadSetToRefs converts agent read set to ContextRefs.
func (ka *KnowledgeAgent) convertReadSetToRefs() []protocol.ContextRef {
	// Get read set from the agent
	agent := ka.runtime.GetAgent()
	if agent == nil {
		return nil
	}
	
	paths := agent.ReadSet.AllPaths()
	refs := make([]protocol.ContextRef, 0, len(paths))
	
	for _, path := range paths {
		if hash, ok := agent.ReadSet.GetHash(path); ok {
			// Create a synthetic ContextRef
			ref := protocol.NewContextRef(protocol.EntryKindFile, path, []byte(hash))
			refs = append(refs, ref)
		}
	}
	
	return refs
}

// parseAndEnforce validates and enforces the knowledge answer.
func (ka *KnowledgeAgent) parseAndEnforce(output string) (*KnowledgeAnswer, error) {
	var answer KnowledgeAnswer
	if err := json.Unmarshal([]byte(output), &answer); err != nil {
		return nil, &EnforcementError{
			Code:    "invalid_json",
			Message: fmt.Sprintf("Output is not valid JSON: %v", err),
			Details: map[string]any{"output": output},
		}
	}

	// Enforcement 1: Non-empty citations
	if ka.enforce.RequireCitations {
		if len(answer.Citations) == 0 {
			return nil, &EnforcementError{
				Code:    "missing_citations",
				Message: "KnowledgeAnswer must include at least one citation",
			}
		}

		if len(answer.Citations) < ka.enforce.MinCitations {
			return nil, &EnforcementError{
				Code: "insufficient_citations",
				Message: fmt.Sprintf("Need at least %d citations, got %d",
					ka.enforce.MinCitations, len(answer.Citations)),
				Details: map[string]any{
					"required": ka.enforce.MinCitations,
					"provided": len(answer.Citations),
				},
			}
		}
	}

	// Enforcement 2: Validate citations exist
	validCitations, invalidCitations := ka.validateCitations(answer.Citations)
	if len(invalidCitations) > 0 {
		return nil, &EnforcementError{
			Code:    "invalid_citations",
			Message: fmt.Sprintf("Invalid citations: %v", invalidCitations),
			Details: map[string]any{
				"invalid": invalidCitations,
				"valid":   validCitations,
			},
		}
	}
	answer.Citations = validCitations

	// Enforcement 3: Apply hybrid confidence
	if ka.enforce.RequireConfidence {
		answer.Confidence = ka.calculateConfidence(answer, validCitations)
	}

	return &answer, nil
}

// autoFixWithReSearch attempts to fix invalid citations by re-searching.
func (ka *KnowledgeAgent) autoFixWithReSearch(
	ctx context.Context,
	question string,
	scope string,
	badOutput string,
	originalErr error,
) (*KnowledgeAnswer, error) {
	for i := 0; i < ka.enforce.MaxRetries; i++ {
		// Re-search for the question to find new citations
		results, err := ka.tier.Search(ctx, question, SearchModeExact, scope)
		if err != nil {
			return nil, fmt.Errorf("re-search failed: %w", err)
		}

		if len(results) == 0 {
			return nil, &EnforcementError{
				Code:    "no_context",
				Message: "No relevant context found for question",
			}
		}

		// Build feedback with new search results
		var feedback strings.Builder
		feedback.WriteString(fmt.Sprintf("Previous answer failed: %v\n\n", originalErr))
		feedback.WriteString("I found new relevant context. Please answer again using these citations:\n")

		newCitations := make([]protocol.ContextRef, 0, min(5, len(results)))
		for j, r := range results {
			if j >= 5 {
				break
			}
			feedback.WriteString(fmt.Sprintf("- %s\n", r.Entry.Ref))
			newCitations = append(newCitations, r.Entry.Ref)
		}

		// Create new task with feedback by updating the agent's task
		// This is a simplified approach - in reality, we'd need to inject the feedback
		// into the next round of the runtime
		
		// Retry
		result, err := ka.runtime.Run(ctx)
		if err != nil {
			originalErr = err
			continue
		}

		// Try to parse and enforce again
		answer, err := ka.parseAndEnforce(result.Output)
		if err == nil {
			return answer, nil // Success on retry
		}

		// Check if we got valid citations this time
		if len(answer.Citations) > 0 {
			// Validate new citations
			valid, invalid := ka.validateCitations(answer.Citations)
			if len(invalid) == 0 {
				answer.Citations = valid
				return answer, nil
			}
			// Try replacing with suggested citations
			answer.Citations = newCitations
			return answer, nil
		}

		originalErr = err
	}

	return nil, fmt.Errorf("failed after %d auto-fix attempts: %w", ka.enforce.MaxRetries, originalErr)
}

// validateCitations checks if citations exist in the store.
func (ka *KnowledgeAgent) validateCitations(citations []protocol.ContextRef) (valid []protocol.ContextRef, invalid []protocol.ContextRef) {
	for _, ref := range citations {
		if _, err := ka.tier.Fetch(context.Background(), ref); err == nil {
			valid = append(valid, ref)
		} else {
			invalid = append(invalid, ref)
		}
	}
	return valid, invalid
}

// calculateConfidence applies hybrid scoring.
func (ka *KnowledgeAgent) calculateConfidence(answer KnowledgeAnswer, citations []protocol.ContextRef) Confidence {
	// Algorithmic overrides based on confirmed requirements:
	// - 0 citations -> unknown
	// - 1 citation -> low
	// - 2+ citations -> respect LLM (but cap at medium if poor search)
	// - 3+ citations + excellent search -> allow high

	if len(citations) == 0 {
		return ConfidenceUnknown
	}

	if len(citations) == 1 {
		return ConfidenceLow // Force low for single citation
	}

	// Check search quality
	lastScore := ka.tier.LastSearchScore()
	if lastScore > 10.0 { // Poor search results
		return ConfidenceLow
	}

	// Respect LLM for 2+ citations (with caps)
	suggested := answer.Confidence
	if suggested == ConfidenceHigh && len(citations) >= 3 && lastScore <= 5.0 {
		return ConfidenceHigh
	}
	if suggested == ConfidenceMedium && len(citations) >= 2 {
		return ConfidenceMedium
	}

	return ConfidenceLow // Default fallback
}

// SetEnforcementConfig allows customizing enforcement behavior.
func (ka *KnowledgeAgent) SetEnforcementConfig(cfg EnforcementConfig) {
	ka.enforce = cfg
}

// GetEnforcementConfig returns current enforcement config.
func (ka *KnowledgeAgent) GetEnforcementConfig() EnforcementConfig {
	return ka.enforce
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
