package native

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/activity"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// PublicProgressTool creates the presentation-only progress capability for a
// single session. It is state-bound by design; child registries must create a
// fresh instance for the child's State.
func PublicProgressTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name:        "progress.update",
		Description: "Publish or revise the concise public progress for this response. Call first in a response, before work tools. This is presentation metadata and does not count as completed work.",
		Schema: json.RawMessage(`{
  "type":"object",
  "properties":{
    "mode":{"type":"string","enum":["begin","revise"]},
    "headline":{"type":"string","minLength":1,"maxLength":160},
    "body":{"type":"string","maxLength":2000},
    "current_action":{"type":"string","maxLength":160},
    "sections":{"type":"array","maxItems":5,"items":{"type":"object","properties":{
      "kind":{"type":"string","enum":["change","evidence","checking","next","work"]},
      "text":{"type":"string","maxLength":600},
      "evidence_refs":{"type":"array","maxItems":8,"items":{"type":"string","minLength":1,"maxLength":128}}
    },"required":["kind","text"],"additionalProperties":false}}
  },
  "required":["mode"],
  "allOf":[{"if":{"properties":{"mode":{"const":"begin"}},"required":["mode"]},"then":{"required":["headline"]}}],
  "additionalProperties":false
}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		receipt, err := ApplyPublicProgress(state, ctx, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{Summary: "public progress updated", Content: fmt.Sprintf("Progress revision %d recorded", receipt.Revision)}, nil
	}
	return tool
}

// ApplyPublicProgress is shared by the runner's dedicated metadata preflight
// and the registered handler. Requiring the response reference in context
// prevents a caller from applying progress without runtime ownership.
func ApplyPublicProgress(state *session.State, ctx context.Context, raw json.RawMessage) (session.PublicProgressReceipt, error) {
	if state == nil {
		return session.PublicProgressReceipt{}, fmt.Errorf("session state unavailable")
	}
	response, ok := activity.FromContext(ctx)
	if !ok || response.RunID == "" || response.ActorID == "" || response.ResponseID == "" {
		return session.PublicProgressReceipt{}, fmt.Errorf("active provider response reference required")
	}
	update, err := activity.DecodeProgress(raw)
	if err != nil {
		return session.PublicProgressReceipt{}, err
	}
	return state.ApplyPublicProgress(response, update)
}
