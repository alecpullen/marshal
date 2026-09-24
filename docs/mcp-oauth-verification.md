# MCP OAuth — manual verification walkthrough

This document walks through verifying the OAuth authorization-code flow for
remote (Streamable-HTTP) MCP servers end to end. It covers the three surfaces
the feature exposes — the local test fixture, a real hosted server driven from
the TUI, and the headless ACP command surface — plus the token-storage model,
the one known limitation, and troubleshooting.

Keep it to hand when testing a change to `internal/tools/mcp/oauth`,
`internal/tools/mcp/manager.go`, `internal/commands/mcp.go`,
`internal/app/tui/mcp.go`, `internal/acp/mcpauth.go`, or
`internal/credentials`.

---

## 1. Local fixture walkthrough

The fastest loop is a local OAuth authorization server (AS) driven from a Go
test. The canonical fixture already lives in
`internal/acp/mcpauth_test.go` (`fakeAuthorizeServer`): a single
`httptest.NewServer` that answers the four endpoints the engine needs.

### What the fixture must serve

| Path | Purpose | Must return |
| --- | --- | --- |
| `/.well-known/oauth-authorization-server` (RFC 8414) or `/.well-known/openid-configuration` (OIDC fallback) | Metadata discovery | `issuer`, `authorization_endpoint`, `token_endpoint`, `registration_endpoint`, `code_challenge_methods_supported: ["S256"]` |
| `/register` (RFC 7591) | Dynamic client registration | `client_id` (and optionally `client_secret`), `token_endpoint_auth_method` |
| `/authorize` | Interactive authorization | Redirects to the supplied `redirect_uri` with `code` + `state`, or (for a non-interactive fixture) is driven directly by the test |
| `/token` (RFC 6749) | Code→token and refresh exchange | `access_token`, `refresh_token`, `token_type`, `expires_in` |

The engine discovers metadata under the server URL's origin, registers a
fresh client bound to the loopback `redirect_uri` it just bound, builds the
authorization URL with PKCE (S256) and a random `state`, hands the URL to the
`Display`, then waits on the loopback callback.

The discovered document is validated before it is used. Every endpoint it
advertises must use the same scheme as the configured server URL, and a
declared `issuer` must match the origin the document was fetched from. A
fixture that advertises an `http://` token endpoint under an `https://` server
URL, or an `issuer` on a different host, is rejected during discovery. The
engine also refuses cross-host redirects, and pins a refresh token to the token
endpoint that issued it (see section 5).

### Pointing a config at the fixture

```toml
[mcp.servers.localoauth]
url = "http://127.0.0.1:PORT/mcp"
type = "http"
auth = "oauth"
trust = "unrestricted"
```

`PORT` is the port the fixture's `httptest.Server` was assigned. Because the
endpoint is loopback, `trust = "unrestricted"` is required to clear the SSRF
check in `ValidateRemoteServer` (loopback/private/link-local are refused
otherwise).

### Plain HTTP requires the test-only seam

`ValidateRemoteServer` is **https-only** in production: a plain-`http://`
remote endpoint is rejected with `must use https; plain http is rejected`
unless `mcp.AllowInsecureHTTP(true)` has been called. That function
(`internal/tools/mcp/validate.go`) is a test seam — production code never
calls it — so the fixture path is exercised **from a Go test**, not from a
production binary. Do not expect a real `marshal` process to connect to the
local http fixture.

### Driving the flow in a test

Model your test on `internal/acp/mcpauth_test.go`:

1. `mcp.AllowInsecureHTTP(true)` (and restore it with `t.Cleanup`).
2. Stand up the fake AS with `httptest.NewServer`.
3. Build an `oauth.Engine` with `ServerURL` set to the fixture URL, an
   in-memory `credentials.MemStore` as the `Store`, and a `Display` that
   captures the URL.
4. Call `eng.Authorize(ctx, disp)` on a goroutine.
5. Parse the captured authorization URL, read `redirect_uri` and `state`, and
   issue an HTTP GET against `redirect_uri + "?code=...&state=..."` to drive
   the callback the browser would have made (`completeLoopback` in the test
   does exactly this).
6. Assert `Authorize` returns nil and the token was persisted under
   `marshal:mcp:<server-url>`.

The fixture never needs a real browser; step 5 is the whole interactive
callback.

---

## 2. Real hosted OAuth MCP server walkthrough

For a genuine https server that advertises OAuth (metadata discovery succeeds
and it supports dynamic client registration):

