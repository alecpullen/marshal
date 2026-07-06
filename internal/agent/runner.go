package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

const (
	DefaultMaxToolIterations  = 16
	DefaultMaxRetries         = 2
	DefaultMaxParallelActions = 4
	loopNudgeMessage          = "You appear to be repeating the same step. Either produce a final answer or ask the user for clarification."
	finalizePressureThreshold = 2
	finalizePressureMessage   = "You are near the tool budget. Unless one specific missing fact is required, produce a final answer now using the results you already have."
)

var ErrMaxIterationsExceeded = errors.New("agent: exceeded max tool iterations without a final answer")

// normalizeArgs returns a canonical JSON representation of a tool's
// arguments so that {"b":1,"a":2} and {"a":2,"b":1} share the same
// cache key. Empty arguments normalise to {}.
func normalizeArgs(args json.RawMessage) ([]byte, error) {
	if len(args) == 0 || string(args) == "null" {
		return []byte("{}"), nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

type RouteResolver interface {
	Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error)
}

// WriteGate serialises non-read-only tool execution across concurrently
// running Runners. The swarm sets one shared gate on every role runner so
// that "only one agent may write files at a time" (docs/07 swarm safety)
// holds even if a future orchestration mode overlaps role turns.
type WriteGate interface {
	// Acquire blocks until the gate is free and returns its release func.
	Acquire() (release func())
}

// MemoryProvider supplies durable project memories for injection into the
// context pack at the start of each turn. It returns contextpack.MemoryNote
// (not a type from internal/knowledge) so that internal/agent never needs
// to depend on internal/knowledge (the two packages must not import each
// other; see the Milestone N design doc).
type MemoryProvider interface {
	Memories(projectID int64) ([]contextpack.MemoryNote, error)
}

// Runner drives one agent turn end to end: classify -> (optionally plan) ->
// loop { call the model, parse its action, execute or answer } -> summarise.
// It is the only thing in Marshal that calls Provider.Chat, Registry.Lookup,
// and PolicyEngine.Evaluate together — everything else (TUI, tools,
// registry, policy) stays decoupled and is exercised independently by
// Milestones C-G's own tests.
type UsageObserver func(promptTokens, completionTokens int)

type Runner struct {
	Provider           provider.Provider
	Registry           *registry.Registry
	Policy             *policy.PolicyEngine
	State              *session.State
	Model              string
	RouteResolver      RouteResolver
	MemoryProvider     MemoryProvider
	ProjectID          int64
	Now                func() time.Time
	MaxToolIterations  int
	MaxRetries         int
	RequestTimeout     time.Duration
	ResponseFormat     *schema.ResponseFormat
	MaxParallelActions int
	MaxToolResultChars int
	ForceClass         string // if set, overrides Classify() in Run()
	SkillIndex         *skills.Index

	// Role selects the system-prompt role addendum. Zero value behaves as
	// RoleGeneral, so existing single-agent construction is unchanged.
	// Swarm sub-runners set this to planner/repo_scout/implementer/reviewer.
	Role AgentRole

	// WriteGate serialises non-read-only tool execution. When nil, no
	// serialisation is performed (default single-agent behaviour).
	WriteGate WriteGate

	UsageObserver UsageObserver

	forceClassMu sync.Mutex
	tracker      *progressTracker
	trackerMu    sync.Mutex
}

func NewRunner(p provider.Provider, reg *registry.Registry, pol *policy.PolicyEngine, state *session.State, model string) *Runner {
	return &Runner{
		Provider:           p,
		Registry:           reg,
		Policy:             pol,
		State:              state,
		Model:              model,
		Now:                time.Now,
		MaxToolIterations:  DefaultMaxToolIterations,
		MaxRetries:         DefaultMaxRetries,
		MaxParallelActions: DefaultMaxParallelActions,
		MaxToolResultChars: DefaultMaxToolResultChars,
	}
}

func (r *Runner) SetForceClass(class string) {
	r.forceClassMu.Lock()
	r.ForceClass = class
	r.forceClassMu.Unlock()
}

func (r *Runner) role() AgentRole {
	if r.Role == "" {
		return RoleGeneral
	}
	return r.Role
}

// Run executes one full agent turn for goal. It records the user's message,
// the assistant's plan (if any), every tool call/result, and the final
// answer directly onto r.State, so the TUI's existing transcript/audit-log/
// approval rendering picks all of it up with no TUI changes.
func (r *Runner) Run(ctx context.Context, goal string) error {
	_, err := r.RunTask(ctx, goal)
	return err
}

// RunTask is Run plus access to the finished Task, so orchestrators (the
// swarm) can read a role's final summary and status without re-parsing
// the session transcript.
func (r *Runner) RunTask(ctx context.Context, goal string) (*Task, error) {
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
	r.State.AddMessage(session.RoleUser, goal, session.ContentTypePlain)
	r.State.ClearTurnToolCache()
	r.trackerMu.Lock()
	r.tracker = newProgressTracker()
	r.trackerMu.Unlock()

	task := NewTask(goal, r.Now())
	r.forceClassMu.Lock()
	fc := r.ForceClass
	r.forceClassMu.Unlock()
	if fc != "" {
		task.Class = TaskClass(fc)
	} else {
		task.Class = Classify(goal)
	}
	turnProvider, turnModel, route := r.resolveRoute(task)
	r.mergeMemories(route.ContextBudget.MaxRepoContextTokens)

	messages := []schema.ChatMessage{
		BuildSystemPrompt(r.role(), r.Registry.List(), r.SkillIndex, r.State.ActiveSkills()),
	}
	messages = appendContextPackMessage(messages, r.State.ContextPack())
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})

	if task.Class != ClassQuestion {
		task.Status = TaskStatusPlanning
		planMessages := append(append([]schema.ChatMessage{}, messages...), BuildPlanningPrompt(goal))
		planText, err := r.chatWithRetry(ctx, turnProvider, turnModel, planMessages)
		if err != nil {
			return task, r.fail(task, err)
		}
		task.Plan = splitPlanLines(planText)
		r.State.SetPlan(task.Plan)
		if current := r.State.ContextPack(); !current.IsEmpty() {
			maxTokens := current.TokenUsage.MaxTokens
			if route.ContextBudget.MaxRepoContextTokens > 0 {
				maxTokens = route.ContextBudget.MaxRepoContextTokens
			}
			updatedPack := contextpack.RefreshPlanWithBudget(current, task.Plan, maxTokens, r.Now)
			r.State.SetContextPack(updatedPack)
			messages = []schema.ChatMessage{BuildSystemPrompt(r.role(), r.Registry.List(), r.SkillIndex, r.State.ActiveSkills())}
			messages = appendContextPackMessage(messages, updatedPack)
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
		}
		r.State.AddMessage(session.RoleAssistant, planText, session.ContentTypePlan)
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: "Plan:\n" + planText})
	}

	task.Status = TaskStatusExecuting
	lastRenderedSkills := r.State.ActiveSkills()
	pressureSent := false
	producedValidAction := false
	for iteration := 0; iteration < r.MaxToolIterations; iteration++ {
		r.State.SetToolBudget(session.ToolBudget{Used: iteration, Max: r.MaxToolIterations})

		if !pressureSent && r.MaxToolIterations-iteration <= finalizePressureThreshold {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: finalizePressureMessage})
			r.State.AddMessage(session.RoleSystem, finalizePressureMessage, session.ContentTypePlain)
			pressureSent = true
		}

		currentSkills := r.State.ActiveSkills()
		if skillsChanged(lastRenderedSkills, currentSkills) {
			messages[0] = BuildSystemPrompt(r.role(), r.Registry.List(), r.SkillIndex, currentSkills)
			lastRenderedSkills = currentSkills
		}

		raw, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages)
		if err != nil {
			return task, r.fail(task, err)
		}

		action, parseErr := ParseAction(raw)
		if parseErr != nil {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
			messages = append(messages, BuildCorrectionMessage(parseErr))
			continue
		}
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
		producedValidAction = true

		if inProgress := r.State.InProgress(); inProgress.Reasoning != "" && action.Type != ActionAnswer && action.Type != ActionFinal {
			r.State.LogThinking(session.ThinkingEntry{
				Text:      inProgress.Reasoning,
				Duration:  time.Since(inProgress.StartedAt),
				StartedAt: inProgress.StartedAt,
			})
		}

		if len(action.Actions) > 0 {
			if err := r.allReadOnly(action.Actions); err != nil {
				messages = append(messages, BuildCorrectionMessage(err))
				continue
			}
			resultMsgs, execErr := r.executeActions(ctx, action.Actions)
			if execErr != nil {
				return task, r.fail(task, execErr)
			}
			messages = append(messages, resultMsgs...)
			if finalized, res, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); finalized {
				return res, ferr
			} else if nudge != "" {
				messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: nudge})
				r.State.AddMessage(session.RoleSystem, nudge, session.ContentTypePlain)
			}
			continue
		}

		switch action.Type {
		case ActionAnswer, ActionFinal:
			task.Summary = action.Content
			task.Status = TaskStatusCompleted
			r.State.AddMessageFinal(session.RoleAssistant, action.Content, session.ContentTypeMarkdown)
			return task, nil
		case ActionToolCall, ActionPatch:
			resultMsgs, err := r.executeToolCall(ctx, action)
			if err != nil {
				return task, r.fail(task, err)
			}
			messages = append(messages, resultMsgs...)
			if finalized, res, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); finalized {
				return res, ferr
			} else if nudge != "" {
				messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: nudge})
				r.State.AddMessage(session.RoleSystem, nudge, session.ContentTypePlain)
			}
		default:
			messages = append(messages, BuildCorrectionMessage(fmt.Errorf("unsupported action type %q", action.Type)))
		}
	}

	if producedValidAction {
		if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, reasonExhausted); ferr == nil {
			return res, nil
		}
	}
	task.Status = TaskStatusFailed
	r.State.AddMessage(session.RoleSystem, "Agent stopped: exceeded max tool iterations without a final answer.", session.ContentTypePlain)
	return task, ErrMaxIterationsExceeded
}

