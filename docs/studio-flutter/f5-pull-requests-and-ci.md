# F5 — pull requests and CI

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`, ahead of F0–F4. Part of [the Studio programme](programme.md).

Read first: [design.md](design.md) §2, [design-system.md](design-system.md),
`web/bridge/forge.go`, `web/bridge/cifixer.go`, `web/bridge/automation.go`,
`web/bridge/factory_types.go` (`FactoryPR`, `FactoryCheck`, `FactoryCI`),
`web/bridge/factory_forge.go`, `web/bridge/exit.go`,
`docs/studio-fleet-factory/factory-narration-spec.md`,
`docs/studio-fleet-factory/preview-environment-links-spec.md`, and the mock's
`lib/pulls.dart`.

**Before starting.** F4 has landed. Re-read the files above at the current
`main`. This is the one phase with substantial new bridge behaviour; design
its Go side against the code as it then is.

## Context

The product's output is pull requests, and the user wants them and their CI
at the front of the review experience, with a failure one click away from a
worker who will investigate and fix it.

What exists:

- The `Forge` interface lists and fetches pull requests, lists the *failed*
  checks on a ref (`ListFailedChecks`) and returns the tail of a failed
  check's log (`CheckLog`). It has no call that lists every check with its
  state.
- A factory work item records its pull request (`FactoryPR`) and observes
  CI for the head it expects (`FactoryCI`, a list of `FactoryCheck` with
  status, conclusion, URL and log tail), polling on the item's settings.
  Inside the item's round allowance, a failure is repaired without the
  user.
- The CI fixer (`cifixer.go`) reacts to a failed check on a watched branch:
  `fixCI` fetches the log, `startCIFix` runs the fix recipe within a time
  and cost limit, `forbiddenChange` refuses a fix that deletes or disables
  tests or edits CI configuration, and `finishCI` pushes or opens a pull
  request and records a `CIHistoryEntry`. It is configured per project by
  `CIFixerSettings`.
- An independent agent that ships records its pull request's URL in its
  exit result and nothing more.

What does not exist: one place that knows every open pull request Marshal
produced and the state of its checks, and a way for a person to ask for a
failing check on a given pull request to be fixed.

## Goal

- Every open pull request Marshal produced is listed with its checks and
  reviews against its current head, wherever it came from.
- A ready thread opens with its pull requests: CI first, then reviews, then
  the engineer's report, then the way into the code.
- A failing check is sent to a worker in one click, with no form, and the
  fix lands on the same pull request.

## Non-goals

- Merging by rule. Merge is a button the user presses.
- Pull requests Marshal did not produce. The review bot's subjects stay in
  Automations.
- A general CI dashboard. This shows checks on Marshal's pull requests.

## Decisions

### Bridge

1. **A pull-request record.** The bridge keeps, in fleet state with a
   version bump, one record per pull request it opened: repository, number,
   URL, head and base, the head commit last observed, draft or ready,
   state, and its origin — an agent, a factory item with the item's id, or
   an automation with its kind. The exit path writes it for an agent; the
   factory writes it where it records `FactoryPR`; the CI fixer and review
   bot write it when they open one. Existing agents and items are
   backfilled once from what they already record.
2. **Checks for any pull request.** `Forge` gains `ListChecks`, returning
   every check on a ref with name, status, conclusion and URL, implemented
   for GitHub and for Gitea/Forgejo. Where a forge or a credential cannot
   answer — an SSH-only credential cannot call an API — the record says
   checks are unavailable and why. It is never shown as passing.
3. **Observation.** One observer polls open records. A factory item's
   record reuses the item's own `FactoryCI` observation and is not polled
   twice. Evidence is bound to a head commit: when the head moves, the old
   checks are discarded, not carried over.
4. **The index.** `GET /api/pulls` returns the open records with their
   checks, review summary and derived state; `GET /api/pulls/{id}` returns
   one with each failing check's log tail and, when the origin has one, the
   narrated report. Changes are announced on the fleet stream as a new
   delta kind. The derived state is one of: ready, failing, in progress,
   being fixed, checks unavailable.
5. **Fix dispatch.** `POST /api/pulls/{id}/fix` names a failing check and
   optionally a note, a mode (fix, or investigate only) and a worker
   routing. It answers at once with the dispatch record; the work is
   asynchronous.
   - For a factory item's pull request it issues the item's existing repair
     through the factory controller. If the round allowance is exhausted it
     grants one more round with the reason "requested by the user", as
     `FactoryGrant` records.
   - For any other pull request it starts the CI fixer's run against that
     pull request's head branch, pushing to that branch. It reuses
     `startCIFix`, the time and cost limits, and `forbiddenChange`
     unchanged. The rule that a fix never deletes, skips or disables a test
     and never edits CI configuration holds here exactly as it does there.
   - One fix per pull request at a time. A second request while one runs is
     refused with the running one's id.
   - A dispatch is bound to the head it was asked about. If the head has
     moved by the time the worker starts, it is cancelled and reported, not
     run against different code.
   - `POST /api/pulls/{id}/fix/stop` stops it.
6. **Outcomes.** Fixed: pushed, and the record returns to in progress until
   the new head's checks report. Did not reproduce, gave up, refused as a
   forbidden change, and stopped each become an ask in the Inbox naming the
   pull request, the check, what was tried and what the user can do.
   Investigate-only always ends in an ask carrying the finding.
7. **Automatic fixing** is a per-project setting stored beside
   `CIFixerSettings`: when on, a failing check on any of the project's
   Marshal pull requests is dispatched as if the user had clicked. It is
   off by default. Factory items already repair inside their allowance and
   are unaffected.
8. **Every dispatch, stop and grant is audited**, with who asked.
9. All new wire types are defined in `web/bridge/contract`.

### App

10. **The Pull requests place.** Groups in this order: ready for review,
    checks failing, being fixed, in progress, checks unavailable. It is the
    mock's `PullsPage`.
11. **`PrCard`** shows state icon, number, title, repository and branch,
    size, the `CheckStrip`, a one-line check summary, the review summary,
    and the thread it came from. Ready offers Review, Merge and Open on
    forge. Failing shows the failing check's name and log tail and offers
    **Fix with a worker**, Full log and Open on forge. Being fixed shows
    "A worker is on it" with Watch and Stop.
12. **One click.** Fix with a worker posts the dispatch with no further
    input. The caret beside it is a `Menu` with exactly: Investigate only;
    Fix with a note; Choose the worker; Always fix failures automatically
    in this project. The last changes the setting in decision 7 and says so.
13. **The review head.** A thread with a ready pull request opens with it
    at the top of the conversation: title and head, each check by name,
    reviews, the narrated report when there is one, then Review changes,
    Merge, Open on forge and Send back. Review changes expands the
    inspector on Changes. A thread with a failing pull request opens with
    that `PrCard` instead.
14. **Elsewhere.** Ready and failing pull requests sit on Overview directly
    under Needs you. The Inbox's Ready filter lists ready pull requests.
    The sidebar row carries the ready count. The agent inspector's Changes
    tab footer shows the pull request's state once shipped.
15. **Honesty.** Checks for an older head are never shown as current.
    "Checks unavailable" is its own state with the reason. A dispatch that
    was accepted is shown as accepted, not as fixed.

## Work

Suggested pull requests, in order.

1. **Records and backfill.** The store, the three writers, the backfill.
2. **`ListChecks`** for both forges, against recorded forge responses.
3. **Observer, index and stream delta.** With the contract types.
4. **Fix dispatch.** Both paths, the head binding, the single-fix rule,
   stop, outcomes as asks, audit.
5. **Automatic fixing.** The setting and its trigger.
6. **App: model.** `model/pulls_store.dart` and `pulls_model.dart`.
7. **App: card and place.** `PrCard`, `CheckStrip`, the place.
8. **App: review head, Overview, Inbox, sidebar, Changes footer.**
9. **End to end.** Against the fixture forge: an agent ships a pull request
   whose check fails; the app dispatches a fix; the fix is pushed; the
   check passes; the card reaches ready.

## Tests

- `web/bridge`: record writers and backfill; `ListChecks` per forge,
  including the SSH-credential case; the observer discarding checks when
  the head moves and not double-polling a factory item; dispatch through
  the factory path and the fixer path; refusal of a second dispatch;
  cancellation on a moved head; each outcome's ask; that a fix deleting a
  test is refused on this path; audit entries.
- `app/test/model/`: `pulls_store_test.dart`, `pulls_model_test.dart`
  (grouping, derived state, stale head).
- `app/test/ui/pulls/`: `pr_card_test.dart` (each state),
  `fix_dispatch_test.dart` (one click, pending, being fixed, stop, each
  outcome), `review_head_test.dart`, `pulls_place_test.dart`.

## Done when

- Pull requests from an independent agent, a factory item and the CI fixer
  all appear in one list with correct checks for their current heads.
- A failing check is dispatched in one click and the user is asked nothing.
- The fix arrives on the same pull request, and the card moves from failing
  to being fixed to in progress to ready without a refresh.
- A fix that would delete a test is refused and comes back as an ask.
- Pushing a new commit to a pull request clears its old checks at once.
- With an SSH-only credential the card says checks are unavailable.

## Open questions

- Whether "Send back" on a ready pull request reopens the thread's agent or
  starts a worker. Recommended: it steers the thread's own agent when it is
  still alive, and otherwise starts a worker.
- Whether investigate-only needs its own recipe or the fix recipe with
  writes refused.
- The default time and cost limits for a user-requested fix; start from
  `CIFixerSettings`' defaults.
