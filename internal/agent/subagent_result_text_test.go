package agent

import (
	"strings"
	"testing"
)

func TestSubagentResultText(t *testing.T) {
	tests := []struct {
		name           string
		id             int64
		label          string
		summary        string
		salvagedReason string
		errText        string
		wantSummary    string
		wantContent    string
	}{
		{
			name:        "failed shape",
			id:          7,
			label:       "build",
			summary:     "some summary",
			errText:     "boom",
			wantSummary: "subagent 7 failed: build",
			wantContent: "subagent 7 (build) failed: boom",
		},
		{
			name:           "stalled salvage",
			id:             7,
			label:          "build",
			summary:        "partial report",
			salvagedReason: "stalled",
			wantSummary:    "subagent 7 completed (salvaged: stalled): build",
			wantContent:    "[note: this subagent ended early (stalled). It kept repeating similar actions without making progress. The report below is partial.]\n\npartial report",
		},
		{
			name:           "exhausted salvage",
			id:             7,
			label:          "build",
			summary:        "partial report",
			salvagedReason: "exhausted",
			wantSummary:    "subagent 7 completed (salvaged: exhausted): build",
			wantContent:    "[note: this subagent ended early (exhausted). It used its full tool-iteration budget — raise [agent] subtask_iterations or the custom agent's max_iterations for a longer run. The report below is partial.]\n\npartial report",
		},
		{
			name:           "unverified salvage",
			id:             7,
			label:          "build",
			summary:        "partial report",
			salvagedReason: "unverified",
			wantSummary:    "subagent 7 completed (salvaged: unverified): build",
			wantContent:    "[note: this subagent ended early (unverified). It finished without completing its verification step. The report below is partial.]\n\npartial report",
		},
		{
			name:           "unknown reason",
			id:             7,
			label:          "build",
			summary:        "partial report",
			salvagedReason: "weird",
			wantSummary:    "subagent 7 completed (salvaged: weird): build",
			wantContent:    "[note: this subagent ended early (weird).  The report below is partial.]\n\npartial report",
		},
		{
			name:        "plain completed",
			id:          7,
			label:       "build",
			summary:     "done",
			wantSummary: "subagent 7 completed: build",
			wantContent: "done",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summaryLine, content := subagentResultText(tt.id, tt.label, tt.summary, tt.salvagedReason, tt.errText)
			if summaryLine != tt.wantSummary {
				t.Errorf("summaryLine = %q, want %q", summaryLine, tt.wantSummary)
			}
			if content != tt.wantContent {
				t.Errorf("content = %q, want %q", content, tt.wantContent)
			}
		})
	}
}

func TestSubagentResultTextSalvageWording(t *testing.T) {
	// The salvage note must not claim every early end is a budget hit; only
	// "exhausted" involves the tool-iteration budget.
	_, stalled := subagentResultText(1, "l", "s", "stalled", "")
	if !strings.Contains(stalled, "ended early (stalled)") {
		t.Errorf("stalled content missing 'ended early (stalled)': %q", stalled)
	}
	if !strings.Contains(stalled, "repeating similar actions") {
		t.Errorf("stalled content missing hint: %q", stalled)
	}
	if strings.Contains(stalled, "iteration budget") {
		t.Errorf("stalled content must not mention iteration budget: %q", stalled)
	}

	_, exhausted := subagentResultText(1, "l", "s", "exhausted", "")
	if !strings.Contains(exhausted, "tool-iteration budget") {
		t.Errorf("exhausted content missing 'tool-iteration budget': %q", exhausted)
	}
	if !strings.Contains(exhausted, "subtask_iterations") {
		t.Errorf("exhausted content missing 'subtask_iterations': %q", exhausted)
	}

	_, unverified := subagentResultText(1, "l", "s", "unverified", "")
	if !strings.Contains(unverified, "verification") {
		t.Errorf("unverified content missing 'verification': %q", unverified)
	}

	_, weird := subagentResultText(1, "l", "s", "weird", "")
	if !strings.Contains(weird, "ended early (weird)") {
		t.Errorf("unknown-reason content missing 'ended early (weird)': %q", weird)
	}
	if strings.Contains(weird, "budget") {
		t.Errorf("unknown-reason content must not mention budget: %q", weird)
	}
}
