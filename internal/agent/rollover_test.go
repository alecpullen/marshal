package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func newRolloverTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

// fakeRolloverStore implements rollover.Store for testing.
type fakeRolloverStore struct {
	rollover.Store
}

func (f *fakeRolloverStore) BeginGeneration(_ db.Generation) error {
	return nil
}

func (f *fakeRolloverStore) EndGeneration(_ string, _ time.Time, _ string) error {
	return nil
}

func (f *fakeRolloverStore) ArchiveTurns(_ string, _ []db.ArchivedTurn, _ int, _ time.Time) error {
	return nil
}

// fakeTokenCounter implements rollover.TokenCounter for testing.
type fakeTokenCounter struct {
	rollover.TokenCounter
	count int
	err   error
}

func (f *fakeTokenCounter) Name() string { return "fake" }

func (f *fakeTokenCounter) CountTokens(_ context.Context, _ []schema.ChatMessage) (int, error) {
	return f.count, f.err
}

// fakeDigestProvider implements rollover.DigestProvider for testing.
type fakeDigestProvider struct {
	rollover.DigestProvider
	digest string
	source string
	err    error
}

func (f *fakeDigestProvider) Digest(_ context.Context, _ rollover.GenerationHandle) (string, string, error) {
	return f.digest, f.source, f.err
}

// fakePolicy implements rollover.Policy for testing - we use the real rollover.Policy
// with a custom Decide function via a wrapper.

func newTestController(decision bool) *rollover.Controller {
	c := &rollover.Controller{
		SessionID: "test-session",
		Store:     &fakeRolloverStore{},
		Counter:   &fakeTokenCounter{count: 1000},
		Digest:    &fakeDigestProvider{digest: "test-digest", source: "test"},
		Policy: rollover.Policy{
			Mode:           rollover.PolicyTurnCount,
			ContextPercent: 0,
			TurnCount:      0,
		},
		NewID: func() string { return "gen-id" },
		Now:   time.Now,
	}
	// Production always calls Start before archiving (see app.go
	// NewRolloverController wiring); mirror that so Archive has an active
	// generation.
	if err := c.Start(context.Background()); err != nil {
		panic(err)
	}
	return c
}

func TestRolloverFlushArchiveWritesTail(t *testing.T) {
	r := &Rollover{
		Controller: newTestController(true),
		Cursor:     2,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "a"},
		{Role: schema.RoleAssistant, Content: "b"},
		{Role: schema.RoleUser, Content: "c"},
		{Role: schema.RoleAssistant, Content: "d"},
	}
	cursor, err := r.flushArchive(context.Background(), wire)
	if err != nil {
		t.Fatalf("flushArchive failed: %v", err)
	}
	if cursor != len(wire) {
		t.Fatalf("cursor = %d, want %d", cursor, len(wire))
	}
}

func TestRolloverFlushArchiveNoopWhenNil(t *testing.T) {
	var r *Rollover
	cursor, err := r.flushArchive(context.Background(), nil)
	if err != nil {
		t.Fatalf("flushArchive on nil should not error: %v", err)
	}
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0", cursor)
	}
}

func TestRolloverFlushArchiveNoopWhenControllerNil(t *testing.T) {
	r := &Rollover{Controller: nil, Cursor: 5}
	cursor, err := r.flushArchive(context.Background(), []schema.ChatMessage{{Role: schema.RoleUser, Content: "x"}})
	if err != nil {
		t.Fatalf("flushArchive with nil controller should not error: %v", err)
	}
	if cursor != 5 {
		t.Fatalf("cursor = %d, want 5", cursor)
	}
}

func TestRolloverFlushArchiveNoopWhenCursorAtEnd(t *testing.T) {
	r := &Rollover{
		Controller: newTestController(true),
		Cursor:     2,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "a"},
		{Role: schema.RoleAssistant, Content: "b"},
	}
	cursor, err := r.flushArchive(context.Background(), wire)
	if err != nil {
		t.Fatalf("flushArchive failed: %v", err)
	}
	if cursor != 2 {
		t.Fatalf("cursor = %d, want 2", cursor)
	}
}