1. Add the server to `.marshal/config.toml` (project) or
   `~/.config/marshal/config.toml` (user):

   ```toml
   [mcp.servers.<name>]
   url = "https://mcp.example.com/mcp"
   type = "http"
   auth = "oauth"
   trust = "unrestricted"
   ```

2. Start Marshal. On startup the MCP manager tries to connect. With no stored
   token yet, the server's request fails with `oauth.ErrAuthRequired`, and the
   startup notice names the fix:

   ```
   MCP server '<name>' needs OAuth — run /mcp auth <name>
   ```

   (Rendered by `mcpFailureMessage` in `internal/app/app.go`.)

3. Run the command in the TUI:

   ```
   /mcp auth <name>
   ```

   The TUI opens a docked authorization panel, opens the authorization URL in
   your default browser (`mcpauth.OpenBrowser`), and shows the same URL in the
   panel. The engine has bound a loopback receiver on `127.0.0.1:53682` (the
   fixed `oauth.PreferredLoopbackPort`, falling back to an ephemeral port when
   that one is already in use) and registered a fresh client bound to that
   exact `redirect_uri` with the server.

4. Complete the flow in the browser. The provider redirects back to the
   loopback `redirect_uri`; the engine verifies `state`, exchanges the code at
   the token endpoint (PKCE), and writes the token set to the OS keychain.

5. The panel closes and Marshal reports:

   ```
   ✓ Authorized MCP server "<name>". Tokens are stored in the OS keychain.
   ```

6. Call one of the server's tools. Marshal rebuilds the runtime when the flow
   completes (`Model.reconnectMCPServer` in `internal/app/tui/mcp.go`), so the
   server that failed at startup is now started and its tools are registered on
   the live registry — **no restart is required**.

   The rebuild matters because a server that returned `ErrAuthRequired` at
   startup was recorded as a per-server failure and never entered the MCP
   manager's client set. Startup is the only place servers are connected and
   their tools registered, so without the reload the success message would be a
   lie: the token would be stored while the server stayed unreachable until the
   next process start.

   Should the rebuild itself fail, Marshal reports
   `✗ Authorized MCP server "<name>", but it could not be connected: <cause>`
   instead of claiming success. The token is still stored, so a later rebuild
   connects the server.

   Once the server *is* connected, `TokenSource` is consulted once per POST, so
   a token refreshed mid-session is picked up automatically.

---

## 3. Headless / ACP walkthrough

Over ACP the `/mcp` command is reported as `headless` and implemented by the
command manager itself (`internal/acp/mcpauth.go`), because the TUI-only
registry entry needs a wire equivalent. The TUI-only entry still supplies the
description and `auth <name>` args, so `session/command_list` shows it.

1. `session/command_list` — confirm `/mcp` is reported with kind `headless`
   (the manager implements it via `headlessCommands`).

2. `session/command` with params (note `sessionId` is required and `args`
   is a JSON string array, not a single string):

   ```json
   {"sessionId": "sess_...", "name": "mcp", "args": ["auth", "<name>"]}
   ```

   The manager resolves the named server with
   `commands.ResolveOAuthServer` (the same validation the TUI runs), opens the
   OS credential store, and starts `oauth.Engine.Authorize` on its own
   goroutine. The command returns immediately with a text acknowledgement.

3. The authorization URL arrives asynchronously as a `session/update`
   notification containing an `agent_message_chunk` with text like:

   ```
   Authorize MCP server "<name>" by opening this URL (a browser on the Marshal
   host can also complete the flow):
   https://…/authorize?…
   ```

   A terminal result (success, cancellation, or failure) is emitted later as
   another `agent_message_chunk`.

4. Open that URL in a browser **on the same machine as the Marshal host** and
   complete the provider flow. The loopback receiver is still listening, so
   the same-machine browser flow completes it, and the token is written to the
   keychain.

5. **The ACP path reloads the session's runtime in place.** When the flow
   completes, the headless command manager calls the runtime's config-reload
   handle, rebuilding the runtime so the now-authorized server's tools are
   registered live — no wire round-trip needed. The terminal notice reports
   either that the server is now connected, or that it could not be connected,
   with the cause; in the latter case the token stays stored, so a later reload
   or `session/load` still connects it.

   `session/load` and `session/resume` remain valid alternatives — the former
   starts a fresh runtime (`SessionManager.Load` in `internal/acp/session.go`),
   which re-runs MCP startup — now with a stored token — and replays the
   transcript; the latter restarts the runtime the same way without the replay —
   but they are no longer the only recovery. To reload the session over the
   wire:

   ```json
   {"sessionId": "sess_...", "cwd": "/path/to/repo"}
   ```

