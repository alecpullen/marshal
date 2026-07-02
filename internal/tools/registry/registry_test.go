package registry

import (
	"context"
	"testing"
)

func TestRiskLevelValidAcceptsDocumentedValues(t *testing.T) {
	for _, risk := range []RiskLevel{
		RiskReadOnly,
		RiskWorkspaceWrite,
		RiskCommand,
		RiskNetwork,
		RiskDestructive,
	} {
		if !risk.Valid() {
			t.Fatalf("%q Valid() = false, want true", risk)
		}
	}
}

func TestRiskLevelValidRejectsUnknownValue(t *testing.T) {
	if RiskLevel("surprise").Valid() {
		t.Fatal(`RiskLevel("surprise").Valid() = true, want false`)
	}
}

func TestToolHandlerSignature(t *testing.T) {
	handler := ToolHandler(func(ctx context.Context, call ToolCall) (ToolResult, error) {
		if call.Name != "example.tool" {
			t.Fatalf("call.Name = %q, want example.tool", call.Name)
		}
		return ToolResult{Summary: "ok"}, nil
	})

	result, err := handler(context.Background(), ToolCall{Name: "example.tool"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.Summary != "ok" {
		t.Fatalf("result.Summary = %q, want ok", result.Summary)
	}
}
