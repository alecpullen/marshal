# W5.6 · UI: automations — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w5-automations-and-ops-design.md`](../specs/2026-10-03-w5-automations-and-ops-design.md) §6.3
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Runs after:** W1–W4 and W5.1–W5.5.
**Base:** the branch once W5.5 is complete. Names from earlier phases are
as their plans define them.
**Plan slug:** `w5-6-ui-automations`. Commit each task as
`w5-6-ui-automations: task N — <title>`.

## Goal

This plan adds:

- the project automations pages for the review bot and the CI fixer;
- review drafts in the inbox, with post, edit, discard and send to
  author;
- CI fixer history;
- "Request review bot" on the Ship panel;
- the repo webhook secret setup.

## Non-goals

- New backend behaviour. Everything here consumes W5.4.

## Assumptions

These W5.4 routes exist:
- `/api/automations/review/drafts…`, with the actions post, discard and
  send-to-author;
- `/api/automations/ci/history…`;
- `POST /api/repos/{id}/webhook-secret`;
- the `automation` fleet delta.

Project settings with `automations` come from W4.2/W5.4
(`/api/projects/settings`). The project overview page is W4.5
(`Project.svelte`), and the Ship panel is W2.3 (`ShipPanel.svelte`).

---

## Task 1: API client and the `automation` delta

**Goal:** typed functions for the automation routes, and fleet handling of
`automation` deltas.

**Files:**
- `web/ui/src/lib/api.ts`, `api.test.ts`, `web/ui/src/lib/fleet.ts`, `fleet.test.ts`

**Steps:**

1. Types:
   - `ReviewDraft {id, repoId, number, headSHA, agentId, findings, summary, status, createdAt, postedUrl?, error?}`;
   - `Finding {id, severity, path?, line?, title, body, stepNode?}`;
   - `CIHistory {id, repoId, sha, check, status, reason, prUrl?, costUsd}`;
   - `ReviewBotSettings`, `CIFixerSettings`.
2. Functions:
   - `listReviewDrafts`, `getReviewDraft`, `editReviewDraft`;
   - `postReviewDraft`, `discardReviewDraft`, `sendDraftToAuthor`;
   - `listCIHistory`, `getCIHistory`;
   - `createWebhookSecret`.
3. In `fleet.ts`, `automation` deltas push onto an `automations` list,
   with `{type, id, status?}` and a timestamp.
4. Tests: the URLs, and the delta handling.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/api.test.ts src/lib/fleet.test.ts
```

---

## Task 2: Automations pages

**Goal:** `#projects/<root>/automations/review-bot` and `…/ci-fixer`, with
settings, drafts and history (spec §6.3).

**Files:**
- `web/ui/src/views/automations/{ReviewBot,CIFixer}.svelte` (new), tests
- `web/ui/src/lib/automations/{DraftView,FindingCard}.svelte` (new)
- `web/ui/src/views/Project.svelte` (W4.5: add an Automations tab linking to both), `routes.ts`

**Steps:**

1. **Review bot page.**
   - **Settings card:**
     - Fields: enabled, repo, labels, skip drafts, authors, routing (the
       W3.4 chip), auto-post, and hold severities.
     - They save with `PUT /api/projects/settings` into
       `automations.reviewBot`.
     - **Webhook:** shows whether a secret is set (from
       `GET /api/secrets?prefix=hooks/` listing `hooks/<repoId>`).
       **Create secret** calls `createWebhookSecret` and shows the
       secret once, with the payload URL `<origin>/hooks/forge/<repoId>`
       and copy buttons.
   - **Drafts list:** PR number and title link, status, severity counts
     as chips, and the created time.
   - **`DraftView`:**
     - the summary, then findings grouped by severity;
     - each `FindingCard` shows `path:line` (a link to the forge file at
       the head SHA), the title and body, and an evidence link
       (`#chat/<agentId>?node=<stepNode>`) when `stepNode` is present;
     - **Edit** makes the title, body and severity editable inline, then
       saves with `editReviewDraft`;
     - **Post** confirms, then posts and shows the review URL;
     - **Discard**;
     - **Send to author agent** shows checkboxes per finding, then calls
       `sendDraftToAuthor`. A 404 shows "No Marshal agent owns this PR".
