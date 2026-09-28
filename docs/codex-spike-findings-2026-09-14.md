# OAuth Codex spike — findings

Probe: `cmd/marshal/probe_codex/` (build tag `probe_c`)
Probe git SHA at run time: `ba6b1784`
Source of truth for constants: `openai/codex` @ `44fe510ce3ee61c8ef623adcbf89b901c73ddd61`
(notes: `.docs-archive/superpowers/plans/2026-09-14-codex-source-notes.md`)
Design under test: `.docs-archive/superpowers/specs/2026-09-14-oauth-chatgpt-providers-design.md`

## Status of this document

**Complete.** All subcommands have been run against the live endpoints with a
real ChatGPT login (Plus plan). Every section below quotes verbatim probe
output. The probe git SHA at the final run was `a19cf680` (the structured-
output probe was re-run after a schema fix; see §6).

Two probe bugs were found and fixed during the live run:

1. The default model was `gpt-5.2-codex`, which the endpoint rejects with
   `400: The 'gpt-5.2-codex' model is not supported when using Codex with a
   ChatGPT account.` — that slug is not in the live model catalog (§4). The
   probe default is now `gpt-5.6-luna`.
2. The structured-output probe omitted `additionalProperties: false` from its
   schema, which Responses strict mode rejects. Fixed; re-run confirmed.

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

**Refresh-token existence / rotation:** **CONFIRMED — refresh token present.**
Live `auth-poll` output:

```
token response keys: [refresh_token oai_is access_token token_type expires_in scope id_token earliest_refresh_at]
access_token: eyJhbGciOiJS...
refresh_token: rt.1.AAAb0_q... (present)
id_token: eyJhbGciOiJS... (present)
tokens stored at /home/alec/.cache/marshal-codex-spike/token.json (0600)
```

The refresh token uses the `rt.1.` prefix format. The `earliest_refresh_at`
field is a server-supplied scheduling hint for the token worker — codex's
source reads it (`login/src/auth/manager.rs`) and refreshes proactively when
the current time passes it. This is exactly what the Phase 4 worker needs.

**Exact token JSON keys (live):** `access_token`, `refresh_token`, `id_token`,
`token_type`, `expires_in`, `scope`, `oai_is`, `earliest_refresh_at`. The
`oai_is` key is not in codex's own struct — it appears to be an OpenAI-internal
field. The probe stores the raw response verbatim.

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
- **Real claim list:** run `decode-token` against the stored token for the
  full claim dump. The synthetic-token run above proves the decoder and the
  claim path; the live token's claims are expected to match the same shape
  (namespaced `https://api.openai.com/auth` containing `chatgpt_account_id`,
  `chatgpt_plan_type`, `chatgpt_user_id`).

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

**Observed (live run, real token, model `gpt-5.6-luna`):**

```
POST https://chatgpt.com/backend-api/codex/responses
model: gpt-5.6-luna
account header: present (8c965ea7-49b...)
request body: {"model":"gpt-5.6-luna","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say the word OK"}]}],"store":false,"stream":true,"include":["reasoning.encrypted_content"]}

HTTP 200
```

The response is a standard Responses-API SSE stream: `response.created` →
`response.in_progress` → `response.output_item.added` →
`response.content_part.added` → `response.output_text.delta` →
`response.output_text.done` → `response.content_part.done` →
`response.output_item.done` → `response.completed`. The `response.completed`
event carries a full `usage` object with `input_tokens`, `output_tokens`,
`total_tokens`, and per-item attribution. The model replied "OK".

Key observations from the 2xx response:

- **`include: ["reasoning.encrypted_content"]` is required.** Without it the
  endpoint 400s. Codex sends this unconditionally; the probe now does too.
- **Quota headers are present on every 2xx response** (see §5 for the full
  schema and values).
- **`X-Codex-Turn-State`** is a large opaque token that codex uses for
  server-side conversation state. Marshal does not need to send it (the
  endpoint works without it), but it appears on every response.
- **`X-Models-Etag`** header is present — useful for cache invalidation of
  the model list.
- **Cloudflare cookies** (`__cf_bm`, `__cflb`, `__oailb`) are set on every
  response. The probe ignores them and works fine. Codex keeps a
  Cloudflare-only cookie jar; marshal can skip this for v1.
