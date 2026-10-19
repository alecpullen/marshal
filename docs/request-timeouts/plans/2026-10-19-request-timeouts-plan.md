# Request timeouts — implementation plan

Date: 2026-10-19.
Base: `main` at `2e8c5453`.
Spec: [../feature-spec.md](../feature-spec.md).
Authoring: `marshal-writing-plans`. Execution: `marshal-executing-plans`.

## Goal

Replace the single total per-request deadline in `chatOnceAttempt` with two independent bounds — an idle bound that any streamed delta resets, and a hard ceiling — and make each produce a distinguishable error so a slow model is no longer misclassified as a dead socket and retried for another five minutes.

## Non-goals

- Cross-step narration-repetition detection. The 11 verbatim repeats in the failing turn were across separate steps, invisible to the within-stream detector, and `hard_stalls = 0` because the tool calls varied. Separate follow-up.
- Loop-detector threshold changes.
- Salvaging reasoning text on ceiling.
- Per-adapter read deadlines.

## Assumptions

- Defaults: 120s idle, 900s ceiling.
- The watchdog uses `r.Now()` for consistency with the rest of the runner; tests inject short durations against the real clock.
- The ceiling is a hard failure, not a salvage.

## Contracts

Declarations used by more than one task. Every task below may assume these exist.

```go
// internal/agent/runner.go
const defaultIdleTimeout = 2 * time.Minute
const defaultRequestCeiling = 15 * time.Minute

type Runner struct {
	// ... existing fields ...
	IdleTimeout    time.Duration
	RequestCeiling time.Duration
}

func (r *Runner) effectiveIdleTimeout() time.Duration
func (r *Runner) effectiveRequestCeiling() time.Duration
```

```go
// internal/agent/chat.go
var errIdleTimeout = errors.New("stream idle timeout")
var errRequestCeiling = errors.New("request ceiling exceeded")

func (r *Runner) watchIdle(ctx context.Context, cancel context.CancelCauseFunc, lastEvent *atomic.Int64, done <-chan struct{})
func idleCheckInterval(idle time.Duration) time.Duration
func (r *Runner) classifyStreamError(ctx context.Context, model string, err error) error
```

```go
// internal/app/config/types.go
type AgentConfig struct {
	// ... existing fields ...
	IdleTimeoutSeconds    int `toml:"idle_timeout_seconds"`
	RequestCeilingSeconds int `toml:"request_ceiling_seconds"`
}

// internal/app/config/file_types.go
type fileAgent struct {
	// ... existing fields ...
	IdleTimeoutSeconds    *int `toml:"idle_timeout_seconds"`
	RequestCeilingSeconds *int `toml:"request_ceiling_seconds"`
}
```

## Task 1: Runner timeout fields and accessors

**Goal:** `Runner` exposes `IdleTimeout` and `RequestCeiling` with effective accessors; `ChatTimeout` no longer exists anywhere.

**Files:** `internal/agent/runner.go`, `internal/agent/chat.go`, `internal/agent/chat_retry_test.go`, `internal/agent/chat_thinkloop_test.go`, `internal/agent/runner_misc_test.go`, `internal/agent/runner_testhelpers_test.go`.

**Steps**

1. `runner.go` — replace the `defaultChatTimeout` const block:

   old:
   ```go
   // defaultChatTimeout is the fallback used when ChatTimeout is unset —
   // notably by the ad-hoc agent.run subagent path, which never sets either
   // timeout field explicitly.
   const defaultChatTimeout = 5 * time.Minute
   ```

   new:
   ```go
   // defaultIdleTimeout is how long a request may go without any streamed
   // bytes before it is treated as a stalled connection. Any delta — content,
   // reasoning, or tool-call — resets it, so a model that is thinking hard is
   // working, not stalled. Two minutes is generous for a first token even on a
   // slow reasoning model, and short enough that a dead socket is noticed while
   // the user is still watching.
   const defaultIdleTimeout = 2 * time.Minute

   // defaultRequestCeiling bounds a single request's total wall-clock time,
   // regardless of activity. It is the backstop for a model that streams
   // forever without finishing: a request that has not completed in fifteen
   // minutes is stuck, not slow.
   const defaultRequestCeiling = 15 * time.Minute
   ```

