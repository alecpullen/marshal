package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/hooks"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// parseToolArgs splits a tool's raw JSON argument bytes into a Go map and
// the canonical normalized JSON used as the cache key. An empty / null
// argument normalises to {} and produces an empty map.
func parseToolArgs(args json.RawMessage) (map[string]interface{}, json.RawMessage, error) {
	argsMap := map[string]interface{}{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return nil, nil, fmt.Errorf("arguments are not a valid JSON object")
		}
	}
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to normalize arguments")
	}
	return argsMap, normalizedArgs, nil
}

// runPreToolUseHook runs the configured pre_tool_use hooks for toolName.
// The returned args are either the original (when the hook has nothing to
// say) or the rewritten JSON the hook wants the caller to use instead.
// When the hook signals block/halt the original args are still returned so
// the caller can record an accurate audit event for the original call.
func (r *Runner) runPreToolUseHook(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, hooks.Output, error) {
	if r.HookRunner == nil {
		return args, hooks.Output{}, nil
	}
	out, err := r.HookRunner.RunPreToolUse(ctx, hooks.PreToolUseInput{
		Event:     hooks.EventPreToolUse,
		ToolName:  toolName,
		Args:      append(json.RawMessage(nil), args...),
		SessionID: r.State.SessionID(),
		TurnIndex: r.State.TurnIndex(),
		WorkDir:   r.State.WorkingDir,
	})
	if err != nil {
		return args, out, err
	}
	if len(out.Rewrite) > 0 {
		return append(json.RawMessage(nil), out.Rewrite...), out, nil
	}
	return args, out, nil
}

// hookAuditMetadata converts a hooks.Output into a single-element slice
// suitable for AuditEvent.Hooks. Returns nil when no hook actually ran
// (HookCount == 0 and no decision came back), so the audit event is
// indistinguishable from a pre-hooks baseline record.
func hookAuditMetadata(out hooks.Output) []registry.HookMetadata {
	if out.HookCount == 0 && out.Decision == "" && !out.FailedOpen && len(out.Rewrite) == 0 {
		return nil
	}
	return []registry.HookMetadata{{
		Event:      hooks.EventPreToolUse,
		Decision:   string(out.Decision),
		Reason:     out.Reason,
		Rewrote:    len(out.Rewrite) > 0,
		FailedOpen: out.FailedOpen,
	}}
}

// policyLoopResult carries the result of running policy + approval for one
// iteration of the pre_tool_use rewrite loop. Messages is non-empty when the
// caller should return immediately (deny, user-declined, hook error, etc).
type policyLoopResult struct {
	Args           json.RawMessage
	NormalizedArgs json.RawMessage
	Approval       registry.ApprovalState
	Messages       []schema.ChatMessage
}

