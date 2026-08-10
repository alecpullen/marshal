package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

// defaultReconnectMaxWait bounds the total time the wait-for-connectivity
// loop will spend waiting for a dropped connection to come back before
// giving up and failing the turn with the last network error. Two minutes
// covers typical Wi-Fi roams, VPN reconnects, and sleep/wake cycles without
// hanging a turn forever when the outage is real. ReconnectMaxWait on the
// Runner overrides it.
const defaultReconnectMaxWait = 2 * time.Minute

// reconnectBackoff returns the delay before the i-th reconnect attempt
// (0-based): 1s, 2s, 5s, then 10s capped. Long enough to ride out a
// network blip, short enough that the first retry lands while the
// connection is often still establishing.
func reconnectBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 0:
		return 1 * time.Second
	case attempt == 1:
		return 2 * time.Second
	case attempt == 2:
		return 5 * time.Second
	default:
		return 10 * time.Second
	}
}

type chatResult struct {
	Text         string
	ToolCalls    []schema.ToolCall
	FinishReason string
}

// turnRequestOptions carries the resolved per-turn request limits derived from
// the active route. Nil pointers mean "unknown — leave the field unset on the
// wire request". Populated by RunTask after resolveRoute and reset before the
// turn returns; never shared across turns.
type turnRequestOptions struct {
	maxTokens     *int
	contextWindow *int
	thinking      string
}

func intPtr(v int) *int { return &v }

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
		if isReconnectEligible(ctx, err) {
			// A dropped connection is not a request problem: retrying the
			// normal ladder burns the whole budget in ~150ms, long before a
			// real network blip (Wi-Fi roam, VPN reconnect, sleep/wake)
			// heals. Wait for connectivity instead, resending the same
			// request until it succeeds, the outage outlasts
			// effectiveReconnectMaxWait, or the provider answers with a
			// non-network error (which falls through to the normal ladder
			// below). A per-request DeadlineExceeded with a live parent ctx
			// (device sleep, stalled stream) is also reconnect-eligible.
			res, err = r.waitForConnectivity(ctx, p, model, messages, responseFormat, includeNativeTools, err, &best)
			if err == nil {
				return res, nil
			}
			if isNetworkError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return best, err
			}
			// Connectivity is back but the provider answered with an HTTP or
			// decoding error — handle it like any first-attempt failure.
		}
		if !isRetryableChatError(err) {
			return best, err
		}
		lastErr = err
		if i < attempts-1 {
			// Small exponential backoff so a transient provider hiccup does
			// not become a tight retry loop. Base 50ms gives 50ms/100ms/200ms.
			// Ctx-aware so Esc during the wait stops the turn immediately.
			if serr := r.sleepCtx(ctx, time.Duration(1<<i)*50*time.Millisecond); serr != nil {
				return best, serr
			}
		}
	}
	return best, lastErr
}

// waitForConnectivity resends the same chat request while the provider is
// unreachable, backing off 1s/2s/5s/10s(capped) between attempts, for up
// to effectiveReconnectMaxWait in total. It publishes an
// ActivityReconnecting status while waiting so the user sees "connection
// lost — retrying" instead of a stalled turn. It returns success, the last
// network error when the cap is reached, ctx cancellation when the user
// aborts, or the provider's first non-network error once connectivity is
// back.
func (r *Runner) waitForConnectivity(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeTools bool, firstErr error, best *chatResult) (chatResult, error) {
	started := r.Now()
	if r.State != nil {
		r.State.Logger().Warn("provider connection lost; waiting for connectivity", "error", firstErr, "max_wait", r.effectiveReconnectMaxWait())
	}
	for attempt := 0; ; attempt++ {
		delay := reconnectBackoff(attempt)
		if r.State != nil {
			r.State.SetActivity(session.Activity{
				Kind:      session.ActivityReconnecting,
				Label:     fmt.Sprintf("connection lost — retrying in %s", delay.Round(time.Second)),
				StartedAt: r.Now(),
			})
		}
		if err := r.sleepCtx(ctx, delay); err != nil {
			return *best, err
		}
		res, err := r.chatOnce(ctx, p, model, messages, responseFormat, includeNativeTools)
		if err == nil {
			if r.State != nil {
				r.State.Logger().Info("provider connection restored", "waited", r.Now().Sub(started).Round(time.Second), "attempts", attempt+1)
			}
			return res, nil
		}
		if len(res.Text) > len(best.Text) {
			*best = res
		}
		if !isReconnectEligible(ctx, err) {
			// The network is back; this is a provider answer, not a drop. A
			// DeadlineExceeded here means a fresh per-request timeout fired
			// during this retry — still down, so keep waiting.
			return res, err
		}
		if r.Now().Sub(started)+reconnectBackoff(attempt+1) > r.effectiveReconnectMaxWait() {
			if r.State != nil {
				r.State.Logger().Warn("provider connection did not recover; failing turn", "waited", r.Now().Sub(started).Round(time.Second), "error", err)
			}
			return *best, err
		}
	}
}

