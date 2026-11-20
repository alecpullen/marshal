# One daemon plan 8: previews in a browser — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20.

Base: `main` at `bbdc4486`, with plans [1](2026-11-20-od-1-one-daemon-plan.md),
[2](2026-11-20-od-2-hosts-over-ssh-plan.md),
[3](2026-11-20-od-3-web-listener-plan.md) (with its guard) and
[6](2026-11-20-od-6-remote-previews-plan.md) applied first. It does not depend
on plans 4, 5 and 7. Most of what it changes is code that plans 3 and 6
create, so steps name that code by the functions those plans declare.

Design: [design.md](../design.md) D11 and open question 1, carried from the
native Studio (plan 6) to a browser behind a proxy. The user chose, on
2026-11-20, to do two things: relay forwarded hosts' previews through the
local preview listener, and accept a `{token}` template in `preview_url`.
Planning settled these, and design.md records them:
- **`preview_url` may be a template.**
  - `https://p{token}.preview.example.com` gives each preview a hostname, and
    so an origin, of its own, as `p<token>.localhost` does on one machine.
  - `{token}` appears once, in the host's first label. Preview tokens are
    lower-case hex, so they survive DNS.
  - It needs a proxy with wildcard hostnames and a wildcard certificate.
    Without one, a plain `preview_url` keeps one shared preview origin.
- **The forward picks the preview origin by who is asking.**
  - A request whose `Host` is a loopback name (`localhost`, `127.0.0.1`,
    `::1`) comes from this machine. It keeps plan 6's
    `http://localhost:<previewPort>`.
  - Any other `Host` is the proxy's: plan 3's guard admits only those and
    `web_url`'s host. It gets this daemon's own `preview_url`, plain or
    templated, when one is set.
- **The local preview listener relays a forwarded host's previews.**
  - When the forward carries a preview request out for a proxied browser, it
    records the issued link's token and agent against the host.
  - The preview listener sends a request for a recorded preview to that
    host's tunnel, over plan 6's preview forward.
  - With a per-preview hostname, the relay rewrites `Host` to
    `p<token>.localhost`, which the remote daemon already reads.
  - With a shared origin, it keeps the path, the `?t=` token and the cookie.
  - The remote checks the token itself, as it does for a local request.
- **Both ends need this plan.** The remote daemon builds the link from the
  header, so it must read a template there. An older remote ignores one, and
  its preview links stay unreachable from a proxied browser.

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- A browser behind the operator's proxy opens previews of this daemon's agents
  and of every forwarded host's agents.
- Each preview gets an origin of its own where the proxy supports wildcard
  hostnames, and a shared preview origin where it does not. The preview origin
  is never the Studio's.
- Nothing changes for the native Studio, or for a browser on the daemon's own
  machine.

## Non-goals

- Wildcard DNS and certificates themselves. They are the proxy's to provide,
  and the docs show how.
- Per-preview origins on Tailscale Serve, which publishes one hostname per
  machine. It keeps the shared origin.
- Relaying previews between two forwarded hosts, or through more than one
  daemon.
- Notifications (later).

## Assumptions

- **Tokens.** `newPreviewToken` (`web/bridge/preview.go:75`) is 32 bytes of
  hex, and lives for `previewTokenTTL`, 12 hours (`:23`).
- **Serving.** `preview` (`:330`) authenticates by the host's token when
  `hostPreviewToken` (`:310`) reads one from `p<token>.localhost`. Otherwise it
  uses `?t=`, which it trades for a cookie `mp_<agent>_<port>` scoped to
  `/preview/<agent>/<port>/` (`:349–369`), then that cookie.
  `parsePreviewPath` (`:197`) reads the agent and port from the path.
- **Previews from the forward today.** After plan 6:
  - `hostForward`'s `Rewrite` sets `X-Marshal-Preview-Origin:
    http://localhost:<previewPort>` for a host with a preview port;
  - `issuePreview` builds the link from a valid header with
    `previewOriginFrom`, which takes a plain `http(s)` origin with an explicit
    port;
  - the remote's answer is `{"url": "<origin>/preview/<agent>/<port>/…"}`,
    with `?t=` unless the token is in the host.