2. `runner.go` — replace the `ChatTimeout` field:

   old:
   ```go
   	// ChatTimeout bounds chatOnce's per-request context deadline for the
   	// model call itself. It is deliberately separate from approvals and
   	// questions, which carry no wall-clock timeout: a short chat ceiling
   	// must never cut off a slower-but-working completion from a
   	// large-context cloud model.
   	ChatTimeout time.Duration
   ```

   new:
   ```go
   	// IdleTimeout bounds how long a request may go without any streamed
   	// bytes before it is treated as a stalled connection. Any delta —
   	// content, reasoning, or tool-call — resets it. Zero uses
   	// defaultIdleTimeout.
   	IdleTimeout time.Duration
   	// RequestCeiling bounds a single request's total wall-clock time,
   	// regardless of activity. It is the backstop for a model that streams
   	// forever without finishing. Zero uses defaultRequestCeiling.
   	RequestCeiling time.Duration
   ```

3. `runner.go` — `Clone`:

   old:
   ```go
   	r.ChatTimeout = other.ChatTimeout
   ```

   new:
   ```go
   	r.IdleTimeout = other.IdleTimeout
   	r.RequestCeiling = other.RequestCeiling
   ```

4. `runner.go` — the persisted-fields doc comment:

   old:
   ```
   //     Now, MaxToolIterations, MaxRetries, MaxTurnContextTokens, ChatTimeout,
   //     ReconnectMaxWait, ResponseFormat (seed), NativeTools, MaxParallelActions, MaxToolResultChars,
   ```

   new:
   ```
   //     Now, MaxToolIterations, MaxRetries, MaxTurnContextTokens, IdleTimeout,
   //     RequestCeiling, ReconnectMaxWait, ResponseFormat (seed), NativeTools,
   //     MaxParallelActions, MaxToolResultChars,
   ```

5. `chat.go` — replace `effectiveChatTimeout` with two accessors:

   old:
   ```go
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
   ```

   new:
   ```go
   // effectiveIdleTimeout returns how long a request may go without any
   // streamed bytes before it is treated as a stalled connection, falling back
   // to defaultIdleTimeout when IdleTimeout is zero.
   func (r *Runner) effectiveIdleTimeout() time.Duration {
   	if r.IdleTimeout > 0 {
   		return r.IdleTimeout
   	}
   	return defaultIdleTimeout
   }

   // effectiveRequestCeiling returns the total wall-clock bound for a single
   // request, falling back to defaultRequestCeiling when RequestCeiling is
   // zero. It is deliberately independent of approvals and questions, which
   // carry no wall-clock timeout — a runner must not have its chat completion
   // silently truncated by a separate approval ceiling.
   func (r *Runner) effectiveRequestCeiling() time.Duration {
   	if r.RequestCeiling > 0 {
   		return r.RequestCeiling
   	}
   	return defaultRequestCeiling
   }
   ```

6. `chat.go` — the call site in `chatOnceAttempt` (Task 2 replaces this block entirely; this keeps Task 1 compiling):

   old:
   ```go
   	var cancel context.CancelFunc
   	ctx, cancel = context.WithTimeout(ctx, r.effectiveChatTimeout())
   	defer cancel()
   ```

   new:
   ```go
   	var cancel context.CancelFunc
   	ctx, cancel = context.WithTimeout(ctx, r.effectiveRequestCeiling())
   	defer cancel()
   ```