Reloads are serialized by a whole-reload gate (`Runtime.reloadMu`), so two
reloads cannot interleave their build/swap/cleanup phases. The reload still
mutates the live `*agent.Runner` in place (`Runner.CopyFrom`), though, so a
turn that is already running keeps reading the same runner pointer while its
fields are updated — the same posture the TUI has always had via
`reconnectMCPServer`, not a new ACP-only hazard. An in-flight turn may
therefore observe a mix of old and new provider and registry fields for the
duration of the swap.

`Engine.Open` is deliberately nil on this path: a headless server must not
spawn a browser on the host it runs on. Only the URL is emitted.

---

## 4. Known limitation: out-of-band paste is not reachable

The spec's fallback for a browser on a *different* machine — the user copies
the full redirect URL back to Marshal so the flow completes remotely — is
**not reachable with the landed engine API**, and is a deferred item.

`oauth.Engine.Authorize` binds its loopback receiver internally
(`StartLoopback`) and reads the single result it produces. There is no
exported seam to inject a pasted redirect URL or a callback result, and the
loopback `redirect_uri` is pinned to `127.0.0.1:<random port>` **on the Marshal
host**, which the user's browser must be able to reach.

Making the paste path reachable would require an `oauth`-package change, e.g.
an exported `RedirectResult`/`Inject`, or an `Authorize` variant that accepts a
caller-supplied redirect handler. Until then the only completion path — TUI or
headless — is the same-machine loopback browser flow; the headless URL notice
says so explicitly. This is documented in the source at
`internal/acp/mcpauth.go` (`headlessMCPDisplay`).

---

## 5. Token storage

OAuth tokens live **only** in the OS credential manager, keyed as

```
marshal:mcp:<server-url>
```

(`Engine.storageKey` in `internal/tools/mcp/oauth/oauth.go`; the value is a
JSON blob holding access token, refresh token, expiry, and cached client
id/secret).

- Nothing is written to `config.toml`.
- Nothing is written to the session database.
- The on-disk metadata cache (under the project database directory) stores
  only URLs and endpoint names — never secrets.

The store is opened through `internal/credentials`, whose whitelist is the
macOS Keychain, the Windows Credential Manager, and the Linux Secret Service.
It **never** falls back to plaintext on-disk storage. On a headless Linux host
with no Secret Service, `credentials.New` fails with an error wrapping
`credentials.ErrKeyringUnavailable`, which both the TUI and the headless path
surface as an actionable notice telling you to run Marshal on a machine with a
working keychain.

---

## 6. Troubleshooting

**401 after a successful-looking auth.** The stored access token was rejected
(e.g. revoked, or the server rotated its signing key). Re-run
`/mcp auth <name>` (or `session/command` with
`{"sessionId":"sess_...","name":"mcp","args":["auth","<name>"]}`) to drive
a fresh authorization and overwrite the keychain entry.
If a *refresh* was rejected with `invalid_grant`, the engine deletes the stale
entry itself and returns `ErrAuthRequired` — the same re-auth fix applies, and
the startup notice reappears on the next reload.

**Keychain unavailable.** The notice reads roughly: "OAuth tokens are stored
in the OS keychain, which is unavailable…". This happens on headless Linux
without a Secret Service (`gnome-keyring` / `KWallet` and a running session
bus). Either run Marshal where a secure backend exists, or accept that OAuth
MCP servers cannot be authorized on this host — there is no plaintext
fallback by design.

**Server rejects dynamic registration.** When discovery returns a
`registration_endpoint`, the engine registers a **fresh client on every flow**,
because the `redirect_uri` it sends carries a per-flow port and a strict server
rejects a client registered for a different one — the very redirect-URI
matching the pinning rule exists to enforce. DCR is therefore required on that
path: a server whose `/register` fails cannot be authorized.

Only when discovery returns **no** `registration_endpoint` does the engine fall
back to a cached `client_id`. If none was stored, `Authorize` fails with
`server does not support dynamic client registration and no client_id is
cached`. Use a server that supports DCR, or a provider that lets you
pre-register a client (pre-registration UI is not yet wired into the Engine's
config surface).

**`must use https` on a local endpoint.** Expected in production: only the
test seam `mcp.AllowInsecureHTTP(true)` permits plain `http://`. See section 1.

**Discovery fails.** The engine fetches `/.well-known/oauth-authorization-server`
(RFC 8414) with an OIDC fallback. A server that serves neither, or whose
metadata omits `authorization_endpoint`/`token_endpoint`, cannot be used with
`auth = "oauth"`.