- **The proxy's `Host`.** Plan 3's guard admits a request only when its host is
  a loopback name or `web_url`'s host. So a request through `hostForward`
  whose host is not a loopback name came through the proxy.
- **The preview listener** is served by `PreviewHandler` (`:246`), on the
  loopback address plan 3 opens, published by the proxy at `preview_url`.
- **`preview_url` checks.** Plan 3's `CheckPreviewURL` refuses a path, a query,
  a fragment and a non-http scheme. `cmd/marshal`'s `daemonPublicURL` checks
  `daemon.preview_url` before the unit is written. Neither accepts `{`, which
  `url.Parse` refuses in a host.
- **Go's `httputil.ReverseProxy`** carries a WebSocket upgrade, which a
  preview's dev server may use for reloading.

## Contracts

```go
// web/bridge/preview.go

// previewHostTemplate is a preview_url with {token} in its host:
// Scheme://Prefix<token>Suffix[:Port].
type previewHostTemplate struct {
	Scheme, Prefix, Suffix, Port string
}

// parsePreviewTemplate reads base as a template, and ok is false when base
// has no {token}. A {token} anywhere but once in the first host label is an
// error.
func parsePreviewTemplate(base string) (t previewHostTemplate, ok bool, err error)

// Origin is the template with token in place.
func (t previewHostTemplate) Origin(token string) string

// Token reads a token from a request's host (any port), when the host
// matches the template.
func (t previewHostTemplate) Token(host string) (string, bool)

// previewHostToken reads a token from a request's host: the localhost rule
// hostPreviewToken has, or this daemon's preview_url template. It replaces
// the package function at the one call in preview.
func (s *Server) previewHostToken(host string) (tok string, inHost bool)

// isLoopbackName reports whether host (port dropped) is localhost,
// 127.0.0.1 or ::1.
func isLoopbackName(host string) bool
```

```go
// web/bridge/previewrelay.go (new)

// previewRelay is one forwarded host's preview, as the forward issued it.
type previewRelay struct {
	Host    string // the forwarded host's name
	AgentID string
	Port    int
	Token   string
	InHost  bool // the token is in the link's host
	Expires time.Time
}

// maxPreviewRelays bounds the table; the oldest entry goes first.
const maxPreviewRelays = 1024

// recordPreviewRelay reads an issued link from a forwarded preview answer and
// records it for host.
func (s *Server) recordPreviewRelay(host, issued string)

// previewRelayFor finds the relay a preview request belongs to, when it is
// not this daemon's own.
func (s *Server) previewRelayFor(r *http.Request) (previewRelay, bool)

// relayPreview sends r over the relay's host's preview forward.
func (s *Server) relayPreview(w http.ResponseWriter, r *http.Request, rel previewRelay)

// New Server fields (http.go):
relaysMu      sync.Mutex
relaysByToken map[string]previewRelay
relaysByAgent map[string]previewRelay // key: agent ID and port, "<id>/<port>"
```

```go
// cmd/marshal/daemon.go

// daemonPreviewURL validates daemon.preview_url: an http or https origin,
// or a template with {token} once in its first host label.
func daemonPreviewURL(setting string) (string, error)
```

## Task 1: `preview_url` may be a template

**Goal:**
- `CheckPreviewURL` and `SetPreviewURL` accept `{token}` in the first host
  label.
- This daemon's own previews get `<scheme>://<prefix><token><suffix>`
  origins.
- Its preview listener reads the token from such a host.

**Files:** `web/bridge/preview.go`, `web/bridge/preview_test.go`.

**Steps:**
1. Add `previewHostTemplate`, `parsePreviewTemplate`, `Origin` and `Token`, as
   Contracts declares them.
   - `parsePreviewTemplate` finds `{token}` in the text after `://` and before
     the next `/`, `:` or end of string. It refuses the template when `{token}`
     appears twice, or sits outside the first label (after its first `.`).
     Then it checks the rest as `CheckPreviewURL` does, with `{token}`
     replaced by `0`.
   - `Token` drops a port, lower-cases the host, and requires the prefix and
     the suffix. It also requires the middle to be non-empty lower-case hex.