2. **CI fixer page.**
   - **Settings card:** enabled, repo, branches, max minutes, max USD,
     push, and push branches (shown only when push is on, with a warning).
     The same webhook block as the review bot.
   - **History table:** SHA, check, status (`Tag`: fixed is ok, didn't
     reproduce is neutral, gave up is err), reason, PR link, and cost.
   - A row opens the agent session (`#chat/<agentId>`) when the agent
     still exists.
3. Tests:
   - the settings save posts nested `automations`;
   - the webhook secret is shown once;
   - draft actions call the right routes;
   - send-to-author's 404 message;
   - CI history renders a tag per status.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/automations && npx svelte-check
```

---

## Task 3: Inbox drafts and "Request review bot"

**Goal:** review drafts appear in Home's Ready to ship, and the Ship panel
can request a review (spec §6.3).

**Files:**
- `web/ui/src/views/Home.svelte`, `Home.test.ts`
- `web/ui/src/lib/review/ShipPanel.svelte` (W2.3), `web/ui/src/views/Review.test.ts`

**Steps:**

1. **Home, Ready to ship.**
   - Add the drafts with `status: draft` (`listReviewDrafts` on mount,
     refreshed on `automation` deltas) as rows: "Review draft for PR #N"
     with severity chips and **Open**, which goes to the draft on the
     project automations page.
   - CI fixer results with status `fixed` appear in the same group for 24
     hours, as "CI fix ready: PR #N".
2. **ShipPanel.**
   - Enable "Request review bot" when the agent's project settings have
     `automations.reviewBot.enabled`, read with
     `GET /api/projects/settings?root=`.
   - When it's checked, after `exitAgent` returns a `prUrl`, the bot runs
     on that PR. Because the PR came from the bridge, there is no webhook
     event.
   - Add a bridge route for this: `POST /api/automations/review/run`
     `{repoId, number}`, which calls W5.4's `onPREvent`, with a bridge
     test, in `web/bridge/reviewbot.go`.
3. Tests:
   - Home renders a draft row and opens the right route;
   - ShipPanel with the bot enabled calls the run route after a
     successful push;
   - with the bot disabled, the toggle is disabled.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestReviewBot' -v
cd ../ui && npx vitest run src/views/Home.test.ts src/views/Review.test.ts && npx svelte-check
```

---

## Task 4: Rebuild static, and close out the docs

**Goal:** the committed bundle includes W5.6, and the docs record that
the roadmap is done.

**Files:**
- `web/bridge/static/**`
- `docs/web-studio/design.md` (§9)
- `AGENTS.md` (`web/` notes)

**Steps:**

1. Run `cd web/ui && npm run build`, and commit `web/bridge/static`.
2. In `design.md` §9, mark W1–W5 as shipped, with links to each phase
   spec. Leave "Later" as is.
3. In `AGENTS.md`, extend the `web/` bullet with one line listing the
   bridge subsystems added across W1–W5:
   - the control agent;
   - templates and builds;
   - secrets;
   - the egress proxy;
   - terminals;
   - recipes and schedules;
   - automations.

   Each points at its main file, so future agents find them before
   rebuilding them, as the "Check here before building something" rule
   asks.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../bridge && go test ./... && cd ../.. && grep -n "W5" docs/web-studio/design.md | head
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./... && go build ./cmd/webbridge
CGO_ENABLED=1 go test ./... ; go vet ./... ; gofmt -l .
cd web/bridge && go test ./... -race
cd ../ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
```

Expected:
- the repo-wide Go run fails only on the five known tests;
- the bridge passes under `-race`;
- the UI is clean, and the bundle is committed.

Check by hand, end to end:
- open a PR on a watched repo, so a draft appears;
- post it;
- break CI with a real bug, so a fix PR opens;
- break CI so the only "fix" would be skipping a test, and confirm the
  fixer gives up with a forbidden change.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | Every file comes from W1–W5.5 as defined. Task 3 adds the one missing bridge route, with a test. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §6.3. Task 4 updates the design doc and AGENTS.md. |