// handlePolicyDecision runs one iteration of policy + approval for the
// (possibly rewritten) args. The two early-return cases (deny /
// user-declined) are returned as Messages so the caller can short-circuit.
// On an error from approval (context cancel, etc.) it is returned to the
// caller. When the decision is allow, Approval is set to ApprovalNotRequired.
func (r *Runner) handlePolicyDecision(ctx context.Context, tool registry.Tool, toolName string, args json.RawMessage, argsMap map[string]interface{}, normalizedArgs json.RawMessage, decision policy.Decision, reason, toolCallID string) (policyLoopResult, error) {
	approval := registry.ApprovalNotRequired
	switch decision {
	case policy.DecisionDeny:
		event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("denied: %s", reason))
		r.logToolCall(event)
		r.countToolCall(true, false)
		return policyLoopResult{Messages: []schema.ChatMessage{r.buildToolErrorMessage(toolName, "denied by policy: "+reason, toolCallID)}}, nil
	case policy.DecisionConfirm:
		// State.PendingApproval is a single slot; two concurrent approvals
		// would overwrite each other and strand the first caller. Serialize
		// all approval requests on this runner, including those launched
		// from the parallel read-only batch in executeActions.
		r.approvalMu.Lock()
		approved, edited, waitErr := r.requestApproval(ctx, tool, toolName, args, argsMap, reason)
		r.approvalMu.Unlock()
		if waitErr != nil {
			return policyLoopResult{}, waitErr
		}
		if !approved {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, errors.New("denied by user"))
			r.logToolCall(event)
			r.countToolCall(true, false)
			return policyLoopResult{Messages: []schema.ChatMessage{r.buildToolErrorMessage(toolName, "denied by user", toolCallID)}}, nil
		}
		approval = registry.ApprovalApproved
		if edited != "" {
			var nerr error
			if toolName == "shell.run" {
				argsMap["command"] = edited
				if remarshalled, merr := json.Marshal(argsMap); merr == nil {
					args = remarshalled
					normalizedArgs, nerr = normalizeArgs(args)
					if nerr != nil {
						slog.Default().Warn("tool-arg-edit normalize failed", "tool", toolName, "error", nerr)
					}
				} else {
					slog.Default().Warn("tool-arg-edit marshal failed", "tool", toolName, "error", merr)
				}
			} else {
				if !json.Valid([]byte(edited)) {
					return policyLoopResult{}, fmt.Errorf("user-supplied edit for %s is not valid JSON: %q", toolName, edited)
				}
				args = json.RawMessage(edited)
				normalizedArgs, nerr = normalizeArgs(args)
				if nerr != nil {
					return policyLoopResult{}, fmt.Errorf("normalize edited %s args: %w", toolName, nerr)
				}
				updated := map[string]interface{}{}
				if uerr := json.Unmarshal(args, &updated); uerr != nil {
					return policyLoopResult{}, fmt.Errorf("decode edited %s args: %w", toolName, uerr)
				}
				argsMap = updated
				// Re-evaluate policy against the new args. The user has
				// already approved this call, so only a DecisionDeny from
				// the edited args should block execution. DecisionConfirm
				// is treated as "proceed" because the user has already
				// provided consent.
				newDecision, newReason, perr := r.Policy.Evaluate(toolName, argsMap)
				if perr != nil {
					return policyLoopResult{}, fmt.Errorf("policy re-evaluate after edit: %w", perr)
				}
				if newDecision == policy.DecisionDeny {
					return policyLoopResult{Messages: []schema.ChatMessage{
						r.buildToolErrorMessage(toolName, "denied by policy after edit: "+newReason, toolCallID),
					}}, nil
				}
				// DecisionAllow and DecisionConfirm: proceed — the user
				// already confirmed in the approval dialog.
			}
		}
	case policy.DecisionAllow:
		approval = registry.ApprovalNotRequired
	}
	return policyLoopResult{
		Args:           args,
		NormalizedArgs: normalizedArgs,
		Approval:       approval,
	}, nil
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
	toolCallID := action.ToolCallID

	tool, ok := r.Registry.Lookup(toolName)
	if !ok {
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "unknown tool", toolCallID)}, nil
	}

	args := action.Args
	if action.Type == ActionPatch {
		encoded, err := json.Marshal(map[string]string{"patch": action.Content})
		if err != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "failed to encode patch arguments", toolCallID)}, nil
		}
		args = encoded
	}

	argsMap := map[string]interface{}{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "arguments are not a valid JSON object", toolCallID)}, nil
		}
	}

	normalizedArgs, normErr := normalizeArgs(args)
	if normErr != nil {
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "failed to normalize arguments", toolCallID)}, nil
	}

	// Validate before the cache lookup and before policy/approval, so the
	// user is never prompted to approve a call that cannot run and a bad
	// call can never be answered from cache.
	if err := registry.ValidateArgs(tool, args); err != nil {
		r.countToolCall(true, false)
		r.noteInvalidArgs()
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, err.Error(), toolCallID)}, nil
	}

	// Cacheable read-only cache lookup.
	if tool.Cacheable {
		if cached, hit := r.State.GetTurnToolResult(toolName, normalizedArgs); hit {
			r.trackerMu.Lock()
			count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(cached.Content), true)
			r.trackerMu.Unlock()
			logged := cached
			logged.Summary = "(cached) " + logged.Summary
			call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
			event := registry.NewAuditEvent(r.Now(), tool, call, logged, registry.ApprovalNotRequired, nil)
			r.logToolCall(event)
			r.countToolCall(false, true)
			msg := r.buildCachedToolResultMessage(toolName, cached, toolCallID)
			msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
			return []schema.ChatMessage{msg}, nil
		}
	}

	// Bounded pre_tool_use rewrite loop. The hook runs AFTER policy +
	// user approval so the user is always in control of the original call
	// before any silent rewrite takes effect; a Rewrite value forces the
	// next iteration to re-parse, re-evaluate policy, and re-prompt the
	// user. The loop is hard-bounded at one rewrite to prevent a hostile
	// hook from thrashing the tool budget.
	var approval registry.ApprovalState
	var lastHookOut hooks.Output
	var originalApprovedArgs json.RawMessage
	var toolWasRewritten bool
	for rewriteCount := 0; ; rewriteCount++ {
		if rewriteCount > 1 {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "pre_tool_use hook rewrote arguments more than once", toolCallID)}, nil
		}

		argsMap, normalizedArgs, parseErr := parseToolArgs(args)
		if parseErr != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, parseErr.Error(), toolCallID)}, nil
		}

		// Re-validate on every pass: a pre_tool_use hook may rewrite the
		// arguments into any shape after the user approved the original.
		if err := registry.ValidateArgs(tool, args); err != nil {
			r.countToolCall(true, false)
			r.noteInvalidArgs()
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, err.Error(), toolCallID)}, nil
		}

		r.Policy.SetSessionRules(r.State.SessionRules())
		decision, reason, evalErr := r.Policy.Evaluate(toolName, argsMap)
		if evalErr != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, evalErr.Error(), toolCallID)}, nil
		}

		policyResult, err := r.handlePolicyDecision(ctx, tool, toolName, args, argsMap, normalizedArgs, decision, reason, toolCallID)
		if err != nil {
			return nil, err
		}
		if len(policyResult.Messages) > 0 {
			return policyResult.Messages, nil
		}
		args = policyResult.Args
		normalizedArgs = policyResult.NormalizedArgs
		approval = policyResult.Approval

		// Capture the user-approved args before any hook rewrite so the
		// audit event can record what the user approved vs what was
		// actually executed after a rewrite.
		preHookArgs := args
		rewrittenArgs, hookOut, hookErr := r.runPreToolUseHook(ctx, toolName, args)
		lastHookOut = hookOut
		if hookErr != nil {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("blocked by pre_tool_use hook: %s", hookErr.Error()))
			event.Hooks = hookAuditMetadata(hookOut)
			r.logToolCall(event)
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "blocked by pre_tool_use hook: "+hookErr.Error(), toolCallID)}, nil
		}
		if hookOut.Decision == hooks.DecisionBlock {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("blocked by pre_tool_use hook: %s", hookOut.Reason))
			event.Hooks = hookAuditMetadata(hookOut)
			r.logToolCall(event)
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "blocked by pre_tool_use hook: "+hookOut.Reason, toolCallID)}, nil
		}
		if hookOut.Decision == hooks.DecisionHalt {
			return nil, fmt.Errorf("halted by pre_tool_use hook: %s", hookOut.Reason)
		}
		if len(hookOut.Rewrite) > 0 {
			originalApprovedArgs = preHookArgs
			toolWasRewritten = true
			args = rewrittenArgs
			continue
		}
		break
	}

	label := toolName
	if command, ok := argsMap["command"].(string); ok && command != "" {
		label = fmt.Sprintf("%s: %s", toolName, command)
	}
	r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
	r.State.SetActiveToolCall(session.ActiveToolCall{
		Name:      toolName,
		Args:      SummarizeToolArgs(toolName, args),
		Path:      firstPatchPathFromArgs(toolName, args),
		StartedAt: r.Now(),
	})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
	defer r.State.ClearActiveToolCall()

	if r.Snapshotter != nil && r.SnapshotRecorder != nil && tool.Risk != registry.RiskReadOnly {
		files := changedFilesForTool(toolName, argsMap)
		if hash, snapErr := r.Snapshotter.Track(ctx); snapErr == nil && hash != "" {
			if _, saveErr := r.SnapshotRecorder.SaveSnapshot(r.State.SessionID(), r.State.TurnIndex(), hash, files, r.Now()); saveErr != nil {
				r.State.Logger().Warn("failed to record pre-write snapshot", "error", saveErr)
			}
		} else if snapErr != nil {
			r.State.Logger().Warn("pre-write snapshot failed", "error", snapErr)
		}
	}

	if r.WriteGate != nil && tool.Risk != registry.RiskReadOnly {
		release := r.WriteGate.Acquire()
		defer release()
	}

	callID := toolCallID
	if callID == "" {
		callID = fmt.Sprintf("call_%d", r.Now().UnixNano())
	}
	call := registry.ToolCall{ID: callID, Name: toolName, Args: args}
	start := time.Now()
	result, execErr := tool.Handler(ctx, call)
	if execErr != nil {
		event := registry.NewAuditEvent(r.Now(), tool, call, registry.ToolResult{}, approval, execErr)
		event.Duration = time.Since(start)
		event.Hooks = hookAuditMetadata(lastHookOut)
		event.OriginalArgs = originalApprovedArgs
		event.Rewritten = toolWasRewritten
		r.logToolCall(event)
		r.trackerMu.Lock()
		count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(execErr.Error()), false)
		r.trackerMu.Unlock()
		r.countToolCall(true, false)
		msg := r.buildToolErrorMessage(toolName, execErr.Error(), toolCallID)
		msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
		return []schema.ChatMessage{msg}, nil
	}

	summarized := SummarizeToolResult(toolName, result, 0) // per-tool line limits only; 0 keeps the default char cap out of play here
	summarized = spillToolResult(r.State.WorkingDir, toolName, summarized, r.MaxToolResultChars)
	if tool.Cacheable {
		r.State.SetTurnToolResult(toolName, normalizedArgs, summarized)
	}
	event := registry.NewAuditEvent(r.Now(), tool, call, summarized, approval, nil)
	event.Duration = time.Since(start)
	event.Hooks = hookAuditMetadata(lastHookOut)
	event.OriginalArgs = originalApprovedArgs
	event.Rewritten = toolWasRewritten
	r.logToolCall(event)

	msg := r.buildToolResultMessage(toolName, summarized, toolCallID)
	r.trackerMu.Lock()
	count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(summarized.Content), true)
	r.trackerMu.Unlock()
	msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
	r.countToolCall(false, false)
	return []schema.ChatMessage{msg}, nil
}

