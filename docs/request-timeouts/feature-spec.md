# Request timeouts: idle bound and hard ceiling

Status: approved design and written specification; not yet implemented.
Date: 2026-10-19.
Source baseline: `2e8c5453667fb73ced00bbd232e7a7859114d706`.
Workflow: [Marshal brainstorming](../../internal/skills/builtin/brainstorming.md), followed by Marshal's internal plan-writing skill after written-spec review.

## Context and classification

Two turns on 2026-10-19 ended with no output and the error `read stream: context deadline exceeded`. The user read this as a context-window overflow. It was not: the error is Go's `context.Context` deadline, and the model's context window was never the constraint.

This is a **bounded** change to the existing per-request timeout and retry flow in `internal/agent/chat.go`. It replaces one total deadline with two independent bounds and makes their errors distinguishable. It does not add a subsystem, a scheduler, or a new transport.

## Evidence

The failing turn is `sess_1792395834405436000`, started 2026-10-19T13:11:10Z: 31 iterations, 53 tool calls, 28m07s, outcome `failed`. Its session log (`.marshal/marshal.log`, local +11:00) records:

```
13:16:31 WARN thinking loop detected; aborting stream  provider=ollama-cloud model=deepseek-v4.1-flash:1017 thinking_bytes=22256
13:23:05 WARN provider connection lost; waiting for connectivity  error="read stream: context deadline exceeded" max_wait=2m0s
13:23:20 INFO provider connection restored  waited=15s attempts=1
13:29:07 WARN provider connection lost; waiting for connectivity  error="read stream: context deadline exceeded"
13:29:16 INFO provider connection restored  waited=9s attempts=1
13:34:16 WARN provider connection lost; waiting for connectivity  error="read stream: context deadline exceeded"
13:39:17 WARN provider connection did not recover; failing turn  waited=5m1s
```

Four findings:

1. **One total deadline.** `chatOnceAttempt` wraps the whole request in `context.WithTimeout(ctx, r.effectiveChatTimeout())` (`chat.go:573`), defaulting to `defaultChatTimeout = 5 * time.Minute` (`runner.go:73`). It fires whether or not bytes are flowing, so a model streaming reasoning for five minutes is killed exactly like a dead socket.
2. **Empty content fails the turn.** The failure gate is `strings.TrimSpace(res.Text) == ""` (`runner.go:1606`). Thinking deltas accumulate in `thinkingBuf`, not `res.Text`, so a model that streams reasoning and no content fails with no output. The 22,256 thinking bytes at 13:16:31 are the proof that reasoning was flowing.
3. **Silence is misclassified as connectivity.** `isReconnectEligible` (`chat.go:486`) returns true for `context.DeadlineExceeded` whenever the parent context is alive, so a slow model is handed to `waitForConnectivity` and retried.
4. **The reconnect cap is not enforced during an attempt.** The budget check runs only after `chatOnce` returns (`chat.go:404`), so a single attempt can exceed it. The log records `waited=5m1s` against `max_wait=2m0s`.

The two "recoveries" (15s, 9s) prove the provider was healthy: the retry succeeded almost immediately. The reconnect path masked a stuck request as a network outage and added five minutes.

The same shape appears in `sess_1792389324195362000`: last message 06:05:09Z, failure 06:15:23Z — 10m14s, two timeouts.

The model was in a degenerate repetition loop. Steps 323–333 all open with the same sentence ("All three sub-themes date as already handled. The corpus points at the real driver: failed turns are `role=sub…"), and `think_duration_ms` escalated to 30.7s and 31.0s. The within-stream loop detector fired once at 13:16:31 and nudged; the repetition continued in content, which the detector does not watch (`det.feed` is called only on `schema.DeltaThinking`, `chat.go:733`).

## Scope

Replace the single total per-request deadline with two independent bounds:

| Bound | Fires when | Default | Classified as |
|---|---|---|---|
| Idle | no stream event for `idleTimeout` | 120s | connectivity → reconnect-eligible |
| Ceiling | total request time exceeds `requestCeiling` | 900s | model → fail fast, never retried |

Any `delta` — content, reasoning, or tool-call — resets the idle timer. That is what makes a model streaming reasoning "working" rather than "stalled", and it is why the idle timeout can legitimately mean connectivity.

The stream event types are only `delta`, `done`, and `error` (`internal/llm/schema/event.go:10-12`); there is no keepalive event, so stamping on every event cannot be reset spuriously.

## Design

### Two bounds in `chatOnceAttempt`

Replace `chat.go:573`:

```go
var cancel context.CancelFunc
ctx, cancel = context.WithTimeout(ctx, r.effectiveChatTimeout())
defer cancel()
```

with:

```go
ctx, cancelIdle := context.WithCancelCause(ctx)
defer cancelIdle(nil)
ctx, cancelCeiling := context.WithCancelCause(ctx)
defer cancelCeiling(nil)
ceilingTimer := time.AfterFunc(r.effectiveRequestCeiling(), func() { cancelCeiling(errRequestCeiling) })
defer ceilingTimer.Stop()
```

`context.Cause(ctx)` then distinguishes the two: `errIdleTimeout` for the watchdog, `errRequestCeiling` for the ceiling. The ceiling uses an explicit cause rather than `context.WithTimeout` because a plain timeout reports `context.DeadlineExceeded`, which is indistinguishable from an outer deadline — notably the reconnect budget that `waitForConnectivity` places around each attempt. `context.WithCancelCause` is already used in `internal/app/session/steering.go:375` and `internal/acp/questions.go:110`.

### Watchdog

Started before `p.Chat` so it covers time-to-first-byte, not only streaming:

```go
var lastEvent atomic.Int64
lastEvent.Store(r.Now().UnixNano())
watchdogDone := make(chan struct{})
defer close(watchdogDone)
go r.watchIdle(ctx, cancelIdle, &lastEvent, watchdogDone)
```

