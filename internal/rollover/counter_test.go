package rollover

import (
	"context"
	"testing"

	"marshal/internal/llm/schema"
)

func TestEstimatorCounter_Name(t *testing.T) {
	c := EstimatorCounter{}
	if got := c.Name(); got != "estimator" {
		t.Errorf("EstimatorCounter.Name() = %q, want %q", got, "estimator")
	}
}

func TestEstimatorCounter_CountTokens_Empty(t *testing.T) {
	c := EstimatorCounter{}
	n, err := c.CountTokens(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("CountTokens(nil) = %d, want 0", n)
	}

	n, err = c.CountTokens(context.Background(), []schema.ChatMessage{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("CountTokens([]) = %d, want 0", n)
	}
}

func TestEstimatorCounter_CountTokens_SingleMessage(t *testing.T) {
	c := EstimatorCounter{}
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	// "hello" = 5 runes → (5+3)/4 = 2
	n, err := c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("CountTokens = %d, want 2", n)
	}
}

func TestEstimatorCounter_CountTokens_MultipleMessages(t *testing.T) {
	c := EstimatorCounter{}
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "You are a helpful assistant."},
		{Role: schema.RoleUser, Content: "What is the weather?"},
		{Role: schema.RoleAssistant, Content: "I'll check that for you."},
	}
	// "You are a helpful assistant." = 28 runes
	// "What is the weather?" = 20 runes
	// "I'll check that for you." = 24 runes
	// total = 72 → (72+3)/4 = 18
	n, err := c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 18 {
		t.Errorf("CountTokens = %d, want 18", n)
	}
}

func TestEstimatorCounter_CountTokens_Unicode(t *testing.T) {
	c := EstimatorCounter{}
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "Hello, 世界!"},
	}
	// "Hello, 世界!" = 10 runes → (10+3)/4 = 3
	n, err := c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("CountTokens = %d, want 3", n)
	}
}

func TestUsageCounter_Name(t *testing.T) {
	c := NewUsageCounter()
	if got := c.Name(); got != "usage" {
		t.Errorf("UsageCounter.Name() = %q, want %q", got, "usage")
	}
}

func TestUsageCounter_FallsBackToEstimator(t *testing.T) {
	c := NewUsageCounter()
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	// estimator says 2, observed is 0 → returns 2
	n, err := c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("CountTokens = %d, want 2 (estimator fallback)", n)
	}
}

func TestUsageCounter_PrefersObserved(t *testing.T) {
	c := NewUsageCounter()
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello"},
	}
	// estimator says 2, observed is 5 → returns 5
	c.Observe(5)
	n, err := c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("CountTokens = %d, want 5 (observed > estimator)", n)
	}
}

func TestUsageCounter_PrefersEstimatorWhenLarger(t *testing.T) {
	c := NewUsageCounter()
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hello world and more text here"},
	}
	// "hello world and more text here" = 31 runes → (31+3)/4 = 8
	// observed is 3 → returns 8 (estimator is larger)
	c.Observe(3)
	n, err := c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 8 {
		t.Errorf("CountTokens = %d, want 8 (estimator > observed)", n)
	}
}

func TestUsageCounter_ObserveUpdates(t *testing.T) {
	c := NewUsageCounter()
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hi"},
	}
	// estimator says 1 (2 runes → (2+3)/4 = 1)
	// observed 10 → returns 10
	c.Observe(10)
	n, err := c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Errorf("CountTokens = %d, want 10", n)
	}

	// observed 3 → estimator (1) is smaller, returns 3
	c.Observe(3)
	n, err = c.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("CountTokens after Observe(3) = %d, want 3", n)
	}
}

func TestResolveCounter_Usage(t *testing.T) {
	usage := NewUsageCounter()
	c := ResolveCounter("usage", usage)
	if c == nil {
		t.Fatal("ResolveCounter returned nil")
	}
	if c.Name() != "usage" {
		t.Errorf("Name() = %q, want %q", c.Name(), "usage")
	}
	// Verify it is the same *UsageCounter instance
	uc, ok := c.(*UsageCounter)
	if !ok {
		t.Fatal("ResolveCounter(\"usage\", usage) did not return *UsageCounter")
	}
	if uc != usage {
		t.Error("ResolveCounter returned a different *UsageCounter instance")
	}
}

func TestResolveCounter_UsageNil(t *testing.T) {
	c := ResolveCounter("usage", nil)
	if c == nil {
		t.Fatal("ResolveCounter returned nil")
	}
	if c.Name() != "estimator" {
		t.Errorf("Name() = %q, want %q", c.Name(), "estimator")
	}
}

func TestResolveCounter_Estimator(t *testing.T) {
	usage := NewUsageCounter()
	c := ResolveCounter("estimator", usage)
	if c == nil {
		t.Fatal("ResolveCounter returned nil")
	}
	if c.Name() != "estimator" {
		t.Errorf("Name() = %q, want %q", c.Name(), "estimator")
	}
}

func TestResolveCounter_UnknownName(t *testing.T) {
	c := ResolveCounter("unknown", nil)
	if c == nil {
		t.Fatal("ResolveCounter returned nil")
	}
	if c.Name() != "estimator" {
		t.Errorf("Name() = %q, want %q", c.Name(), "estimator")
	}
}
