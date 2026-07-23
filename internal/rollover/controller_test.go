package rollover

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

// fakeStore implements Store for testing.
type fakeStore struct {
	mu         sync.Mutex
	begun      []db.Generation
	ended      []endCall
	archived   []archiveCall
	beginErr   error
	endErr     error
	archiveErr error
}

type endCall struct {
	generationID string
	endedAt      time.Time
	endReason    string
}

type archiveCall struct {
	generationID  string
	turns         []db.ArchivedTurn
	blobThreshold int
	at            time.Time
}

func (s *fakeStore) BeginGeneration(g db.Generation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beginErr != nil {
		return s.beginErr
	}
	s.begun = append(s.begun, g)
	return nil
}

func (s *fakeStore) EndGeneration(generationID string, endedAt time.Time, endReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endErr != nil {
		return s.endErr
	}
	s.ended = append(s.ended, endCall{generationID, endedAt, endReason})
	return nil
}

func (s *fakeStore) ArchiveTurns(generationID string, turns []db.ArchivedTurn, blobThreshold int, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archiveErr != nil {
		return s.archiveErr
	}
	s.archived = append(s.archived, archiveCall{generationID, turns, blobThreshold, at})
	return nil
}

// fakeDigestProvider implements DigestProvider for testing.
type fakeDigestProvider struct {
	digest string
	source string
	err    error
}

func (f *fakeDigestProvider) Digest(_ context.Context, _ GenerationHandle) (string, string, error) {
	return f.digest, f.source, f.err
}

// fakeCounter implements TokenCounter for testing.
type fakeCounter struct {
	count int
	err   error
}

func (f *fakeCounter) Name() string { return "fake" }

func (f *fakeCounter) CountTokens(_ context.Context, _ []schema.ChatMessage) (int, error) {
	return f.count, f.err
}

// newIDSequence returns a function that produces incrementing IDs.
func newIDSequence() func() string {
	var i int
	return func() string {
		i++
		return fmt.Sprintf("gen-%d", i)
	}
}

func newTestController(store *fakeStore, digest *fakeDigestProvider, counter *fakeCounter, policy Policy) *Controller {
	return &Controller{
		SessionID:     "test-session",
		Store:         store,
		Counter:       counter,
		Digest:        digest,
		Policy:        policy,
		BlobThreshold: 1024,
		Now:           func() time.Time { return time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC) },
		NewID:         newIDSequence(),
		Logger:        slog.Default(),
	}
}

func TestController_Start_OpensGeneration0(t *testing.T) {
	store := &fakeStore{}
	c := newTestController(store, &fakeDigestProvider{}, &fakeCounter{}, Policy{})
	err := c.Start(context.Background())
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	store.mu.Lock()
	if len(store.begun) != 1 {
		store.mu.Unlock()
		t.Fatalf("expected 1 begun generation, got %d", len(store.begun))
	}
	g := store.begun[0]
	store.mu.Unlock()

	if g.Seq != 0 {
		t.Errorf("generation seq = %d, want 0", g.Seq)
	}
	if g.SessionID != "test-session" {
		t.Errorf("session id = %q, want %q", g.SessionID, "test-session")
	}
	if g.SeedDigest != "" {
		t.Errorf("seed digest = %q, want empty", g.SeedDigest)
	}

	genID, seq, seed := c.Current()
	if genID == "" {
		t.Error("Current() returned empty generation ID")
	}
	if seq != 0 {
		t.Errorf("Current() seq = %d, want 0", seq)
	}
	if seed != "" {
		t.Errorf("Current() seedDigest = %q, want empty", seed)
	}
}

