# W5.3 · Bridge: recipes, schedules, notifications, status links — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w5-automations-and-ops-design.md`](../specs/2026-10-03-w5-automations-and-ops-design.md) §5.3–§5.6
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Runs after:** W1–W4, [W5.1](2026-10-03-w5-1-engine-plan.md) and [W5.2](2026-10-03-w5-2-bridge-terminal-preview-plan.md).
**Base:** the branch once W5.2 is complete. Bridge anchors were checked on
`2ddc09e`. Later symbols are as their plans define them:
- `Fleet.Spawn` with a `Workspace` and `Routing`;
- W3.2 budgets (per-agent caps, overrides);
- W1 `Registry.Stack`, W3.2 `StartRun`;
- `fleet.json` v10.

**Plan slug:** `w5-3-bridge-recipes-schedules`. Commit each task as
`w5-3-bridge-recipes-schedules: task N — <title>`.

## Goal

This plan delivers:

- recipes, with built-ins, a runner and structured-output parsing;
- schedules, with a cron parser and a scheduler;
- three new origins;
- outbound notification webhooks;
- status links, with a public data endpoint and page.

## Non-goals

- Forge automations (W5.4). They use this plan's recipe runner.
- UI (W5.5, W5.6).

---

## Task 1: Recipe store and built-ins

**Goal:** recipes are stored as JSON, with four built-ins (spec §5.3).

**Files:**
- `web/bridge/recipes.go` (new), `recipes_builtin.go` (new), `recipes_test.go` (new)
- `web/bridge/audit.go`

**Steps:**

1. **Type.** `Recipe` has the fields from spec §5.3, with JSON tags as
   shown there.
   - Validate:
     - the name matches `^[a-z0-9][a-z0-9-]{0,40}$`;
     - `kind` is in its set;
     - `mode` is a valid ACP mode;
     - every `{{x}}` in `prompt` names a declared input;
     - `output` is in its set.
2. **Store.** `RecipeStore` lives at `<state>/recipes/`.
   - It provides `List`, `Get`, `Put` (refused for `builtin`; save a copy
     under a new name instead), `Delete` (refused for `builtin`) and
     `Copy(from, to)`.
   - Built-ins come from `recipes_builtin.go`, a Go slice. They're merged
     into `List`, and a stored recipe of the same name overrides them.
3. **Built-ins.**
   - `review-pr`:
     - plan mode;
     - inputs `pr` and `title`;
     - output `review-findings`.

     The prompt states:
     1. review the diff against the base, citing files and lines;
     2. classify each finding as blocking, should-fix or nit;
     3. end with a fenced `json` block of exactly the spec's shape.
   - `fix-ci`:
     - edit mode;
     - inputs `check`, `log` and `command`;
     - output `ci-result`.

     The prompt states:
     1. reproduce first, by running the failing command;
     2. if it doesn't reproduce, stop and report `reproduced: false`;
     3. never skip, disable or quarantine tests, or change CI config to
        hide failures;
     4. end with the `ci-result` json.
   - `summarize-changes`: plan mode, input `since`, output `none`.
   - `update-deps`: edit mode, input `ecosystem`, output `none`.
4. Add the audit constants `recipe_saved` and `recipe_deleted`.
5. Tests:
   - validation, including an undeclared placeholder;
   - built-ins are listed;
   - a stored copy overrides a built-in;
   - built-ins can't be deleted;
   - copy.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestRecipe' -v