// maybeFinalizeOnStall inspects the tracker after a tool execution. On a
// hard stall it forces a final answer via finalize and reports finalized so
// the caller returns immediately. On a soft stall it returns a nudge message
// for the caller to append to its own messages slice (messages is passed by
// value here, so appending inside this helper would not propagate back to
// the loop's slice).
func (r *Runner) maybeFinalizeOnStall(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task) (finalized bool, res *Task, err error, nudge string) {
	r.trackerMu.Lock()
	a := r.tracker.assess()
	r.trackerMu.Unlock()

	switch a {
	case assessHardStall:
		res, ferr := r.finalize(ctx, p, model, messages, task, reasonStalled)
		return true, res, ferr, ""
	case assessStalling:
		return false, task, nil, loopNudgeMessage
	}
	return false, task, nil, ""
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
		r.State.SetActiveRoute(session.RouteInfo{})
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

// mergeMemories injects the project's current durable memories into the
// context pack, if a MemoryProvider is configured. Failures are ignored so a
// missing or unhealthy memory source never blocks a turn.
func (r *Runner) mergeMemories(maxTokenOverride int) {
	if r.MemoryProvider == nil {
		return
	}

	memories, err := r.MemoryProvider.Memories(r.ProjectID)
	if err != nil {
		return
	}

	current := r.State.ContextPack()
	maxTokens := maxTokenOverride
	if maxTokens <= 0 {
		maxTokens = current.TokenUsage.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = contextpack.DefaultMaxTokens
	}
	r.State.SetContextPack(contextpack.MergeMemories(current, memories, maxTokens, r.Now))
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
	r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Agent failed: %s", err.Error()), session.ContentTypePlain)
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
	if r.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.RequestTimeout)
		defer cancel()
	}

	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:          model,
		Messages:       messages,
		Stream:         true,
		ResponseFormat: r.ResponseFormat,
	})
	if err != nil {
		return "", err
	}

	r.State.BeginStreaming()
	r.State.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: r.Now()})
	defer r.State.EndStreaming()
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	var sb strings.Builder
	var usage *schema.TokenUsage
	for event := range events {
		switch event.Type {
		case schema.ChatEventDelta:
			if event.Kind == schema.DeltaThinking {
				r.State.AppendThinking(event.Delta)
			} else {
				sb.WriteString(event.Delta)
			}
		case schema.ChatEventError:
			return "", event.Err
		case schema.ChatEventDone:
			usage = event.Usage
		}
	}
	if r.UsageObserver != nil && usage != nil {
		r.UsageObserver(usage.PromptTokens, usage.CompletionTokens)
	}
	return sb.String(), nil
}