7. Update the test sites. Each is a mechanical rename; the semantics are unchanged because the ceiling is still the only bound in force after this task.

   - `chat_retry_test.go`: both occurrences of `r.ChatTimeout = 50 * time.Millisecond` → `r.RequestCeiling = 50 * time.Millisecond`.
   - `chat_thinkloop_test.go`: `r.ChatTimeout = 10 * time.Second` → `r.RequestCeiling = 10 * time.Second`.
   - `runner_misc_test.go`: `runner.ChatTimeout = 50 * time.Millisecond` → `runner.RequestCeiling = 50 * time.Millisecond`; `runner.ChatTimeout = 500 * time.Millisecond` → `runner.RequestCeiling = 500 * time.Millisecond`.
   - `runner_misc_test.go`: rename `TestChatOnceDefaultsChatTimeoutWhenUnset` → `TestChatOnceDefaultsRequestCeilingWhenUnset`, change `runner.effectiveChatTimeout()` → `runner.effectiveRequestCeiling()`, and update its comment to say "never sets a timeout at all. chatOnce must still apply a bounded default".
   - `runner_testhelpers_test.go`: the `delayedProvider` comment "governed by ChatTimeout" → "governed by RequestCeiling".

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/agent -run 'TestChatOnce|TestChatWithRetry' -count=1
```

Expected: pass. `TestChatOnceTimesOutPerRequest` still sees `context.DeadlineExceeded` because the ceiling is the only bound in force.

## Task 2: Two bounds, idle watchdog, and error classification

**Goal:** a silent stream trips the idle bound and stays reconnect-eligible; an overrunning stream trips the ceiling and fails fast with a distinct error.

**Files:** `internal/agent/chat.go`, `internal/agent/chat_idle_test.go` (new), `internal/agent/chat_retry_test.go`, `internal/agent/runner_misc_test.go`.

**Steps**

1. `chat.go` — add the two sentinels immediately after the `defaultReconnectMaxWait` const:

   ```go
   // errIdleTimeout is the cancel cause when the watchdog decides the stream
   // has gone silent. It is deliberately distinct from the ceiling so the
   // caller can tell "nothing arrived" (a connectivity signal) from "too much
   // arrived for too long" (a model signal).
   var errIdleTimeout = errors.New("stream idle timeout")

   // errRequestCeiling is the cancel cause when a request exceeds its total
   // wall-clock budget. It is never retried and never treated as a
   // connectivity failure: resending the same request reproduces the same
   // overrun.
   var errRequestCeiling = errors.New("request ceiling exceeded")
   ```

2. `chat.go` — replace the timeout block in `chatOnceAttempt`:

   old:
   ```go
   	var cancel context.CancelFunc
   	ctx, cancel = context.WithTimeout(ctx, r.effectiveRequestCeiling())
   	defer cancel()
   ```

   new:
   ```go
   	// Two independent bounds. The idle cause fires when the stream goes
   	// silent; the ceiling fires regardless of activity. Both carry an
   	// explicit cause so context.Cause distinguishes them from an outer
   	// deadline — notably the reconnect budget waitForConnectivity places
   	// around each attempt.
   	ctx, cancelIdle := context.WithCancelCause(ctx)
   	defer cancelIdle(nil)
   	ctx, cancelCeiling := context.WithCancelCause(ctx)
   	defer cancelCeiling(nil)
   	ceilingTimer := time.AfterFunc(r.effectiveRequestCeiling(), func() { cancelCeiling(errRequestCeiling) })
   	defer ceilingTimer.Stop()
   ```

3. `chat.go` — start the watchdog immediately before `events, err := p.Chat(ctx, req)`:

   ```go
   	var lastEvent atomic.Int64
   	lastEvent.Store(r.Now().UnixNano())
   	watchdogDone := make(chan struct{})
   	defer close(watchdogDone)
   	go r.watchIdle(ctx, cancelIdle, &lastEvent, watchdogDone)
   ```

4. `chat.go` — stamp the stream loop. Anchor: `for event := range events {`.

   old:
   ```go
   	for event := range events {
   		if loopSnippet != "" {
   ```

   new:
   ```go
   	for event := range events {
   		lastEvent.Store(r.Now().UnixNano())
   		if loopSnippet != "" {
   ```

5. `chat.go` — add `watchIdle` and `idleCheckInterval` immediately after `effectiveReconnectMaxWait`:

   ```go
   // watchIdle cancels the attempt when no stream event has arrived for
   // effectiveIdleTimeout. It samples a stamp the stream loop updates on every
   // event, so any delta — content, reasoning, or tool-call — counts as
   // activity. It exits when the attempt ends or the context is done.
   func (r *Runner) watchIdle(ctx context.Context, cancel context.CancelCauseFunc, lastEvent *atomic.Int64, done <-chan struct{}) {
   	idle := r.effectiveIdleTimeout()
   	ticker := time.NewTicker(idleCheckInterval(idle))
   	defer ticker.Stop()
   	for {
   		select {
   		case <-done:
   			return
   		case <-ctx.Done():
   			return
   		case <-ticker.C:
   			if r.Now().Sub(time.Unix(0, lastEvent.Load())) >= idle {
   				cancel(errIdleTimeout)
   				return
   			}
   		}
   	}
   }

   // idleCheckInterval samples often enough that the idle bound fires within a
   // fraction of its threshold, and never faster than 10ms so a short test
   // timeout does not spin.
   func idleCheckInterval(idle time.Duration) time.Duration {
   	d := idle / 4
   	if d > time.Second {
   		d = time.Second
   	}
   	if d < 10*time.Millisecond {
   		d = 10 * time.Millisecond
   	}
   	return d
   }
   ```

6. `chat.go` — add `classifyStreamError` immediately after `isRetryableChatError`:

   ```go
   // classifyStreamError maps a stream failure onto the bound that caused it.
   // The adapter reports a cancelled body read as a generic stream error, so
   // the cause — not the error text — is what distinguishes silence from an
   // overrun. An outer deadline (the reconnect budget) falls through unchanged
   // and stays reconnect-eligible.
   func (r *Runner) classifyStreamError(ctx context.Context, model string, err error) error {
   	switch cause := context.Cause(ctx); {
   	case errors.Is(cause, errIdleTimeout):
   		return fmt.Errorf("read stream: %w", context.DeadlineExceeded)
   	case errors.Is(cause, errRequestCeiling):
   		return fmt.Errorf("%w: no completion from %s within %s (stream was active); raise agent.request_ceiling_seconds if this model legitimately needs longer",
   			errRequestCeiling, model, r.effectiveRequestCeiling())
   	}
   	return err
   }
   ```

7. `chat.go` — apply it in the `ChatEventError` branch:

   old:
   ```go
   			return chatResult{Text: sb.String(), ToolCalls: toolCalls, FinishReason: finishReason}, event.Err
   ```

   new:
   ```go
   			return chatResult{Text: sb.String(), ToolCalls: toolCalls, FinishReason: finishReason}, r.classifyStreamError(ctx, model, event.Err)
   ```

8. `chat.go` — apply it after the loop for the channel-closed case. Insert between the `if loopSnippet != "" { ... }` block and the `steering := steeringAbort || ...` line:

   ```go
   	// A bound fired and the channel closed without an error event: report
   	// which one rather than a clean completion. Steering is excluded so a
   	// user interruption is never relabelled.
   	if !steeringAbort && !errors.Is(session.CheckSteering(ctx), session.ErrSendNow) {
   		if cause := context.Cause(ctx); errors.Is(cause, errIdleTimeout) || errors.Is(cause, errRequestCeiling) {
   			return chatResult{Text: sb.String(), ToolCalls: toolCalls, FinishReason: finishReason}, r.classifyStreamError(ctx, model, cause)
   		}
   	}
   ```

9. `chat.go` — `isReconnectEligible`:

   old:
   ```go
   func isReconnectEligible(ctx context.Context, err error) bool {
   	if isNetworkError(err) {
   		return true
   	}
   	return errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
   }
   ```

   new:
   ```go
   func isReconnectEligible(ctx context.Context, err error) bool {
   	if errors.Is(err, errRequestCeiling) {
   		return false
   	}
   	if isNetworkError(err) {
   		return true
   	}
   	return errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
   }
   ```

10. `chat.go` — `isRetryableChatError`:

    old:
    ```go
    	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
    		return false
    	}
    ```

    new:
    ```go
    	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errRequestCeiling) {
    		return false
    	}
    ```

11. Switch the three stalled-stream tests from the ceiling to the idle bound. Each models a provider that sends nothing until the context ends, which is now the idle case.

    - `chat_retry_test.go`, `TestChatWithRetryReconnectOnPerRequestDeadline`: `r.RequestCeiling = 50 * time.Millisecond` → `r.IdleTimeout = 50 * time.Millisecond`.
    - `chat_retry_test.go`, `TestChatWithRetryParentCancelNotReconnectEligible`: `r.RequestCeiling = 50 * time.Millisecond` → `r.IdleTimeout = 50 * time.Millisecond`.
    - `runner_misc_test.go`, `TestChatOnceTimesOutPerRequest`: `runner.RequestCeiling = 50 * time.Millisecond` → `runner.IdleTimeout = 50 * time.Millisecond`.

12. Create `internal/agent/chat_idle_test.go`:

    ```go
    package agent

    import (
    	"context"
    	"errors"
    	"testing"
    	"time"

    	"marshal/internal/app/config"
    	"marshal/internal/llm/schema"
    	"marshal/internal/tools/policy"
    	"marshal/internal/tools/registry"
    )

    // trickleProvider emits a thinking delta every interval, then a done
    // event. It models a reasoning model that is working but produces no
    // content.
    type trickleProvider struct {
    	interval time.Duration
    	count    int
    }

    func (p *trickleProvider) Name() string { return "trickle" }

    func (p *trickleProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }

    func (p *trickleProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
    	return schema.ProviderCapabilities{}
    }

    func (p *trickleProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
    	events := make(chan schema.ChatEvent, 1)
    	go func() {
    		defer close(events)
    		for i := 0; i < p.count; i++ {
    			select {
    			case <-ctx.Done():
    				events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
    				return
    			case <-time.After(p.interval):
    			}
    			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: "thinking "}
    		}
    		events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: "stop"}
    	}()
    	return events, nil
    }

    func TestChatOnceIdleTimeoutFiresOnSilentStream(t *testing.T) {
    	state := newTestState(t)
    	p := &blockingProvider{}
    	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
    	r.IdleTimeout = 50 * time.Millisecond

    	start := time.Now()
    	_, err := r.chatOnce(context.Background(), p, "test-model",
    		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
    	elapsed := time.Since(start)

    	if !errors.Is(err, context.DeadlineExceeded) {
    		t.Fatalf("chatOnce returned %v, want context.DeadlineExceeded", err)
    	}
    	if elapsed > 2*time.Second {
    		t.Fatalf("idle timeout took too long: %v", elapsed)
    	}
    }

    func TestChatOnceIdleTimeoutResetsOnThinkingDeltas(t *testing.T) {
    	state := newTestState(t)
    	p := &trickleProvider{interval: 20 * time.Millisecond, count: 10}
    	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
    	r.IdleTimeout = 50 * time.Millisecond

    	res, err := r.chatOnce(context.Background(), p, "test-model",
    		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
    	if err != nil {
    		t.Fatalf("chatOnce err = %v, want success: reasoning deltas must reset the idle timer", err)
    	}
    	if res.Text != "" {
    		t.Fatalf("res.Text = %q, want empty (thinking only)", res.Text)
    	}
    }

    func TestChatOnceCeilingFiresDespiteActivity(t *testing.T) {
    	state := newTestState(t)
    	p := &trickleProvider{interval: 5 * time.Millisecond, count: 1000}
    	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
    	r.IdleTimeout = 50 * time.Millisecond
    	r.RequestCeiling = 150 * time.Millisecond

    	_, err := r.chatOnce(context.Background(), p, "test-model",
    		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
    	if !errors.Is(err, errRequestCeiling) {
    		t.Fatalf("chatOnce returned %v, want errRequestCeiling", err)
    	}
    	if errors.Is(err, context.DeadlineExceeded) {
    		t.Fatalf("ceiling error must not be DeadlineExceeded: %v", err)
    	}
    }

    func TestRequestCeilingIsNotReconnectEligible(t *testing.T) {
    	if isReconnectEligible(context.Background(), errRequestCeiling) {
    		t.Fatal("errRequestCeiling must not be reconnect-eligible")
    	}
    }
    ```

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/agent -run 'TestChatOnce|TestChatWithRetry|TestRequestCeiling' -count=1
```

Expected: pass. The idle tests complete in well under a second; the ceiling test completes in ~150ms.

## Task 3: Reconnect cap enforcement

**Goal:** the reconnect budget bounds the total wait, including the attempt in flight, so a 2-minute cap can no longer become five minutes.

**Files:** `internal/agent/chat.go`, `internal/agent/chat_idle_test.go`.

**Steps**

1. `chat.go` — in `waitForConnectivity`, replace the attempt call with a pre-attempt budget check and a bounded attempt. Anchor: the `res, err := r.chatOnce(ctx, p, model, messages, responseFormat, includeNativeTools)` line inside the loop.

   old:
   ```go
   		res, err := r.chatOnce(ctx, p, model, messages, responseFormat, includeNativeTools)
   ```

   new:
   ```go
   		// Check the budget before spending an attempt, not after: a single
   		// attempt can consume the whole idle timeout, so a post-attempt check
   		// lets a 2-minute cap become five minutes (observed: waited=5m1s
   		// against max_wait=2m0s). Bounding the attempt by what remains makes
   		// the cap the real ceiling on the total wait.
   		remaining := r.effectiveReconnectMaxWait() - r.Now().Sub(started)
   		if remaining <= 0 {
   			if r.State != nil {
   				r.State.Logger().Warn("provider connection did not recover; failing turn", "waited", r.Now().Sub(started).Round(time.Second), "error", err)
   			}
   			return *best, fmt.Errorf("no stream activity from %s for %s; reconnect budget of %s exhausted: %w",
   				model, r.effectiveIdleTimeout(), r.effectiveReconnectMaxWait(), err)
   		}
   		attemptCtx, cancelAttempt := context.WithTimeout(ctx, remaining)
   		res, err := r.chatOnce(attemptCtx, p, model, messages, responseFormat, includeNativeTools)
   		cancelAttempt()
   ```

2. `chat.go` — delete the now-redundant post-attempt cap block:

   old:
   ```go
   		if r.Now().Sub(started)+reconnectBackoff(attempt+1) > r.effectiveReconnectMaxWait() {
   			if r.State != nil {
   				r.State.Logger().Warn("provider connection did not recover; failing turn", "waited", r.Now().Sub(started).Round(time.Second), "error", err)
   			}
   			return *best, err
   		}
   ```

   new: delete the block entirely. The pre-attempt check in step 1 owns the give-up. The `sleepCtx` failure path above it keeps its existing `return *best, err` — that path is a user or turn cancellation, not budget exhaustion, so it must not claim the idle timeout.

3. `chat_idle_test.go` — add the cap test:

   ```go
   func TestWaitForConnectivityRespectsCapBeforeAttempt(t *testing.T) {
   	state := newTestState(t)
   	p := &blockingProvider{}
   	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
   	r.IdleTimeout = 400 * time.Millisecond
   	r.ReconnectMaxWait = 150 * time.Millisecond
   	r.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

   	var best chatResult
   	start := time.Now()
   	_, err := r.waitForConnectivity(context.Background(), p, "test-model",
   		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false,
   		context.DeadlineExceeded, &best)
   	elapsed := time.Since(start)

   	if err == nil {
   		t.Fatal("expected failure when the reconnect budget is exhausted")
   	}
   	if elapsed > 300*time.Millisecond {
   		t.Fatalf("waitForConnectivity %v exceeded its 150ms cap plus one bounded attempt", elapsed)
   	}
   }
   ```

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/agent -run 'TestWaitForConnectivity|TestChatWithRetry' -count=1
```

Expected: pass. The cap test completes in ~150ms; the old code took ~400ms and fails the 300ms bound.

## Task 4: Config plumbing

**Goal:** `agent.idle_timeout_seconds` and `agent.request_ceiling_seconds` load, merge, save, and reach every runner construction.

**Files:** `internal/app/config/types.go`, `internal/app/config/file_types.go`, `internal/app/config/merge.go`, `internal/app/config/save.go`, `internal/app/config/save_test.go`, `internal/app/app.go`.

**Steps**

1. `types.go` — add to `AgentConfig`, immediately after `ReconnectMaxWaitSeconds`:

   ```go
   	// IdleTimeoutSeconds bounds how long a request may go without any
   	// streamed bytes before it is treated as a stalled connection. 0 =
   	// built-in default (2 minutes). Any delta — content, reasoning, or
   	// tool-call — resets it.
   	IdleTimeoutSeconds int `toml:"idle_timeout_seconds"`
   	// RequestCeilingSeconds bounds a single request's total wall-clock
   	// time, regardless of activity. 0 = built-in default (15 minutes).
   	RequestCeilingSeconds int `toml:"request_ceiling_seconds"`
   ```

2. `file_types.go` — add to `fileAgent`, immediately after `ReconnectMaxWaitSeconds`:

   ```go
   	IdleTimeoutSeconds      *int    `toml:"idle_timeout_seconds"`
   	RequestCeilingSeconds   *int    `toml:"request_ceiling_seconds"`
   ```

3. `merge.go` — add to the `file.Agent != nil` block, immediately after the `ReconnectMaxWaitSeconds` line:

   ```go
   		set(&cfg.Agent.IdleTimeoutSeconds, file.Agent.IdleTimeoutSeconds)
   		set(&cfg.Agent.RequestCeilingSeconds, file.Agent.RequestCeilingSeconds)
   ```

4. `save.go` — add to the `agent := &fileAgent{}` block, immediately after the `ReconnectMaxWaitSeconds` `putKey`:

   ```go
   	putKey(&agent.IdleTimeoutSeconds, fileField[int](file.Agent, "IdleTimeoutSeconds"), cfg.Agent.IdleTimeoutSeconds, def.Agent.IdleTimeoutSeconds)
   	putKey(&agent.RequestCeilingSeconds, fileField[int](file.Agent, "RequestCeilingSeconds"), cfg.Agent.RequestCeilingSeconds, def.Agent.RequestCeilingSeconds)
   ```

5. `app.go` — wire all three runner constructions. Each site already has a `ReconnectMaxWaitSeconds` block; add the two new blocks immediately after it.

   Site 1 (`runner`, after `if cfg.Agent.ReconnectMaxWaitSeconds > 0 { ... }`):
   ```go
   	if cfg.Agent.IdleTimeoutSeconds > 0 {
   		runner.IdleTimeout = time.Duration(cfg.Agent.IdleTimeoutSeconds) * time.Second
   	}
   	if cfg.Agent.RequestCeilingSeconds > 0 {
   		runner.RequestCeiling = time.Duration(cfg.Agent.RequestCeilingSeconds) * time.Second
   	}
   ```

   Site 2 (`r`, after `if s.cfg.Agent.ReconnectMaxWaitSeconds > 0 { ... }`):
   ```go
   	if s.cfg.Agent.IdleTimeoutSeconds > 0 {
   		r.IdleTimeout = time.Duration(s.cfg.Agent.IdleTimeoutSeconds) * time.Second
   	}
   	if s.cfg.Agent.RequestCeilingSeconds > 0 {
   		r.RequestCeiling = time.Duration(s.cfg.Agent.RequestCeilingSeconds) * time.Second
   	}
   ```

   Site 3 (`childRunner`, after `if cfg.Agent.ReconnectMaxWaitSeconds > 0 { ... }`):
   ```go
   		if cfg.Agent.IdleTimeoutSeconds > 0 {
   			childRunner.IdleTimeout = time.Duration(cfg.Agent.IdleTimeoutSeconds) * time.Second
   		}
   		if cfg.Agent.RequestCeilingSeconds > 0 {
   			childRunner.RequestCeiling = time.Duration(cfg.Agent.RequestCeilingSeconds) * time.Second
   		}
   ```

6. `save_test.go` — add a round-trip test modelled on `TestSaveProjectConfigRoundTripsAgentAndToolSettings`:

   ```go
   func TestSaveProjectConfigRoundTripsAgentTimeoutKnobs(t *testing.T) {
   	tmp := t.TempDir()
   	path := filepath.Join(tmp, ".marshal", "config.toml")

   	cfg := Default()
   	cfg.Agent.IdleTimeoutSeconds = 90
   	cfg.Agent.RequestCeilingSeconds = 600

   	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
   		t.Fatalf("SaveProjectConfig failed: %v", err)
   	}

   	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
   	if err != nil {
   		t.Fatalf("Load failed: %v", err)
   	}
   	if loaded.Agent.IdleTimeoutSeconds != 90 {
   		t.Fatalf("IdleTimeoutSeconds = %d, want 90", loaded.Agent.IdleTimeoutSeconds)
   	}
   	if loaded.Agent.RequestCeilingSeconds != 600 {
   		t.Fatalf("RequestCeilingSeconds = %d, want 600", loaded.Agent.RequestCeilingSeconds)
   	}
   }
   ```

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/config -run 'TestSaveProjectConfigRoundTripsAgentTimeoutKnobs|TestLoadParsesAgentLimits|TestDefaultAgentLimitsAreZero' -count=1
```

Expected: pass. `Default()` leaves both at 0, so the runner defaults apply.

## Final verification

```sh
CGO_ENABLED=1 go build ./cmd/marshal
CGO_ENABLED=1 go test ./internal/agent/... ./internal/app/config/... -count=1
CGO_ENABLED=1 go vet ./...
gofmt -l internal/agent internal/app
```

Expected: build succeeds; the two packages pass; `go vet` is clean; `gofmt -l` prints nothing.

Then run the full suite once, since `AGENTS.md` notes full-suite regressions surface only at release time:

```sh
CGO_ENABLED=1 go test ./... -count=1
```

Known pre-existing failures unrelated to this work: `TestRuntimeCloseRunsNoGitGC`, `TestStartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces`, `TestCompletedJobsDoNotConsumeConcurrency`, `TestStandaloneBackendNavigateAndRead` (network).

## Integration notes

- The spec and this plan should be committed together with the implementation, per `AGENTS.md` ("commit them with the work they describe").
- Commit per task: `request-timeouts: task N — <task title>`.
- Task 2 changes the meaning of `TestChatOnceTimesOutPerRequest` from "ceiling" to "idle"; the test name stays accurate because a silent provider is the stalled case.
- After this lands, a turn that hits the ceiling fails with a message naming the model and `agent.request_ceiling_seconds`, which is the user-facing half of the fix.

## Plan authoring checks

- Every task has exact files, steps that state decisions, named tests and a runnable Verify block.
- Cross-task types and signatures are declared in Contracts.
- Anchors verified at `2e8c5453`: `defaultChatTimeout` (`runner.go:70-73`), the `ChatTimeout` field (`runner.go:271-276`), `Clone` (`runner.go:679`), the persisted-fields comment (`runner.go:203`), `effectiveChatTimeout` (`chat.go:519-529`), the `WithTimeout` call site (`chat.go:573`), the stream loop (`chat.go:698`), the `ChatEventError` return (`chat.go:775`), `isReconnectEligible` (`chat.go:486`), `isRetryableChatError` (`chat.go:500`), `waitForConnectivity` (`chat.go:358`) and its cap check (`chat.go:404`), `ReconnectMaxWaitSeconds` plumbing (`types.go:545-548`, `file_types.go:55`, `merge.go:39`, `save.go:220`), and the three wiring sites (`app.go:1266`, `:1590`, `:1853`).
- Test anchors verified: `blockingProvider` and `delayedProvider` (`runner_testhelpers_test.go`), `deadlineThenSuccessProvider` (`chat_retry_test.go:353`), `newTestState`, `SaveProjectConfig`/`Load` (`save_test.go:145-200`).
- No task adds system-prompt bytes.
- No placeholders or TBDs.
- Contradicts nothing in the spec.
