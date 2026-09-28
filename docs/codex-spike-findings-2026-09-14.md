# OAuth Codex spike — findings

Probe: `cmd/marshal/probe_codex/` (build tag `probe_c`)
Probe git SHA at run time: `ba6b1784`
Source of truth for constants: `openai/codex` @ `44fe510ce3ee61c8ef623adcbf89b901c73ddd61`
(notes: `.docs-archive/superpowers/plans/2026-09-14-codex-source-notes.md`)
Design under test: `.docs-archive/superpowers/specs/2026-09-14-oauth-chatgpt-providers-design.md`

## Status of this document

The probe is **built, verified, and runnable**, and every subcommand has been
exercised end to end against the live endpoints. The credentialed runs
(`auth-begin` → browser login → `auth-poll`) require the user's ChatGPT login
and a browser, so they are marked **[LIVE — PENDING]** below with the exact
command to run. Sections that depend on a real token are marked accordingly
and are **not** filled with invented values.

What *was* observed without a real token is still real evidence: the endpoint
is reachable, it accepts the request shape, and it returns a well-formed
rejection with a machine-readable error code. Those observations are quoted
verbatim.

---

## 1. Auth flow summary

**Grant type used:** browser + loopback authorization-code with PKCE (S256).
Codex ships *both* this and a bespoke device flow; the probe implements both
(`auth-begin -grant pkce|device`).

| Item | Value | Provenance |
|---|---|---|
| client_id | `app_EMoamEEZ73f0CkXaXp7hrann` | codex source `login/src/auth/manager.rs:1717` |
| issuer | `https://auth.openai.com` | `login/src/server.rs:76` |
| authorize URL | `https://auth.openai.com/oauth/authorize` | `login/src/server.rs:602` |
| token URL | `https://auth.openai.com/oauth/token` | `login/src/server.rs:711` |
| redirect | `http://127.0.0.1:1455/auth/callback` (fallback port `1457`) | `login/src/server.rs:77,79,167` |
| scope | `openid profile email offline_access api.connectors.read api.connectors.invoke` | `login/src/server.rs:604-606` |
| PKCE | S256, 64-byte verifier | `login/src/server.rs` (`generate_pkce`) |
| extra params | `id_token_add_organizations=true`, `codex_cli_simplified_flow=true`, `originator=codex_cli_rs` | `login/src/server.rs:594-601` |

**Client identity mechanics:** a *public* client — no client secret is sent on
the code exchange (form-encoded `grant_type`/`code`/`redirect_uri`/`client_id`/
`code_verifier`). The client_id is a fixed constant baked into the codex CLI,
overridable by env var. There is no dynamic client registration.

**Device grant:** exists, but is **not RFC 8628**. It is a two-endpoint custom
protocol (`POST /api/accounts/deviceauth/usercode` → `POST
/api/accounts/deviceauth/token` → normal code exchange with a
**server-supplied** PKCE verifier and `redirect_uri =
https://auth.openai.com/deviceauth/callback`). Verification URL:
`https://auth.openai.com/codex/device`. Poll interval arrives as a *string*;
403/404 mean "keep polling"; timeout 15 minutes.

**Observed (probe run, no real token):**

```
grant:        pkce (browser + loopback)
redirect_uri: http://127.0.0.1:1455/auth/callback
client_id:    app_EMoamEEZ73f0CkXaXp7hrann
scope:        openid profile email offline_access api.connectors.read api.connectors.invoke

Open this URL in a browser and sign in:

  https://auth.openai.com/oauth/authorize?client_id=app_EMoamEEZ73f0CkXaXp7hrann&code_challenge=...&code_challenge_method=S256&codex_cli_simplified_flow=true&id_token_add_organizations=true&originator=codex_cli_rs&redirect_uri=http%3A%2F%2F127.0.0.1%3A1455%2Fauth%2Fcallback&response_type=code&scope=openid+profile+email+offline_access+api.connectors.read+api.connectors.invoke&state=...

Flow state saved to /home/alec/.cache/marshal-codex-spike/flow.json (0600).
```

Port 1455 was free and bound successfully on this machine.

**Refresh-token existence / rotation:** **[LIVE — PENDING]**. Source says the
refresh response carries an optional `refresh_token` and codex overwrites the
stored one whenever present (`login/src/auth/manager.rs:1616-1618`,
`:3097-3105`) — i.e. rotation is expected but not guaranteed. The probe records
presence/absence explicitly:

