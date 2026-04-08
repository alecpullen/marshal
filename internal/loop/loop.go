package loop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/alecpullen/marshal/internal/config"
)

// Loop manages the executor-critic feedback cycle.
type Loop struct {
	cfg       *config.Config
	executor  *Executor
	critic    *Critic
	git       GitLayer
	compactor *Compactor
	sessionID string
	round     int
	history   []Round
}

// Round represents a single iteration of the loop.
type Round struct {
	Number       int
	ExecutorReq  ExecutorRequest
	ExecutorResp string
	Diff         string
	CriticResp   string
	Verdict      Verdict
	Tokens       TokenUsage
}

// Result is the final outcome of the loop.
type Result struct {
	Status       string // "SUCCESS" | "FAILED" | "EXHAUSTED"
	FinalVerdict *Verdict
	Rounds       []Round
	TotalTokens  TokenUsage
}

// TokenUsage tracks prompt and completion tokens for a round.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

// GitLayer abstracts git operations for the loop.
// M2 uses a mock implementation; M3 implements real git.
type GitLayer interface {
	CreateIsolationBranch(name string) error
	GetDiff() (string, error)
	StageAndCommit(message string) error
	HardResetToHead() error
	DeleteBranch(name string) error
}

// generateSessionID creates a unique session identifier.
func generateSessionID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(b))
}

// New creates a new Loop instance.
func New(cfg *config.Config, executor *Executor, critic *Critic, git GitLayer) *Loop {
	return &Loop{
		cfg:       cfg,
		executor:  executor,
		critic:    critic,
		git:       git,
		sessionID: generateSessionID(),
		round:     0,
		history:   make([]Round, 0),
	}
}

// NewWithCompactor creates a Loop with compaction enabled.
func NewWithCompactor(cfg *config.Config, executor *Executor, critic *Critic, git GitLayer, compactor *Compactor) *Loop {
	l := New(cfg, executor, critic, git)
	l.compactor = compactor
	return l
}

// Run executes the full loop until completion or exhaustion.
func (l *Loop) Run(ctx context.Context, task string) (*Result, error) {
	// Create isolation branch
	branchName := fmt.Sprintf("marshal-session-%s", l.sessionID)
	if err := l.git.CreateIsolationBranch(branchName); err != nil {
		return nil, fmt.Errorf("create isolation branch: %w", err)
	}

	var feedback string
	var compacted bool

	for l.round < l.cfg.Loop.MaxRounds {
		l.round++

		// Check if we should compact before this round
		if !compacted && l.compactor != nil && l.round >= l.cfg.Loop.CompactAfter {
			_, _ = l.compactor.Compact(ctx, l.history)
			compacted = true
		}

		round, err := l.runRound(ctx, task, feedback)
		if err != nil {
			return nil, fmt.Errorf("round %d: %w", l.round, err)
		}

		l.history = append(l.history, *round)

		if round.Verdict.IsPass() {
			return &Result{
				Status:       "SUCCESS",
				FinalVerdict: &round.Verdict,
				Rounds:       l.history,
			}, nil
		}

		// Prepare feedback for next round
		feedback = fmt.Sprintf("Previous attempt failed:\nIssue: %s\nFix needed: %s",
		round.Verdict.Issue, round.Verdict.Fix)
	}

	// Exhausted max rounds
	return &Result{
		Status:       "EXHAUSTED",
		FinalVerdict: &l.history[len(l.history)-1].Verdict,
		Rounds:       l.history,
	}, fmt.Errorf("exhausted max rounds (%d)", l.cfg.Loop.MaxRounds)
}

// runRound executes a single round of executor → diff → critic.
func (l *Loop) runRound(ctx context.Context, task string, priorFeedback string) (*Round, error) {
	// Executor generates code
	execReq := ExecutorRequest{
		Task:          task,
		PriorFeedback: priorFeedback,
	}

	execResp, err := l.executor.Execute(ctx, execReq)
	if err != nil {
		return nil, fmt.Errorf("executor: %w", err)
	}

	// Get diff from git
	diff, err := l.git.GetDiff()
	if err != nil {
		return nil, fmt.Errorf("get diff: %w", err)
	}

	// Critic reviews the diff
	verdict, err := l.critic.Review(ctx, diff, task)
	if err != nil {
		return nil, fmt.Errorf("critic: %w", err)
	}

	return &Round{
		Number:       l.round,
		ExecutorReq:  execReq,
		ExecutorResp: execResp,
		Diff:         diff,
		Verdict:      *verdict,
	}, nil
}