2. `CheckPreviewURL` accepts a valid template. It is otherwise unchanged.
3. `previewOrigin`: when `s.previewURL` is a template, answer `t.Origin(tok)`
   with `tokenInHost` true, before the plain `preview_url` rule.
4. Add `previewHostToken` as a method, as Contracts declares it. `preview`
   calls it in place of `hostPreviewToken`. Keep `hostPreviewToken` as the
   localhost half, which the method calls first.
5. Tests:
   - `TestAPreviewTemplateParses`: `https://p{token}.preview.example.com`,
     `http://{token}.p.example.com:8445`, and the refusals (`{token}` twice,
     in a second label, in the path, and a scheme that is not http);
   - `TestAPreviewTemplateNamesEachPreviewsOrigin`: two tokens, two origins,
     no `?t=`;
   - `TestThePreviewListenerReadsATemplatedHost`: a request to
     `p<tok>.preview.example.com` with a valid token reaches the agent, and a
     wrong token answers 404;
   - `TestAPlainPreviewURLIsUnchanged`.

**Verify:** `go test ./web/bridge -run Preview -count=1` passes.

## Task 2: The preview origin header may carry a template

**Goal:** a remote daemon builds a per-preview origin from a templated
`X-Marshal-Preview-Origin`, and accepts a plain origin without a port.

**Files:** `web/bridge/preview.go`, `web/bridge/preview_test.go`.

**Steps:**
1. `previewOriginFrom` (plan 6) answers the header's base and whether it is a
   template. It accepts:
   - a template, by `parsePreviewTemplate`;
   - a plain `http(s)` origin with or without a port.

   Everything else is still ignored.
2. In `issuePreview`:
   - a template header answers `t.Origin(tok)` with `tokenInHost` true;
   - a plain one answers as plan 6 made it, with the scheme's default port
     when none is given;
   - plan 6's `localhost` rule is unchanged.
3. Tests:
   - `TestATemplatedPreviewOriginHeaderNamesThePreviewsHost`;
   - `TestAPlainPreviewOriginHeaderWithoutAPort`;
   - plan 6's `TestABadPreviewOriginHeaderIsIgnored`, extended with a broken
     template.

**Verify:** `go test ./web/bridge -run Preview -count=1` passes.

## Task 3: The forward picks the preview origin, and records what it issued

**Goal:**
- A forwarded preview request from the proxy names this daemon's
  `preview_url`.
- One from this machine names plan 6's local port.
- The link that comes back is recorded against the host.

**Files:** `web/bridge/hostforward.go`, `web/bridge/previewrelay.go` (new),
`web/bridge/http.go`, `web/bridge/hostforward_test.go`,
`web/bridge/previewrelay_test.go` (new).

