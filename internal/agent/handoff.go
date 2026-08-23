package agent

import (
	"context"
	"errors"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
)

var errEmptyHandoffSummary = errors.New("agent: handoff summarization returned empty text")

// summarizeAndContinue is the crush-style alternative to destructive
// compaction: request a handoff summary of the oversized transcript, then
// rebuild the working message list from scratch around it so the loop can
// keep working with full instructions intact.
func (r *Runner) summarizeAndContinue(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, goal string, responseFormat *schema.ResponseFormat) ([]schema.ChatMessage, error) {
	req := append(append([]schema.ChatMessage{}, messages...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: rollover.SummaryDirective})
	res, err := r.chatWithRetryNoNativeTools(ctx, p, model, req, responseFormat)
	if err != nil {
		return nil, err
	}
	summary := strings.TrimSpace(res.Text)
	if summary == "" {
		return nil, errEmptyHandoffSummary
	}

	r.contextPackMsgIndex = -1
	r.emittedSkills = nil
	fresh := []schema.ChatMessage{
		BuildSystemPromptWithAddendum(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools, r.Policy.ApprovalMode(), r.SystemPromptAddendum, r.State.WorkingDir, RenderAgentRoster(r.State.Config), r.State.LoadedToolNames()...),
	}
	fresh = r.setContextPackMessage(fresh, r.State.ContextPack())
	fresh = r.appendSkillBodies(fresh)
	fresh = append(fresh,
		schema.ChatMessage{Role: schema.RoleUser, Content: goal},
		schema.ChatMessage{Role: schema.RoleAssistant, Content: "Progress summary (earlier transcript was compacted to fit the context budget):\n\n" + summary},
		schema.ChatMessage{Role: schema.RoleUser, Content: "Continue the task from that summary. Do not repeat work the summary marks as completed."},
	)

	r.State.AddMessage(session.RoleSystem, "Context compacted mid-turn; continuing from a progress summary.", session.ContentTypePlain)
	return fresh, nil
}
