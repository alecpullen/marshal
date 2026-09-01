package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

func calibrationMsgs() []schema.ChatMessage {
	return []schema.ChatMessage{{Role: schema.RoleUser, Content: strings.Repeat("a", 400)}} // est 100
}

func TestCalibratedEstimateDefaultsToRaw(t *testing.T) {
	r := &Runner{}
	if got := r.calibratedEstimate(calibrationMsgs()); got != 100 {
		t.Fatalf("calibratedEstimate = %d, want 100 with unset ratio", got)
	}
}

func TestNotePromptTokensSetsRatio(t *testing.T) {
	r := &Runner{}
	r.notePromptTokens(calibrationMsgs(), 160) // ratio 1.6
	if got := r.calibratedEstimate(calibrationMsgs()); got != 160 {
		t.Fatalf("calibratedEstimate = %d, want 160 (ratio 1.6)", got)
	}
}

func TestNotePromptTokensClampsRatio(t *testing.T) {
	r := &Runner{}
	r.notePromptTokens(calibrationMsgs(), 10) // 0.1 → clamp 0.5
	if got := r.calibratedEstimate(calibrationMsgs()); got != 50 {
		t.Fatalf("low clamp: calibratedEstimate = %d, want 50", got)
	}
	r.notePromptTokens(calibrationMsgs(), 900) // 9.0 → clamp 2.0
	if got := r.calibratedEstimate(calibrationMsgs()); got != 200 {
		t.Fatalf("high clamp: calibratedEstimate = %d, want 200", got)
	}
}

func TestNotePromptTokensIgnoresNonPositiveReports(t *testing.T) {
	r := &Runner{}
	r.notePromptTokens(calibrationMsgs(), 160)
	r.notePromptTokens(calibrationMsgs(), 0) // ignored
	r.notePromptTokens(nil, 500)             // est 0 → ignored
	if got := r.calibratedEstimate(calibrationMsgs()); got != 160 {
		t.Fatalf("calibratedEstimate = %d, want 160 (ratio unchanged)", got)
	}
}

func TestResetTokenRatioRestoresRawEstimate(t *testing.T) {
	r := &Runner{}
	r.notePromptTokens(calibrationMsgs(), 160)
	r.resetTokenRatio()
	if got := r.calibratedEstimate(calibrationMsgs()); got != 100 {
		t.Fatalf("after reset: calibratedEstimate = %d, want 100", got)
	}
}

// In native-tools mode the tool-definition block travels on the wire with
// every request but is not part of the message list — the estimate must
// include it, or small-window turns trip compaction a turn late.
func TestWireEstimateIncludesToolDefinitionsInNativeMode(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "noop.tool", Description: "does nothing at all", Risk: registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`),
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})

	msgs := calibrationMsgs()
	r := &Runner{Registry: reg, NativeTools: true}
	withDefs := r.calibratedEstimate(msgs)
	withoutDefs := estimateTokens(msgs)
	if withDefs <= withoutDefs {
		t.Fatalf("native-mode estimate %d should exceed the message-only estimate %d", withDefs, withoutDefs)
	}

	// Envelope mode sends no tool definitions: identical estimates.
	r.NativeTools = false
	if got := r.calibratedEstimate(msgs); got != withoutDefs {
		t.Fatalf("envelope-mode estimate = %d, want %d (no tool definitions on the wire)", got, withoutDefs)
	}
}
