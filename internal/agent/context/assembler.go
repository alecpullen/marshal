// Package context provides context assembly for agents.
package context

import (
	"context"
	"fmt"
	"sort"

	ctxstore "github.com/alecpullen/marshal/internal/context"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// ContextPolicy defines how context is assembled.
// This mirrors agent.ContextPolicy to avoid import cycle.
type ContextPolicy struct {
	Inherit         []string `yaml:"inherit"`
	Exclude         []string `yaml:"exclude"`
	IncludeExplicit []string `yaml:"include_explicit,omitempty"`
	MaxTokens       int      `yaml:"max_tokens"`
	SummarizeIfOver int      `yaml:"summarize_if_over"`
}

// Assembler builds context according to ContextPolicy.
type Assembler struct {
	store *ctxstore.Store
}

// ContextEntry is a resolved context item.
type ContextEntry struct {
	Key     string
	Ref     protocol.ContextRef
	Content string
	Tokens  int
}

// NewAssembler creates a context assembler.
func NewAssembler(store *ctxstore.Store) *Assembler {
	return &Assembler{store: store}
}

// Assemble builds the context for an agent turn.
func (a *Assembler) Assemble(
	ctx context.Context,
	policy ContextPolicy,
	initialContext map[string]protocol.ContextRef,
) ([]ContextEntry, error) {
	var entries []ContextEntry

	// 1. Include initial context (from task)
	for key, ref := range initialContext {
		entry, err := a.store.Get(ref)
		if err != nil {
			continue // Skip missing entries
		}
		entries = append(entries, ContextEntry{
			Key:     key,
			Ref:     entry.Ref,
			Content: string(entry.Content),
			Tokens:  entry.SizeTokens,
		})
	}

	// 2. Inherit from parent tasks (if specified)
	for _, inheritKey := range policy.Inherit {
		// Query store for entries matching inheritKey as a tag
		// Note: Using List instead of Search since we don't have Search implemented
		results, err := a.store.List(protocol.ListQuery{
			Tags:       []string{inheritKey},
			LatestOnly: true,
		})
		if err != nil {
			continue
		}
		for _, result := range results {
			entries = append(entries, ContextEntry{
				Key:     fmt.Sprintf("%s/%s", inheritKey, result.Key.Path()),
				Ref:     result.Ref,
				Content: string(result.Content),
				Tokens:  result.SizeTokens,
			})
		}
	}

	// 3. Include explicit entries
	for _, refStr := range policy.IncludeExplicit {
		ref, err := protocol.ParseContextRef(refStr)
		if err != nil {
			continue
		}
		entry, err := a.store.Get(ref)
		if err != nil {
			continue
		}
		entries = append(entries, ContextEntry{
			Key:     string(entry.Key.Kind()),
			Ref:     ref,
			Content: string(entry.Content),
			Tokens:  entry.SizeTokens,
		})
	}

	// 4. Apply exclusions
	entries = a.applyExclusions(entries, policy.Exclude)

	// 5. Sort by priority (newest first, based on ref timestamp)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Ref > entries[j].Ref
	})

	// 6. Enforce token limit
	if policy.MaxTokens > 0 {
		entries = a.enforceLimit(entries, policy.MaxTokens, policy.SummarizeIfOver)
	}

	return entries, nil
}

// AssembleWithTruncation assembles context with truncation notice.
func (a *Assembler) AssembleWithTruncation(
	ctx context.Context,
	policy ContextPolicy,
	initialContext map[string]protocol.ContextRef,
) ([]ContextEntry, bool, error) {
	entries, err := a.Assemble(ctx, policy, initialContext)
	if err != nil {
		return nil, false, err
	}

	total := 0
	for _, e := range entries {
		total += e.Tokens
	}

	truncated := false
	if policy.MaxTokens > 0 && total > policy.MaxTokens {
		truncated = true
	}

	return entries, truncated, nil
}

// TotalTokens calculates the total token count of entries.
func TotalTokens(entries []ContextEntry) int {
	total := 0
	for _, e := range entries {
		total += e.Tokens
	}
	return total
}

// ToContextRefs converts entries to a map of context refs.
func ToContextRefs(entries []ContextEntry) map[string]protocol.ContextRef {
	refs := make(map[string]protocol.ContextRef)
	for _, e := range entries {
		refs[e.Key] = e.Ref
	}
	return refs
}

func (a *Assembler) applyExclusions(
	entries []ContextEntry,
	exclude []string,
) []ContextEntry {
	if len(exclude) == 0 {
		return entries
	}

	var filtered []ContextEntry
	for _, e := range entries {
		excluded := false
		kind := e.Ref.Kind()

		for _, ex := range exclude {
			if string(kind) == ex || e.Key == ex {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (a *Assembler) enforceLimit(
	entries []ContextEntry,
	maxTokens int,
	summarizeThreshold int,
) []ContextEntry {
	total := TotalTokens(entries)

	if total <= maxTokens {
		return entries
	}

	// Check if summarization threshold is crossed
	if summarizeThreshold > 0 && total > summarizeThreshold {
		// In Phase 3.8, we would summarize here
		// For now, we truncate
	}

	// Truncate to fit by removing oldest entries (end of sorted list)
	var result []ContextEntry
	running := 0
	for _, e := range entries {
		if running+e.Tokens <= maxTokens {
			result = append(result, e)
			running += e.Tokens
		} else {
			break
		}
	}

	return result
}
