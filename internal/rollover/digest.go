// Package rollover provides context-window rollover logic for long
// conversations. This file defines the digest providers used to summarise
// a generation before rolling over.
package rollover

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/llm/schema"
)

// GenerationHandle carries the metadata and wire messages for a generation
// that is about to be rolled over.
type GenerationHandle struct {
	SessionID, GenerationID string
	Seq                     int
	Wire                    []schema.ChatMessage
	Goal                    string
}

// DigestProvider produces a digest string and a source label for a generation.
type DigestProvider interface {
	// Digest returns the digest text, a source label, and any error.
	Digest(ctx context.Context, outgoing GenerationHandle) (digest string, source string, err error)
}

// chatModel is the interface LLMSummaryProvider uses to call the LLM. It
// matches the contract's expected schema.Model.Chat signature.
type chatModel interface {
	Chat(ctx context.Context, messages []schema.ChatMessage) (schema.ChatMessage, error)
}

// LLMSummaryProvider produces a digest by appending a summary directive to the
// outgoing wire and calling the model.
type LLMSummaryProvider struct {
	Model            chatModel
	SummaryDirective string
}

// NewLLMSummaryProvider returns a new LLMSummaryProvider with the given model
// and summary directive.
func NewLLMSummaryProvider(model chatModel, directive string) *LLMSummaryProvider {
	return &LLMSummaryProvider{Model: model, SummaryDirective: directive}
}

// Digest appends SummaryDirective to the wire, calls the model, and returns
// the trimmed response as the digest. It returns ErrEmptyDigest when the
// model returns an empty or whitespace-only summary.
func (p *LLMSummaryProvider) Digest(ctx context.Context, outgoing GenerationHandle) (string, string, error) {
	outgoing.Wire = append(outgoing.Wire, schema.ChatMessage{
		Role:    schema.RoleSystem,
		Content: p.SummaryDirective,
	})
	resp, err := p.Model.Chat(ctx, outgoing.Wire)
	if err != nil {
		return "", "", fmt.Errorf("digest: chat: %w", err)
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return "", "", ErrEmptyDigest
	}
	return summary, SourceLLMSummary, nil
}

// MinimalDigest returns a minimal digest string for the given sequence number.
// It names the generation and points at marshal history.
func MinimalDigest(seq int) string {
	return fmt.Sprintf("generation %d — resumed from marshal history", seq)
}

// SummaryDirective is the prompt appended to the wire when requesting a
// handoff summary from the model. It is the single source of truth for both
// the rollover digest path and the summarizeAndContinue fallback path, so
// the two compaction paths cannot drift.
const SummaryDirective = `Summarize this conversation so the task can continue with no other context. This summary will be the ONLY context available afterwards, so be thorough and specific. Do not call tools; respond with plain text only, covering:

## Current State
The exact original request, what has been completed, what is in progress, and what remains.

## Files & Changes
Files modified (with what changed), files read and why they matter, and important file paths / line numbers.

## Technical Context
Commands that worked and commands that failed (exact commands), key decisions made and why, gotchas discovered, assumptions made.

## Exact Next Steps
Numbered, concrete steps with file paths — "3. Update parseAction in internal/agent/protocol.go to accept the actions array", not "continue implementing".`

const (
	// SourceLLMSummary indicates the digest was produced by an LLM summary.
	SourceLLMSummary = "llm_summary"

	// SourceStructured indicates the digest was produced by a structured
	// extraction process.
	SourceStructured = "structured"

	// SourceMinimal indicates the digest is a minimal placeholder.
	SourceMinimal = "minimal"
)

// ErrEmptyDigest is returned when a digest provider receives an empty or
// whitespace-only summary from the model.
var ErrEmptyDigest = errors.New("empty digest from provider")
