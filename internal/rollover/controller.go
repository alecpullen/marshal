// Package rollover provides context-window rollover logic for long
// conversations. This file defines the rollover controller that sequences
// Archive -> EndGeneration -> Digest -> BeginGeneration.
package rollover

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

// Store wraps the database operations needed by the rollover controller.
type Store interface {
	BeginGeneration(g db.Generation) error
	EndGeneration(generationID string, endedAt time.Time, endReason string) error
	ArchiveTurns(generationID string, turns []db.ArchivedTurn, blobThreshold int, at time.Time) error
}

// Controller sequences Archive -> EndGeneration -> Digest -> BeginGeneration.
type Controller struct {
	SessionID          string
	Store              Store
	Counter            TokenCounter
	Digest             DigestProvider
	Policy             Policy
	BlobThreshold      int
	ModelContextWindow int // model's full context window in tokens; when >0, Due uses this instead of the contextWindow parameter
	Now                func() time.Time
	NewID              func() string
	Logger             *slog.Logger

	mu         sync.Mutex
	genID      string
	genSeq     int
	seedDigest string
	turnSeq    int
	turnsInGen int
	requested  bool
	closed     bool
	failed     bool // true if begin failed after EndGeneration; controller is unusable
}

// Start opens generation 0.
func (c *Controller) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.begin(0, "", "")
}

// Current returns the current generation ID, sequence number, and seed digest.
func (c *Controller) Current() (generationID string, seq int, seedDigest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.genID, c.genSeq, c.seedDigest
}

// RequestRollover sets a flag for caller_checkpoint mode.
func (c *Controller) RequestRollover() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requested = true
}

// Archive maps wire messages to turns with increasing seq and stores them.
// It is a no-op on empty input.
func (c *Controller) Archive(ctx context.Context, msgs []schema.ChatMessage) error {
	if len(msgs) == 0 {
		return nil
	}

	c.mu.Lock()
	if c.failed {
		c.mu.Unlock()
		return fmt.Errorf("archive: rollover controller is in a failed state")
	}
	if c.genID == "" {
		c.mu.Unlock()
		return fmt.Errorf("archive: no active generation — call Start or begin before archiving")
	}
	genID := c.genID
	startSeq := c.turnSeq
	c.turnSeq += len(msgs)
	c.turnsInGen += len(msgs)
	c.mu.Unlock()

	turns := make([]db.ArchivedTurn, len(msgs))
	now := c.Now()
	for i, msg := range msgs {
		turns[i] = db.ArchivedTurn{
			TurnSeq:   startSeq + i,
			Role:      string(msg.Role),
			Content:   msg.Content,
			CreatedAt: now,
		}
	}

	return c.Store.ArchiveTurns(genID, turns, c.BlobThreshold, now)
}

// EndTurn increments the turn counter for the current generation.
func (c *Controller) EndTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turnsInGen++
}

// Due reports whether a rollover is due based on policy and current state.
// contextWindow is the per-turn compaction threshold; when
// c.ModelContextWindow > 0, the model's full context window is used
// instead for the percentage calculation.
func (c *Controller) Due(ctx context.Context, wire []schema.ChatMessage, contextWindow int) bool {
	c.mu.Lock()
	if c.failed {
		c.mu.Unlock()
		return false
	}
	turnsInGen := c.turnsInGen
	requested := c.requested
	c.mu.Unlock()

	tokens, err := c.Counter.CountTokens(ctx, wire)
	if err != nil {
		if c.Logger != nil {
			c.Logger.Warn("rollover: token count failed, assuming not due", "error", err)
		}
		return false
	}

	// Use the model's full context window when set; otherwise fall back
	// to the per-turn compaction threshold (contextWindow parameter)
	// as the denominator for the percentage calculation.
	window := contextWindow
	if c.ModelContextWindow > 0 {
		window = c.ModelContextWindow
	}

	sig := Signal{
		TurnsInGeneration: turnsInGen,
		Tokens:            tokens,
		ContextWindow:     window,
		Requested:         requested,
	}
	return Decide(c.Policy, sig)
}

// Rollover closes the old generation, digests it, and opens the next.
func (c *Controller) Rollover(ctx context.Context, h GenerationHandle) (seedDigest string, err error) {
	c.mu.Lock()
	if c.failed {
		c.mu.Unlock()
		return "", fmt.Errorf("rollover: controller is in a failed state")
	}
	genID, genSeq := c.genID, c.genSeq
	c.mu.Unlock()

	// Close the outgoing generation BEFORE asking for a digest (constraint).
	if err := c.Store.EndGeneration(genID, c.Now(), "rollover"); err != nil {
		return "", fmt.Errorf("close generation %s: %w", genID, err)
	}

	// Ask for a digest.
	digest, source, digestErr := c.Digest.Digest(ctx, h)
	if digestErr != nil {
		if c.Logger != nil {
			c.Logger.Warn("rollover: digest failed, using minimal fallback", "error", digestErr)
		}
		digest = MinimalDigest(genSeq)
		source = SourceMinimal
	}

	// Begin the next generation.
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.begin(genSeq+1, digest, source); err != nil {
		c.failed = true
		return "", fmt.Errorf("begin generation %d: %w", genSeq+1, err)
	}
	return digest, nil
}

// Close ends the live generation with reason session_end.
func (c *Controller) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.failed {
		return nil
	}
	c.closed = true
	return c.Store.EndGeneration(c.genID, c.Now(), "session_end")
}

// begin starts a new generation with the given seq, seed digest, and source.
// Must be called with c.mu held. On failure c.genID is cleared and c.failed
// is set so the controller does not keep archiving into a dead generation.
func (c *Controller) begin(seq int, seedDigest, digestSource string) error {
	genID := c.NewID()
	now := c.Now()
	g := db.Generation{
		ID:           genID,
		SessionID:    c.SessionID,
		Seq:          seq,
		StartedAt:    now,
		SeedDigest:   seedDigest,
		DigestSource: digestSource,
	}
	if err := c.Store.BeginGeneration(g); err != nil {
		c.failed = true
		c.genID = ""
		return fmt.Errorf("begin generation: %w", err)
	}
	c.genID = genID
	c.genSeq = seq
	c.seedDigest = seedDigest
	c.turnSeq = 0
	c.turnsInGen = 0
	c.requested = false
	return nil
}
