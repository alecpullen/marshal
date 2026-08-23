package agent

import (
	"strings"
	"testing"

	"marshal/internal/llm/schema"
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
