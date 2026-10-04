# W5.4 · Bridge: forge automations (review bot, CI fixer) — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w5-automations-and-ops-design.md`](../specs/2026-10-03-w5-automations-and-ops-design.md) §5.7–§5.10
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W5.3](2026-10-03-w5-3-bridge-recipes-schedules-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Bridge anchors were checked on
`2ddc09e`. These are as their plans define them:

| Plan | Symbols |
|---|---|
| W5.3 | `RunRecipe`, `RecipeResult`, `ReviewFindings`, `CIResult`, the origins, `f.emit` |
| W4.3 | The secret provider, repos |
| W4.2 | `ProjectSettings` |
| W2.1 | Review comments |
| W2 / earlier | `Fleet.Diff`, `Exit` |

**Plan slug:** `w5-4-bridge-automations`. Commit each task as
`w5-4-bridge-automations: task N — <title>`.

## Goal

This plan delivers:

- forge methods for PRs, reviews, checks and logs (GitHub and Gitea);
- signed webhooks in, with a polling fallback;
- the review bot: drafts, post, edit, discard, and send to the author
  agent;
- the CI fixer, with its deterministic guards and history.

## Non-goals

- UI (W5.6).

---

## Task 1: Forge interface extensions

**Goal:** `ListPRs`, `GetPR`, `PostReview`, `ListFailedChecks` and
`CheckLog` on GitHub and Gitea (spec §5.7).

**Files:**
- `web/bridge/forge.go` (`Forge` `:47`), `forge_github.go`, `forge_gitea.go`
- `web/bridge/forge_github_test.go`, `forge_gitea_test.go`

**Steps:**

1. Types, with no JSON tags, following the existing forge types
   (`forge.go:14-38`):
   - `PRQuery{State string; Since time.Time; Label string}`;
   - `PRInfo{Number int; Title, URL, HeadRef, HeadSHA, BaseRef, Author string; Draft bool; Labels []string; UpdatedAt time.Time}`;
   - `Review{Body string; Comments []ReviewLine}`;
   - `ReviewLine{Path string; Line int; Body string}`;
   - `CheckInfo{ID int64; Name, Conclusion, URL, Ref string; JobID int64}`.
2. Add the five methods to the `Forge` interface.
3. GitHub, using `doJSON` (`forge_github.go:75`) and `authHeader` (`:34`):
   - `ListPRs`: `GET /repos/o/n/pulls?state=open&per_page=100&sort=updated&direction=desc`,
     filtered client-side by label and `Since`;
   - `GetPR`: `GET /repos/o/n/pulls/N`;
   - `PostReview`: `POST /repos/o/n/pulls/N/reviews`, with
     `{body, event:"COMMENT", comments:[{path, line, side:"RIGHT", body}]}`,
     returning `html_url`;
   - `ListFailedChecks`:
     `GET /repos/o/n/commits/{ref}/check-runs?per_page=100`, keeping
     `conclusion == "failure"`. `JobID` is parsed from `details_url` when
     it matches `/actions/runs/\d+/job/(\d+)`;
   - `CheckLog`: with a `JobID`, `GET /repos/o/n/actions/jobs/{id}/logs`,
     following the redirect and keeping the last 256 KiB. Otherwise, the
     check's `output.text` from `GET /check-runs/{id}`.
4. Gitea, using the existing helpers in `forge_gitea.go`:
   - `ListPRs`: `GET /repos/o/n/pulls?state=open`;
   - `GetPR`: `GET /pulls/N`;
   - `PostReview`: `POST /repos/o/n/pulls/N/reviews` with
     `{body, event:"COMMENT", comments:[{path, new_position: line, body}]}`;
   - `ListFailedChecks`: `GET /repos/o/n/commits/{ref}/statuses`, keeping
     `status == "failure"`, with `ID` set to the status ID;
   - `CheckLog`: returns the status `description` plus `target_url` as
     text.
5. Rate limits: honour the existing `retryAfterDuration`
   (`forge_github.go:57`) and `noteRateLimit` (`poller.go:122`) behaviour.
6. Tests, extending the `httptest` forge servers used by
   `testFleetWithIssueForge` (`issues_test.go:13`), with one test per
   method per forge:
   - label filtering;
   - review comment mapping;
   - failed-check filtering;
   - job-log tail truncation;
   - the Gitea fallback text.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestForge|TestGitHub|TestGitea' -v && go test ./
```

---

## Task 2: Webhook receiver and secrets

**Goal:** `POST /hooks/forge/{repoId}` verifies signatures and dispatches
PR and check events. `POST /api/repos/{id}/webhook-secret` issues secrets
(spec §5.8).

**Files:**
- `web/bridge/hooks.go` (new), `hooks_test.go` (new)
- `web/bridge/http.go` (`ServeHTTP` `:91`)

**Steps:**

1. Route `/hooks/forge/` before the static handler, as W5.3 did for
   `/s/`. Only `POST` is accepted, and the body is limited to 1 MiB with
   `http.MaxBytesReader`.
2. **Verification.** Load `vault:hooks/<repoId>` through the provider:
   - GitHub: `X-Hub-Signature-256: sha256=<hex>`;
   - Gitea: `X-Gitea-Signature: <hex>`.

   Compute HMAC-SHA256 over the raw body, and compare with
   `hmac.Equal`. A missing secret, header or mismatch returns 401.
3. **Dispatch** by event header (`X-GitHub-Event` or `X-Gitea-Event`):

   | Event | Condition | Becomes |
   |---|---|---|
   | `pull_request` | action in opened, synchronize, reopened, ready_for_review | `prEvent{repoID, number, headSHA}` → review bot (Task 3) |
   | `check_run` / `check_suite` / `workflow_run` | completed with conclusion failure | `checkEvent{repoID, ref, sha}` → CI fixer (Task 5) |
   | Gitea `status` | `state: failure` | The same check event |

   Everything else returns 204. Dispatch runs in a goroutine, and the
   handler answers 202.
4. `POST /api/repos/{id}/webhook-secret` creates 32 random bytes as hex,
   writes them with `Put` to `vault:hooks/<id>`, and returns `{secret}`
   once. It's audited as `webhook_secret_set`. The env backend returns
   409 with "configure a secrets backend".
5. Tests:
   - a valid GitHub signature dispatches a PR event;
   - a bad signature returns 401;
   - Gitea signatures;
   - an irrelevant action returns 204;
   - oversize bodies are rejected;
   - the secret route stores and returns once.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestHooks' -v
```

---

## Task 3: Review bot — settings, triggering and drafts

**Goal:** PR events run `review-pr` with origin `review-bot`, and results
become stored drafts (spec §5.9).

**Files:**
- `web/bridge/reviewbot.go` (new), `reviewbot_test.go` (new)
- `web/bridge/workspace.go` (`ProjectSettings.Automations`), `web/bridge/projects.go` (W4.2 settings validation)
- `web/bridge/poller.go` (`pollRepo` `:75`)

**Steps:**

1. Add `Automations *Automations` to `ProjectSettings`, with camelCase
   JSON throughout:
   - `Automations` has the fields `ReviewBot *ReviewBotSettings` and
     `CIFixer *CIFixerSettings`.
   - `ReviewBotSettings`:
     - `Enabled bool`, `RepoID string`;
     - `Labels []string`, `SkipDrafts bool`, `Authors []string`;
     - `Routing json.RawMessage`;
     - `AutoPost bool`, `HoldSeverities []string`.
   - `CIFixerSettings`:
     - `Enabled bool`, `RepoID string`, `Branches []string`;
     - `MaxMinutes int`, `MaxUSD float64`;
     - `Push bool`, `PushBranches []string`.

   It's an optional field, so the workspace version doesn't change. Validate
   it in W4.2's `PUT /api/projects/settings`.
2. `func (f *Fleet) onPREvent(ctx, ev prEvent)`:
   1. Find the project whose settings enable the review bot for
      `ev.repoID`.
   2. Get the PR with `GetPR`.
   3. Apply the filters: drafts, labels, authors.
   4. De-dupe on `(repo, number, headSHA)` against the stored drafts.
   5. Call `RunRecipe("review-pr", {RepoID, Ref: headSHA, Project, Inputs: {pr, title}, Origin: OriginReviewBot})`.
      Pass `routing` from the settings as `RecipeRunRequest.Routing`
      (W5.3 Task 2).
   6. `OnDone` stores a draft at `<state>/automations/review/<id>.json`
      (spec shape). It then:
      - calls `f.emit({kind:"automation", type:"review_draft", id})`;
      - posts immediately (Task 4) when `AutoPost` is set and no finding's
        severity is in `HoldSeverities`;
      - on a parse failure, stores the draft with `status:"failed"` and
        the error.
3. **Polling fallback.** In `pollRepo`, when the repo has the review bot
   enabled and no webhook secret (`Get` returns not found), also run
   `ListPRs{Since: r.LastPolled}` and dispatch `onPREvent` for each.
4. Tests:
   - a matching event spawns via the recipe (fake child records it);
   - filters exclude drafts and other labels;
   - de-dupe on the same SHA;
   - a findings result stores a draft and emits;
   - auto-post respects hold severities;
   - the poll fallback dispatches.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestReviewBot|TestPoll' -v && go test ./
```

---

## Task 4: Review bot — draft actions

**Goal:** list, read, edit, post and discard drafts, send findings to
the author agent, and run the bot on demand (spec §5.9, §6.3).

**Files:**
- `web/bridge/reviewbot.go`, `reviewbot_test.go`, `web/bridge/http.go`, `web/bridge/audit.go`

**Steps:**

1. Routes:

   | Route | Behaviour |
   |---|---|
   | `GET /api/automations/review/drafts?project=&status=` | Lists drafts |
   | `GET /api/automations/review/drafts/{id}` | Reads one draft |
   | `PUT …/{id}` `{findings, summary}` | Edits a draft that is still a draft |
   | `POST …/{id}/post` | Posts the review (see step 2) |
   | `POST …/{id}/discard` | Sets `status = discarded` |
   | `POST …/{id}/send-to-author` `{findingIds}` | Sends findings to the author agent (see step 3) |
   | `POST /api/automations/review/run` `{repoId, number}` | Runs the bot on a PR now, through `onPREvent` with the PR's current head SHA (from `GetPR`). The W5.6 Ship panel calls it after a push, since a PR the bridge created raises no webhook event. Returns 202, or 409 when the project's review bot is off. |

2. **Posting.** `post` builds a `Review`:
   - the body is the summary plus the findings without a path or line, as
     a list grouped by severity;
   - `Comments` are the findings that have a path and line, with the body
     formatted as `**<severity>**: <title>\n\n<body>`.

   It calls `forgeFor(repo).PostReview` (`exit.go:198`), sets
   `status = posted` with the returned URL, and audits `review_posted`.
3. **Sending to the author.**
   - Find the agent whose `Branch` equals the PR's `HeadRef` and whose
     `PRUrl` matches the PR URL.
   - For each finding, call W2.1's `AddReviewComment` with `path`, `line`
     (side `new`), an empty quote, and the body with a "From review bot:"
     prefix.
   - If no agent matches, return 404 "no Marshal agent owns this PR".
4. Tests:
   - post maps findings to inline and body sections against the fake
     forge;
   - editing a posted draft is refused;
   - discard;
   - send-to-author creates review comments on the matching agent;
   - send-to-author with no match returns 404;
   - the on-demand run route dispatches `onPREvent` and returns 409 when
     the bot is off;
   - audit entries.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestReviewDraft' -v && go test ./
```

---

## Task 5: CI fixer with guards

**Goal:** failed checks run `fix-ci`. Deterministic guards reject
unreproduced runs and test tampering. Successful fixes ship through the
gate (spec §5.10).

**Files:**
- `web/bridge/cifixer.go` (new), `cifixer_test.go` (new), `web/bridge/http.go`, `web/bridge/audit.go`

**Steps:**

1. `func (f *Fleet) onCheckEvent(ctx, ev checkEvent)`:
   1. Find the project with the CI fixer enabled for `ev.repoID`, and
      check that `ev.ref` is in `Branches` (glob match with
      `path.Match`).
   2. Run `ListFailedChecks`, then de-dupe on `(repo, sha, check name)`
      against history.
   3. For the first failed check:
      - get `CheckLog`;
      - infer the command: the last line of the log tail starting with
        `$ ` or `+ `, or empty;
      - call `RunRecipe("fix-ci", {RepoID, Ref: sha, Inputs: {check, log: tail 32 KiB, command}, Origin: OriginCI})`,
        with `MaxMinutes` and `MaxUSD` passed as
        `RecipeRunRequest.Limits` (W5.3 Task 2).
2. `OnDone` applies the guards in order and writes history at
   `<state>/automations/ci/<id>.json` (spec fields):

   1. **Reproduced.** When the result isn't parsed, or
      `reproduced == false`: status "didn't reproduce" (or "gave up: no
      structured result"). Discard the agent with `Fleet.Discard`.
   2. **No test tampering.** Get `Fleet.Diff(ctx, id, "")` for the file
      list, then the per-file diffs. `forbiddenChange(files, diffs) (string, bool)`
      applies every spec §5.10 pattern. Write it as a pure function with
      table tests. A match gives "gave up: forbidden change (<pattern> in
      <file>)", and the agent is discarded.
   3. **Fixed.** When `fixed == false`: "gave up", with the agent's
      notes.
   4. **Ship:**
      - `Push && ref in PushBranches`: push to the branch through `Exit`
        (`exit.go:54`) with no override.
      - Otherwise `Exit` opens a PR, which is the default for git-sourced
        agents.
      - A blocked gate gives "gave up: gate failed". Automation never
        overrides.
      - Success gives "fixed", with the PR URL.
   5. Look up the cost from the usage ledger (W3.2) for the agent.

   Emit `{kind:"automation", type:"ci_result", id, status}` and audit
   `ci_fixer_result`.
3. **Polling fallback.** In `pollRepo`, for repos with the CI fixer
   enabled and no webhook secret, check `ListFailedChecks` on the head of
   each watched branch. Get the head with `mirrorHead` after
   `EnsureMirror` (`mirror.go:28/127`).
4. Routes:
   - `GET /api/automations/ci/history?project=`;
   - `GET /api/automations/ci/history/{id}`.
5. Tests:
   - `forbiddenChange` on every pattern (each test file deletion pattern,
     each skip marker, the CI-config edits), plus negative cases (a new
     test added, a test edited without skipping);
   - not reproduced is discarded;
   - a forbidden change is discarded with the reason;
   - a gate failure gives "gave up";
   - success opens a PR (fake forge) and records "fixed";
   - de-dupe;
   - the poll fallback.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestCIFixer|TestForbiddenChange' -v && go test ./... -race && go vet ./...
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

- **Webhooks** must reach `/hooks/forge/…`, which needs the bridge's
  public URL (`--tls-*` or a reverse proxy). Without that, polling covers
  everything at the poller interval (`defaultPollInterval`,
  `poller.go:12`).
- **Automation agents** are ordinary agents with origins `review-bot` and
  `ci`. They show in the fleet, the Live wall and Usage.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Forge, hooks, bot trigger, bot actions, then the CI fixer, each with tests. The guard logic is a pure function with table tests. |
| Anchors verified? | Checked on `2ddc09e`: `Forge` and the types (`forge.go:14-47`), `doJSON`/`authHeader`/`retryAfterDuration` (`forge_github.go:75/34/57`), `pollRepo`/`noteRateLimit`/`defaultPollInterval` (`poller.go:75/122/12`), `forgeFor` (`exit.go:198`), `Exit` (`:54`), `EnsureMirror`/`mirrorHead`, `testFleetWithIssueForge` (`issues_test.go:13`), `ServeHTTP`. Later symbols are as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §5.7–§5.10. |