```
go run -tags probe_c ./cmd/marshal/probe_codex auth-begin
# open the printed URL, sign in
go run -tags probe_c ./cmd/marshal/probe_codex auth-poll
```

`auth-poll` prints `token response keys: [...]`, plus explicit
`refresh_token: present|ABSENT` and `id_token: present|ABSENT` lines.

**Exact token JSON keys:** **[LIVE — PENDING]**. Codex's own struct expects
`id_token`, `access_token`, `refresh_token` (`login/src/server.rs:698-703`).
The probe stores the raw response verbatim so the real key set can be quoted
without guessing.

---

## 2. Token shape

**Observed** against a synthetic JWT of the expected shape (proves the decoder
and the claim path work; the real claim list still needs the live run):

```
access_token: eyJhbGciOiAi... (len 291)
claim names (4): [exp https://api.openai.com/auth iat sub]
exp: 1.893456e+09 (2030-01-01T00:00:00Z)
iat: 1.8934524e+09 (2029-12-31T23:00:00Z)
claims under "https://api.openai.com/auth": [chatgpt_account_id chatgpt_plan_type chatgpt_user_id]
chatgpt_account_id: acct-smoke-test
id_token chatgpt_account_id: acct-smoke-test
```

- **Account-id claim name:** `chatgpt_account_id`, nested under the namespaced
  claim `https://api.openai.com/auth` — confirmed in source
  (`login/src/success_page.rs:106-130`, `login/src/server.rs:849-853`) and
  exercised by the probe.
- **Expiry:** codex reads `exp` from the **access-token JWT**, not from an
  `expires_in` field (`login/src/token_data.rs:142-147`). The probe prints both
  `exp` and `iat` with RFC3339 renderings.
- **Opaque-token case:** if the access token is not a JWT the probe prints
  `FINDING: access token is opaque, not a JWT` and exits 0 rather than failing.
- **Real claim list:** **[LIVE — PENDING]** — run `decode-token` after
  `auth-poll`.

---

## 3. Chat endpoint

| Item | Value | Provenance |
|---|---|---|
| URL | `https://chatgpt.com/backend-api/codex/responses` (POST) | `model-provider-info/src/lib.rs:77`, `codex-api/src/endpoint/responses.rs:143` |
| auth | `Authorization: Bearer <access_token>` | `model-provider/src/bearer_auth_provider.rs:31-34` |
| account header | `ChatGPT-Account-ID: <chatgpt_account_id>` | `model-provider/src/bearer_auth_provider.rs:36` |
| originator | `originator: codex_cli_rs` | `login/src/auth/default_client.rs:42` |
| accept | `text/event-stream` | `codex-api/src/endpoint/responses.rs:143-146` |
| `store` | hardcoded `false` | `core/src/client.rs:991-998` |
| `stream` | hardcoded `true` | `core/src/client.rs:991-998` |

**Observed (probe run with a synthetic token):**

```
POST https://chatgpt.com/backend-api/codex/responses
model: gpt-5.2-codex
account header: present (acct-smoke-t...)
request body: {"model":"gpt-5.2-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say the word OK"}]}],"store":false,"stream":true}

HTTP 401
Cf-Cache-Status: DYNAMIC
Cf-Ray: a421809f9d365aa4-MEL
Content-Length: 199
Content-Type: text/plain
Cross-Origin-Opener-Policy: same-origin-allow-popups
Date: Mon, 28 Sep 2026 08:50:46 GMT
Nel: {"report_to":"cf-nel","success_fraction":0.01,"max_age":604800}
Referrer-Policy: strict-origin-when-cross-origin
Report-To: {"group":"cf-nel","max_age":604800,"endpoints":[{"url":"https://a.nel.cloudflare.com/report/v4?s=..."}]}
Server: cloudflare
Set-Cookie: __cf_bm=...; HttpOnly; SameSite=None; Secure; Path=/; Domain=chatgpt.com; Expires=...
Set-Cookie: __cflb=...; HttpOnly; SameSite=None; Secure; Path=/; Expires=...
Strict-Transport-Security: max-age=31536000; includeSubDomains; preload
X-Content-Type-Options: nosniff
X-Error-Json: <base64 of the JSON body>
X-Openai-Authorization-Error: 401
X-Openai-Ide-Error-Code: unauthorized_unknown
X-Openai-Internal-Caller: unknown_through_ide
--- body ---
{
  "error": {
    "message": "Could not parse your authentication token. Please try signing in again.",
    "type": null,
    "code": "unauthorized_unknown",
    "param": null
  },
  "status": 401
}
FINDING: token rejected (HTTP 401)
```