// executeToolCall evaluates policy, blocks for user approval if required,
// executes the tool, logs an audit event, and returns one or more
// schema.ChatMessages to feed the result (or failure reason) back to the
// model. Loop-detection/stall handling is done by the caller (RunTask), not
// here — this only records the call into the progress tracker.
func (r *Runner) executeToolCall(ctx context.Context, action ModelAction) ([]schema.ChatMessage, error) {
	toolName := action.Tool
	if action.Type == ActionPatch {
		toolName = "file.write_patch"
	}

	tool, ok := r.Registry.Lookup(toolName)
	if !ok {
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, "unknown tool")}, nil
	}

	args := action.Args
	if action.Type == ActionPatch {
		encoded, err := json.Marshal(map[string]string{"patch": action.Content})
		if err != nil {
			return []schema.ChatMessage{BuildToolErrorMessage(toolName, "failed to encode patch arguments")}, nil
		}
		args = encoded
	}

	argsMap := map[string]interface{}{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return []schema.ChatMessage{BuildToolErrorMessage(toolName, "arguments are not a valid JSON object")}, nil
		}
	}

	normalizedArgs, normErr := normalizeArgs(args)
	if normErr != nil {
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, "failed to normalize arguments")}, nil
	}

	// Cacheable read-only cache lookup.
	if tool.Cacheable {
		if cached, hit := r.State.GetTurnToolResult(toolName, normalizedArgs); hit {
			r.trackerMu.Lock()
			r.tracker.record(toolName, string(normalizedArgs))
			r.trackerMu.Unlock()
			logged := cached
			logged.Summary = "(cached) " + logged.Summary
			call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
			event := registry.NewAuditEvent(r.Now(), tool, call, logged, registry.ApprovalNotRequired, nil)
			r.State.LogToolCall(event)
			return []schema.ChatMessage{BuildCachedToolResultMessage(toolName, cached)}, nil
		}
	}

	r.Policy.SetSessionRules(r.State.SessionRules())
	decision, reason, err := r.Policy.Evaluate(toolName, argsMap)
	if err != nil {
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, err.Error())}, nil
	}

	approval := registry.ApprovalNotRequired
	switch decision {
	case policy.DecisionDeny:
		event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("denied: %s", reason))
		r.State.LogToolCall(event)
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, "denied by policy: "+reason)}, nil
	case policy.DecisionConfirm:
		approved, edited, waitErr := r.requestApproval(ctx, tool, toolName, args, argsMap, reason)
		if waitErr != nil {
			return nil, waitErr
		}
		if !approved {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, errors.New("denied by user"))
			r.State.LogToolCall(event)
			return []schema.ChatMessage{BuildToolErrorMessage(toolName, "denied by user")}, nil
		}
		approval = registry.ApprovalApproved
		if edited != "" {
			if toolName == "shell.run" {
				argsMap["command"] = edited
				if remarshalled, merr := json.Marshal(argsMap); merr == nil {
					args = remarshalled
					normalizedArgs, _ = normalizeArgs(args)
				}
			} else {
				if json.Valid([]byte(edited)) {
					args = json.RawMessage(edited)
					normalizedArgs, _ = normalizeArgs(args)
					_ = json.Unmarshal(args, &argsMap)
				}
			}
		}
	case policy.DecisionAllow:
		approval = registry.ApprovalNotRequired
	}

	label := toolName
	if command, ok := argsMap["command"].(string); ok && command != "" {
		label = fmt.Sprintf("%s: %s", toolName, command)
	}
	r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
	r.State.SetActiveToolCall(session.ActiveToolCall{
		Name:      toolName,
		Args:      SummarizeToolArgs(toolName, args),
		StartedAt: r.Now(),
	})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
	defer r.State.ClearActiveToolCall()

	if r.WriteGate != nil && tool.Risk != registry.RiskReadOnly {
		release := r.WriteGate.Acquire()
		defer release()
	}

	call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
	result, execErr := tool.Handler(ctx, call)
	if execErr != nil {
		event := registry.NewAuditEvent(r.Now(), tool, call, registry.ToolResult{}, approval, execErr)
		r.State.LogToolCall(event)
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, execErr.Error())}, nil
	}

	summarized := SummarizeToolResult(toolName, result, r.MaxToolResultChars)
	if tool.Cacheable {
		r.State.SetTurnToolResult(toolName, normalizedArgs, summarized)
	}
	event := registry.NewAuditEvent(r.Now(), tool, call, summarized, approval, nil)
	r.State.LogToolCall(event)

	msgs := []schema.ChatMessage{BuildToolResultMessage(toolName, summarized)}
	r.trackerMu.Lock()
	r.tracker.record(toolName, string(normalizedArgs))
	r.trackerMu.Unlock()
	return msgs, nil
}