func (r *Runner) buildToolResultMessage(name string, result registry.ToolResult, toolCallID string) schema.ChatMessage {
	if toolCallID != "" {
		return BuildNativeToolResultMessage(name, result, toolCallID)
	}
	return BuildToolResultMessage(name, result)
}

func (r *Runner) buildCachedToolResultMessage(name string, result registry.ToolResult, toolCallID string) schema.ChatMessage {
	if toolCallID != "" {
		return BuildCachedNativeToolResultMessage(name, result, toolCallID)
	}
	return BuildCachedToolResultMessage(name, result)
}

func (r *Runner) buildToolErrorMessage(name, reason, toolCallID string) schema.ChatMessage {
	if toolCallID != "" {
		return BuildNativeToolErrorMessage(name, reason, toolCallID)
	}
	return BuildToolErrorMessage(name, reason)
}

// normalizeToolName resolves truncated provider tool names (e.g. "read"
// for "file.read") to their fully-qualified registered name. If the name
// is already registered, it passes through unchanged. If the name is not
// registered but exactly one registered tool ends with "."+name, that
// tool's full name is returned. Otherwise the original name is returned
// unchanged (the caller will produce an "unknown tool" error).
func normalizeToolName(reg *registry.Registry, name string) string {
	if reg == nil {
		return name
	}
	if _, ok := reg.Lookup(name); ok {
		return name
	}
	// The tool schema advertised to the model uses toolNameToAlias's dotless
	// alias (see chat.go buildToolDefinitions), so a well-behaved provider
	// echoes the alias back verbatim — reverse it before falling back to the
	// truncated-name heuristic below.
	if canonical, ok := toolAliasMap(reg.List())[name]; ok {
		return canonical
	}
	suffix := "." + name
	var match string
	count := 0
	for _, tool := range reg.List() {
		if strings.HasSuffix(tool.Name, suffix) {
			match = tool.Name
			count++
		}
	}
	if count == 1 {
		return match
	}
	return name
}