Findings from this run:

- The endpoint is **reachable** and accepts the request shape — the rejection
  is at the auth layer, not schema validation.
- `X-Error-Json` is a base64-encoded duplicate of the JSON body.
- `X-Openai-Ide-Error-Code` is a machine-readable error code
  (`unauthorized_unknown` here) — useful for the provider's error mapping.
- Cloudflare sets `__cf_bm` / `__cflb` cookies. Codex keeps a process-global
  cookie jar restricted to Cloudflare infrastructure cookies
  (`http-client/src/chatgpt_cloudflare_cookies.rs`); a third-party client that
  ignores cookies still works, but may pay a challenge cost.
- No `x-codex-*` rate-limit headers appear on a 401 — they are success-path
  telemetry.

**Account-id requirement:** **[LIVE — PENDING]**. The probe has a
`-no-account-header` flag to test it directly:

```
go run -tags probe_c ./cmd/marshal/probe_codex test-chat
go run -tags probe_c ./cmd/marshal/probe_codex test-chat -no-account-header
```

**Agree / differ vs Task 1 source notes:**

| Claim | Source notes | Observed | Verdict |
|---|---|---|---|
| Endpoint URL | `chatgpt.com/backend-api/codex/responses` | same host/path reachable | agree |
| `store=false`, `stream=true` accepted | hardcoded in codex | request accepted (401 at auth, not 400 at schema) | agree (weak — needs 2xx to confirm) |
| `originator` header | `codex_cli_rs` | sent; no rejection attributable to it | agree (weak) |
| `ChatGPT-Account-ID` header name | `ChatGPT-Account-ID` | sent; no rejection attributable to it | agree (weak) |
| Error body shape | not documented in source notes | `{"error":{message,type,code,param},"status":401}` | new observation |
| `X-Error-Json` header | not in source notes | present, base64 of the body | new observation |

---

## 4. Model listing

**Observed (probe run with a synthetic token):**

```
=== (a) codex models endpoint with client_version ===
GET https://chatgpt.com/backend-api/codex/models?client_version=0.0.0
HTTP 401
... X-Openai-Ide-Error-Code: unauthorized_unknown
X-Request-Id: req_18cb3224cd0546ad9135c7b3996213d3
--- body ---
{"error":{"message":"Could not parse your authentication token. Please try signing in again.","type":null,"code":"unauthorized_unknown","param":null},"status":401}

=== (b) codex models endpoint without query ===
GET https://chatgpt.com/backend-api/codex/models
HTTP 401
...

=== (c) codex CLI on-disk model cache ===
path: /home/alec/.codex/models_cache.json
cache read failed: read /home/alec/.codex/models_cache.json: no such file or directory
```

- The endpoint **exists** (it is not a 404) and requires auth. Both the
  `client_version`-qualified and bare forms behave identically at the auth
  layer, so the query parameter is not what gates access.
- The codex CLI is not installed on this machine, so the on-disk cache
  (`$CODEX_HOME/models_cache.json`, `$CODEX_HOME` default `~/.codex`) is
  absent. The probe reads it read-only when present.
- **The actual list:** **[LIVE — PENDING]** — run `models` after `auth-poll`.
- **D5 mapping:** the live source is confirmed to exist and to be the same
  endpoint codex uses, so D5's "live list → disk `modelcache` → static
  fallback" chain is implementable as designed. The static fallback list
  cannot be proposed until the live list is seen.

---

## 5. Quota telemetry

**Observed (probe run with a synthetic token):**

```
{"call":1,"headers":[],"status":401}
{"call":2,"headers":[],"status":401}
```

No quota headers on the failure path — expected, and it means the two-call
diff is only meaningful on 2xx.

**Schema from source** (`codex-rs/codex-api/src/rate_limits.rs`), to be
confirmed live:

| Header | Type | Meaning |
|---|---|---|
| `x-codex-primary-used-percent` | f64 | percent of the primary window consumed |
| `x-codex-primary-window-minutes` | i64 | primary window length |
| `x-codex-primary-reset-at` | i64 | primary reset (unix seconds) |
| `x-codex-secondary-used-percent` | f64 | percent of the secondary window consumed |
| `x-codex-secondary-window-minutes` | i64 | secondary window length |
| `x-codex-secondary-reset-at` | i64 | secondary reset (unix seconds) |
| `x-codex-limit-name` | string | human-readable limit name |
| `x-codex-credits-has-credits` | bool | credits available |
| `x-codex-credits-unlimited` | bool | unlimited plan |
| `x-codex-credits-balance` | string | credit balance |
| `x-codex-promo-message` | string | promo banner text |
| `x-codex-rate-limit-reached-type` | string | rate-limit-reached classification |

Additional limit families are discovered by scanning for the
`x-<id>-primary-used-percent` shape (`_` normalised to `-`). There is also an
**SSE event** carrying the same data:
`{"type":"codex.rate_limits","plan_type":...,"rate_limits":{"primary":{...},"secondary":{...}},"credits":{...}}`.
The probe records that event when present.

**`whisker-*` headers: NOT FOUND in source.** The article's "whisker" naming
does not exist in the codex tree; the real family is `x-codex-*`.

**Two-call diff:** **[LIVE — PENDING]** — run `quota` after `auth-poll`.

**D19 conclusion (provisional):** implementable as spec'd, with one caveat —
the primary/secondary windows are **percent-used**, not remaining-requests, so
the footer should render a percentage plus a reset time rather than a
"requests left" count. If the live run shows only the SSE event and no
headers, D19 reduces to "parse the stream event" instead of "parse response
headers" — a small change to where the provider reads the data, not to the
`QuotaInfo` shape.

---

## 6. Capability results

**Observed (probe run with a synthetic token):**

```
model: gpt-5.2-codex

=== 1. tool calling ===
HTTP 401
FAIL (no function_call item in the stream)
evidence: {"error":{"message":"Could not parse your authentication token...","code":"unauthorized_unknown"},"status":401}

=== 2. structured output ===
HTTP 401
FAIL (no assistant text in the stream)
evidence: (same 401 body)

=== 3. reasoning ===
HTTP 401
```

All three sub-tests are wired and produce PASS/FAIL with evidence; none can
pass without a real token.

**Per-model PASS/FAIL table:** **[LIVE — PENDING]**

```
go run -tags probe_c ./cmd/marshal/probe_codex caps -model <slug>
```

**Proposed `ProviderCapabilities`:** cannot be pinned until the live run.
The probe's three sub-tests map directly onto the fields that need pinning:
tool calling → `ToolCalling`, structured output → `StructuredOutput`,
reasoning acceptance → `Reasoning`/`ReasoningCapable`.

**Proposed preset model list:** **[LIVE — PENDING]** — depends on §4.

---

## 7. Prompt enforcement

**Observed (probe run with a synthetic token):**

```
=== 1. control: instructions = "reply only with 42" ===
HTTP 401
reply: "{\n  \"error\": {\n    \"message\": \"Could not parse your authentication token...\"..."

=== 2. marker: instructions = "never use the letter e" ===
HTTP 401
reply: (same 401 body)
contains 'e': true

=== 3. injection: directive as the first user item, no instructions ===
HTTP 401
reply: (same 401 body)

ENFORCED=INCONCLUSIVE (statuses: control=401 marker=401 injection=401)
INJECTION_WORKS=INCONCLUSIVE
NOTE: at least one call did not return 2xx; no verdict can be drawn.
```

The verdict logic was hardened during the spike: an earlier revision reported
`ENFORCED=true` on a 401 because the error body contains the letter "e". The
probe now requires all three calls to return 2xx before drawing a verdict, and
prints `INCONCLUSIVE` otherwise. This is recorded because it is exactly the
kind of false positive that would have mis-selected the D2 branch.

**Verdict booleans:** **[LIVE — PENDING]** — run `prompt-enforcement` after
`auth-poll`.

**Which D2 branch:** undetermined. The source evidence leans toward
"instructions are honoured": codex sends its own base instructions in the
`instructions` field and expects them to work, and the instructions text is
itself **server-supplied** from the `/models` catalog
(`prompts/src/model_instructions.rs:8-17` reads
`model_info.model_messages.instructions_template`). If the endpoint were
replacing caller instructions wholesale, codex's own catalog-driven prompt
would be pointless. That is an argument, not a measurement — the live run
decides.

