# ACP v1 Support Matrix

Marshal exposes a headless subcommand (`marshal acp`) that speaks the
[Agent Client Protocol (ACP) v1](https://agentclientprotocol.com/) over
stdio JSON-RPC 2.0. This document describes exactly which parts of ACP v1 are
supported and which are intentionally omitted.

## Supported methods

| Method | Status | Notes |
|--------|--------|-------|
| `initialize` | Full | Protocol version negotiation, truthful capability advertisement. No auth methods. |
| `session/new` | Full | Creates a new project and session record. |
| `session/load` | Full | Loads an existing persisted session. Replays the active conversation branch through standard `session/update` notifications before returning. No project/session/message rows are created or modified. |
| `session/list` | Full | Requires an absolute `cwd`. Filters by project root; no global session registry exists, so a request with no `cwd` returns `-32602`. Cursor-paginated. |
| `session/resume` | Full | Restores an existing persisted session like `session/load` but does **not** replay history. Returns an empty object. |
| `session/delete` | Full | Requires an absolute `cwd` and a `sessionId`. Cancels and closes any loaded runtime for the id, then removes the session row (and its messages via FK cascade) from `<cwd>/.marshal/marshal.db`. Returns an empty object on success, `-32000` for an unknown session id. Per-cwd, like the rest of the lifecycle API. |
| `session/create`/`session/load`/`session/resume` (`additionalDirectories`) | Full | The `additionalDirectories` array on the three lifecycle methods is now accepted and forwarded to the runtime as extra workspace roots. Capped at 8 entries; each must be an absolute path (`-32602` on overflow or relative path). The restricted tool-layer path validation extends the allowed-cwd set to include these roots. `initialize` advertises `sessionCapabilities.additionalDirectories: {}`. |
| `session/prompt` | Full | Sends prompt content to the agent. Serialized per session — a second prompt to the same session returns `-32000`. |
| `session/cancel` | Full | Cancels the active turn in a session and returns `cancelled` from the original prompt. |
| `session/close` | Full | Removes the runtime, cancels the active turn, joins the runner, and closes owned resources. Unknown sessions return `-32000`. |
| `session/update` | Full | Standard notification emitted for message chunks, thought chunks, and replay during `session/load`. Uses standard `user_message_chunk`, `agent_message_chunk`, and `agent_thought_chunk` update methods. |
| `request_permission` | Full | Surfaces tool permission requests with the same allow/deny/always semantics as the TUI approval flow. A permission request/response transport failure cancels the turn. |

### Advertised capabilities

`initialize` reports `agentCapabilities.loadSession: true` and
`sessionCapabilities: { close, list, resume, additionalDirectories,
delete }` (each an empty object). No other lifecycle, content, or MCP
capabilities are advertised.

## Prompt content blocks

| Block type | Status | Notes |
|------------|--------|-------|
| `text` | Supported | Requires non-empty text. Contributed exactly to the prompt. |
| `resource_link` | Supported | Requires both `name` and `uri`. Normalized to `Resource link: <name> (<uri>)`. Resource links are not dereferenced by the ACP layer. |

All other content block types (`image`, `audio`, `embedded_resource`, etc.) return
`-32602` (invalid params). Unadvertised block types are rejected honestly — they
are never silently ignored.

## Concurrency model

- **One prompt per session**: a second `session/prompt` for an already-active
  session returns `-32000` ("session already has an active turn"). The first
  prompt is never implicitly cancelled or replaced.
- **Multiple sessions concurrently**: prompts for distinct sessions run in
  parallel, each in their own tracked goroutine.
- **Transport reader remains available**: the input loop routes frames to the
  correct handler or waiter, so `session/cancel` and permission responses are
  never blocked behind a running prompt handler.

## Unsupported features

The following ACP v1 features are **not supported** and are either rejected
with an appropriate error or omitted from capability advertisement:

| Feature | Reason |
|---------|--------|
| `$/cancel_request` (generic request cancellation) | Only per-session `session/cancel` is supported. |
| Dynamic MCP server arrays in `session/new` | MCP configuration is static. |
| Image, audio, embedded resource blocks | Rejected with `-32602`. |
| Full tool-call / plan / config-option projection | Session events do not yet preserve all mandatory ACP identifiers. |
| Structured question / elicitation support | Pending questions are received from the runner and answered with `AnswerUnanswered` when the turn context cancels, but they are never presented to the ACP client (no `elicitation/create` is emitted). |
| HTTP, SSE, or WebSocket transports | Only stdio transport is implemented. |
| ACP v2 | Not targeted. |

## Shutdown

EOF on stdin, context cancellation, or `session/cancel` during an active turn:

1. Releases all outbound waiters (permission handlers unblock immediately).
2. Cancels handler contexts and joins tracked goroutines.
3. Calls `Runtime.Close` through the Batch 1 lifecycle — which calls
   `Quiesce` (cancels the in-flight turn, resolves pending state, shuts down
   background jobs) before closing MCP, brokers, snapshots, and the database.
4. All active runtimes are closed deterministically within the shutdown
   budget. The total upper bound is ~10s: `Serve` waits up to 5s for active
   handlers to join, then `CloseAll` waits up to 5s for runtimes to close.

No handler, runtime, or outbound waiter remains attached after shutdown.

## References

- [ACP Initialization](https://agentclientprotocol.com/protocol/v1/initialization)
- [ACP Session setup and loading](https://agentclientprotocol.com/protocol/v1/session-setup)
- [ACP Prompt turns](https://agentclientprotocol.com/protocol/v1/prompt-turn)
- [ACP Content blocks](https://agentclientprotocol.com/protocol/v1/content)
- [ACP Cancellation](https://agentclientprotocol.com/protocol/v1/cancellation)