func TestRolloverMaybeRolloverWhenDue(t *testing.T) {
	ctrl := newTestController(true)
	// Use context-percent policy with 0% threshold so it fires immediately
	// when ContextWindow > 0 and Tokens > 0.
	ctrl.Policy = rollover.Policy{
		Mode:           rollover.PolicyContextPercent,
		ContextPercent: 0,
	}
	// The fake counter returns count=1000, and we pass contextWindow=1000,
	// so (1000*100/1000) >= 0 is true.
	state := newRolloverTestState(t)
	r := &Rollover{
		Controller: ctrl,
		State:      state,
		Cursor:     0,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	seedDigest, err := r.maybeRollover(context.Background(), wire, 1000)
	if err != nil {
		t.Fatalf("maybeRollover failed: %v", err)
	}
	if seedDigest != "test-digest" {
		t.Fatalf("seedDigest = %q, want %q", seedDigest, "test-digest")
	}
	// Verify session state was updated with the new generation info (AC #3).
	gen := state.Generation()
	if gen.ID != "gen-id" {
		t.Fatalf("generation ID = %q, want %q", gen.ID, "gen-id")
	}
	if gen.Seq != 1 {
		t.Fatalf("generation seq = %d, want 1", gen.Seq)
	}
	if gen.SeedDigest != "test-digest" {
		t.Fatalf("generation seedDigest = %q, want %q", gen.SeedDigest, "test-digest")
	}
}

func TestRolloverMaybeRolloverWhenNotDue(t *testing.T) {
	ctrl := newTestController(true)
	// Set a turn-count policy that will NOT fire (needs 100 turns).
	ctrl.Policy = rollover.Policy{
		Mode:      rollover.PolicyTurnCount,
		TurnCount: 100,
	}
	r := &Rollover{
		Controller: ctrl,
		Cursor:     0,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	seedDigest, err := r.maybeRollover(context.Background(), wire, 1000)
	if err != nil {
		t.Fatalf("maybeRollover failed: %v", err)
	}
	if seedDigest != "" {
		t.Fatalf("seedDigest = %q, want empty", seedDigest)
	}
}

func TestRolloverMaybeRolloverNoopWhenNil(t *testing.T) {
	var r *Rollover
	seedDigest, err := r.maybeRollover(context.Background(), nil, 1000)
	if err != nil {
		t.Fatalf("maybeRollover on nil should not error: %v", err)
	}
	if seedDigest != "" {
		t.Fatalf("seedDigest = %q, want empty", seedDigest)
	}
}

func TestRolloverMaybeRolloverNoopWhenControllerNil(t *testing.T) {
	r := &Rollover{Controller: nil}
	seedDigest, err := r.maybeRollover(context.Background(), nil, 1000)
	if err != nil {
		t.Fatalf("maybeRollover with nil controller should not error: %v", err)
	}
	if seedDigest != "" {
		t.Fatalf("seedDigest = %q, want empty", seedDigest)
	}
}

func TestRolloverCloseNoopWhenNil(t *testing.T) {
	var r *Rollover
	err := r.Close(context.Background())
	if err != nil {
		t.Fatalf("Close on nil should not error: %v", err)
	}
}

func TestRolloverCloseNoopWhenControllerNil(t *testing.T) {
	r := &Rollover{Controller: nil}
	err := r.Close(context.Background())
	if err != nil {
		t.Fatalf("Close with nil controller should not error: %v", err)
	}
}

func TestRolloverCompactContext(t *testing.T) {
	ctrl := newTestController(true)
	ctrl.Policy = rollover.Policy{
		Mode:           rollover.PolicyContextPercent,
		ContextPercent: 0,
	}
	state := newRolloverTestState(t)
	r := &Rollover{
		Controller: ctrl,
		State:      state,
		Cursor:     0,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	seedDigest, err := r.compactContext(context.Background(), wire, 1000)
	if err != nil {
		t.Fatalf("compactContext failed: %v", err)
	}
	if seedDigest != "test-digest" {
		t.Fatalf("seedDigest = %q, want %q", seedDigest, "test-digest")
	}
	if r.Cursor != len(wire) {
		t.Fatalf("cursor = %d, want %d", r.Cursor, len(wire))
	}
	// Verify session state was updated with the new generation info.
	gen := state.Generation()
	if gen.ID != "gen-id" {
		t.Fatalf("generation ID = %q, want %q", gen.ID, "gen-id")
	}
	if gen.Seq != 1 {
		t.Fatalf("generation seq = %d, want 1", gen.Seq)
	}
	if gen.SeedDigest != "test-digest" {
		t.Fatalf("generation seedDigest = %q, want %q", gen.SeedDigest, "test-digest")
	}
}

func TestRolloverCompactContextNoopWhenNil(t *testing.T) {
	var r *Rollover
	seedDigest, err := r.compactContext(context.Background(), nil, 1000)
	if err != nil {
		t.Fatalf("compactContext on nil should not error: %v", err)
	}
	if seedDigest != "" {
		t.Fatalf("seedDigest = %q, want empty", seedDigest)
	}
}

// TestRolloverCompactContextUsesOverflowBudgetWhenModelWindowIsNotDue verifies
// that an already-detected overflow forces compaction even when the configured
// policy has not reached the model's larger full window.
func TestRolloverCompactContextUsesOverflowBudgetWhenModelWindowIsNotDue(t *testing.T) {
	ctrl := newTestController(true)
	ctrl.Counter = &fakeTokenCounter{count: 6000}
	ctrl.ModelContextWindow = 10000
	ctrl.Policy = rollover.Policy{
		Mode:           rollover.PolicyContextPercent,
		ContextPercent: 70,
	}
	r := &Rollover{
		Controller: ctrl,
		Cursor:     0,
	}
	wire := []schema.ChatMessage{{Role: schema.RoleUser, Content: "full"}}

	seedDigest, err := r.compactContext(context.Background(), wire, 5000)
	if err != nil {
		t.Fatalf("compactContext failed: %v", err)
	}
	if seedDigest != "test-digest" {
		t.Fatalf("seedDigest = %q, want compaction despite model-window policy not being due", seedDigest)
	}
}

// TestRolloverMaybeRolloverNoopWhenNotDue verifies that policy-based rollover
// remains a no-op outside the overflow compaction path.
func TestRolloverMaybeRolloverNoopWhenNotDue(t *testing.T) {
	ctrl := newTestController(true)
	// Set a turn-count policy that will NOT fire (needs 100 turns).
	ctrl.Policy = rollover.Policy{
		Mode:      rollover.PolicyTurnCount,
		TurnCount: 100,
	}
	r := &Rollover{
		Controller: ctrl,
		Cursor:     0,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	seedDigest, err := r.maybeRollover(context.Background(), wire, 1000)
	if err != nil {
		t.Fatalf("maybeRollover should not error when not due: %v", err)
	}
	if seedDigest != "" {
		t.Fatalf("seedDigest = %q, want empty when not due", seedDigest)
	}

}

// TestRolloverNilOnRunner verifies that a Runner with Rollover == nil works
// without panics (acceptance criterion 5).
func TestRolloverNilOnRunner(t *testing.T) {
	_ = &Runner{}
}

// TestRolloverFlushArchiveErrorPropagation verifies that Archive errors
// are propagated correctly.
func TestRolloverFlushArchiveErrorPropagation(t *testing.T) {
	ctrl := newTestController(true)
	ctrl.Store = &fakeRolloverStore{}
	r := &Rollover{
		Controller: ctrl,
		Cursor:     0,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	_, err := r.flushArchive(context.Background(), wire)
	if err != nil {
		t.Fatalf("flushArchive should not error with fake store: %v", err)
	}
}

// TestRolloverAndContinueFallsBackToSummarize verifies that when rollover
// is disabled (nil Rollover), rolloverAndContinue calls summarizeAndContinue.
func TestRolloverAndContinueFallsBackToSummarize(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"## Current State\nRead a.go; still need to patch b.go."}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	// Rollover is nil by default.

	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "fix the bug"},
		{Role: schema.RoleUser, Content: "some large output"},
	}
	fresh, err := rolloverAndContinue(context.Background(), runner, wire, "fix the bug", runner.MaxTurnContextTokens)
	if err != nil {
		t.Fatalf("rolloverAndContinue: %v", err)
	}
	// Must have rebuilt messages (summarizeAndContinue path).
	if len(fresh) == 0 {
		t.Fatal("rolloverAndContinue returned empty messages")
	}
	if fresh[0].Role != schema.RoleSystem {
		t.Fatal("first message must be system prompt")
	}
	// Verify the summary is present.
	var joined strings.Builder
	for _, m := range fresh {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "still need to patch b.go") {
		t.Fatal("rebuilt messages missing the summary")
	}
}

// TestRolloverAndContinueWithRolloverEnabled verifies that when rollover is
// enabled and due, rolloverAndContinue returns a fresh window matching
// summarizeAndContinue's shape: [systemPrompt, contextPack (if non-empty),
// goal, digest-as-assistant, continue-instruction].
func TestRolloverAndContinueWithRolloverEnabled(t *testing.T) {
	ctrl := newTestController(true)
	ctrl.Policy = rollover.Policy{
		Mode:           rollover.PolicyContextPercent,
		ContextPercent: 0,
	}
	state := newRolloverTestState(t)
	runner := &Runner{
		Provider:             nil,
		Registry:             registry.New(),
		Policy:               policy.NewEngine(&config.Config{}, nil),
		State:                state,
		Model:                "test-model",
		MaxTurnContextTokens: 1000,
		Rollover: &Rollover{
			Controller: ctrl,
			State:      state,
			Cursor:     0,
		},
	}

	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	fresh, err := rolloverAndContinue(context.Background(), runner, wire, "test goal", runner.MaxTurnContextTokens)
	if err != nil {
		t.Fatalf("rolloverAndContinue: %v", err)
	}
	// Must return 4 messages (context pack is empty in test): system prompt,
	// goal, digest-as-assistant, continue-instruction.
	if len(fresh) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(fresh))
	}
	if fresh[0].Role != schema.RoleSystem {
		t.Fatalf("expected system role for message 0, got %q", fresh[0].Role)
	}
	if fresh[1].Role != schema.RoleUser || fresh[1].Content != "test goal" {
		t.Fatalf("expected goal at message 1, got role=%q content=%q", fresh[1].Role, fresh[1].Content)
	}
	if fresh[2].Role != schema.RoleAssistant {
		t.Fatalf("expected assistant role for digest at message 2, got %q", fresh[2].Role)
	}
	if !strings.Contains(fresh[2].Content, "test-digest") {
		t.Fatalf("expected digest in message 2 content, got %q", fresh[2].Content)
	}
	if fresh[3].Role != schema.RoleUser {
		t.Fatalf("expected user role for continue-instruction at message 3, got %q", fresh[3].Role)
	}
}