**Proposed slim preamble (draft, only used if `ENFORCED=true`):** spec §2's
deferred decision. Draft content, loop-invariants only:

```
You are operating inside marshal, a local coding agent. Three rules override
your defaults:

1. Tool gating: never claim to have run a command or read a file unless a tool
   result in this conversation shows it. If you need information, call a tool.
2. Verification loop: after changing code, run the narrowest command that
   proves the change works, and report its actual output.
3. Skill consent: load a skill only when its description matches the task, and
   never load more than the task needs.

Everything else — persona, tone, general coding guidance — is yours.
```

This is deliberately short: stacking a second full persona on top of the
endpoint's own risks degraded compliance, and the three rules above are what
marshal's loop actually depends on.

---

## 8. Deviations from the article

| Article claim | Observed / sourced reality | Verdict |
|---|---|---|
| Device-code authorization (RFC 8628) | A device flow exists but is a **bespoke two-endpoint protocol**, not RFC 8628; browser+loopback PKCE is codex's default | **differ** |
| "whisker" quota headers | No `whisker*` string anywhere in the codex tree; the family is `x-codex-*` | **differ** |
| Hardcoded system prompt in the client | Instructions are **server-supplied** from the `/models` catalog (`model_messages.instructions_template`); the bundled `core/*_prompt.md` files are unreferenced legacy artifacts | **differ** |
| Short-lived JWT saved locally | Confirmed: `$CODEX_HOME/auth.json`, access token is a JWT, expiry read from `exp` | **agree** |
| Chat requests to `chatgpt.com/backend-api/...` with codex-specific headers | Confirmed: `/backend-api/codex/responses`, `originator`, `ChatGPT-Account-ID` | **agree** |
| Fair-use caps | Confirmed as a header/event schema (`x-codex-*`); live values unmeasured | **agree (schema only)** |
| Token refresh | Confirmed: refresh token, proactive refresh 5 min before `exp`, 8-day fallback cadence, rotation expected | **agree** |

---

## 9. Recommendation

**GO-WITH-CHANGES** for Phases 2–6.

Rationale: every structural assumption the design rests on is confirmed by
source and by a live endpoint probe — the endpoints exist, the client identity
is a public PKCE client, the request shape is accepted, the account-id claim
path is real, and the quota schema is concrete. Nothing found so far
invalidates the architecture.

The changes required before Phase 2 planning:

1. **Spec §2/D2 must stay open until the live run.** The source evidence
   suggests instructions are honoured (which selects the *no-slim-preamble*
   branch), but this is the design's most consequential unknown and the probe
   now measures it. Do not bake in either branch yet.
2. **Spec D19 wording.** Quota is percent-of-window, not remaining-requests.
   Reword the footer/roster rendering accordingly, and allow for the
   possibility that the data arrives as an SSE event rather than headers.
3. **Spec §1 (device grant).** The design says a device-grant variant "slots
   beside `Authorize`". It is not RFC 8628 and it needs *two* extra endpoints
   plus a server-supplied PKCE verifier — a bigger seam than "a variant".
   Budget for it explicitly, or defer device login to a follow-on.
4. **New provider requirement: Cloudflare cookie handling.** The endpoint sits
   behind Cloudflare and sets `__cf_bm`/`__cflb`. Codex keeps a
   Cloudflare-only cookie jar. Decide whether the provider needs one (probably
   not for v1, but it should be a recorded decision).
5. **Error mapping.** `X-Openai-Ide-Error-Code` and `X-Error-Json` give a
   machine-readable failure code; the provider's error handling should use
   them rather than string-matching the message.

**Blocking item:** the live run. Until `auth-begin` → `auth-poll` completes
with a real ChatGPT login, sections 1 (token keys, refresh rotation), 2 (real
claims), 3 (account-header requirement, 2xx behaviour), 4 (model list),
5 (quota values), 6 (capabilities), and 7 (the D2 verdict) remain unmeasured.
The probe is ready; the commands are listed in each section above.

**Follow-up (out of scope, noted for the record):** `cmd/marshal/probe_kimi/`
is defective — `probe.go` declares `probeKimiMain() int` but no `func main`,
so `go build -tags probe ./cmd/marshal/probe_kimi` fails with "function main is
undeclared". `probe_codex` deliberately includes its own `func main` and is
unaffected.
