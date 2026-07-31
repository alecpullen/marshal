package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

type chatResult struct {
	Text         string
	ToolCalls    []schema.ToolCall
	FinishReason string
}

// chatWithRetry calls chatOnce up to MaxRetries+1 times, returning the first
// success. This is the loop's only retry point: transport-level failures
// (connection errors, malformed HTTP responses) are retried; malformed
// model *output* is handled separately in Run via BuildCorrectionMessage; it
// is not retried here because it is not a chatOnce failure — chatOnce
// succeeded, the text just didn't parse as an action.
func (r *Runner) chatWithRetry(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat) (chatResult, error) {
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, true)
}

func (r *Runner) chatWithRetryNoNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat) (chatResult, error) {
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, false)
}

func (r *Runner) chatWithRetryWithNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeTools bool) (chatResult, error) {
	attempts := r.MaxRetries + 1
	var lastErr error
	// best keeps the most substantial partial response seen across attempts,
	// so an exhausted retry loop still hands the caller something to salvage
	// rather than an empty result. See chatOnce's ChatEventError branch.
	var best chatResult
	for i := 0; i < attempts; i++ {
		res, err := r.chatOnce(ctx, p, model, messages, responseFormat, includeNativeTools)
		if err == nil {
			return res, nil
		}
		if len(res.Text) > len(best.Text) {
			best = res
		}
		if !isRetryableChatError(err) {
			return best, err
		}
		lastErr = err
		if i < attempts-1 {
			// Small exponential backoff so a transient provider hiccup does
			// not become a tight retry loop. Base 50ms gives 50ms/100ms/200ms.
			time.Sleep(time.Duration(1<<i) * 50 * time.Millisecond)
		}
	}
	return best, lastErr
}

// isRetryableChatError classifies errors so the retry loop stops wasting
// requests on failures that cannot succeed. Non-retryable errors are:
//   - context cancellation/timeout (the user asked to stop or we ran out of time)
//   - provider 4xx responses, except 429 Too Many Requests which is a rate-limit
//     signal worth retrying.
func isRetryableChatError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pe *provider.ProviderError
	if errors.As(err, &pe) {
		if pe.StatusCode >= 400 && pe.StatusCode < 500 && pe.StatusCode != 429 {
			return false
		}
	}
	return true
}

func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeTools bool) (chatResult, error) {
	if r.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.RequestTimeout)
		defer cancel()
	}

	var tools []schema.ToolDefinition
	if r.NativeTools {
		if includeNativeTools {
			tools = r.buildToolDefinitions()
		}
	}
	// responseFormat is passed in from RunTask (or a caller in the chain)
	// so that per-turn mutations (e.g. JSON-mode escalation after parse
	// failures) do not leak across RunTask calls on the same *Runner.

	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:          model,
		Messages:       messages,
		Stream:         true,
		ResponseFormat: responseFormat,
		Tools:          tools,
	})
	if err != nil {
		return chatResult{}, err
	}

	r.State.BeginStreaming()
	r.State.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: r.Now()})
	defer r.State.EndStreaming()
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	var sb strings.Builder
	var usage *schema.TokenUsage
	var toolCalls []schema.ToolCall
	var finishReason string
	for event := range events {
		switch event.Type {
		case schema.ChatEventDelta:
			if event.Kind == schema.DeltaThinking {
				r.State.AppendThinking(event.Delta)
			} else {
				sb.WriteString(event.Delta)
			}
		case schema.ChatEventError:
			// Return what the stream already delivered. A single undecodable
			// SSE chunk aborts the stream (openai_compatible.go:338), and the
			// deltas before it can amount to a complete, parseable action.
			// Discarding them turns a recoverable hiccup into a failed turn.
			return chatResult{Text: sb.String(), ToolCalls: toolCalls, FinishReason: finishReason}, event.Err
		case schema.ChatEventDone:
			usage = event.Usage
			toolCalls = event.ToolCalls
			finishReason = event.FinishReason
		}
	}
	if r.UsageObserver != nil && usage != nil {
		r.UsageObserver(*usage)
	}
	if r.CalibrationObserver != nil && usage != nil {
		r.CalibrationObserver(messages, usage.PromptTokens)
	}
	if usage != nil {
		r.withStats(func(s *turnStats) {
			s.m.PromptTokens += usage.PromptTokens
			s.m.CompletionTokens += usage.CompletionTokens
			s.m.ReasoningTokens += usage.ReasoningTokens
			s.m.CacheReadTokens += usage.CacheReadTokens
			s.m.CacheWriteTokens += usage.CacheWriteTokens
		})
	}
	return chatResult{Text: sb.String(), ToolCalls: toolCalls, FinishReason: finishReason}, nil
}

func (r *Runner) buildToolDefinitions() []schema.ToolDefinition {
	tools := r.Registry.List()
	// Aliased to satisfy the common OpenAI-compatible function-name
	// constraint (letters/digits/underscore/dash only, no dots) enforced by
	// some providers even though Marshal's own canonical tool names use
	// dots (e.g. "file.read"). See toolalias.go; normalizeToolName in
	// execute.go reverses this when a tool call comes back using the alias.
	nameToAlias := toolNameToAlias(tools)
	deferred := make(map[string]bool)
	for _, t := range r.Registry.ListDeferred() {
		deferred[t.Name] = true
	}
	loaded := make(map[string]bool)
	if r.State != nil {
		for _, name := range r.State.LoadedToolNames() {
			loaded[name] = true
		}
	}
	defs := make([]schema.ToolDefinition, 0, len(tools)+1)
	hasAskUser := false
	for _, tool := range tools {
		// Deferred MCP tools are hidden from the agent's prompt by default
		// and only revealed once the agent explicitly opts in via
		// tools.select. Native tools are never deferred.
		if deferred[tool.Name] && !loaded[tool.Name] {
			continue
		}
		if tool.Name == "ask_user" {
			hasAskUser = true
		}
		parameters := tool.Schema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object"}`)
		}
		defs = append(defs, schema.ToolDefinition{
			Name:        nameToAlias[tool.Name],
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	// Fallback for a registry that doesn't already register the real
	// ask_user tool (internal/tools/native/question.go's askUserTool, wired
	// in native.go). Registering it twice produces two identically-named
	// tool definitions — some providers (Kimi, confirmed live) reject the
	// whole request outright with "function name ask_user is duplicated".
	if r.role() == RoleGeneral && !hasAskUser {
		defs = append(defs, schema.ToolDefinition{
			Name:        "ask_user",
			Description: "Ask the user one specific clarifying question.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`),
		})
	}
	return defs
}

// truncateForLog bounds model output for a single log line. Parse failures log
// the raw text so a malformed response can be diagnosed after the fact; the
// cap keeps a runaway response from flooding the log file.
func truncateForLog(s string) string {
	const cap = 400
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", "\\n")
	if len(s) <= cap {
		return s
	}
	return s[:cap] + "…"
}