// sleepCtx sleeps for d or until ctx is done, whichever comes first, so a
// reconnect wait never outlives an Esc or a turn deadline. Runner.Sleep
// overrides it in tests.
func (r *Runner) sleepCtx(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isNetworkError reports whether err looks like a lost or unreachable
// connection rather than a provider answer: the shapes http.Client.Do
// produces when the network is down (*url.Error wrapping *net.OpError or a
// syscall errno, *net.DNSError) and the EOF family a dropped stream
// surfaces mid-read. HTTP responses (ProviderError, any status) are
// answers — the network demonstrably worked — and context errors mean the
// caller, not the network, ended the request.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pe *provider.ProviderError
	if errors.As(err, &pe) {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	var ne *net.OpError
	if errors.As(err, &ne) {
		return true
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	for _, errno := range []syscall.Errno{
		syscall.ECONNRESET,
		syscall.ECONNREFUSED,
		syscall.ECONNABORTED,
		syscall.ETIMEDOUT,
		syscall.ENETUNREACH,
		syscall.ENETDOWN,
		syscall.EHOSTUNREACH,
		syscall.EPIPE,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

// isReconnectEligible reports whether err should route into
// waitForConnectivity: a network drop, or the per-request chat timeout
// firing while the parent ctx is still alive (device sleep, stalled
// stream — chatOnce's WithTimeout at chatOnce:273 fired, not a user
// cancel). A DeadlineExceeded with a done parent ctx is a turn/esc cancel
// and is not reconnect-eligible.
func isReconnectEligible(ctx context.Context, err error) bool {
	if isNetworkError(err) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
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

// effectiveChatTimeout returns the per-request timeout for the model call
// itself, falling back to a sensible default if r.ChatTimeout is zero. It
// is deliberately independent of approvals and questions, which carry no
// wall-clock timeout — a runner must not have its chat completion silently
// truncated by a separate approval ceiling.
func (r *Runner) effectiveChatTimeout() time.Duration {
	if r.ChatTimeout > 0 {
		return r.ChatTimeout
	}
	return defaultChatTimeout
}

// effectiveReconnectMaxWait returns the reconnect cap to use, falling back
// to defaultReconnectMaxWait if r.ReconnectMaxWait is zero.
func (r *Runner) effectiveReconnectMaxWait() time.Duration {
	if r.ReconnectMaxWait > 0 {
		return r.ReconnectMaxWait
	}
	return defaultReconnectMaxWait
}

func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeTools bool) (chatResult, error) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, r.effectiveChatTimeout())
	defer cancel()

	var tools []schema.ToolDefinition
	if r.NativeTools {
		if includeNativeTools {
			tools = r.buildToolDefinitions()
		}
	}
	// responseFormat is passed in from RunTask (or a caller in the chain)
	// so that per-turn mutations (e.g. JSON-mode escalation after parse
	// failures) do not leak across RunTask calls on the same *Runner.

	// Gate the thinking effort on the provider's reasoning capability: a
	// preset thinking value must not be sent to a backend that does not
	// accept it.
	thinking := r.turnRequestOptions.thinking
	if thinking != "" && !p.Capabilities(ctx).Reasoning {
		thinking = ""
	}
	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:          model,
		Messages:       messages,
		Stream:         true,
		MaxTokens:      r.turnRequestOptions.maxTokens,
		ContextWindow:  r.turnRequestOptions.contextWindow,
		ResponseFormat: responseFormat,
		Tools:          tools,
		Thinking:       thinking,
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
