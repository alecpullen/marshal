# F6 — the factory

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`, ahead of F0–F5. Part of [the Studio programme](programme.md).

Read first: [design.md](design.md) §2 and §3,
[design-system.md](design-system.md),
`docs/studio-fleet-factory/design-notes.md`,
`docs/studio-fleet-factory/studio-uiux-alignment-design.md`, the factory
specs beside them (completion, recovery routing, plan revisions, working
drafts, narration, project defaults, project and factory settings, the
Claude ACP planner), `docs/web-studio/design.md` §8.7,
`web/bridge/factory_types.go`, `factory_controller.go`, `factory_http.go`,
and in the Svelte client: `lib/factory.ts`, `lib/factoryStore.ts`,
`lib/planner.ts`, `lib/working-drafts.ts`, `lib/studio/model.ts`,
`lib/factory/`, `lib/FactoryRevision.svelte`, `views/Factory*.svelte`,
`views/Planner.svelte`, `views/WorkingDrafts.svelte`, `views/Run*.svelte`,
`views/Project.svelte`, with their tests.

**Before starting.** F4 has landed; F5 is strongly preferred, because a work
item's pull requests are shown with `PrCard`. The factory is under active
development: re-read everything above at the current `main`, and treat the
contract as it then is as the truth where it differs from this document.

## Context

A factory work item is a plan handed to a project's factory, which
implements it, reviews it, repairs findings and failed checks up to a round
allowance, and stops at a ready pull request. The bridge owns the item; the
routes are `GET` and `POST /api/factory/items`,
`GET /api/factory/items/{id}`, and
`POST /api/factory/items/{id}/commands` for everything the user does to
one. An item's state is one of: draft, queued, preparing, implementing,
reviewing, checking, repairing, needs_input, ready, stopping, stopped,
closed. It carries its approvals and their revisions, its workers
(`FactoryWorker`: agent, session, role, parked), its pull requests, its
observed CI, its rounds and grants, any pending decision, and its history.

Planning has its own routes under `/api/planners`: start, prompt, cancel,
events, context, requests, snapshots, and a snapshot's handoff into a
factory item. Working drafts autosave unfinished handoffs under
`/api/factory/working-drafts`, with conversion to an item and conflict
handling across devices. Plan runs (`/sdd` plans and swarms) are
`/api/runs`. Project defaults and factory settings are
`/api/projects/settings`; health is `/api/projects/health`; repository
discovery is `POST /api/repos/{id}/discover`.

**The mock is illustrative here more than anywhere.** It shows one item
with five pull requests and two parallel implementers. Build what the
contract describes. Where the contract has one implementer, one reviewer
and one pull-request chain, the screen shows that.

## Goal

- A work item is a thread: its stage, its activity, its asks, and an
  inspector with its plan, agents, checks, changes and pull requests.
- Planning is a thread that can become a work item through a handoff
  review, without a separate form page.
- A plan run is a thread with its tasks as lanes.
- Each project has a page: its factory board, its other threads, its
  defaults and its health.
- The new-work dialog's three choices all work.

## Non-goals

- The workflow editor, voice capture, host placement, adopting an
  independent agent's work into a factory. Each is a disabled row where it
  will live.
- Changing factory behaviour. This is a client for what the bridge does.
- Parallel implementers or multiple pull-request chains, unless the
  contract has them by then.

## Decisions

1. **Contract first, in slices.** The factory item and its parts, the
   command request and result, planner, working-draft, run, project-settings
   and health types move into `web/bridge/contract` with the slice that
   needs them, not all at once.
2. **Stages.** The strip has four: Plan, Implement, Review, Ready. The
   mapping from state is fixed and lives in `model/work_item_model.dart`:
   draft, queued and preparing are Plan; implementing and repairing are
   Implement; reviewing and checking are Review; ready is Ready.
   `needs_input` keeps the stage it interrupted and sets the thread's
   status to needs you. Stopping, stopped and closed are shown as words
   beside the strip, not as a fifth stage. A state this client does not
   know is shown as its raw name under the stage Plan, never hidden.
3. **Status of a work-item thread**, for the sidebar dot: needs you when a
   decision is pending or the state is draft or needs_input; ready when
   ready; failed when stopped with an error; idle when closed or stopped;
   running otherwise.
4. **The centre is activity, not a transcript.** A timeline of the item's
   history: who did what, with the planner, implementer, reviewer and the
   user named in words. Pending asks are pinned at the top as `AskCard`s.
   A ready or failing pull request heads it as in F5.
5. **Asks.** A draft awaiting approval, a decision, a recovery after
   stagnation or an exhausted allowance, and a completion assessment are
   four kinds of `AskCard`. Each states the consequence of each choice.
   Recovery's card carries its settings inline: the model to continue with,
   whether to continue the context or start fresh, and how many more
   rounds. No ask opens a different page.
6. **Inspector tabs for a work item**: Plan, Agents, Checks, Changes, Pull
   requests.
   - Plan: goal, scope, the approved revision's settings in plain words,
     and the documents. "Propose a change" starts a plan revision and shows
     proposed against approved before it is sent.
   - Agents: each worker with role, model and what it is doing. Opening one
     swaps the centre for that agent's conversation — the F3 screen,
     embedded — with a way back.
   - Checks: observed CI for the current head and the review results.
   - Changes: the F4 tab, for the item's branch.
   - Pull requests: `PrCard` for each.
7. **Steering.** The composer says who receives the message and defaults to
   the planner where the item has one. A message that cannot be delivered
   now is shown as queued, can be edited or cancelled, and is never shown
   as done.
8. **Planning.** "Plan first" opens a planning thread: the conversation in
   the centre, the evolving plan in the inspector. "Hand to the factory"
   opens the handoff review in a `Sheet`: scope, repository, environment,
   the effective models by role, expected checks, the round allowance, what
   the planner may decide alone, and what approving authorises. Inherited
   values are marked as inherited; an override is marked as an override.
   Approving creates the item and the thread becomes a work item.
9. **Work item from the dialog.** Choosing "Work item" in the new-work
   dialog opens the same handoff review directly, with the prompt as the
   plan's starting text.
10. **Drafts.** Unfinished handoffs autosave as working drafts. A draft
    changed elsewhere since it was opened shows both versions and asks
    which to keep; it never overwrites silently.
11. **Plan runs.** A run is a thread of kind plan run. Its centre has three
    views of the same run — lanes, graph, timeline — and the live step
    below. Lanes are tasks by Implement, Verify, Review and Commit. The
    graph draws `Depends on` edges where the plan has them. Its inspector
    tabs are Plan, Roles, Changes, Terminal, Inspect. Selecting a cell
    shows that role's conversation.
12. **The project page.** Tabs: Work, Automations, Defaults, Health.
    - Work: a board of the project's work items by stage, then its agents
      and runs.
    - Automations: read-only summary linking to the Automations place until
      F7.
    - Defaults: the project's agent defaults and factory settings, with
      live inheritance, saved with the conditional write the bridge already
      requires; a stale save warns, keeps the edit and offers a reload.
    - Health: gate runnable, mirror freshness, index, retained workspaces,
      forge connection, and the discovery report.
13. **The tree changes.** Work items and plan runs now appear under their
    project. An agent that is a factory worker of any item, current or
    historical, is no longer listed as an independent agent.
14. **One shared factory store.** `model/factory_store.dart` is the only
    reader of the item list and the only poller; every screen projects from
    it.

## Work

Suggested pull requests, in order. Each begins with the contract types it
needs.

1. **Store and models.** `factory_store.dart`, `work_item_model.dart`
   (stage and status mapping, filters), the tree change, with tests ported
   from `lib/studio/model.test.ts`, `factoryStore.test.ts` and
   `factory.test.ts` where the rule survives.
2. **Work-item thread: read.** Strip, activity, the five tabs read-only.
3. **Asks and commands.** Approval, decision, recovery, completion; stop
   and the other commands the endpoint accepts.
4. **Agent drill-in and steering.**
5. **Plan revisions.**
6. **Planning thread and handoff review.**
7. **Working drafts and conflicts.**
8. **Plan runs.**
9. **Project page.**
10. **New-work dialog completed.**
11. **End to end.** Against the fixture bridge: plan, hand off, approve,
    one decision, one repair, ready.

## Tests

- `web/bridge/contract`: fixtures per moved type.
- `app/test/model/`: `work_item_model_test.dart` (every state to stage and
  status, an unknown state, filters), `factory_store_test.dart`,
  `planning_model_test.dart`, `drafts_model_test.dart` (conflict rules),
  `run_model_test.dart`, `project_model_test.dart` (inheritance, stale
  save).
- `app/test/ui/factory/`: one file per slice above, each registering with
  the tooltip, states and overflow tests.

## Done when

- A work item goes from a planning conversation to a ready pull request in
  the app, including one decision and one repair, with no page other than
  its thread and one handoff sheet.
- Before approving a handoff the user can state its scope, repository,
  environment, models, expected checks, round allowance, planner authority
  and what approval authorises, from the sheet alone.
- No factory worker appears as an independent agent.
- A draft edited on two devices is never silently overwritten.
- A plan run can be followed and steered task by task.

## Open questions

- Whether a planning thread with no project belongs under a "No project"
  group in the tree. Recommended: yes, until a repository is connected.
- How much of plan revisions to carry if their runtime wiring is still
  incomplete on `main`; show what the bridge serves and mark the rest
  planned.