The stream loop stamps `lastEvent.Store(r.Now().UnixNano())` at the top of each iteration, including the two `continue` drain branches, so a draining stream still counts as active.

```go
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

The watchdog rides the existing cancellation path. The comment at `chat.go:700` establishes that no backend selects on `ctx.Done()`, so `cancel()` fails the body read and the channel closes transitively. **The drain logic is not modified.**

### Error classification

Two sentinels in `chat.go`:

```go
var errIdleTimeout = errors.New("stream idle timeout")
var errRequestCeiling = errors.New("request ceiling exceeded")
```

The adapter reports a cancelled body read as a generic stream error, so the cause — not the error text — decides:

```go
func (r *Runner) classifyStreamError(ctx context.Context, model string, err error) error {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errIdleTimeout):
		return fmt.Errorf("read stream: %w", context.DeadlineExceeded)
	case errors.Is(cause, context.DeadlineExceeded):
		return fmt.Errorf("%w: no completion from %s within %s (stream was active); raise agent.request_ceiling_seconds if this model legitimately needs longer",
			errRequestCeiling, model, r.effectiveRequestCeiling())
	}
	return err
}
```

Applied in the `ChatEventError` branch and again after the loop for the channel-closed case, guarded by the existing `steering` computation so a user interruption is never relabelled.

`isReconnectEligible` gains an explicit exclusion:

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

`isRetryableChatError` adds the ceiling to its non-retryable list.

### Reconnect cap enforcement

In `waitForConnectivity`, move the budget check before the attempt and bound the attempt by what remains:

```go
remaining := r.effectiveReconnectMaxWait() - r.Now().Sub(started)
if remaining <= 0 {
	// give up, as today
}
attemptCtx, cancelAttempt := context.WithTimeout(ctx, remaining)
res, err := r.chatOnce(attemptCtx, p, model, messages, responseFormat, includeNativeTools)
cancelAttempt()
```

This makes `waited=5m1s` against `max_wait=2m0s` impossible.

### Configuration

Follows the `ReconnectMaxWaitSeconds` precedent (`types.go:545-548`, `file_types.go:55`, `merge.go:39`, `save.go:220`, wiring at `app.go:1266`, `:1590`, `:1853`):

- `agent.idle_timeout_seconds` → `Runner.IdleTimeout` (default 120)
- `agent.request_ceiling_seconds` → `Runner.RequestCeiling` (default 900)

`Runner.ChatTimeout` is renamed to `RequestCeiling`: the old name encoded the wrong semantics. The rename is contained to `runner.go` (field, `Clone` at line 679, doc comment at line 203) plus six test sites (`chat_retry_test.go:391,429`, `chat_thinkloop_test.go:303`, `runner_misc_test.go:692,716`, `runner_testhelpers_test.go:83`).

### Failure text

The ceiling names the model, the elapsed time, and the knob:

> `request ceiling exceeded: no completion from deepseek-v4.1-flash:1017 within 15m0s (stream was active). Raise agent.request_ceiling_seconds if this model legitimately needs longer.`

The reconnect give-up names the idle timeout rather than "connection lost":

> `no stream activity from deepseek-v4.1-flash:1017 for 2m0s; reconnect budget of 2m0s exhausted`

## Error handling

- **Idle timeout** → wrapped `context.DeadlineExceeded` → `isReconnectEligible` true → `waitForConnectivity` retries within the cap. This is correct: silence really is a connectivity signal.
- **Ceiling** → `errRequestCeiling` → not reconnect-eligible, not retryable → `failTurn` immediately.
- **User interruption** → unchanged. `steeringAbort` and `session.CheckSteering` take precedence over the cause mapping.
- **Thinking loop** → unchanged. `loopSnippet` is checked before the cause mapping.
- **Partial content** → unchanged. A stream that delivered content before a bound fired is still salvage-eligible at `runner.go:1606`.

## Verification

| Test | Pins |
|---|---|
| `TestChatOnceIdleTimeoutFiresOnSilentStream` | provider sends nothing → idle fires, error wraps `DeadlineExceeded` |
| `TestChatOnceIdleTimeoutResetsOnThinkingDeltas` | continuous reasoning deltas slower than the threshold → request completes |
| `TestChatOnceCeilingFiresDespiteActivity` | provider streams forever → `errRequestCeiling`, not `DeadlineExceeded` |
| `TestRequestCeilingIsNotReconnectEligible` | `isReconnectEligible` false for the ceiling |
| `TestWaitForConnectivityRespectsCapBeforeAttempt` | always-timing-out provider → total wait bounded by the cap |
| `TestIdleTimeoutDefaultsWhenUnset` | `effectiveIdleTimeout()` > 0 |
| config round-trip | both keys survive save/load/merge |

Run `go test ./internal/agent/... ./internal/app/config/...` and `go vet ./...`.

## Assumptions

- Defaults are 120s idle and 900s ceiling. The failing turn's requests each ran the full five minutes, so 120s of true silence is comfortably diagnostic.
- The watchdog uses `r.Now()` for consistency with the rest of the runner; tests inject short durations against the real clock.
- The ceiling is a hard failure, not a salvage. A request that overruns fifteen minutes has produced nothing usable.

## Non-goals

- **Cross-step narration-repetition detection.** The 11 verbatim repeats were across separate steps, invisible to the within-stream detector, and `hard_stalls = 0` because the tool calls varied. This is a separate follow-up with its own spec.
- Loop-detector threshold changes.
- Salvaging reasoning text on ceiling; it is not user-facing output.
- Per-adapter read deadlines (rejected: three adapters, duplicated logic, wrong layer for a policy decision).

## Open questions

None.