func (r *Runner) allReadOnly(actions []ModelAction) error {
	for _, a := range actions {
		if a.Type != ActionToolCall {
			return fmt.Errorf("action type %q in actions array is not a tool_call", a.Type)
		}
		tool, ok := r.Registry.Lookup(a.Tool)
		if !ok {
			return fmt.Errorf("unknown tool %q in actions array", a.Tool)
		}
		if tool.Risk != registry.RiskReadOnly {
			return fmt.Errorf("tool %q is read-write, not read-only — actions array only supports read-only tools", a.Tool)
		}
	}
	return nil
}

func (r *Runner) executeActions(ctx context.Context, actions []ModelAction) ([]schema.ChatMessage, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, r.MaxParallelActions)
	results := make([][]schema.ChatMessage, len(actions))

	for i, a := range actions {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, act ModelAction) {
			defer wg.Done()
			defer func() { <-sem }()
			msgs, err := r.executeToolCall(ctx, act)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			results[idx] = msgs
		}(i, a)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	var flat []schema.ChatMessage
	for _, msgs := range results {
		flat = append(flat, msgs...)
	}
	return flat, nil
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
		Schema:       tool.Description,
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	r.State.SetPendingApproval(tc)

	label := fmt.Sprintf("waiting for approval: %s", command)
	r.State.SetActivity(session.Activity{Kind: session.ActivityApproval, Label: label, StartedAt: r.Now()})

	select {
	case decision := <-tc.ResponseChan:
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return false, "", ctx.Err()
	}
}

func skillsChanged(prev, curr []string) bool {
	if len(prev) != len(curr) {
		return true
	}
	prevSet := make(map[string]bool, len(prev))
	for _, s := range prev {
		prevSet[s] = true
	}
	for _, s := range curr {
		if !prevSet[s] {
			return true
		}
	}
	return false
}
