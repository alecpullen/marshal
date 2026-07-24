package rollover

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/llm/schema"
)

// fakeChatModel implements chatModel for testing.
type fakeChatModel struct {
	response schema.ChatMessage
	err      error
}

func (f *fakeChatModel) Chat(_ context.Context, _ []schema.ChatMessage) (schema.ChatMessage, error) {
	return f.response, f.err
}

func TestLLMSummaryProvider_New(t *testing.T) {
	p := NewLLMSummaryProvider(&fakeChatModel{}, "summarise this")
	if p == nil {
		t.Fatal("NewLLMSummaryProvider returned nil")
	}
	if p.SummaryDirective != "summarise this" {
		t.Errorf("SummaryDirective = %q, want %q", p.SummaryDirective, "summarise this")
	}
}

func TestLLMSummaryProvider_Digest_AppendsDirective(t *testing.T) {
	// The provider must append the directive to the wire before calling the
	// model. We verify this by checking that the model receives a wire with
	// the directive appended.
	var captured []schema.ChatMessage
	model := &capturingModel{capture: func(msgs []schema.ChatMessage) {
		captured = msgs
	}}
	p := NewLLMSummaryProvider(model, "summarise this")
	handle := GenerationHandle{
		Wire: []schema.ChatMessage{
			{Role: schema.RoleUser, Content: "hello"},
		},
	}
	_, _, err := p.Digest(context.Background(), handle)
	if err != nil {
		t.Fatalf("Digest returned error: %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("model received %d messages, want 2", len(captured))
	}
	if captured[0].Role != schema.RoleUser || captured[0].Content != "hello" {
		t.Errorf("first message = %+v, want {Role:user Content:hello}", captured[0])
	}
	if captured[1].Role != schema.RoleSystem || captured[1].Content != "summarise this" {
		t.Errorf("second message = %+v, want {Role:system Content:summarise this}", captured[1])
	}
}

func TestLLMSummaryProvider_Digest_ReturnsTrimmedSummary(t *testing.T) {
	model := &fakeChatModel{
		response: schema.ChatMessage{Content: "  this is a summary  "},
	}
	p := NewLLMSummaryProvider(model, "summarise")
	digest, source, err := p.Digest(context.Background(), GenerationHandle{})
	if err != nil {
		t.Fatalf("Digest returned error: %v", err)
	}
	if digest != "this is a summary" {
		t.Errorf("digest = %q, want %q", digest, "this is a summary")
	}
	if source != SourceLLMSummary {
		t.Errorf("source = %q, want %q", source, SourceLLMSummary)
	}
}

func TestLLMSummaryProvider_Digest_RejectsEmptySummary(t *testing.T) {
	model := &fakeChatModel{
		response: schema.ChatMessage{Content: "   "},
	}
	p := NewLLMSummaryProvider(model, "summarise")
	_, _, err := p.Digest(context.Background(), GenerationHandle{})
	if !errors.Is(err, ErrEmptyDigest) {
		t.Errorf("Digest error = %v, want %v", err, ErrEmptyDigest)
	}
}

func TestLLMSummaryProvider_Digest_RejectsEmptyContent(t *testing.T) {
	model := &fakeChatModel{
		response: schema.ChatMessage{Content: ""},
	}
	p := NewLLMSummaryProvider(model, "summarise")
	_, _, err := p.Digest(context.Background(), GenerationHandle{})
	if !errors.Is(err, ErrEmptyDigest) {
		t.Errorf("Digest error = %v, want %v", err, ErrEmptyDigest)
	}
}

func TestLLMSummaryProvider_Digest_PropagatesChatError(t *testing.T) {
	boom := errors.New("chat failed")
	model := &fakeChatModel{err: boom}
	p := NewLLMSummaryProvider(model, "summarise")
	_, _, err := p.Digest(context.Background(), GenerationHandle{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("Digest error = %v, want wrapped %v", err, boom)
	}
}

func TestMinimalDigest(t *testing.T) {
	got := MinimalDigest(3)
	want := "generation 3 — resumed from marshal history"
	if got != want {
		t.Errorf("MinimalDigest(3) = %q, want %q", got, want)
	}
}

func TestMinimalDigest_Zero(t *testing.T) {
	got := MinimalDigest(0)
	want := "generation 0 — resumed from marshal history"
	if got != want {
		t.Errorf("MinimalDigest(0) = %q, want %q", got, want)
	}
}

func TestSourceConstants(t *testing.T) {
	if SourceLLMSummary != "llm_summary" {
		t.Errorf("SourceLLMSummary = %q, want %q", SourceLLMSummary, "llm_summary")
	}
	if SourceStructured != "structured" {
		t.Errorf("SourceStructured = %q, want %q", SourceStructured, "structured")
	}
	if SourceMinimal != "minimal" {
		t.Errorf("SourceMinimal = %q, want %q", SourceMinimal, "minimal")
	}
}

func TestErrEmptyDigest(t *testing.T) {
	if ErrEmptyDigest == nil {
		t.Fatal("ErrEmptyDigest is nil")
	}
	if ErrEmptyDigest.Error() != "empty digest from provider" {
		t.Errorf("ErrEmptyDigest.Error() = %q, want %q", ErrEmptyDigest.Error(), "empty digest from provider")
	}
}

// capturingModel records the messages passed to Chat for verification.
type capturingModel struct {
	capture func([]schema.ChatMessage)
}

func (m *capturingModel) Chat(_ context.Context, msgs []schema.ChatMessage) (schema.ChatMessage, error) {
	m.capture(msgs)
	return schema.ChatMessage{Content: "summary"}, nil
}