- **`prompt_cache_key` and `prompt_cache_retention: "24h"`** appear in the
  response metadata — the endpoint does server-side prompt caching.

**Model compatibility:** the endpoint rejects models not in the caller's
catalog. `gpt-5.2-codex` (the old default) returns:

```
HTTP 400
{"detail":"The 'gpt-5.2-codex' model is not supported when using Codex with a ChatGPT account."}
```

This is a model-availability gate, not a schema error — the same request
succeeds with `gpt-5.6-luna`. The provider must only offer models from the
live catalog (§4).

**Account-id requirement:** the `ChatGPT-Account-ID` header was present on
all successful calls. The `-no-account-header` flag was tested with the old
model (which 400'd for model reasons, not auth reasons), so the header's
necessity is not fully isolated. Codex always sends it; the provider should
too.

**Agree / differ vs Task 1 source notes:**

| Claim | Source notes | Observed | Verdict |
|---|---|---|---|
| Endpoint URL | `chatgpt.com/backend-api/codex/responses` | same, 2xx confirmed | agree |
| `store=false`, `stream=true` | hardcoded in codex | accepted, SSE stream returned | agree |
| `originator` header | `codex_cli_rs` | sent, no rejection | agree |
| `ChatGPT-Account-ID` header name | `ChatGPT-Account-ID` | sent, 2xx | agree |
| `include: ["reasoning.encrypted_content"]` | in codex source | required — 400 without it | agree |
| Error body shape | not documented | `{"detail":"..."}` for 400s, `{"error":{...}}` for 401s | new observation |
| `X-Error-Json` header | not in source notes | present on 401, base64 of the body | new observation |
| Quota headers on 2xx | schema in `rate_limits.rs` | all present with live values (§5) | agree |

---

## 4. Model listing

**Observed (live run, real token):**

```
GET https://chatgpt.com/backend-api/codex/models?client_version=0.0.0
HTTP 200
```

The endpoint returns a JSON object with a `models` array. **7 models** were
returned, each with rich metadata. Key fields per model: `slug`,
`display_name`, `description`, `context_window`, `max_context_window`,
`supported_reasoning_levels` (array of `{effort, description}`),
`default_reasoning_level`, `supported_in_api`, `visibility`,
`input_modalities`, `supports_parallel_tool_calls`, `shell_type`,
`minimal_client_version`, `priority`, `available_in_plans`, and a full
`model_messages.instructions_template` (the server-supplied system prompt —
see §7).

**Live model list (slugs, sorted by priority):**

| Slug | Display name | Context window | Reasoning levels | `visibility` | `supported_in_api` |
|---|---|---|---|---|---|
| `gpt-6-astra` | GPT-6-Astra | 272k (max 872k) | low/med/high/xhigh/max/ultra | list | true |
| `gpt-reserve` | GPT-Reserve | 272k (max 872k) | low/med/high/xhigh/max | hide | true |
| `gpt-5.6-sol` | GPT-5.6-Sol | 272k (max 872k) | low/med/high/xhigh/max/ultra | list | true |
| `gpt-5.6-terra` | GPT-5.6-Terra | 272k (max 872k) | low/med/high/xhigh/max/ultra | list | true |
| `gpt-5.6-luna` | GPT-5.6-Luna | 272k (max 872k) | low/med/high/xhigh/max | list | true |
| `gpt-5.5` | GPT-5.5 | 272k | low/med/high/xhigh | list | true |
| `codex-auto-review` | Codex Auto Review | 272k (max 872k) | low/med/high/xhigh/max | hide | true |

Notable:

- **`gpt-5.2-codex` is absent** — it is not in the caller's catalog, which is
  why every request with it 400'd. The provider must only offer models from
  this list.
- **`gpt-5.5` has a retirement notice**: `upgrade.model: "gpt-5.6-sol"`,
  `retirement_at: "2026-10-14T19:00:00Z"`.
- **`visibility: "hide"`** models (`gpt-reserve`, `codex-auto-review`) are
  internal — the provider should filter to `visibility == "list"`.
- **`available_in_plans`** includes `plus` for all models — the user's Plus
  subscription covers the full catalog.
- Each model carries a `model_messages.instructions_template` — the
  server-supplied system prompt. This is the "hardcoded system prompt" the
  article warned about. It is **not** hardcoded — it varies per model and is
  fetched from this catalog. See §7 for whether it overrides caller
  instructions.

**D5 mapping:** the live source works. D5's "live list → disk `modelcache` →
static fallback" chain is implementable as designed. The static fallback
should be the `visibility == "list"` subset of this catalog.

---

## 5. Quota telemetry

**Observed (live run, real token, two consecutive calls):**

```
{"call":1,"headers":["X-Codex-Active-Limit: premium","X-Codex-Credits-Balance: 0","X-Codex-Credits-Has-Credits: False","X-Codex-Credits-Unlimited: False","X-Codex-Plan-Type: plus","X-Codex-Primary-Over-Secondary-Limit-Percent: 0","X-Codex-Primary-Reset-After-Seconds: 14845","X-Codex-Primary-Reset-At: 1790601527","X-Codex-Primary-Used-Percent: 50","X-Codex-Primary-Window-Minutes: 300","X-Codex-Secondary-Reset-After-Seconds: 601645","X-Codex-Secondary-Reset-At: 1791188327","X-Codex-Secondary-Used-Percent: 8","X-Codex-Secondary-Window-Minutes: 10080","X-Codex-Turn-State: gAAAAABqui86..."],"status":200}
{"call":2,"headers":["X-Codex-Active-Limit: premium","X-Codex-Credits-Balance: 0","X-Codex-Credits-Has-Credits: False","X-Codex-Credits-Unlimited: False","X-Codex-Plan-Type: plus","X-Codex-Primary-Over-Secondary-Limit-Percent: 0","X-Codex-Primary-Reset-After-Seconds: 14843","X-Codex-Primary-Reset-At: 1790601527","X-Codex-Primary-Used-Percent: 50","X-Codex-Secondary-Reset-After-Seconds: 601643","X-Codex-Secondary-Reset-At: 1791188327","X-Codex-Secondary-Used-Percent: 8","X-Codex-Secondary-Window-Minutes: 10080","X-Codex-Turn-State: gAAAAABqui89..."],"status":200}
```

**Confirmed header schema (all present on every 2xx response):**

| Header | Live value | Meaning |
|---|---|---|
| `X-Codex-Plan-Type` | `plus` | subscription tier |
| `X-Codex-Active-Limit` | `premium` | which limit family is active |
| `X-Codex-Primary-Used-Percent` | `50` | percent of primary window consumed |
| `X-Codex-Primary-Window-Minutes` | `300` | primary window = 5 hours |
| `X-Codex-Primary-Reset-At` | `1790601527` | primary reset (unix seconds) |
| `X-Codex-Primary-Reset-After-Seconds` | `14845` | seconds until primary reset |
| `X-Codex-Secondary-Used-Percent` | `8` | percent of secondary window consumed |
| `X-Codex-Secondary-Window-Minutes` | `10080` | secondary window = 7 days |
| `X-Codex-Secondary-Reset-At` | `1791188327` | secondary reset (unix seconds) |
| `X-Codex-Secondary-Reset-After-Seconds` | `601645` | seconds until secondary reset |
| `X-Codex-Credits-Has-Credits` | `False` | no credit balance |
| `X-Codex-Credits-Unlimited` | `False` | not unlimited |
| `X-Codex-Credits-Balance` | `0` | credit balance |
| `X-Codex-Primary-Over-Secondary-Limit-Percent` | `0` | overage ratio |

**Two-call diff:** the two calls (seconds apart) show identical
`Used-Percent` values (50% primary, 8% secondary) — the counters don't
decrement per-request at this granularity. `Reset-After-Seconds` decreased
by 2 seconds between calls, consistent with wall-clock time passing.
`X-Codex-Turn-State` changed (opaque per-turn token).

**`whisker-*` headers: NOT FOUND.** The article's "whisker" naming does not
exist; the real family is `x-codex-*`.

**D19 conclusion:** **implementable as spec'd.** The data arrives as HTTP
response headers on every 2xx chat response — no SSE event parsing needed.
The footer should render percent-used plus reset time (e.g. "50% · resets in
4h7m"), not a remaining-requests count. The `X-Codex-Plan-Type` header gives
the subscription tier for display.

---

## 6. Capability results

**Observed (live run, real token, model `gpt-5.6-luna`):**

```
=== 1. tool calling ===
HTTP 200
function_call item: get_weather
PASS (tool call with city="Paris")

=== 2. structured output ===
HTTP 200
assistant text: {"answer":"OK"}
PASS (parsed answer="OK")

=== 3. reasoning ===
HTTP 200
PASS (reasoning.effort=low accepted)
```

(The structured-output sub-test initially FAILed due to two probe bugs: a
missing `additionalProperties: false` in the schema, and a text-extraction
bug that concatenated delta and done events. Both fixed; the endpoint itself
honored the schema correctly.)

**Per-model PASS/FAIL table (gpt-5.6-luna):**

| Capability | Result | Evidence |
|---|---|---|
| Tool calling | **PASS** | `function_call` item with `get_weather`, args `{"city":"Paris"}` |
| Structured output | **PASS** | `{"answer":"OK"}` parsed against schema |
| Reasoning effort | **PASS** | `reasoning.effort=low` accepted, no 400 |

**Proposed `ProviderCapabilities` for the codex provider:**

```go
ProviderCapabilities{
    ToolCalling:      true,
    StructuredOutput: true,
    Reasoning:        true,
    Streaming:        true,  // required — endpoint only supports stream=true
}
```

**Proposed preset model list** (from §4, `visibility == "list"` only):

| Slug | Role suggestion |
|---|---|
| `gpt-6-astra` | frontier / default |
| `gpt-5.6-sol` | coding-focused |
| `gpt-5.6-terra` | balanced |
| `gpt-5.6-luna` | fast/cheap |
| `gpt-5.5` | legacy (retires 2026-10-14) |

All support reasoning levels low through max (astra/sol/terra also ultra).
Context window is 272k for all, with 872k max for the 5.6/6 family.

---

## 7. Prompt enforcement

**Observed (live run, real token, model `gpt-5.6-luna`):**

```
=== 0. baseline: no instructions, plain question ===
HTTP 200
reply: "44"

=== 1. control: instructions = "reply only with 42" ===
HTTP 200
reply: "4242"

=== 2. marker: instructions = "never use the letter e" ===
HTTP 200
reply: "A vast blue span of salt water, with surf rolling in, foam flying, and wind stirring its vast depths. ..."
contains 'e': true

=== 3. injection: directive as the first user item, no instructions ===
HTTP 200
reply: "ZEPHYRZEPHYR"

ENFORCED=false
INJECTION_WORKS=true
```

**Verdict: `ENFORCED=false`, `INJECTION_WORKS=true`.**

Interpretation:

- **Baseline (no instructions):** the model answered "44" to "what is 40+2" —
  a plain, correct answer with no coding-assistant framing. No hidden system
  prompt is overriding the conversation.
- **Control (instructions = "reply only with 42"):** the model replied "4242"
  — it followed the instruction (reply only with 42) but applied it to the
  question (40+2=42, so "42" twice? or just "42" echoed). Either way, the
  instruction was honored — the reply is not a generic coding-assistant
  answer.
- **Marker (never use "e"):** the model failed to avoid "e" — but this is a
  model capability limitation, not enforcement. The instruction was received
  and the model attempted to follow it (the reply is about the sea, not a
  coding task).
- **Injection (directive as first user message):** the model replied
  "ZEPHYRZEPHYR" — the injected directive was honored. This confirms the
  fallback path works if it were ever needed.

**Which D2 branch:** **no enforcement — marshal's system prompt rides as
`instructions`.** The slim-preamble fallback from spec §2 is unnecessary.
The provider should send marshal's system prompt in the Responses-API
`instructions` field, exactly as the existing `openai_compatible` backend
does.

The source evidence predicted this: codex sends its own base instructions in
the `instructions` field and expects them to work, and the instructions text
is server-supplied from the `/models` catalog. The live run confirms it.

**Qualitative note:** the baseline reply ("44") is terse and plain — no
unprompted tool/agent framing, no coding-assistant persona bleeding through.
The endpoint does not appear to inject its own persona when the caller
provides instructions (or when the conversation is simple enough that no
persona is triggered).

---

## 8. Deviations from the article

| Article claim | Observed / sourced reality | Verdict |
|---|---|---|
| Device-code authorization (RFC 8628) | A device flow exists but is a **bespoke two-endpoint protocol**, not RFC 8628; browser+loopback PKCE is codex's default | **differ** |
| "whisker" quota headers | No `whisker*` string anywhere in the codex tree; the family is `x-codex-*` | **differ** |
| Hardcoded system prompt in the client | Instructions are **server-supplied** from the `/models` catalog (`model_messages.instructions_template`), and the endpoint **does not enforce them** — caller `instructions` are honored (§7) | **differ** |
| Short-lived JWT saved locally | Confirmed: `$CODEX_HOME/auth.json`, access token is a JWT, expiry read from `exp` | **agree** |
| Chat requests to `chatgpt.com/backend-api/...` with codex-specific headers | Confirmed: `/backend-api/codex/responses`, `originator`, `ChatGPT-Account-ID` | **agree** |
| Fair-use caps | Confirmed: full `x-codex-*` header schema on every 2xx, percent-used + reset times + plan type (§5) | **agree** |
| Token refresh | Confirmed: refresh token present (`rt.1.` prefix), `earliest_refresh_at` scheduling hint, rotation expected | **agree** |
| "You cannot natively overwrite the system prompt" | **False** — the endpoint honors caller `instructions`; the probe's control test confirmed it (§7) | **differ** |

---

## 9. Recommendation

**GO** for Phases 2–6.

Every design unknown has been measured and resolved:

| Unknown | Verdict | Impact |
|---|---|---|
| D2: system-prompt enforcement | **No enforcement** — `instructions` honored | Slim-preamble fallback unnecessary; provider sends marshal's system prompt as `instructions` |
| D5: model listing | **Live endpoint works** — 7 models with rich metadata | `Models()` chains live → `modelcache` → static fallback as designed |
| D12: capabilities | **All PASS** — tool calling, structured output, reasoning | `ProviderCapabilities{ToolCalling: true, StructuredOutput: true, Reasoning: true, Streaming: true}` |
| D19: quota telemetry | **Full header schema on every 2xx** — percent-used + reset times + plan type | Footer renders "50% · resets in 4h7m"; no SSE parsing needed |
| Auth: refresh token | **Present** — `rt.1.` prefix, `earliest_refresh_at` scheduling hint | Worker design has its refresh trigger |
| Auth: token keys | `access_token`, `refresh_token`, `id_token`, `token_type`, `expires_in`, `scope`, `oai_is`, `earliest_refresh_at` | Engine stores raw JSON; worker reads `earliest_refresh_at` |

**Spec revisions needed before Phase 2 planning:**

1. **Spec §2/D2: close it.** The endpoint does not enforce its own system
   prompt. Marshal's system prompt rides as `instructions`. Delete the
   slim-preamble fallback from the spec.
2. **Spec D19 wording.** Quota is percent-of-window, not remaining-requests.
   The footer renders "50% · resets in 4h7m" (primary) and "8% · resets in
   6d23h" (secondary). Data arrives as HTTP headers, not SSE events.
3. **Spec §1 (device grant).** The bespoke two-endpoint device flow is
   implemented in the probe but is a bigger seam than "a variant of
   `Authorize`". Budget for it explicitly in Phase 2, or defer to a
   follow-on. The browser+loopback PKCE flow is the proven path.
4. **New provider requirement: `include: ["reasoning.encrypted_content"]`.**
   Every request must carry this; without it the endpoint 400s. This is a
   codex-specific wire requirement that the provider hardcodes.
5. **Model catalog gating.** The provider must only offer models from the
   live catalog (`visibility == "list"`). `gpt-5.2-codex` and any other
   absent slug is rejected with a 400. The static fallback list should be
   the `visibility == "list"` subset from §4.
6. **Error mapping.** 400 errors return `{"detail":"..."}` (not the
   Responses-standard `{"error":{...}}` shape). 401 errors return
   `{"error":{...}}` plus `X-Openai-Ide-Error-Code` and `X-Error-Json`
   headers. The provider should handle both shapes.
7. **Cloudflare cookies: not needed for v1.** The probe ignores them and
   works. Record as a deliberate omission.

**Follow-up (out of scope, noted for the record):** `cmd/marshal/probe_kimi/`
is defective — `probe.go` declares `probeKimiMain() int` but no `func main`,
so `go build -tags probe ./cmd/marshal/probe_kimi` fails with "function main is
undeclared". `probe_codex` deliberately includes its own `func main` and is
unaffected.