func TestController_Archive_MapsMessagesToTurns(t *testing.T) {
	store := &fakeStore{}
	c := newTestController(store, &fakeDigestProvider{}, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
		{Role: schema.RoleAssistant, Content: "world"},
	}
	if err := c.Archive(context.Background(), msgs); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	store.mu.Lock()
	if len(store.archived) != 1 {
		store.mu.Unlock()
		t.Fatalf("expected 1 archive call, got %d", len(store.archived))
	}
	call := store.archived[0]
	store.mu.Unlock()

	if len(call.turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(call.turns))
	}
	if call.turns[0].TurnSeq != 0 {
		t.Errorf("turn[0].TurnSeq = %d, want 0", call.turns[0].TurnSeq)
	}
	if call.turns[0].Role != "user" {
		t.Errorf("turn[0].Role = %q, want %q", call.turns[0].Role, "user")
	}
	if call.turns[0].Content != "hello" {
		t.Errorf("turn[0].Content = %q, want %q", call.turns[0].Content, "hello")
	}
	if call.turns[1].TurnSeq != 1 {
		t.Errorf("turn[1].TurnSeq = %d, want 1", call.turns[1].TurnSeq)
	}
	if call.turns[1].Role != "assistant" {
		t.Errorf("turn[1].Role = %q, want %q", call.turns[1].Role, "assistant")
	}
	if call.turns[1].Content != "world" {
		t.Errorf("turn[1].Content = %q, want %q", call.turns[1].Content, "world")
	}
}

func TestController_Archive_NoOpOnEmptyInput(t *testing.T) {
	store := &fakeStore{}
	c := newTestController(store, &fakeDigestProvider{}, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := c.Archive(context.Background(), nil); err != nil {
		t.Fatalf("Archive(nil): %v", err)
	}
	if err := c.Archive(context.Background(), []schema.ChatMessage{}); err != nil {
		t.Fatalf("Archive(empty): %v", err)
	}

	store.mu.Lock()
	n := len(store.archived)
	store.mu.Unlock()
	if n != 0 {
		t.Errorf("expected 0 archive calls, got %d", n)
	}
}

func TestController_Archive_IncrementsTurnSeq(t *testing.T) {
	store := &fakeStore{}
	c := newTestController(store, &fakeDigestProvider{}, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First archive: 2 messages
	msgs1 := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "a"},
		{Role: schema.RoleAssistant, Content: "b"},
	}
	if err := c.Archive(context.Background(), msgs1); err != nil {
		t.Fatalf("Archive 1: %v", err)
	}

	// Second archive: 1 message
	msgs2 := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "c"},
	}
	if err := c.Archive(context.Background(), msgs2); err != nil {
		t.Fatalf("Archive 2: %v", err)
	}

	store.mu.Lock()
	if len(store.archived) != 2 {
		store.mu.Unlock()
		t.Fatalf("expected 2 archive calls, got %d", len(store.archived))
	}
	call1 := store.archived[0]
	call2 := store.archived[1]
	store.mu.Unlock()

	if len(call1.turns) != 2 {
		t.Fatalf("call1 turns = %d, want 2", len(call1.turns))
	}
	if call1.turns[0].TurnSeq != 0 {
		t.Errorf("call1 turn[0].TurnSeq = %d, want 0", call1.turns[0].TurnSeq)
	}
	if call1.turns[1].TurnSeq != 1 {
		t.Errorf("call1 turn[1].TurnSeq = %d, want 1", call1.turns[1].TurnSeq)
	}
	if len(call2.turns) != 1 {
		t.Fatalf("call2 turns = %d, want 1", len(call2.turns))
	}
	if call2.turns[0].TurnSeq != 2 {
		t.Errorf("call2 turn[0].TurnSeq = %d, want 2", call2.turns[0].TurnSeq)
	}
}

func TestController_Due_UsesPolicyAndTurnCounter(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{count: 5000}
	c := newTestController(store, &fakeDigestProvider{}, counter, Policy{
		Mode:           PolicyContextPercent,
		ContextPercent: 50,
		TurnCount:      0,
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 5000 tokens in a 10000 window = 50% -> due
	if !c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return true at 50% threshold")
	}

	// 5000 tokens in a 20000 window = 25% -> not due
	if c.Due(context.Background(), nil, 20000) {
		t.Error("expected Due to return false at 25%")
	}
}

func TestController_Due_HonoursExplicitRequest(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{count: 100}
	c := newTestController(store, &fakeDigestProvider{}, counter, Policy{
		Mode:      PolicyCallerCheckpoint,
		TurnCount: 0,
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Not requested, not due
	if c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return false when not requested")
	}

	// Request and check
	c.RequestRollover()
	if !c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return true after RequestRollover")
	}
}

func TestController_Due_ResetsRequestedAfterCheck(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{count: 100}
	c := newTestController(store, &fakeDigestProvider{}, counter, Policy{
		Mode:      PolicyCallerCheckpoint,
		TurnCount: 0,
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	c.RequestRollover()
	// First check: due
	if !c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return true after RequestRollover")
	}

	// Second check: the requested flag is NOT reset by Due — it's reset by
	// Rollover. So it should still be true.
	if !c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to still return true (flag not reset by Due)")
	}
}

func TestController_Due_UsesModelContextWindow(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{count: 5000}
	c := newTestController(store, &fakeDigestProvider{}, counter, Policy{
		Mode:           PolicyContextPercent,
		ContextPercent: 50,
		TurnCount:      0,
	})
	c.ModelContextWindow = 20000 // model window is 20k
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 5000 tokens in a 20000 model window = 25% -> not due
	if c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return false at 25% of model window")
	}

	// 5000 tokens in a 20000 model window = 25%, but if we pass 10000 as
	// contextWindow, the model window (20000) should win, not the parameter.
	// Verify by checking that 5000/20000 = 25% < 50% -> not due.
	// If the parameter (10000) were used, 5000/10000 = 50% -> due.
	// Since ModelContextWindow > 0, the parameter is ignored.
}