**Steps:**
1. Add `isLoopbackName`, as Contracts declares it, beside the guard's
   `allowedHost` (plan 3's `webguard.go`), and use it there too.
2. In `hostForward`'s `Rewrite` (after plan 6), choose the header:
   - **from this machine** (`isLoopbackName(r.Host)`): plan 6's
     `http://localhost:<previewPort>`, when the port is positive;
   - **from the proxy, with `s.previewURL` set:** `s.previewURL` as it is,
     plain or templated;
   - **from the proxy, without one:** plan 6's header, unchanged.
3. Add `previewRelay`, `maxPreviewRelays` and `recordPreviewRelay`, and the
   three `Server` fields, in `previewrelay.go`. `NewServer` makes both maps.
4. `recordPreviewRelay(host, issued)`:
   1. It parses `issued`, and reads the agent and port with
      `parsePreviewPath`.
   2. It reads the token: from the host, by `s.previewHostToken`
      (`InHost` true), else from `?t=`.
   3. It stores the entry under the token, and under `<agent>/<port>`, with
      `Expires` set to now plus `previewTokenTTL`.
   4. Expired entries are dropped first. A full table drops its oldest
      entry.
   5. A link it cannot read records nothing.
5. In `hostForward`, only when step 2 chose `s.previewURL`, and the request is
   `POST` with a remainder matching `api/agents/<id>/preview/<port>`: give the
   proxy a `ModifyResponse`.
   - On a 200 it reads the body (bounded at 64 KiB) and decodes `{"url":
     …}`.
   - It calls `recordPreviewRelay` with the host's name.
   - It puts the same bytes back as the body, with `Content-Length`
     unchanged.
   - Plan 3's existing `ModifyResponse`, if it has one, still runs.
6. Tests:
   - `TestAForwardFromThisMachineNamesThePreviewPort` (`hostforward_test.go`);
   - `TestAForwardFromTheProxyNamesThePreviewURL`: `Host:
     studio.example.com` and a templated `preview_url` reach the fake tunnel
     as the template;
   - `TestAForwardedPreviewIsRecorded`: the fake answers a templated link,
     the table holds its token and agent, and the caller gets the body
     unchanged;
   - `TestASharedOriginPreviewIsRecordedByItsToken`: a `?t=` link;
   - `TestThePreviewRelayTableIsBounded` (`previewrelay_test.go`): entries
     past `maxPreviewRelays` drop the oldest;
   - `TestAnExpiredPreviewRelayIsDropped`.

**Verify:** `go test ./web/bridge -run 'Forward|PreviewRelay' -count=1` passes.

## Task 4: The preview listener relays a forwarded host's previews

**Goal:** a request on this daemon's preview listener for a recorded forwarded
preview is sent to that host's preview forward. Anything else is served as
before.

**Files:** `web/bridge/previewrelay.go`, `web/bridge/preview.go`,
`web/bridge/previewrelay_test.go`.

**Steps:**
1. `previewRelayFor(r)`. The order matters: this daemon's own agent always
   wins.
   1. A host token (`s.previewHostToken(r.Host)`) that is valid for the path's
      agent here is not relayed. One that is in `relaysByToken` and
      unexpired is relayed.
   2. With no host token, the path's `<agent>/<port>` is relayed when it is in
      `relaysByAgent`, unexpired, and the fleet has no agent of that ID.
   3. Otherwise, no relay.
2. `relayPreview(w, r, rel)`:
   - Find the host in `s.reportedHosts()`. A host that is gone, not live, or
     reports no preview port answers 404, as every preview failure does.
   - Otherwise, serve through an `httputil.ReverseProxy` to
     `http://127.0.0.1:<PreviewPort>`, with `FlushInterval` -1.
   - Its `Rewrite` keeps the path, the query and the cookies.
   - For an `InHost` relay, `Host` becomes `p<token>.localhost`, which the
     remote's `hostPreviewToken` reads. For a shared-origin relay, `Host` stays
     as the browser sent it, so the remote uses its `?t=` and cookie path.
   - `X-Forwarded-*` headers are not added.
   - The remote's answer is passed back unchanged: its redirect, its
     `Set-Cookie` scoped to the preview's path, and its 404s.
3. In `PreviewHandler` (`preview.go:246`), after its prefix check: when
   `previewRelayFor` answers a relay, call `relayPreview` and return.
   Otherwise call `s.preview` as today.
4. Tests (`previewrelay_test.go`). A fake remote preview listener stands in on
   a loopback port, and is reported as the host's `PreviewPort` with the host
   live.
   - `TestARecordedTemplatedPreviewIsRelayed`: `Host: p<tok>.preview.example.com`
     reaches the fake as `Host: p<tok>.localhost`, with the path unchanged.
   - `TestARecordedSharedPreviewIsRelayedWithItsCookie`: a `?t=` request,
     then a cookie request, both reach the fake. The fake's `Set-Cookie`
     comes back.
   - `TestThisDaemonsOwnPreviewIsNotRelayed`: a token valid here is served
     here, even if a relay records the same agent ID.
   - `TestAnUnrecordedPreviewIs404`.
   - `TestARelayToAHostThatIsDownIs404`.
   - `TestARelayCarriesAWebSocketUpgrade`: the fake upgrades and echoes one
     message.

**Verify:** `go test ./web/bridge -run 'PreviewRelay|Relayed|Relay' -count=1`
passes, and so does `go test ./web/bridge/... -count=1`.

## Task 5: `marshal daemon install` accepts a templated `preview_url`

**Goal:** `daemon.preview_url` may be a template, and the unit writes it as
it is.

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps:**
1. Add `daemonPreviewURL`, as Contracts declares it.
   - It finds `{token}` with the same rules as Task 1: once, in the first host
     label.
   - It replaces it with `0`, and checks the result with plan 3's
     `daemonPublicURL("daemon.preview_url", …)`.
   - It answers the setting unchanged.
   - Its refusal names the rule: `daemon.preview_url: {token} goes once, in
     the first label of the host, as in https://p{token}.preview.example.com`.
2. `daemonUnitSpec` (plan 3) calls it for `PreviewURL` in place of
   `daemonPublicURL`.
3. Tests:
   - `TestDaemonInstallAcceptsAPreviewTemplate`;
   - `TestDaemonInstallRefusesABadPreviewTemplate`: twice, in the path, and
     in a second label.

**Verify:** `go test ./cmd/marshal -run DaemonInstall -count=1` passes.

## Task 6: The docs, and verification

**Goal:** the docs show how to publish previews behind a proxy, and the whole
works by hand.

**Files:** `AGENTS.md`, and `docs/DEPLOYMENT.md` if plan 5 has rewritten it.

**Steps:**
1. In `AGENTS.md`'s web listener paragraph (plan 3's Task 21), add one
   sentence:
   - `preview_url` may be a template, `https://p{token}.<domain>`, for a proxy
     with wildcard hostnames;
   - a proxied browser's previews of forwarded hosts are relayed by this
     daemon's preview listener over each host's tunnel.
