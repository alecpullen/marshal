# OAuth-backed providers

Marshal can talk to the ChatGPT subscription endpoint through the
`openai_codex` provider type. This document covers what that provider is, how
to set it up, and the terms-of-service considerations you should weigh before
using it.

## What it is

The `openai_codex` backend authenticates with a **ChatGPT subscription login**
rather than an API key. It speaks the OpenAI Responses API against
`https://chatgpt.com/backend-api/codex/responses`, using the same public
client identity as the `codex` CLI.

It is a distinct provider type from `openai_compatible`. The two are not
interchangeable: the codex endpoint is streaming-only, requires a specific
`include` field on every request, and serves a different model catalog.

## Setup

1. Run `/connect` in the TUI.
2. Pick **OpenAI (ChatGPT subscription)**.
3. A browser window opens; sign in with your ChatGPT account.
4. Marshal stores the resulting tokens in your OS keyring under
   `marshal:provider:<name>` and proceeds to model selection.

There is no API key to paste. The provider entry in `config.toml` looks like:

```toml
[providers.codex]
type = "openai_codex"
base_url = "https://chatgpt.com/backend-api"
auth = "oauth"
```

`auth = "oauth"` is mutually exclusive with `api_key` / `api_key_env`; config
diagnostics reject the combination.

## Terms of service

**Read this before using the provider.**

- **This is not an officially supported API.** The ChatGPT subscription
  endpoint is the one the `codex` CLI uses. It is not the OpenAI Platform API,
  it is not covered by a Platform API agreement, and it is not documented as a
  public integration surface. Marshal reaches it by using the same public
  client identity the CLI does.
- **Usage is subject to fair-use caps.** The endpoint reports subscription
  quota through `x-codex-*` response headers, which Marshal surfaces in the
  status line and the `/agents` roster. Exceeding the caps is a subscription
  matter between you and OpenAI, not something Marshal can negotiate.
- **Your subscription terms govern.** Whether this use is permitted is
  determined by the agreement you accepted with OpenAI. Marshal does not
  grant you any rights there, and using this provider does not change your
  obligations.
- **Review the terms yourself.** Marshal respects OpenAI's terms and does not
  attempt to circumvent any control. You should read the current terms and
  decide whether this usage fits them.

If you need a supported, contractually-backed integration, use the
`openai_compatible` provider type with a Platform API key instead.

## Operational notes

- **Tokens are per provider entry.** Two `openai_codex` entries hold two
  independent logins, keyed `marshal:provider:<name>`.
- **Refresh is automatic.** A background worker refreshes proactively using
  the server's `earliest_refresh_at` hint, and the provider refreshes inline
  as a safety net. Both paths share one mutex, so they cannot race.
- **Re-authentication.** If a refresh is rejected, the provider surfaces an
  auth-required error and the TUI offers a `/connect` hint. ACP sessions get a
  recoverable error naming the TUI, since ACP has no login flow.
- **Model list.** The provider prefers the live catalog, falls back to a disk
  cache, then to a static list. Only models the endpoint will actually serve
  are offered — a model outside your catalog is rejected with a 400.
- **Token material never reaches logs, wire captures, or exports.** The
  redaction chain is pinned by `internal/llm/provider/leak_audit_test.go`.

## Where the wire details come from

Every constant in the provider (client id, endpoints, headers, the required
`include` value, the quota header names) is transcribed from the `codex` CLI
source and confirmed against the live endpoint. The evidence is recorded in
`docs/codex-spike-findings-2026-09-14.md`, including the claims from secondary
sources that turned out to be wrong.
