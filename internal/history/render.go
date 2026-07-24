package history

import (
	"context"
	"fmt"
	"strings"
	"time"

	"marshal/internal/db"
)

// GenerationSummary is a user-facing summary of one generation.
type GenerationSummary struct {
	ID         string
	Seq        int
	StartedAt  time.Time
	EndedAt    *time.Time
	SeedDigest string
	TurnCount  int
	Live       bool
}

// DumpOptions controls which generation to dump.
type DumpOptions struct {
	SessionID     string
	GenerationSeq int
	Limit         int // max turns to include; 0 = unlimited
}

// ListGenerations returns summaries for all generations in a session.
func ListGenerations(ctx context.Context, database *db.DB, sessionID string) ([]GenerationSummary, error) {
	gens, err := database.GenerationsForSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("list generations: %w", err)
	}
	out := make([]GenerationSummary, len(gens))
	for i, g := range gens {
		count, _ := database.GenerationTurnCount(g.ID)
		out[i] = GenerationSummary{
			ID:         g.ID,
			Seq:        g.Seq,
			StartedAt:  g.StartedAt,
			EndedAt:    g.EndedAt,
			SeedDigest: g.SeedDigest,
			TurnCount:  count,
			Live:       g.EndedAt == nil,
		}
	}
	return out, nil
}

// DumpGeneration renders a generation's full transcript as a string.
func DumpGeneration(ctx context.Context, database *db.DB, opts DumpOptions) (string, error) {
	gens, err := database.GenerationsForSession(opts.SessionID)
	if err != nil {
		return "", fmt.Errorf("dump generation: %w", err)
	}

	var gen *db.Generation
	for i, g := range gens {
		if g.Seq == opts.GenerationSeq {
			gen = &gens[i]
			break
		}
	}
	if gen == nil {
		return "", fmt.Errorf("generation seq %d not found in session %s", opts.GenerationSeq, opts.SessionID)
	}

	turns, err := database.TurnsForGeneration(gen.ID)
	if err != nil {
		return "", fmt.Errorf("dump generation: %w", err)
	}

	if opts.Limit > 0 && len(turns) > opts.Limit {
		turns = turns[:opts.Limit]
	}

	var b strings.Builder
	status := "ended"
	if gen.EndedAt == nil {
		status = "live"
	}
	fmt.Fprintf(&b, "Generation %d (%s)\n", gen.Seq, status)
	fmt.Fprintf(&b, "  Started: %s\n", gen.StartedAt.UTC().Format(time.RFC3339))
	if gen.EndedAt != nil {
		fmt.Fprintf(&b, "  Ended:   %s\n", gen.EndedAt.UTC().Format(time.RFC3339))
	}
	if gen.SeedDigest != "" {
		fmt.Fprintf(&b, "  Digest:  %s\n", gen.SeedDigest)
	}
	fmt.Fprintf(&b, "  Turns:   %d\n", len(turns))
	b.WriteString("\n")

	for _, t := range turns {
		fmt.Fprintf(&b, "--- turn %d (%s) ---\n", t.TurnSeq, t.Role)
		b.WriteString(t.Content)
		b.WriteString("\n")
		if t.ToolCalls != "" {
			b.WriteString("[tool_calls]\n")
			b.WriteString(t.ToolCalls)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Search renders search hits with generation context.
func Search(ctx context.Context, database *db.DB, sessionID, query string, limit int) (string, error) {
	hits, err := database.SearchArchivedTurns(sessionID, query, "", limit)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	if len(hits) == 0 {
		return "No matches found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d match(es):\n\n", len(hits))
	for _, h := range hits {
		fmt.Fprintf(&b, "  gen %d, turn %d (%s)\n", h.GenerationSeq, h.Turn.TurnSeq, h.Turn.Role)
		// Show a snippet of the content
		snippet := h.Turn.Content
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		// Indent the snippet
		for _, line := range strings.Split(snippet, "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