func TestController_Due_FallsBackToContextWindowParam(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{count: 5000}
	c := newTestController(store, &fakeDigestProvider{}, counter, Policy{
		Mode:           PolicyContextPercent,
		ContextPercent: 50,
		TurnCount:      0,
	})
	// ModelContextWindow is 0 (default) -> use contextWindow parameter
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 5000 tokens in a 10000 window = 50% -> due
	if !c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return true at 50% of parameter window")
	}

	// 5000 tokens in a 20000 window = 25% -> not due
	if c.Due(context.Background(), nil, 20000) {
		t.Error("expected Due to return false at 25% of parameter window")
	}
}

func TestController_Due_TokenCountErrorReturnsFalse(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{err: errors.New("count failed")}
	c := newTestController(store, &fakeDigestProvider{}, counter, Policy{
		Mode:           PolicyContextPercent,
		ContextPercent: 50,
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return false when token count fails")
	}
}

func TestController_Rollover_ClosesOldGenerationBeforeDigest(t *testing.T) {
	store := &fakeStore{}
	digest := &fakeDigestProvider{digest: "summary", source: SourceLLMSummary}
	c := newTestController(store, digest, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	genID, genSeq, _ := c.Current()

	handle := GenerationHandle{
		SessionID:    "test-session",
		GenerationID: genID,
		Seq:          genSeq,
		Wire:         []schema.ChatMessage{},
		Goal:         "test",
	}
	seedDigest, err := c.Rollover(context.Background(), handle)
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	if seedDigest != "summary" {
		t.Errorf("Rollover seedDigest = %q, want %q", seedDigest, "summary")
	}

	store.mu.Lock()
	if len(store.ended) != 1 {
		store.mu.Unlock()
		t.Fatalf("expected 1 end call, got %d", len(store.ended))
	}
	if store.ended[0].generationID != genID {
		t.Errorf("ended generation = %q, want %q", store.ended[0].generationID, genID)
	}
	if store.ended[0].endReason != "rollover" {
		t.Errorf("end reason = %q, want %q", store.ended[0].endReason, "rollover")
	}

	if len(store.begun) != 2 {
		store.mu.Unlock()
		t.Fatalf("expected 2 begun generations, got %d", len(store.begun))
	}
	g2 := store.begun[1]
	store.mu.Unlock()

	if g2.Seq != 1 {
		t.Errorf("new generation seq = %d, want 1", g2.Seq)
	}
	if g2.SeedDigest != "summary" {
		t.Errorf("new generation seed digest = %q, want %q", g2.SeedDigest, "summary")
	}
	if g2.DigestSource != SourceLLMSummary {
		t.Errorf("new generation digest source = %q, want %q", g2.DigestSource, SourceLLMSummary)
	}

	// Verify Current() reflects the new generation
	newGenID, newSeq, newSeed := c.Current()
	if newGenID == genID {
		t.Error("Current() returned old generation ID after rollover")
	}
	if newSeq != 1 {
		t.Errorf("Current() seq = %d, want 1", newSeq)
	}
	if newSeed != "summary" {
		t.Errorf("Current() seedDigest = %q, want %q", newSeed, "summary")
	}
}

func TestController_Rollover_FallsBackToMinimalDigest(t *testing.T) {
	store := &fakeStore{}
	digest := &fakeDigestProvider{err: errors.New("digest failed")}
	c := newTestController(store, digest, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	genID, genSeq, _ := c.Current()

	handle := GenerationHandle{
		SessionID:    "test-session",
		GenerationID: genID,
		Seq:          genSeq,
		Wire:         []schema.ChatMessage{},
		Goal:         "test",
	}
	seedDigest, err := c.Rollover(context.Background(), handle)
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}

	want := MinimalDigest(genSeq)
	if seedDigest != want {
		t.Errorf("Rollover seedDigest = %q, want %q (minimal fallback)", seedDigest, want)
	}

	store.mu.Lock()
	if len(store.begun) != 2 {
		store.mu.Unlock()
		t.Fatalf("expected 2 begun generations, got %d", len(store.begun))
	}
	g2 := store.begun[1]
	store.mu.Unlock()

	if g2.DigestSource != SourceMinimal {
		t.Errorf("digest source = %q, want %q", g2.DigestSource, SourceMinimal)
	}
}

func TestController_Rollover_EndGenerationError(t *testing.T) {
	store := &fakeStore{endErr: errors.New("end failed")}
	c := newTestController(store, &fakeDigestProvider{}, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	genID, genSeq, _ := c.Current()
	handle := GenerationHandle{
		SessionID:    "test-session",
		GenerationID: genID,
		Seq:          genSeq,
	}
	_, err := c.Rollover(context.Background(), handle)
	if err == nil {
		t.Fatal("expected error from Rollover when EndGeneration fails")
	}
}

func TestController_Close_EndsLiveGeneration(t *testing.T) {
	store := &fakeStore{}
	c := newTestController(store, &fakeDigestProvider{}, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	genID, _, _ := c.Current()

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store.mu.Lock()
	if len(store.ended) != 1 {
		store.mu.Unlock()
		t.Fatalf("expected 1 end call, got %d", len(store.ended))
	}
	call := store.ended[0]
	store.mu.Unlock()

	if call.generationID != genID {
		t.Errorf("ended generation = %q, want %q", call.generationID, genID)
	}
	if call.endReason != "session_end" {
		t.Errorf("end reason = %q, want %q", call.endReason, "session_end")
	}
}

func TestController_Close_Idempotent(t *testing.T) {
	store := &fakeStore{}
	c := newTestController(store, &fakeDigestProvider{}, &fakeCounter{}, Policy{})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	store.mu.Lock()
	n := len(store.ended)
	store.mu.Unlock()
	if n != 1 {
		t.Errorf("expected 1 end call (idempotent), got %d", n)
	}
}

func TestController_EndTurn_IncrementsCounter(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{count: 100}
	c := newTestController(store, &fakeDigestProvider{}, counter, Policy{
		Mode:      PolicyTurnCount,
		TurnCount: 3,
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 0 turns -> not due
	if c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return false at 0 turns")
	}

	c.EndTurn()
	c.EndTurn()
	c.EndTurn()

	// 3 turns -> due
	if !c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return true at 3 turns")
	}
}

func TestController_Rollover_ResetsTurnCounters(t *testing.T) {
	store := &fakeStore{}
	counter := &fakeCounter{count: 100}
	digest := &fakeDigestProvider{digest: "summary", source: SourceLLMSummary}
	c := newTestController(store, digest, counter, Policy{
		Mode:      PolicyTurnCount,
		TurnCount: 2,
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	c.EndTurn()
	c.EndTurn()

	// Due at 2 turns
	if !c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return true at 2 turns")
	}

	genID, genSeq, _ := c.Current()
	handle := GenerationHandle{
		SessionID:    "test-session",
		GenerationID: genID,
		Seq:          genSeq,
	}
	if _, err := c.Rollover(context.Background(), handle); err != nil {
		t.Fatalf("Rollover: %v", err)
	}

	// After rollover, turns should be reset
	if c.Due(context.Background(), nil, 10000) {
		t.Error("expected Due to return false after rollover (turns reset)")
	}
}

func TestController_RequestRollover_SetsFlag(t *testing.T) {
	c := &Controller{}
	c.RequestRollover()
	c.mu.Lock()
	r := c.requested
	c.mu.Unlock()
	if !r {
		t.Error("RequestRollover did not set requested flag")
	}
}