2. In `docs/DEPLOYMENT.md`'s browser section, if it exists, replace the
   previews bullet with a short subsection, "Previews behind a proxy", that
   says:
   - Set `preview_listen` to a fixed loopback port, and publish it.
   - With wildcard DNS and a wildcard certificate (Caddy with a DNS
     challenge, or Cloudflare), set `preview_url =
     "https://p{token}.preview.example.com"`, and each preview gets its own
     origin. Give a five-line Caddy example.
   - With one hostname (Tailscale Serve), set a plain `preview_url` on a
     second port. Previews then share an origin, which is still not the
     Studio's.
   - Previews carry their own token, so let the preview hostnames past the
     proxy's login, as `/hooks/` is.
   - A forwarded host's previews come through this daemon, so the host needs
     nothing published. Both daemons need this plan.
3. Run:
   - `gofmt -l .` prints nothing, and `go vet ./...` passes;
   - `scripts/test.sh` passes;
   - `cd app && flutter analyze && flutter test` passes (no app change is
     expected).
4. **By hand, with no proxy.** Browsers resolve every `*.localhost` name to
   loopback, which stands in for wildcard DNS.
   1. On this machine, set `web_listen = "127.0.0.1:7700"`, `web_url =
      "http://studio.localhost:7700"`, `preview_listen = "127.0.0.1:8445"` and
      `preview_url = "http://p{token}.preview.localhost:8445"`. Then run
      `marshal daemon install`.
   2. Browse to `http://studio.localhost:7700`, which plan 3's guard admits as
      `web_url`'s host, and the forward treats as the proxy.
   3. A preview of this daemon's agent opens at
      `p<token>.preview.localhost:8445`.
   4. A preview of an agent on plan 2's `self` host also opens at
      `p<token>.preview.localhost:8445`, relayed. `lsof` shows the daemon
      dialling the tunnel's preview port.
   5. With a plain `preview_url = "http://preview.localhost:8445"`, both open
      on the shared origin, and the cookie path is the preview's.
   6. A dev server's live reload (WebSocket) works through the relay.

**Verify:** `grep -n 'p{token}' AGENTS.md` prints the new sentence, and the step
4 checks pass.

## Integration notes

- **Both ends need this plan.** A remote daemon from before it ignores a
  templated header and answers its own link, which a proxied browser cannot
  open. Its plain-origin previews still relay once this daemon has the plan.
- **Without a wildcard proxy, previews share an origin.** One agent's preview
  can read another's storage there. Neither can read the Studio's.
- **The relay table is in memory.** A restart of this daemon forgets which
  forwarded previews it issued. Reopening the preview from the Studio issues
  a fresh link and records it.