func (r *Runner) executeNativeToolCalls(ctx context.Context, calls []schema.ToolCall) ([]schema.ChatMessage, error) {
	r.trackerMu.Lock()
	r.invalidArgsThisRound = 0
	r.trackerMu.Unlock()
	msgs := make([]schema.ChatMessage, 0, len(calls))
	for _, call := range calls {
		call.Name = normalizeToolName(r.Registry, call.Name)
		if call.Name == "ask_user" {
			msg, err := r.executeNativeAskUser(ctx, call)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
			continue
		}
		if call.Name == "question.ask" {
			msg, err := r.executeNativeQuestionAsk(ctx, call)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
			continue
		}
		resultMsgs, err := r.executeToolCall(ctx, ModelAction{
			Type:       ActionToolCall,
			Tool:       call.Name,
			Args:       call.Args,
			ToolCallID: call.ID,
		})
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, resultMsgs...)
	}
	return msgs, nil
}

func (r *Runner) executeNativeAskUser(ctx context.Context, call schema.ToolCall) (schema.ChatMessage, error) {
	if r.role() != RoleGeneral {
		return BuildNativeToolErrorMessage(call.Name, fmt.Sprintf("%s is not available for the %s role", call.Name, r.role()), call.ID), nil
	}
	if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
		return BuildNativeToolErrorMessage(call.Name, "ask_user is not available in auto mode; proceed with your best judgment and state the assumption you made", call.ID), nil
	}
	// executeNativeToolCalls short-circuits on tool name before the agent
	// path's early validation runs, so ask_user/question.ask need an
	// explicit ValidateArgs here — the schema (required, minLength) covers
	// both decode and content checks.
	if tool, ok := r.Registry.Lookup(call.Name); ok {
		if err := registry.ValidateArgs(tool, call.Args); err != nil {
			r.countToolCall(true, false)
			r.noteInvalidArgs()
			return BuildNativeToolErrorMessage(call.Name, err.Error(), call.ID), nil
		}
	}
	var payload struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(call.Args, &payload); err != nil {
		r.countToolCall(true, false)
		return BuildNativeToolErrorMessage(call.Name, "arguments are not valid JSON", call.ID), nil
	}
	answer, waitErr := r.requestAnswer(ctx, payload.Question)
	if waitErr != nil {
		return schema.ChatMessage{}, waitErr
	}
	r.countToolCall(false, false)
	if r.turnBudget != nil {
		r.turnBudget.overhead++
		r.withStats(func(s *turnStats) { s.m.Iterations = r.turnBudget.total() })
	}
	if strings.TrimSpace(answer) == "" {
		r.trackerMu.Lock()
		r.tracker.recordIdle("ask_user declined")
		r.trackerMu.Unlock()
		return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "The user declined to answer. Proceed with your best judgment and state the assumption you made."}, nil
	}
	return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "User answered: " + answer}, nil
}

