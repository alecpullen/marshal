package agent

import (
	"context"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
)

// Rollover manages context-window archival and cross-turn generation rollover
// for the agent runner. A nil Rollover or nil Controller is a no-op for all
// methods, so existing runners without rollover support work unchanged.
type Rollover struct {
	Controller *rollover.Controller
	State      *session.State
	Cursor     int
}

// flushArchive writes only the new tail of wire (messages after Cursor) to
// the controller's archive store and advances the cursor. It is a no-op when
// Rollover or its Controller is nil, or when the cursor is already at the
// end of wire.
func (r *Rollover) flushArchive(ctx context.Context, wire []schema.ChatMessage) (int, error) {
	if r == nil {
		return 0, nil
	}
	if r.Controller == nil {
		return r.Cursor, nil
	}
	if r.Cursor >= len(wire) {
		return r.Cursor, nil
	}
	if err := r.Controller.Archive(ctx, wire[r.Cursor:]); err != nil {
		return r.Cursor, fmt.Errorf("flush archive: %w", err)
	}
	r.Cursor = len(wire)
	return r.Cursor, nil
}

// maybeRollover checks whether a generation rollover is due via
// Controller.Due. When due, it calls Controller.Rollover and updates
// session state with the new generation info. It is a no-op when Rollover
// or its Controller is nil, or when the rollover is not due.
func (r *Rollover) rollover(ctx context.Context, wire []schema.ChatMessage, reason string) (string, error) {
	if r == nil || r.Controller == nil {
		return "", nil
	}
	seedDigest, err := r.Controller.Rollover(ctx, rollover.GenerationHandle{
		SessionID: r.Controller.SessionID,
		Wire:      wire,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", reason, err)
	}
	if r.State != nil {
		genID, genSeq, genSeed := r.Controller.Current()
		r.State.BeginGeneration(genID, genSeq, genSeed)
	}
	return seedDigest, nil
}

func (r *Rollover) maybeRollover(ctx context.Context, wire []schema.ChatMessage, contextWindow int) (string, error) {
	if r == nil || r.Controller == nil {
		return "", nil
	}
	if !r.Controller.Due(ctx, wire, contextWindow) {
		return "", nil
	}
	return r.rollover(ctx, wire, "maybe rollover")
}

// compactContext archives the current wire messages and performs a rollover.
// It is called from rolloverAndContinue after the runner has detected an
// overflow. Returns the seed digest if a rollover occurred, or "" when
// rollover is disabled (nil Controller).
//
// Compaction is entered only after the runner has detected that its live wire
// exceeds the per-turn budget. That condition must force a rollover: the
// controller's policy window may be larger than the budget used for this turn.
// The contextWindow parameter is retained for API compatibility.
func (r *Rollover) compactContext(ctx context.Context, wire []schema.ChatMessage, contextWindow int) (string, error) {
	if r == nil || r.Controller == nil {
		return "", nil
	}
	if _, err := r.flushArchive(ctx, wire); err != nil {
		return "", fmt.Errorf("compact context: %w", err)
	}
	return r.rollover(ctx, wire, "compact context")
}

// rolloverAndContinue is the unified intra-turn compaction entry point. When
// rollover is enabled it archives, optionally rolls over, and returns a short
// fresh window matching summarizeAndContinue's shape: [systemPrompt,
// contextPack, goal, digest-as-assistant, continue-instruction]. When rollover
// is disabled it falls back to summarizeAndContinue. When rollover is enabled
// but not due (seedDigest is empty), it also falls back to
// summarizeAndContinue so the prior wire is not silently discarded.
//
// turnThreshold is the per-turn-derived compaction threshold (see
// Runner.effectiveTurnThreshold). It is passed in rather than read from the
// runner so the same threshold that triggered compaction bounds the
// archival decision — otherwise a small-window turn can roll over too
// aggressively and a big-window turn can hold past its real ceiling.
func rolloverAndContinue(ctx context.Context, r *Runner, wire []schema.ChatMessage, goal string, turnThreshold int) ([]schema.ChatMessage, error) {
	if r == nil || r.Rollover == nil || r.Rollover.Controller == nil {
		return r.summarizeAndContinue(ctx, r.Provider, r.Model, wire, goal, r.ResponseFormat)
	}
	seedDigest, err := r.Rollover.compactContext(ctx, wire, turnThreshold)
	if err != nil {
		return nil, fmt.Errorf("rollover and continue: %w", err)
	}
	if seedDigest == "" {
		// Rollover is enabled but not due — fall back to summarizeAndContinue
		// so the prior wire is not silently discarded.
		return r.summarizeAndContinue(ctx, r.Provider, r.Model, wire, goal, r.ResponseFormat)
	}
	// Rebuild the full window matching summarizeAndContinue's shape.
	r.contextPackMsgIndex = -1
	r.emittedSkills = nil
	fresh := []schema.ChatMessage{
		BuildSystemPromptWithAddendum(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools, r.Policy.ApprovalMode(), r.SystemPromptAddendum, r.State.WorkingDir, RenderAgentRoster(r.State.Config), r.State.LoadedToolNames()...),
	}
	fresh = r.setContextPackMessage(fresh, r.State.ContextPack())
	fresh = r.appendSkillBodies(fresh)
	fresh = append(fresh,
		schema.ChatMessage{Role: schema.RoleUser, Content: goal},
		schema.ChatMessage{Role: schema.RoleAssistant, Content: "Progress summary (earlier transcript was compacted to fit the context budget):\n\n" + seedDigest},
		schema.ChatMessage{Role: schema.RoleUser, Content: "Continue the task from that summary. Do not repeat work the summary marks as completed."},
	)
	return fresh, nil
}

// Close ends the live generation on the controller. It is a no-op when
// Rollover or its Controller is nil.
func (r *Rollover) Close(ctx context.Context) error {
	if r == nil || r.Controller == nil {
		return nil
	}
	return r.Controller.Close(ctx)
}