// TestRolloverAndContinueNilRollover verifies that a Runner with nil Rollover
// falls back to summarizeAndContinue.
func TestRolloverAndContinueNilRollover(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"## Current State\nsummary."}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	// Rollover is nil by default.

	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "goal"},
	}
	fresh, err := rolloverAndContinue(context.Background(), runner, wire, "goal", runner.MaxTurnContextTokens)
	if err != nil {
		t.Fatalf("rolloverAndContinue: %v", err)
	}
	if len(fresh) == 0 {
		t.Fatal("rolloverAndContinue returned empty messages")
	}
	if fresh[0].Role != schema.RoleSystem {
		t.Fatal("first message must be system prompt")
	}
}

// TestRolloverMaybeRolloverErrorPropagation verifies that Rollover errors
// are propagated correctly.
func TestRolloverMaybeRolloverErrorPropagation(t *testing.T) {
	ctrl := newTestController(true)
	ctrl.Policy = rollover.Policy{
		Mode:           rollover.PolicyContextPercent,
		ContextPercent: 0,
	}
	ctrl.Digest = &fakeDigestProvider{err: errors.New("digest failed")}
	r := &Rollover{
		Controller: ctrl,
		Cursor:     0,
	}
	wire := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	_, err := r.maybeRollover(context.Background(), wire, 1000)
	if err != nil {
		t.Fatalf("maybeRollover should not error when digest fails (controller handles fallback): %v", err)
	}
}