// executeNativeQuestionAsk handles the new structured question.ask tool,
// which accepts one or more questions with optional options/multi/other.
func (r *Runner) executeNativeQuestionAsk(ctx context.Context, call schema.ToolCall) (schema.ChatMessage, error) {
	if r.role() != RoleGeneral {
		return BuildNativeToolErrorMessage(call.Name, fmt.Sprintf("%s is not available for the %s role", call.Name, r.role()), call.ID), nil
	}
	if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
		return BuildNativeToolErrorMessage(call.Name, "question.ask is not available in auto mode; proceed with your best judgment and state the assumption you made", call.ID), nil
	}
	// executeNativeToolCalls short-circuits on tool name before the agent
	// path's early validation runs, so ask_user/question.ask need an
	// explicit ValidateArgs here — the schema (minItems) covers both
	// decode and array-emptiness checks.
	if tool, ok := r.Registry.Lookup(call.Name); ok {
		if err := registry.ValidateArgs(tool, call.Args); err != nil {
			r.countToolCall(true, false)
			r.noteInvalidArgs()
			return BuildNativeToolErrorMessage(call.Name, err.Error(), call.ID), nil
		}
	}
	var payload struct {
		Questions []session.Question `json:"questions"`
	}
	if err := json.Unmarshal(call.Args, &payload); err != nil {
		r.countToolCall(true, false)
		return BuildNativeToolErrorMessage(call.Name, "arguments are not valid JSON", call.ID), nil
	}
	answers, waitErr := r.requestQuestions(ctx, payload.Questions)
	if waitErr != nil {
		return schema.ChatMessage{}, waitErr
	}
	r.countToolCall(false, false)
	if r.turnBudget != nil {
		r.turnBudget.overhead++
		r.withStats(func(s *turnStats) { s.m.Iterations = r.turnBudget.total() })
	}
	parts := []string{"User answers:"}
	allUnanswered := true
	for _, a := range answers {
		if a.Answer != session.AnswerUnanswered {
			allUnanswered = false
		}
		parts = append(parts, fmt.Sprintf("- %q: %q", a.Question, a.Answer))
	}
	if allUnanswered {
		r.trackerMu.Lock()
		r.tracker.recordIdle("question.ask declined")
		r.trackerMu.Unlock()
		return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "The user declined to answer every question. Proceed with your best judgment."}, nil
	}
	return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: strings.Join(parts, "\n")}, nil
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

