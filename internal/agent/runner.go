package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

const (
	DefaultMaxToolIterations = 8
	DefaultMaxRetries        = 2
)

var ErrMaxIterationsExceeded = errors.New("agent: exceeded max tool iterations without a final answer")

type RouteResolver interface {
	Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error)
}

// Runner drives one agent turn end to end: classify -> (optionally plan) ->
// loop { call the model, parse its action, execute or answer } -> summarise.
// It is the only thing in Marshal that calls Provider.Chat, Registry.Lookup,
// and PolicyEngine.Evaluate together — everything else (TUI, tools,
// registry, policy) stays decoupled and is exercised independently by
// Milestones C-G's own tests.
type Runner struct {
	Provider          provider.Provider
	Registry          *registry.Registry
	Policy            *policy.PolicyEngine
	State             *session.State
	Model             string
	RouteResolver     RouteResolver
	Now               func() time.Time
	MaxToolIterations int
	MaxRetries        int
}

func NewRunner(p provider.Provider, reg *registry.Registry, pol *policy.PolicyEngine, state *session.State, model string) *Runner {
	return &Runner{
		Provider:          p,
		Registry:          reg,
		Policy:            pol,
		State:             state,
		Model:             model,
		Now:               time.Now,
		MaxToolIterations: DefaultMaxToolIterations,
		MaxRetries:        DefaultMaxRetries,
	}
}

// Run executes one full agent turn for goal. It records the user's message,
// the assistant's plan (if any), every tool call/result, and the final
// answer directly onto r.State, so the TUI's existing transcript/audit-log/
// approval rendering picks all of it up with no TUI changes.
func (r *Runner) Run(ctx context.Context, goal string) error {
	r.State.AddMessage(session.RoleUser, goal)

	task := NewTask(goal, r.Now())
	task.Class = Classify(goal)
	turnProvider, turnModel, route := r.resolveRoute(task)

	messages := []schema.ChatMessage{
		BuildSystemPrompt(r.Registry.List()),
	}
	messages = appendContextPackMessage(messages, r.State.ContextPack())
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})

	if task.Class != ClassQuestion {
		task.Status = TaskStatusPlanning
		planMessages := append(append([]schema.ChatMessage{}, messages...), BuildPlanningPrompt(goal))
		planText, err := r.chatWithRetry(ctx, turnProvider, turnModel, planMessages)
		if err != nil {
			return r.fail(task, err)
		}
		task.Plan = splitPlanLines(planText)
		if current := r.State.ContextPack(); !current.IsEmpty() {
			maxTokens := current.TokenUsage.MaxTokens
			if route.ContextBudget.MaxRepoContextTokens > 0 {
				maxTokens = route.ContextBudget.MaxRepoContextTokens
			}
			updatedPack := contextpack.RefreshPlanWithBudget(current, task.Plan, maxTokens, r.Now)
			r.State.SetContextPack(updatedPack)
			messages = []schema.ChatMessage{BuildSystemPrompt(r.Registry.List())}
			messages = appendContextPackMessage(messages, updatedPack)
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
		}
		r.State.AddMessage(session.RoleAssistant, "Plan:\n"+planText)
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: "Plan:\n" + planText})
	}

	task.Status = TaskStatusExecuting
	for iteration := 0; iteration < r.MaxToolIterations; iteration++ {
		raw, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages)
		if err != nil {
			return r.fail(task, err)
		}

		action, parseErr := ParseAction(raw)
		if parseErr != nil {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
			messages = append(messages, BuildCorrectionMessage(parseErr))
			continue
		}
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})

		switch action.Type {
		case ActionAnswer, ActionFinal:
			task.Summary = action.Content
			task.Status = TaskStatusCompleted
			r.State.AddMessage(session.RoleAssistant, action.Content)
			return nil
		case ActionToolCall, ActionPatch:
			resultMsg, err := r.executeToolCall(ctx, action)
			if err != nil {
				return r.fail(task, err)
			}
			messages = append(messages, resultMsg)
		default:
			messages = append(messages, BuildCorrectionMessage(fmt.Errorf("unsupported action type %q", action.Type)))
		}
	}

	task.Status = TaskStatusFailed
	r.State.AddMessage(session.RoleSystem, "Agent stopped: exceeded max tool iterations without a final answer.")
	return ErrMaxIterationsExceeded
}

func (r *Runner) resolveRoute(task *Task) (provider.Provider, string, routing.Route) {
	turnProvider := r.Provider
	turnModel := r.Model
	if r.RouteResolver == nil {
		return turnProvider, turnModel, routing.Route{}
	}

	route, resolvedProvider, err := r.RouteResolver.Resolve(routing.TaskProfile{Class: string(task.Class)})
	if err != nil {
		r.State.SetProviderError(err)
		return turnProvider, turnModel, routing.Route{}
	}
	if resolvedProvider != nil {
		turnProvider = resolvedProvider
	}
	if route.Preset.Model != "" {
		turnModel = route.Preset.Model
	}
	r.State.SetActiveRoute(session.RouteInfo{
		Role:      route.Role,
		Profile:   route.Profile,
		Preset:    route.Preset.Name,
		Provider:  route.Preset.Provider,
		Model:     route.Preset.Model,
		LocalOnly: route.Preset.LocalOnly,
		Legacy:    route.Legacy,
		Active:    true,
	})
	if route.ContextBudget.MaxRepoContextTokens > 0 {
		pack := r.State.ContextPack()
		if !pack.IsEmpty() {
			pack = contextpack.Rebudget(pack, route.ContextBudget.MaxRepoContextTokens, r.Now)
			r.State.SetContextPack(pack)
		}
	}
	return turnProvider, turnModel, route
}

