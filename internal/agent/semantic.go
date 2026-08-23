package agent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"marshal/internal/contextpack"
	"marshal/internal/retrieval"
)

// semanticRetrievalLimit bounds how many snippets passive injection adds so it
// never dominates the pack.
const semanticRetrievalLimit = 5

// retrieveSemanticContext runs one semantic query for the goal and maps the
// hits to context-pack snippets. A nil source (embeddings unconfigured / empty
// index) yields nil — graceful-off.
func retrieveSemanticContext(ctx context.Context, goal string, src retrieval.Source) []contextpack.FileSnippet {
	if src == nil || goal == "" {
		return nil
	}
	hits, err := src.Retrieve(ctx, retrieval.Query{Text: goal, Limit: semanticRetrievalLimit})
	if err != nil || len(hits) == 0 {
		return nil
	}
	out := make([]contextpack.FileSnippet, 0, len(hits))
	for _, h := range hits {
		out = append(out, contextpack.FileSnippet{
			Path: h.FilePath, StartLine: h.StartLine, EndLine: h.EndLine, Content: h.Content,
		})
	}
	return out
}

// semanticRequeryThreshold is how many newly-referenced paths accumulate
// before a follow-up semantic query fires mid-turn (AI-10).
const semanticRequeryThreshold = 3

// semanticRequeryTracker accumulates the paths referenced by tool calls
// during a turn and the semantic snippets already merged into the pack, so a
// re-query can target what the agent just discovered without re-injecting
// what it has already seen.
//
// The tracker is mutated from parallel tool-execution goroutines (read-only
// tools run concurrently in executeActions), so every access is guarded by mu.
type semanticRequeryTracker struct {
	mu       sync.Mutex
	seen     map[string]bool // referenced paths + snippet paths already represented
	snippets []contextpack.FileSnippet
	pending  []string // newly referenced paths since the last re-query
}

func newSemanticRequeryTracker() *semanticRequeryTracker {
	return &semanticRequeryTracker{seen: map[string]bool{}}
}

// note records tool-referenced paths; first-seen ones queue toward a re-query.
func (t *semanticRequeryTracker) note(paths []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range paths {
		if t.seen[p] {
			continue
		}
		t.seen[p] = true
		t.pending = append(t.pending, p)
	}
}

// addSnippets records snippets as represented in the pack.
func (t *semanticRequeryTracker) addSnippets(snips []contextpack.FileSnippet) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range snips {
		t.seen[s.Path] = true
	}
	t.snippets = append(t.snippets, snips...)
}

// maybeRequerySemantic runs a follow-up semantic query once enough new paths
// have been referenced this turn. The query is the goal plus the new
// basenames; hits for already-represented paths are dropped. Pending paths
// are consumed regardless of outcome — a failed or empty re-query must not
// retry every iteration. Nil source / empty index is a silent no-op.
func (r *Runner) maybeRequerySemantic(ctx context.Context, goal string, src retrieval.Source, maxTokenOverride int) {
	t := r.semTracker
	if t == nil {
		return
	}
	t.mu.Lock()
	if len(t.pending) < semanticRequeryThreshold {
		t.mu.Unlock()
		return
	}
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	if src == nil {
		return
	}
	names := make([]string, 0, len(pending))
	for _, p := range pending {
		names = append(names, filepath.Base(p))
	}
	snips := retrieveSemanticContext(ctx, goal+" "+strings.Join(names, " "), src)
	var fresh []contextpack.FileSnippet
	t.mu.Lock()
	for _, s := range snips {
		if t.seen[s.Path] {
			continue
		}
		fresh = append(fresh, s)
	}
	if len(fresh) == 0 {
		t.mu.Unlock()
		return
	}
	for _, s := range fresh {
		t.seen[s.Path] = true
	}
	t.snippets = append(t.snippets, fresh...)
	all := append([]contextpack.FileSnippet(nil), t.snippets...)
	t.mu.Unlock()
	r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		maxTokens := maxTokenOverride
		if maxTokens <= 0 {
			maxTokens = pack.TokenUsage.MaxTokens
		}
		if maxTokens <= 0 {
			maxTokens = contextpack.DefaultMaxTokens
		}
		return contextpack.MergeSemanticContext(pack, all, maxTokens, r.Now)
	})
}