// requiresSerialTool is the deny list of tools that share a single
// process-wide slot (today: State.PendingQuestion). They must never run
// concurrently inside executeActions, or two calls will clobber each
// other and leak the inner ResponseChan. They are still admitted by
// allReadOnly; executeActions is responsible for ordering them.
//
// If future question tool aliases are added (for example a renamed
// "question.ask.v2"), every spelling must be added to this switch.
// Adding a new alias without listing it here will reintroduce the
// parallel-batch race on the single PendingQuestion slot.
func requiresSerialTool(name string) bool {
	switch name {
	case "question.ask", "ask_user":
		return true
	}
	return false
}

func (r *Runner) executeActions(ctx context.Context, actions []ModelAction) ([]schema.ChatMessage, error) {
	results := make([][]schema.ChatMessage, len(actions))

	// Phase 1: run any question.ask / ask_user calls one at a time so they
	// can't race on the single PendingQuestion slot. If one errors we still
	// execute the remaining serial tools and substitute an error message
	// for the failed slot so the model sees one Tool message per call.
	var parallelIdx []int
	for i, a := range actions {
		if !requiresSerialTool(a.Tool) {
			parallelIdx = append(parallelIdx, i)
			continue
		}
		msgs, err := r.executeToolCall(ctx, a)
		if err != nil {
			results[i] = []schema.ChatMessage{BuildToolErrorMessage(a.Tool, err.Error())}
			continue
		}
		results[i] = msgs
	}

	// Phase 2: run the remaining read-only tools concurrently as before.
	// Tool errors are recorded as error messages in the appropriate results
	// slot so every tool call produces exactly one result batch.
	if len(parallelIdx) > 0 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, r.MaxParallelActions)
		for _, idx := range parallelIdx {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, act ModelAction) {
				defer wg.Done()
				defer func() { <-sem }()
				msgs, err := r.executeToolCall(ctx, act)
				if err != nil {
					results[i] = []schema.ChatMessage{BuildToolErrorMessage(act.Tool, err.Error())}
					return
				}
				results[i] = msgs
			}(idx, actions[idx])
		}
		wg.Wait()
	}

	var flat []schema.ChatMessage
	for _, msgs := range results {
		flat = append(flat, msgs...)
	}
	return flat, nil
}

// logToolCall stamps the current turn's provider finish reason onto the event
// and records it. Every audit event the runner emits goes through here: the
// events are built at seven sites, and a stamp applied at each of them is a
// stamp that will be forgotten at the eighth.
func (r *Runner) logToolCall(event registry.AuditEvent) {
	if event.FinishReason == "" {
		event.FinishReason = r.turnFinishReason
	}
	r.State.LogToolCall(event)
}