func appendContextPackMessage(messages []schema.ChatMessage, pack contextpack.Pack) []schema.ChatMessage {
	if msg, ok := BuildContextPackMessage(pack); ok {
		return append(messages, msg)
	}
	return messages
}

func (r *Runner) fail(task *Task, err error) error {
	task.Status = TaskStatusFailed
	r.State.SetProviderError(err)
	r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Agent failed: %s", err.Error()))
	return err
}

// chatWithRetry calls chatOnce up to MaxRetries+1 times, returning the first
// success. This is the loop's only retry point: transport-level failures
// (connection errors, malformed HTTP responses) are retried; malformed
// model *output* is handled separately in Run via BuildCorrectionMessage; it
// is not retried here because it is not a chatOnce failure — chatOnce
// succeeded, the text just didn't parse as an action.
func (r *Runner) chatWithRetry(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (string, error) {
	attempts := r.MaxRetries + 1
	var lastErr error
	for i := 0; i < attempts; i++ {
		text, err := r.chatOnce(ctx, p, model, messages)
		if err == nil {
			return text, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (string, error) {
	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for event := range events {
		switch event.Type {
		case schema.ChatEventDelta:
			sb.WriteString(event.Delta)
		case schema.ChatEventError:
			return "", event.Err
		case schema.ChatEventDone:
			return sb.String(), nil
		}
	}
	return sb.String(), nil
}

// executeToolCall evaluates policy, blocks for user approval if required,
// executes the tool, logs an audit event, and returns the schema.ChatMessage
// to feed the result (or failure reason) back to the model.
func (r *Runner) executeToolCall(ctx context.Context, action ModelAction) (schema.ChatMessage, error) {
	toolName := action.Tool
	if action.Type == ActionPatch {
		toolName = "file.write_patch"
	}

	tool, ok := r.Registry.Lookup(toolName)
	if !ok {
		return BuildToolErrorMessage(toolName, "unknown tool"), nil
	}

	args := action.Args
	if action.Type == ActionPatch {
		encoded, err := json.Marshal(map[string]string{"patch": action.Content})
		if err != nil {
			return BuildToolErrorMessage(toolName, "failed to encode patch arguments"), nil
		}
		args = encoded
	}

	argsMap := map[string]interface{}{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return BuildToolErrorMessage(toolName, "arguments are not a valid JSON object"), nil
		}
	}

	r.Policy.SetSessionRules(r.State.SessionRules())
	decision, reason, err := r.Policy.Evaluate(toolName, argsMap)
	if err != nil {
		return BuildToolErrorMessage(toolName, err.Error()), nil
	}

	approval := registry.ApprovalNotRequired
	switch decision {
	case policy.DecisionDeny:
		event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("denied: %s", reason))
		r.State.LogToolCall(event)
		return BuildToolErrorMessage(toolName, "denied by policy: "+reason), nil
	case policy.DecisionConfirm:
		approved, edited, waitErr := r.requestApproval(ctx, tool, toolName, args, argsMap, reason)
		if waitErr != nil {
			return schema.ChatMessage{}, waitErr
		}
		if !approved {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, errors.New("denied by user"))
			r.State.LogToolCall(event)
			return BuildToolErrorMessage(toolName, "denied by user"), nil
		}
		approval = registry.ApprovalApproved
		if edited != "" {
			argsMap["command"] = edited
			if remarshalled, merr := json.Marshal(argsMap); merr == nil {
				args = remarshalled
			}
		}
	case policy.DecisionAllow:
		approval = registry.ApprovalNotRequired
	}

	call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
	result, execErr := tool.Handler(ctx, call)
	event := registry.NewAuditEvent(r.Now(), tool, call, result, approval, execErr)
	r.State.LogToolCall(event)
	if execErr != nil {
		return BuildToolErrorMessage(toolName, execErr.Error()), nil
	}
	return BuildToolResultMessage(toolName, result), nil
}

// requestApproval blocks until the TUI (or any caller driving
// session.PendingToolCall) resolves the pending approval, or ctx is
// cancelled. It follows the exact protocol internal/app/tui/model.go already
// implements for Milestone F/G: set PendingApproval, wait on ResponseChan,
// clear PendingApproval.
func (r *Runner) requestApproval(ctx context.Context, tool registry.Tool, toolName string, args json.RawMessage, argsMap map[string]interface{}, reason string) (approved bool, edited string, err error) {
	command, _ := argsMap["command"].(string)
	if command == "" {
		command = toolName
	}

	diff := ""
	if toolName == "file.write_patch" {
		if patchText, ok := argsMap["patch"].(string); ok {
			if preview, previewErr := PreviewPatchDiff(r.State.WorkingDir, patchText); previewErr == nil {
				diff = preview
			}
		}
	}

	tc := &session.PendingToolCall{
		ID:           fmt.Sprintf("call_%d", r.Now().UnixNano()),
		Name:         toolName,
		Args:         string(args),
		Command:      command,
		Risk:         string(tool.Risk),
		Reason:       reason,
		Diff:         diff,
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	r.State.SetPendingApproval(tc)

	select {
	case decision := <-tc.ResponseChan:
		r.State.SetPendingApproval(nil)
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		r.State.SetPendingApproval(nil)
		return false, "", ctx.Err()
	}
}
