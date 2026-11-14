package registry

import (
	"context"
	"testing"
)

func TestWorkspaceEffectsStayConservativeAcrossRegistryViews(t *testing.T) {
	for _, effect := range []WorkspaceEffect{WorkspaceEffectUnknown, WorkspaceEffectSessionOnly, WorkspaceEffect(255)} {
		reg := New()
		tool := Tool{Name: "todo.write", Risk: RiskWorkspaceWrite, WorkspaceEffect: effect, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }}
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
		view := WithoutNetwork(reg)
		got, ok := view.Lookup(tool.Name)
		if !ok || got.WorkspaceEffect != effect || got.NeedsWorkspacePreflight() != (effect != WorkspaceEffectSessionOnly) {
			t.Fatalf("effect %d lost or misclassified: %+v", effect, got)
		}
		if _, ok := ReadOnlyView(reg).Lookup(tool.Name); ok {
			t.Fatal("session-only metadata widened a read-only role")
		}
	}
}