```

---

## Task 2: Recipe runner and structured outputs

**Goal:** `RunRecipe` spawns or runs, renders the prompt, applies limits,
and parses structured results on completion (spec §5.3).

**Files:**
- `web/bridge/reciperun.go` (new), `reciperun_test.go` (new)
- `web/bridge/workspace.go` (origin constants), `web/bridge/http.go`

**Steps:**

1. Add the origins `OriginSchedule = "schedule"`,
   `OriginReviewBot = "review-bot"` and `OriginCI = "ci"` next to
   `OriginUI` (`workspace.go:25-30`).
2. `render(prompt string, inputs map[string]string) (string, error)`
   replaces each `{{name}}` with its value.
   - An input value containing `{{` is rejected.
   - A missing required input is an error.
3. `func (f *Fleet) RunRecipe(ctx, name string, req RecipeRunRequest) (string, error)`,
   where `RecipeRunRequest{Project, RepoID, Ref string; Inputs map[string]string; Origin string; Routing json.RawMessage; Limits *RecipeLimits; OnDone func(RecipeResult)}`.
   `Routing` and `Limits`, when set, override the recipe's own values.
   W5.4's automations use them.
   1. Check the budget gate (W3.2's `budgetGate`).
   2. Spawn through `f.Spawn` with the recipe's mode, workspace and
      routing:
      - origin `OriginUI` by default;
      - isolated;
      - git-sourced when `RepoID` is set, with `Ref`.
   3. Then, by kind:
      - `prompt`: send the rendered prompt with `rt.reg.Prompt`;
      - `sdd`: `StartRun` with the rendered prompt as the plan;
      - `swarm`: `StartRun` with the goal.
   4. Apply the limits:
      - `maxUsd` sets a per-agent cap for this agent in W3.2's
        `budgetState`. Add `agentCaps map[string]float64` there, with
        `pause` as the action;
      - `maxMinutes` starts a deadline that cancels and pauses (W4.2
        timeout path).
   5. **Completion.** On the agent's next `turn_end` event
      (`Registry.OnEvent`), or on run finish for SDD and swarm (the W3.2
      `run` delta with `finished`):
      - fetch the stack with `rt.reg.Stack` (W1);
      - take the last `final` node's `message.content`;
      - parse the last ` ```json ` fenced block into `ReviewFindings` or
        `CIResult` according to `output`;
      - call `OnDone(RecipeResult{AgentID, Output, Parsed, Err})`.

      A parse failure sets
      `Err = "gave up: no structured result"`.
   6. Record each recipe run on the agent: add
      `Recipe string "recipe,omitempty"` to `Agent` (workspace v11; this
      task bumps the version).
4. Route: `POST /api/recipes/{name}/run {project, repoId?, ref?, inputs}`
   returns 202 `{agentId}`. Also add routes for the store:
   `GET/PUT/DELETE /api/recipes…` and `POST /api/recipes/{name}/copy`.
5. Tests:
   - render errors;
   - a prompt run spawns with the mode and sends the prompt (fake
     child);
   - SDD and swarm go through `StartRun`;
   - `maxUsd` sets the cap;
   - completion parses a findings block from a stack fixture;
   - a missing block gives the gave-up error;
   - a v10 → v11 load.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestRecipeRun|TestRender|TestWorkspace' -v && go test ./
```

---

## Task 3: Cron parser and scheduler

**Goal:** five-field UTC cron schedules run recipes, with overlap
skipping (spec §5.4).

**Files:**
- `web/bridge/cron.go` (new), `cron_test.go` (new)
- `web/bridge/schedules.go` (new), `schedules_test.go` (new)
- `web/bridge/workspace.go` (`schedules` in v11), `web/bridge/poller.go` (start alongside `StartPoller` `:20`), `cmd/webbridge/main.go` (call `StartScheduler` next to `StartPoller` `:287`)

**Steps:**

1. `cron.go` provides `ParseCron(expr string) (Cron, error)` and
   `(Cron) Next(after time.Time) time.Time`.
   - Fields are minute, hour, day of month, month and day of week.
   - Each field supports `*`, lists, ranges and `/step`.
   - Day of month and day of week combine with POSIX OR semantics when
     both are restricted.
   - `@hourly`, `@daily` and `@weekly` are aliases.
   - Table tests cover each field form, the OR rule, month and year
     rollover, and invalid input.
2. Store `Schedule` (spec §5.4 fields) in `fleet.json` `schedules`
   (v11), with `PutSchedule`, `RemoveSchedule` and `Schedules()`.
3. `func (f *Fleet) StartScheduler(clock func() time.Time)` runs a
   ticker every minute. For each enabled schedule where
   `Next(lastTick) <= now`:
   - if `lastRunAgent` is still running (its live status), skip it and
     record `lastResult = "skipped: previous run active"`;
   - otherwise call `RunRecipe` with `Origin: OriginSchedule`, and store
     `lastRun`, `lastRunAgent`, and `lastResult` (from `OnDone`).
4. Routes:
   - `GET/POST /api/schedules`;
   - `PUT/DELETE /api/schedules/{id}`;
   - `POST /api/schedules/{id}/run`.

   They validate the cron expression and that the recipe exists. Changes
   are audited as `schedule_saved` and `schedule_deleted`.
5. Tests, with an injected clock:
   - fires at the right minute;
   - skips on overlap;
   - run-now;
   - routes and validation.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestCron|TestSchedule' -race -v && go test ./
```

---

## Task 4: Outbound notification webhooks

**Goal:** configurable webhooks receive selected fleet events, signed
when a secret is set, with retries (spec §5.5).

**Files:**
- `web/bridge/notify.go` (new), `notify_test.go` (new)
- `web/bridge/workspace.go` (`notifications` in v11), `web/bridge/fleet.go` (fleet-log append hook)

**Steps:**

1. Config: `{webhooks: [{id, url, secretRef?, events: []}]}`. Validate
   that the URL is http or https and that the events are known.
2. **Event mapping.** Hook into the places that append fleet deltas: wrap
   `f.fleetLog.Append(fleetStreamKey, d)` with a small `f.emit(d)` helper
   and replace the direct calls (`grep -n "fleetLog.Append" web/bridge/*.go`).
   The mapping:

   | Delta | Notification event |
   |---|---|
   | `pending` | `needs_you` |
   | a `run` delta with `finished` | `run_finished` |
   | `budget` | `budget` |
   | `automation` (W5.4) | `automation` |
   | `watch` with state `fired` | `watch_fired` |
   | `network_block` | `network_block` |

3. **Delivery** is a goroutine per message:
   - POST JSON `{event, at, agentId, title, text, url}`. Build `url` from
     the server's `publicURLBase` (`NewServer`, `http.go:42-56`), which is
     passed into `Fleet` with a setter; an empty base gives a relative
     URL.
   - When the webhook has a secret, add
     `X-Marshal-Signature: sha256=<hex HMAC>` over the body, with the
     secret read from the provider.
   - Use a 10s timeout and 3 attempts with backoff of 2s, 8s, then 30s.
   - Delivery never blocks `emit`; a full queue drops the message and
     logs it.
4. Routes:
   - `GET/PUT /api/notifications`;
   - `POST /api/notifications/test`, which sends a `test` event to every
     webhook.
5. Tests, with an `httptest` receiver:
   - the event mapping per delta kind;
   - the signature verifies;
   - retries on a 500;
   - unselected events aren't sent;
   - `emit` doesn't block when the receiver hangs.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestNotify' -race -v && go test ./
```

---

## Task 5: Status links and the public page

**Goal:** create, list and revoke status links. A public `/s/{token}`
page and `/data` endpoint expose only whitelisted fields, with a rate
limit (spec §5.6).

**Files:**
- `web/bridge/status.go` (new), `status_test.go` (new), `status_page.html` (new, embedded)
- `web/bridge/workspace.go` (`statusLinks` in v11), `web/bridge/http.go` (`ServeHTTP` `:91`)

**Steps:**

1. **Storage.** `StatusLink{ID, AgentID, TokenHash, CreatedAt, ExpiresAt, RevokedAt?}`,
   stored in `fleet.json` `statusLinks`. The token comes from 32 bytes of
   `crypto/rand` as base64url, and is stored as `HashToken(token)`
   (`client.go:53`).
2. **Routes:**
   - `POST /api/status-links {agentId, ttlHours}`: the default TTL is
     168h and the maximum is 720h. Returns `{id, url: "/s/<token>", expiresAt}`.
   - `GET /api/status-links` lists links, never the token.
   - `DELETE /api/status-links/{id}` sets `RevokedAt`.
   - Create and revoke are audited as `status_link_created` and
     `status_link_revoked`.
3. **Public routing.** In `ServeHTTP`, before the static handler, route
   `/s/` to `s.statusPublic`:
   - **Rate limit:** a token bucket per remote IP, holding 60 per minute
     and refilling at one per second. The map is pruned every 10 minutes.
   - **Lookup:** hash the presented token, find a link that isn't revoked
     and hasn't expired, and compare with `subtle.ConstantTimeCompare`.
     Any failure returns 404.
   - `/s/<token>` renders `status_page.html` through `html/template`. It
     is light-themed, links to no other Studio page, and polls `data`
     every 10s with `fetch`.
   - `/s/<token>/data` builds and returns the whitelist JSON:
     - read the agent's stack with `Registry.Stack` and the run with
       W3.2's run detail;
     - copy **only** these fields:
       - `name`, the project basename, `status`, `elapsedMs`;
       - `progress {done, total}`;
       - `tasks [{index, content, status}]`;
       - `headline`, the live step's `step.headline`, or the last step's
         headline.
   - Every response sets `Cache-Control: no-store` and
     `Referrer-Policy: no-referrer`.
4. Tests:
   - create, list and revoke;
   - expiry gives 404;
   - revoked gives 404;
   - unknown gives 404;
   - the rate limit returns 429 after 60;
   - the data JSON has exactly the whitelisted keys: assert the key set
     recursively, and that tool output strings from the fixture are
     absent;
   - the HTML page renders with the agent name escaped.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestStatusLink|TestStatusPublic' -v && go test ./... -race && go vet ./...
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./... && go build ./cmd/webbridge
cd web/bridge && go test ./... -race && go vet ./...
cd ../.. && CGO_ENABLED=1 go test ./... ; gofmt -l .
```

Expected: the bridge passes, including `TestWebIsStdlibOnly`, and the
repo-wide run fails only on the five known tests.

## Integration notes

- **Workspace version** is 11 (`recipe`, `schedules`, `notifications`,
  `statusLinks`).
- **The scheduler** runs in fleet mode only. Registry mode, with no
  fleet, has no scheduler.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | Checked on `2ddc09e`: the origin constants (`workspace.go:25-30`), `StartPoller` (`poller.go:20`) and its call (`main.go:287`), `NewServer`/`publicURLBase` (`http.go:42-56`), `ServeHTTP` (`:91`), `HashToken` (`client.go:53`). Later symbols are as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §5.3–§5.6. |
